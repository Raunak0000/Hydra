package storage

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestCategoryRules(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Initial default resolve
	if path := ResolveCategoryPath("movie.mkv"); path != "~/Videos" {
		t.Errorf("expected ~/Videos, got %s", path)
	}
	if path := ResolveCategoryPath("song.mp3"); path != "~/Music" {
		t.Errorf("expected ~/Music, got %s", path)
	}
	if path := ResolveCategoryPath("archive.tar.gz"); path != "~/Downloads/Compressed" {
		t.Errorf("expected ~/Downloads/Compressed, got %s", path)
	}
	if path := ResolveCategoryPath("unknown.xyz"); path != "~/Downloads" {
		t.Errorf("expected default ~/Downloads, got %s", path)
	}

	// Add custom category rule
	customPath := filepath.Join(tmpDir, "Books")
	if err := SetCategoryRule("Ebooks", customPath, []string{"xyzbook", "mobi", "azw3"}); err != nil {
		t.Fatalf("SetCategoryRule failed: %v", err)
	}

	if path := ResolveCategoryPath("novel.xyzbook"); path != customPath {
		t.Errorf("expected customPath %s, got %s", customPath, path)
	}
	if path := ResolveCategoryPath("kindle.MOBI"); path != customPath {
		t.Errorf("expected customPath %s for uppercase MOBI, got %s", customPath, path)
	}

	// Delete category rule
	if err := DeleteCategoryRule("Ebooks"); err != nil {
		t.Fatalf("DeleteCategoryRule failed: %v", err)
	}

	if path := ResolveCategoryPath("novel.xyzbook"); path != "~/Downloads" {
		t.Errorf("expected fallback ~/Downloads after deletion, got %s", path)
	}
}

func TestGlobalRulesPriorityCompoundDisableAndOverrides(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultSavePath = "/data/Downloads"
	cfg.DefaultThreads = 6
	cfg.MaxThreads = 16
	cfg.SpeedLimitBytes = 2048 * 1024

	// Higher priority custom rule beats built-in Video for mkv and applies rule overrides.
	cfg.Categories["4KMovies"] = CategoryRule{
		Extensions:      []string{".MKV"},
		Path:            "/mnt/media/4K",
		Priority:        50,
		DefaultThreads:  24, // clamped down to cfg.MaxThreads (16)
		SpeedLimitBytes: 512 * 1024,
	}
	// Compound extension rule beats shorter suffix ("gz" in Compressed) even when name sorts after it.
	cfg.Categories["ZZ_Bundles"] = CategoryRule{
		Extensions: []string{"bundle.gz"},
		Path:       "/mnt/bundles",
		Priority:   0,
	}

	movieMatch := MatchDownloadRuleInConfig(cfg, "Blade.Runner.mkv")
	if movieMatch.Category != "4KMovies" || movieMatch.DestinationDir != "/mnt/media/4K" {
		t.Fatalf("unexpected movie rule match: %+v", movieMatch)
	}
	if movieMatch.EffectiveThreads != 16 {
		t.Errorf("expected EffectiveThreads clamped to 16, got %d", movieMatch.EffectiveThreads)
	}
	if movieMatch.EffectiveSpeedBps != 512*1024 {
		t.Errorf("expected EffectiveSpeedBps 524288, got %d", movieMatch.EffectiveSpeedBps)
	}

	bundleMatch := MatchDownloadRuleInConfig(cfg, "hydra-assets.bundle.gz")
	if bundleMatch.Category != "ZZ_Bundles" || bundleMatch.DestinationDir != "/mnt/bundles" {
		t.Fatalf("expected compound extension bundle.gz to match ZZ_Bundles, got %+v", bundleMatch)
	}
	if bundleMatch.EffectiveThreads != 6 || bundleMatch.EffectiveSpeedBps != 2048*1024 {
		t.Errorf("expected rule to inherit global threads/speed, got %+v", bundleMatch)
	}

	// Disabling 4KMovies falls back deterministically to built-in Video.
	r := cfg.Categories["4KMovies"]
	r.Disabled = true
	cfg.Categories["4KMovies"] = r
	fallbackVideo := MatchDownloadRuleInConfig(cfg, "Blade.Runner.mkv")
	if fallbackVideo.Category != "Video" || fallbackVideo.DestinationDir != "~/Videos" {
		t.Fatalf("expected disabled rule to skip to Video, got %+v", fallbackVideo)
	}

	// Disabling auto-categorize globally routes everything to DefaultSavePath.
	cfg.DisableAutoCategorize = true
	globalFallback := MatchDownloadRuleInConfig(cfg, "Blade.Runner.mkv")
	if globalFallback.Category != "Default" || globalFallback.DestinationDir != "/data/Downloads" {
		t.Fatalf("expected global fallback /data/Downloads, got %+v", globalFallback)
	}
}

