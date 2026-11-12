package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CategoryRule defines file-type routing, priority, and optional transfer overrides.
type CategoryRule struct {
	Extensions      []string `json:"extensions"`
	Path            string   `json:"path"`
	Disabled        bool     `json:"disabled,omitempty"`
	Priority        int      `json:"priority,omitempty"`
	DefaultThreads  int      `json:"default_threads,omitempty"`
	SpeedLimitBytes int64    `json:"speed_limit_bytes,omitempty"`
}

// ScheduleConfig defines optional off-peak active window rules and default schedule presets.
type ScheduleConfig struct {
	Enabled            bool   `json:"enabled"`
	StartTime          string `json:"start_time"`           // 24h local time "HH:MM", e.g. "01:00"
	EndTime            string `json:"end_time"`             // 24h local time "HH:MM", e.g. "07:00"
	DefaultTime        string `json:"default_time"`         // Default schedule preset "HH:MM", e.g. "23:00"
	PauseOutsideWindow bool   `json:"pause_outside_window"` // Pause active jobs when window ends
}

// Config stores global desktop, transfer, scheduling, and file-type routing settings.
type Config struct {
	MaxConcurrentDownloads int                     `json:"max_concurrent_downloads"`
	DefaultThreads         int                     `json:"default_threads"`
	MaxThreads             int                     `json:"max_threads"`
	DefaultSavePath        string                  `json:"default_save_path"`
	SpeedLimitBytes        int64                   `json:"speed_limit_bytes"` // 0 = unlimited
	DisableAutoCategorize  bool                    `json:"disable_auto_categorize,omitempty"`
	Schedule               ScheduleConfig          `json:"schedule"`
	Categories             map[string]CategoryRule `json:"categories"`
}

// MatchedRule represents the resolved global or category routing outcome for a filename.
type MatchedRule struct {
	Category           string       `json:"category"`
	Rule               CategoryRule `json:"rule"`
	DestinationDir     string       `json:"destination_dir"`
	MatchedExtension   string       `json:"matched_extension,omitempty"`
	EffectiveThreads   int          `json:"effective_threads"`
	EffectiveSpeedBps  int64        `json:"effective_speed_bytes"`
	AutoCategorizeUsed bool         `json:"auto_categorize_used"`
}

// SpeedLimiterTarget matches downloader.RateLimiter without creating a package cycle.
type SpeedLimiterTarget interface {
	SetLimit(bytesPerSec int64)
}

type activeLimiterEntry struct {
	filename       string
	hasJobOverride bool
	limiter        SpeedLimiterTarget
}

var (
	globalConfig     *Config
	globalConfigPath string
	configMutex      sync.RWMutex

	activeLimitersMu sync.Mutex
	activeLimiters   = make(map[string]activeLimiterEntry)
)

// DefaultCategoryRules returns the baseline file-type download routing rules.
func DefaultCategoryRules() map[string]CategoryRule {
	return map[string]CategoryRule{
		"Video": {
			Extensions: []string{"mp4", "mkv", "avi", "mov", "flv", "webm", "wmv", "m4v", "mpg", "mpeg"},
			Path:       "~/Videos",
		},
		"Music": {
			Extensions: []string{"mp3", "flac", "wav", "aac", "ogg", "m4a", "opus", "wma"},
			Path:       "~/Music",
		},
		"Compressed": {
			Extensions: []string{"tar.gz", "tar.bz2", "tar.xz", "tar.zst", "zip", "tar", "gz", "bz2", "xz", "7z", "rar", "iso", "img", "bin"},
			Path:       "~/Downloads/Compressed",
		},
		"Programs": {
			Extensions: []string{"deb", "rpm", "appimage", "apk", "xapk", "snap", "exe", "msi"},
			Path:       "~/Downloads/Programs",
		},
		"Documents": {
			Extensions: []string{"pdf", "epub", "djvu", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "csv"},
			Path:       "~/Documents",
		},
	}
}

// DefaultConfig returns the baseline configuration for Hydra.
func DefaultConfig() *Config {
	return &Config{
		MaxConcurrentDownloads: 2,
		DefaultThreads:         8,
		MaxThreads:             32,
		DefaultSavePath:        "~/Downloads",
		SpeedLimitBytes:        0,
		DisableAutoCategorize:  false,
		Schedule: ScheduleConfig{
			Enabled:            false,
			StartTime:          "01:00",
			EndTime:            "07:00",
			DefaultTime:        "23:00",
			PauseOutsideWindow: false,
		},
		Categories: DefaultCategoryRules(),
	}
}

