package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type CategoryRule struct {
	Extensions []string `json:"extensions"`
	Path       string   `json:"path"`
}

type Config struct {
	MaxConcurrentDownloads int                     `json:"max_concurrent_downloads"`
	DefaultThreads         int                     `json:"default_threads"`
	MaxThreads             int                     `json:"max_threads"`
	DefaultSavePath        string                  `json:"default_save_path"`
	SpeedLimitBytes        int64                   `json:"speed_limit_bytes"` // 0 = unlimited
	Categories             map[string]CategoryRule `json:"categories"`
}

var (
	globalConfig *Config
	configMutex  sync.RWMutex
)

// DefaultConfig returns the baseline configuration for Hydra
func DefaultConfig() *Config {
	return &Config{
		MaxConcurrentDownloads: 2,
		DefaultThreads:         8,
		MaxThreads:             32,
		DefaultSavePath:        "~/Downloads",
		SpeedLimitBytes:        0,
		Categories: map[string]CategoryRule{
			"Video": {
				Extensions: []string{"mp4", "mkv", "avi", "mov", "flv", "webm", "wmv", "m4v", "mpg", "mpeg"},
				Path:       "~/Videos",
			},
			"Music": {
				Extensions: []string{"mp3", "flac", "wav", "aac", "ogg", "m4a", "opus", "wma"},
				Path:       "~/Music",
			},
			"Compressed": {
				Extensions: []string{"zip", "tar", "gz", "bz2", "xz", "7z", "rar", "iso", "img", "bin"},
				Path:       "~/Downloads/Compressed",
			},
			"Programs": {
				Extensions: []string{"deb", "rpm", "appimage", "apk", "xapk", "snap", "exe", "msi"},
				Path:       "~/Downloads/Programs",
			},
			"Documents": {
				Extensions: []string{"pdf", "epub", "djvu", "doc", "docx", "xls", "xlsx"},
				Path:       "~/Documents",
			},
		},
	}
}

// GetConfigPath returns the absolute path to ~/.config/hydra/config.json
func GetConfigPath() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "config.json"), nil
}

// LoadConfig loads or initializes configuration from disk
func LoadConfig() (*Config, error) {
	configMutex.Lock()
	defer configMutex.Unlock()

	configPath, err := GetConfigPath()
	if err != nil {
		globalConfig = DefaultConfig()
		return globalConfig, err
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		globalConfig = DefaultConfig()
		_ = saveConfigUnlocked(globalConfig, configPath)
		return globalConfig, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		globalConfig = DefaultConfig()
		return globalConfig, err
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		globalConfig = DefaultConfig()
		return globalConfig, err
	}

	globalConfig = cfg
	return globalConfig, nil
}

// GetConfig returns the active cached configuration
func GetConfig() *Config {
	configMutex.RLock()
	defer configMutex.RUnlock()
	if globalConfig == nil {
		return DefaultConfig()
	}
	return globalConfig
}

// SaveConfig writes the updated configuration to disk
func SaveConfig(cfg *Config) error {
	configMutex.Lock()
	defer configMutex.Unlock()

	configPath, err := GetConfigPath()
	if err != nil {
		return err
	}

	globalConfig = cfg
	return saveConfigUnlocked(cfg, configPath)
}

func saveConfigUnlocked(cfg *Config, configPath string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return os.WriteFile(configPath, data, 0644)
}

// ResolveCategoryPath returns the mapped folder for a given filename based on extension
func ResolveCategoryPath(filename string) string {
	cfg := GetConfig()
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	if ext == "" {
		return cfg.DefaultSavePath
	}

	for _, rule := range cfg.Categories {
		for _, ruleExt := range rule.Extensions {
			if strings.EqualFold(ruleExt, ext) {
				return rule.Path
			}
		}
	}
	return cfg.DefaultSavePath
}