func TestRulesAPI(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	originalToken := hydraAPIToken
	hydraAPIToken = "test-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	server := NewServer(nil)

	// 1. GET /api/rules
	req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	req.Header.Set("X-Hydra-Token", "test-token")
	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/rules returned status %d", rec.Code)
	}

	var rules map[string]CategoryRule
	if err := json.NewDecoder(rec.Body).Decode(&rules); err != nil {
		t.Fatalf("failed to decode rules JSON: %v", err)
	}
	if len(rules) == 0 {
		t.Fatalf("expected default rules, got empty map")
	}

	// 2. POST /api/rules (Add detailed rule)
	addPayload := map[string]any{
		"name":              "Torrents",
		"path":              "/tmp/Torrents",
		"extensions":        []string{".torrent"},
		"priority":          20,
		"default_threads":   4,
		"speed_limit_bytes": 1048576,
	}
	bodyBytes, _ := json.Marshal(addPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/rules", bytes.NewReader(bodyBytes))
	req.Header.Set("X-Hydra-Token", "test-token")
	rec = httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/rules returned %d: %s", rec.Code, rec.Body.String())
	}

	if path := ResolveCategoryPath("linux.torrent"); path != "/tmp/Torrents" {
		t.Errorf("expected /tmp/Torrents, got %s", path)
	}

	// 3. GET /api/rules/match
	req = httptest.NewRequest(http.MethodGet, "/api/rules/match?filename=linux.torrent", nil)
	rec = httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/rules/match returned %d", rec.Code)
	}
	var matched MatchedRule
	if err := json.NewDecoder(rec.Body).Decode(&matched); err != nil {
		t.Fatalf("decode match: %v", err)
	}
	if matched.Category != "Torrents" || matched.EffectiveThreads != 4 || matched.EffectiveSpeedBps != 1048576 {
		t.Fatalf("unexpected rule match payload: %+v", matched)
	}

	// 4. DELETE /api/rules?name=Torrents
	req = httptest.NewRequest(http.MethodDelete, "/api/rules?name=Torrents", nil)
	req.Header.Set("X-Hydra-Token", "test-token")
	rec = httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/rules returned %d: %s", rec.Code, rec.Body.String())
	}

	if path := ResolveCategoryPath("linux.torrent"); path != "~/Downloads" {
		t.Errorf("expected ~/Downloads after delete, got %s", path)
	}

	// 5. POST /api/rules/reset restores built-in rules cleanly
	_ = DeleteCategoryRule("Video")
	if ResolveCategoryPath("film.mp4") != "~/Downloads" {
		t.Fatalf("expected ~/Downloads after deleting Video")
	}
	req = httptest.NewRequest(http.MethodPost, "/api/rules/reset", nil)
	req.Header.Set("X-Hydra-Token", "test-token")
	rec = httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/rules/reset returned %d: %s", rec.Code, rec.Body.String())
	}
	if ResolveCategoryPath("film.mp4") != "~/Videos" {
		t.Fatalf("expected ~/Videos after reset, got %s", ResolveCategoryPath("film.mp4"))
	}
}

type fakeSpeedLimiter struct {
	mu     sync.Mutex
	limits []int64
}