// Clone creates an independent deep copy of Config so callers cannot race on shared maps/slices.
func (c *Config) Clone() *Config {
	if c == nil {
		return DefaultConfig()
	}
	copyCfg := *c
	copyCfg.Categories = make(map[string]CategoryRule, len(c.Categories))
	for name, rule := range c.Categories {
		extsCopy := append([]string(nil), rule.Extensions...)
		ruleCopy := rule
		ruleCopy.Extensions = extsCopy
		copyCfg.Categories[name] = ruleCopy
	}
	return &copyCfg
}

func resolveConfigPathNoCreate() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "hydra", "config.json")
}

// GetConfigPath returns the absolute path to ~/.config/hydra/config.json.
func GetConfigPath() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "config.json"), nil
}

// ParseClockMinutes parses a strict 24-hour "HH:MM" clock string into minutes since midnight.
func ParseClockMinutes(raw string) (int, error) {
	cleaned := strings.TrimSpace(raw)
	parts := strings.Split(cleaned, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("time must use HH:MM 24-hour format (got %q)", raw)
	}
	hour, errH := strconv.Atoi(parts[0])
	minute, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("time out of range 00:00-23:59 (got %q)", raw)
	}
	return hour*60 + minute, nil
}

// IsWithinScheduleWindow returns true if queue promotion is allowed at the provided local time.
func IsWithinScheduleWindow(sched ScheduleConfig, now time.Time) bool {
	if !sched.Enabled {
		return true
	}

	startMin, errStart := ParseClockMinutes(sched.StartTime)
	endMin, errEnd := ParseClockMinutes(sched.EndTime)
	if errStart != nil || errEnd != nil {
		return true
	}

	if startMin == endMin {
		return true
	}

	localNow := now.Local()
	curMin := localNow.Hour()*60 + localNow.Minute()

	if startMin < endMin {
		return curMin >= startMin && curMin < endMin
	}
	// Overnight window crossing midnight (e.g. 23:00 -> 07:00)
	return curMin >= startMin || curMin < endMin
}

func cleanExtensions(extensions []string) []string {
	seen := make(map[string]bool, len(extensions))
	cleaned := make([]string, 0, len(extensions))
	for _, ext := range extensions {
		norm := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ext, ".")))
		if norm == "" || strings.ContainsAny(norm, "/\\\x00") || seen[norm] {
			continue
		}
		seen[norm] = true
		cleaned = append(cleaned, norm)
	}
	return cleaned
}

func fillConfigDefaults(cfg *Config) {
	def := DefaultConfig()
	if cfg.MaxConcurrentDownloads < 1 {
		cfg.MaxConcurrentDownloads = def.MaxConcurrentDownloads
	}
	if cfg.DefaultThreads < 1 {
		cfg.DefaultThreads = def.DefaultThreads
	}
	if cfg.MaxThreads < cfg.DefaultThreads {
		cfg.MaxThreads = cfg.DefaultThreads
	}
	if strings.TrimSpace(cfg.DefaultSavePath) == "" {
		cfg.DefaultSavePath = def.DefaultSavePath
	}
	if strings.TrimSpace(cfg.Schedule.StartTime) == "" {
		cfg.Schedule.StartTime = def.Schedule.StartTime
	}
	if strings.TrimSpace(cfg.Schedule.EndTime) == "" {
		cfg.Schedule.EndTime = def.Schedule.EndTime
	}
	if strings.TrimSpace(cfg.Schedule.DefaultTime) == "" {
		cfg.Schedule.DefaultTime = def.Schedule.DefaultTime
	}
	if cfg.Categories == nil {
		cfg.Categories = DefaultCategoryRules()
	}
}