func (f *fakeSpeedLimiter) SetLimit(bps int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits = append(f.limits, bps)
}

func (f *fakeSpeedLimiter) Last() (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.limits) == 0 {
		return 0, false
	}
	return f.limits[len(f.limits)-1], true
}

func TestSettingsAPIValidationAndLiveEffects(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	originalToken := hydraAPIToken
	hydraAPIToken = "settings-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	server := NewServer(nil)

	// Requires token for POST /api/settings
	req := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(`{"max_concurrent_downloads":3}`))
	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token on POST /api/settings, got %d", rec.Code)
	}

	// Invalid values rejected cleanly with 400
	invalidPayloads := []string{
		`{"max_concurrent_downloads": 0}`,
		`{"default_threads": 16, "max_threads": 8}`,
		`{"speed_limit_bytes": -100}`,
		`{"default_save_path": "  "}`,
		`{"schedule": {"enabled": true, "start_time": "25:00", "end_time": "07:00"}}`,
	}
	for _, body := range invalidPayloads {
		badReq := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(body))
		badReq.Header.Set("X-Hydra-Token", "settings-token")
		badRec := httptest.NewRecorder()
		server.Router.ServeHTTP(badRec, badReq)
		if badRec.Code != http.StatusBadRequest {
			t.Errorf("payload %s expected 400, got %d (%s)", body, badRec.Code, badRec.Body.String())
		}
	}

	// Register live active limiters (one inheriting global config, one overriding)
	inheritedLimiter := &fakeSpeedLimiter{}
	jobOverrideLimiter := &fakeSpeedLimiter{}
	RegisterActiveLimiter("job-inherit", "movie.mkv", false, inheritedLimiter)
	RegisterActiveLimiter("job-override", "archive.zip", true, jobOverrideLimiter)
	t.Cleanup(func() {
		UnregisterActiveLimiter("job-inherit")
		UnregisterActiveLimiter("job-override")
	})

	// Valid partial update preserves existing categories & updates active inherited limiter
	updateBody := `{
		"max_concurrent_downloads": 4,
		"default_threads": 12,
		"max_threads": 24,
		"default_save_path": "/tmp/HydraDL",
		"speed_limit_bytes": 1048576,
		"schedule": {
			"enabled": true,
			"start_time": "01:00",
			"end_time": "06:30",
			"default_time": "22:45",
			"pause_outside_window": true
		}
	}`
	okReq := httptest.NewRequest(http.MethodPost, "/api/settings", bytes.NewBufferString(updateBody))
	okReq.Header.Set("X-Hydra-Token", "settings-token")
	okRec := httptest.NewRecorder()
	server.Router.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings returned %d: %s", okRec.Code, okRec.Body.String())
	}

	saved := GetConfig()
	if saved.MaxConcurrentDownloads != 4 || saved.DefaultThreads != 12 || saved.MaxThreads != 24 {
		t.Fatalf("unexpected thread/concurrency settings: %+v", saved)
	}
	if saved.DefaultSavePath != "/tmp/HydraDL" || saved.SpeedLimitBytes != 1048576 {
		t.Fatalf("unexpected path/speed settings: %+v", saved)
	}
	if !saved.Schedule.Enabled || saved.Schedule.StartTime != "01:00" || saved.Schedule.EndTime != "06:30" || !saved.Schedule.PauseOutsideWindow {
		t.Fatalf("unexpected schedule settings: %+v", saved)
	}
	if len(saved.Categories) == 0 {
		t.Fatalf("expected categories preserved on partial settings update")
	}

	if got, ok := inheritedLimiter.Last(); !ok || got != 1048576 {
		t.Errorf("expected inherited active limiter updated to 1048576, got %d (ok=%v)", got, ok)
	}
	if _, ok := jobOverrideLimiter.Last(); ok {
		t.Errorf("expected per-job overridden limiter NOT to be overwritten by global config")
	}
}