// NormalizeAndValidateConfig validates settings ranges, folders, scheduling times, and rules.
func NormalizeAndValidateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	if cfg.MaxConcurrentDownloads < 1 || cfg.MaxConcurrentDownloads > 64 {
		return fmt.Errorf("max concurrent downloads must be between 1 and 64")
	}
	if cfg.DefaultThreads < 1 || cfg.DefaultThreads > 64 {
		return fmt.Errorf("default connections must be between 1 and 64")
	}
	if cfg.MaxThreads < 1 || cfg.MaxThreads > 64 {
		return fmt.Errorf("max connections must be between 1 and 64")
	}
	if cfg.MaxThreads < cfg.DefaultThreads {
		return fmt.Errorf("max connections (%d) cannot be lower than default connections (%d)", cfg.MaxThreads, cfg.DefaultThreads)
	}
	if cfg.SpeedLimitBytes < 0 {
		return fmt.Errorf("speed limit cannot be negative")
	}

	cfg.DefaultSavePath = strings.TrimSpace(cfg.DefaultSavePath)
	if cfg.DefaultSavePath == "" || strings.ContainsRune(cfg.DefaultSavePath, '\x00') {
		return fmt.Errorf("default save folder cannot be empty")
	}

	if strings.TrimSpace(cfg.Schedule.StartTime) == "" {
		cfg.Schedule.StartTime = "01:00"
	} else {
		cfg.Schedule.StartTime = strings.TrimSpace(cfg.Schedule.StartTime)
	}
	if strings.TrimSpace(cfg.Schedule.EndTime) == "" {
		cfg.Schedule.EndTime = "07:00"
	} else {
		cfg.Schedule.EndTime = strings.TrimSpace(cfg.Schedule.EndTime)
	}
	cfg.Schedule.DefaultTime = strings.TrimSpace(cfg.Schedule.DefaultTime)

	if _, err := ParseClockMinutes(cfg.Schedule.StartTime); err != nil {
		return fmt.Errorf("invalid schedule start time: %w", err)
	}
	if _, err := ParseClockMinutes(cfg.Schedule.EndTime); err != nil {
		return fmt.Errorf("invalid schedule end time: %w", err)
	}
	if cfg.Schedule.DefaultTime != "" {
		if _, err := ParseClockMinutes(cfg.Schedule.DefaultTime); err != nil {
			return fmt.Errorf("invalid default scheduled time: %w", err)
		}
	}

	if cfg.Categories == nil {
		cfg.Categories = make(map[string]CategoryRule)
	}

	normalized := make(map[string]CategoryRule, len(cfg.Categories))
	for rawName, rule := range cfg.Categories {
		name := strings.TrimSpace(rawName)
		if name == "" || strings.ContainsAny(name, "|\x00\r\n") {
			return fmt.Errorf("invalid category rule name %q", rawName)
		}
		path := strings.TrimSpace(rule.Path)
		if path == "" || strings.ContainsRune(path, '\x00') {
			return fmt.Errorf("category rule %q must have a valid destination folder", name)
		}
		if rule.DefaultThreads < 0 || rule.DefaultThreads > 64 {
			return fmt.Errorf("category rule %q connections override must be 0 or 1-64", name)
		}
		if rule.SpeedLimitBytes < 0 {
			return fmt.Errorf("category rule %q speed limit cannot be negative", name)
		}
		rule.Path = path
		rule.Extensions = cleanExtensions(rule.Extensions)
		normalized[name] = rule
	}
	cfg.Categories = normalized
	return nil
}

// LoadConfig loads or initializes configuration from disk.
func LoadConfig() (*Config, error) {
	configMutex.Lock()
	defer configMutex.Unlock()

	configPath, err := GetConfigPath()
	if err != nil {
		globalConfig = DefaultConfig()
		globalConfigPath = ""
		return globalConfig.Clone(), err
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		globalConfig = DefaultConfig()
		globalConfigPath = configPath
		_ = saveConfigUnlocked(globalConfig, configPath)
		return globalConfig.Clone(), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		globalConfig = DefaultConfig()
		globalConfigPath = configPath
		return globalConfig.Clone(), err
	}

	cfg := DefaultConfig()
	// Decode categories cleanly if specified in file (don't merge stale defaults over deliberately deleted rules).
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err == nil {
		if _, hasCats := rawMap["categories"]; hasCats {
			cfg.Categories = nil
		}
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		globalConfig = DefaultConfig()
		globalConfigPath = configPath
		return globalConfig.Clone(), err
	}

	fillConfigDefaults(cfg)
	globalConfig = cfg
	globalConfigPath = configPath
	return globalConfig.Clone(), nil
}