func TestScheduleWindowAndQueueEnforcement(t *testing.T) {
	loc := time.Local

	// Same-day window 01:00 -> 07:00
	sameDay := ScheduleConfig{Enabled: true, StartTime: "01:00", EndTime: "07:00"}
	if !IsWithinScheduleWindow(sameDay, time.Date(2026, 11, 11, 1, 0, 0, 0, loc)) {
		t.Errorf("01:00 should be inside [01:00, 07:00)")
	}
	if IsWithinScheduleWindow(sameDay, time.Date(2026, 11, 11, 7, 0, 0, 0, loc)) {
		t.Errorf("07:00 should be outside [01:00, 07:00)")
	}

	// Overnight window 23:00 -> 06:00
	overnight := ScheduleConfig{Enabled: true, StartTime: "23:00", EndTime: "06:00"}
	if !IsWithinScheduleWindow(overnight, time.Date(2026, 11, 11, 23, 30, 0, 0, loc)) {
		t.Errorf("23:30 should be inside overnight window")
	}
	if !IsWithinScheduleWindow(overnight, time.Date(2026, 11, 11, 2, 15, 0, 0, loc)) {
		t.Errorf("02:15 should be inside overnight window")
	}
	if IsWithinScheduleWindow(overnight, time.Date(2026, 11, 11, 14, 0, 0, 0, loc)) {
		t.Errorf("14:00 should be outside overnight window")
	}

	// QueueManager window gating & PauseOutsideWindow behavior
	store := newTestDBStore(t)
	_ = store.SaveJob(&models.UIJob{
		ID: "job-queued", FileName: "q.bin", URL: "https://example.com/q.bin", SavePath: "/tmp/q.bin", Status: "QUEUED",
	})
	_ = store.SaveJob(&models.UIJob{
		ID: "job-manual-paused", FileName: "m.bin", URL: "https://example.com/m.bin", SavePath: "/tmp/m.bin", Status: "PAUSED",
	})
	_ = store.SaveJob(&models.UIJob{
		ID: "job-running", FileName: "r.bin", URL: "https://example.com/r.bin", SavePath: "/tmp/r.bin", Status: "DOWNLOADING",
	})

	var started []string
	var startedMu sync.Mutex
	curTime := time.Date(2026, 11, 11, 12, 0, 0, 0, loc) // Outside 01:00-07:00
	var timeMu sync.Mutex

	qm := NewQueueManager(2, func(url, path, id string, headers map[string]string) error {
		startedMu.Lock()
		started = append(started, id)
		startedMu.Unlock()
		return nil
	})
	qm.db = store
	qm.scheduleProvider = func() ScheduleConfig {
		return ScheduleConfig{
			Enabled:            true,
			StartTime:          "01:00",
			EndTime:            "07:00",
			PauseOutsideWindow: true,
		}
	}
	qm.nowFn = func() time.Time {
		timeMu.Lock()
		defer timeMu.Unlock()
		return curTime
	}
	defer qm.Stop()

	// Outside window: ProcessNext must not dispatch queued job, and promoteScheduledJobs pauses job-running.
	qm.promoteScheduledJobs()

	startedMu.Lock()
	if len(started) != 0 {
		t.Fatalf("expected 0 jobs dispatched outside window, got %v", started)
	}
	startedMu.Unlock()

	if j, _ := store.GetJob("job-running"); j.Status != "PAUSED" {
		t.Fatalf("expected job-running to auto-pause outside window, got %s", j.Status)
	}

	// Window opens at 02:00: job-queued starts & scheduler-paused job-running resumes, while job-manual-paused stays PAUSED!
	timeMu.Lock()
	curTime = time.Date(2026, 11, 11, 2, 0, 0, 0, loc)
	timeMu.Unlock()

	qm.promoteScheduledJobs()
	time.Sleep(80 * time.Millisecond)

	if j, _ := store.GetJob("job-manual-paused"); j.Status != "PAUSED" {
		t.Errorf("manually paused job should remain PAUSED, got %s", j.Status)
	}
	startedMu.Lock()
	defer startedMu.Unlock()
	if len(started) < 2 {
		t.Fatalf("expected both job-queued and job-running to run when window opened, got %v", started)
	}
}