// GetConfig returns a thread-safe snapshot of the active configuration.
func GetConfig() *Config {
	expectedPath := resolveConfigPathNoCreate()

	configMutex.RLock()
	if globalConfig != nil && expectedPath != "" && globalConfigPath == expectedPath {
		snap := globalConfig.Clone()
		configMutex.RUnlock()
		return snap
	}
	configMutex.RUnlock()

	configMutex.Lock()
	defer configMutex.Unlock()

	if globalConfig != nil && expectedPath != "" && globalConfigPath == expectedPath {
		return globalConfig.Clone()
	}

	if expectedPath != "" {
		if data, err := os.ReadFile(expectedPath); err == nil {
			cfg := DefaultConfig()
			var rawMap map[string]json.RawMessage
			if uErr := json.Unmarshal(data, &rawMap); uErr == nil {
				if _, hasCats := rawMap["categories"]; hasCats {
					cfg.Categories = nil
				}
			}
			if uErr := json.Unmarshal(data, cfg); uErr == nil {
				fillConfigDefaults(cfg)
				globalConfig = cfg
				globalConfigPath = expectedPath
				return globalConfig.Clone()
			}
		}
	}

	globalConfig = DefaultConfig()
	globalConfigPath = expectedPath
	return globalConfig.Clone()
}

// SaveConfig validates and atomically writes the updated configuration to disk.
func SaveConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	toSave := cfg.Clone()
	if err := NormalizeAndValidateConfig(toSave); err != nil {
		return err
	}

	configMutex.Lock()
	defer configMutex.Unlock()

	configPath, err := GetConfigPath()
	if err != nil {
		return err
	}

	if err := saveConfigUnlocked(toSave, configPath); err != nil {
		return err
	}
	globalConfig = toSave
	globalConfigPath = configPath
	return nil
}

func saveConfigUnlocked(cfg *Config, configPath string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(configPath)
	tmp, err := os.CreateTemp(dir, ".config.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config data: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config file: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}

func filenameMatchesExtension(lowerBase string, ext string) bool {
	if lowerBase == "" || ext == "" {
		return false
	}
	suffix := "." + ext
	return len(lowerBase) > len(suffix) && strings.HasSuffix(lowerBase, suffix)
}

// MatchDownloadRuleInConfig deterministically resolves category routing, thread, and speed defaults.
func MatchDownloadRuleInConfig(cfg *Config, filename string) MatchedRule {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	defaultDir := strings.TrimSpace(cfg.DefaultSavePath)
	if defaultDir == "" {
		defaultDir = "~/Downloads"
	}

	defaultThreads := cfg.DefaultThreads
	if defaultThreads < 1 {
		defaultThreads = 8
	}
	if cfg.MaxThreads > 0 && defaultThreads > cfg.MaxThreads {
		defaultThreads = cfg.MaxThreads
	}

	baseResult := MatchedRule{
		Category:           "Default",
		DestinationDir:     defaultDir,
		EffectiveThreads:   defaultThreads,
		EffectiveSpeedBps:  cfg.SpeedLimitBytes,
		AutoCategorizeUsed: !cfg.DisableAutoCategorize,
	}

	if cfg.DisableAutoCategorize || len(cfg.Categories) == 0 {
		return baseResult
	}

	lowerBase := strings.ToLower(strings.TrimSpace(filepath.Base(filename)))
	if lowerBase == "" || lowerBase == "." || lowerBase == ".." {
		return baseResult
	}

	type candidate struct {
		name string
		rule CategoryRule
		ext  string
	}

	matches := make([]candidate, 0, 2)
	names := make([]string, 0, len(cfg.Categories))
	for name := range cfg.Categories {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		rule := cfg.Categories[name]
		if rule.Disabled || strings.TrimSpace(rule.Path) == "" {
			continue
		}
		bestExt := ""
		for _, rawExt := range rule.Extensions {
			ext := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(rawExt, ".")))
			if filenameMatchesExtension(lowerBase, ext) && len(ext) > len(bestExt) {
				bestExt = ext
			}
		}
		if bestExt != "" {
			matches = append(matches, candidate{
				name: name,
				rule: rule,
				ext:  bestExt,
			})
		}
	}

	if len(matches) == 0 {
		return baseResult
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].rule.Priority != matches[j].rule.Priority {
			return matches[i].rule.Priority > matches[j].rule.Priority
		}
		if len(matches[i].ext) != len(matches[j].ext) {
			return len(matches[i].ext) > len(matches[j].ext)
		}
		return matches[i].name < matches[j].name
	})

	winner := matches[0]
	effThreads := defaultThreads
	if winner.rule.DefaultThreads > 0 {
		effThreads = winner.rule.DefaultThreads
		if cfg.MaxThreads > 0 && effThreads > cfg.MaxThreads {
			effThreads = cfg.MaxThreads
		}
	}

	effSpeed := cfg.SpeedLimitBytes
	if winner.rule.SpeedLimitBytes > 0 {
		effSpeed = winner.rule.SpeedLimitBytes
	}

	return MatchedRule{
		Category:           winner.name,
		Rule:               winner.rule,
		DestinationDir:     winner.rule.Path,
		MatchedExtension:   winner.ext,
		EffectiveThreads:   effThreads,
		EffectiveSpeedBps:  effSpeed,
		AutoCategorizeUsed: true,
	}
}

// ResolveDownloadRule resolves the active rule and transfer defaults for a filename.
func ResolveDownloadRule(filename string) MatchedRule {
	return MatchDownloadRuleInConfig(GetConfig(), filename)
}

// ResolveCategoryPath returns the mapped folder for a given filename based on extension.
func ResolveCategoryPath(filename string) string {
	return ResolveDownloadRule(filename).DestinationDir
}

// SetCategoryRule adds or updates a file category routing rule.
func SetCategoryRule(name string, path string, extensions []string) error {
	return SetCategoryRuleDetailed(name, CategoryRule{
		Extensions: extensions,
		Path:       path,
	})
}

// SetCategoryRuleDetailed adds or updates a category rule including optional priority and transfer defaults.
func SetCategoryRuleDetailed(name string, rule CategoryRule) error {
	cleanedName := strings.TrimSpace(name)
	if cleanedName == "" {
		return fmt.Errorf("category rule name cannot be empty")
	}

	cfg := GetConfig()
	if cfg.Categories == nil {
		cfg.Categories = make(map[string]CategoryRule)
	}
	rule.Extensions = cleanExtensions(rule.Extensions)
	rule.Path = strings.TrimSpace(rule.Path)
	cfg.Categories[cleanedName] = rule

	return SaveConfig(cfg)
}

// DeleteCategoryRule removes a category routing rule.
func DeleteCategoryRule(name string) error {
	cfg := GetConfig()
	if cfg.Categories == nil {
		return nil
	}
	delete(cfg.Categories, name)
	return SaveConfig(cfg)
}

// ResetCategoryRulesToDefault restores built-in global file-type download rules.
func ResetCategoryRulesToDefault() error {
	cfg := GetConfig()
	cfg.Categories = DefaultCategoryRules()
	return SaveConfig(cfg)
}

// RegisterActiveLimiter registers a live job RateLimiter for dynamic global/rule limit updates.
func RegisterActiveLimiter(jobID, filename string, hasJobOverride bool, rl SpeedLimiterTarget) {
	if jobID == "" || rl == nil {
		return
	}
	activeLimitersMu.Lock()
	defer activeLimitersMu.Unlock()
	activeLimiters[jobID] = activeLimiterEntry{
		filename:       filename,
		hasJobOverride: hasJobOverride,
		limiter:        rl,
	}
}

// UnregisterActiveLimiter removes a finished/paused job's limiter from the live registry.
func UnregisterActiveLimiter(jobID string) {
	if jobID == "" {
		return
	}
	activeLimitersMu.Lock()
	defer activeLimitersMu.Unlock()
	delete(activeLimiters, jobID)
}

// ApplyConfigSpeedLimits updates all active jobs that do not have an explicit per-job speed limit.
func ApplyConfigSpeedLimits(cfg *Config) {
	if cfg == nil {
		cfg = GetConfig()
	}
	activeLimitersMu.Lock()
	snapshot := make([]activeLimiterEntry, 0, len(activeLimiters))
	for _, entry := range activeLimiters {
		snapshot = append(snapshot, entry)
	}
	activeLimitersMu.Unlock()

	for _, entry := range snapshot {
		if entry.hasJobOverride || entry.limiter == nil {
			continue
		}
		matched := MatchDownloadRuleInConfig(cfg, entry.filename)
		entry.limiter.SetLimit(matched.EffectiveSpeedBps)
	}
}
