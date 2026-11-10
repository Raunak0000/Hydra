package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func captureNotifications(t *testing.T) *[]DesktopNotification {
	t.Helper()
	var (
		mu     sync.Mutex
		events []DesktopNotification
	)
	SetNotificationHandler(func(n DesktopNotification) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, n)
	})
	t.Cleanup(func() {
		SetNotificationHandler(nil)
	})
	return &events
}

func TestNotificationHelpers_EventMetadata(t *testing.T) {
	captured := captureNotifications(t)

	NotifyDownloadQueued("ubuntu.iso", "/tmp/ubuntu.iso")
	NotifyBatchQueued(3, "/tmp/batch")
	NotifyDownloadScheduled("nightly.tar.gz", "Tue, 10 Nov 2026")
	NotifyDownloadComplete("ubuntu.iso", "/tmp/ubuntu.iso")
	NotifyDownloadFailed("broken.zip", "HTTP 404 Not Found")
	NotifyChecksumFailed("corrupt.tar.gz")
	NotifyPendingPath("archive.zip")

	got := *captured
	if len(got) != 7 {
		t.Fatalf("expected 7 notifications, got %d", len(got))
	}

	if got[0].Event != "queued" || got[0].Category != "transfer" || !strings.Contains(got[0].Message, "ubuntu.iso") {
		t.Errorf("unexpected queued notification: %+v", got[0])
	}
	if got[1].Event != "queued" || !strings.Contains(got[1].Message, "3 downloads") {
		t.Errorf("unexpected batch queued notification: %+v", got[1])
	}
	if got[2].Event != "queued" || !strings.Contains(got[2].Message, "Scheduled for:") {
		t.Errorf("unexpected scheduled notification: %+v", got[2])
	}
	if got[3].Event != "completed" || got[3].Category != "transfer.complete" || !strings.Contains(got[3].Message, "/tmp/ubuntu.iso") {
		t.Errorf("unexpected completed notification: %+v", got[3])
	}
	if got[4].Event != "failed" || got[4].Category != "transfer.error" || got[4].Urgency != "critical" {
		t.Errorf("unexpected failed notification: %+v", got[4])
	}
	if got[5].Event != "failed" || !strings.Contains(got[5].Title, "Checksum") {
		t.Errorf("unexpected checksum failed notification: %+v", got[5])
	}
	if got[6].Event != "pending_path" || !strings.Contains(got[6].Message, "archive.zip") {
		t.Errorf("unexpected pending path notification: %+v", got[6])
	}
}

func TestSendNativeNotification_BackendsAndFallback(t *testing.T) {
	t.Run("prefers notify-send with desktop entry and category", func(t *testing.T) {
		var calledBin string
		var calledArgs []string

		lookPath := func(name string) (string, error) {
			if name == "notify-send" {
				return "/usr/bin/notify-send", nil
			}
			return "", exec.ErrNotFound
		}
		cmdFactory := func(ctx context.Context, name string, args ...string) *exec.Cmd {
			calledBin = name
			calledArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "true")
		}

		err := sendNativeNotification(DesktopNotification{
			Event:    "completed",
			Title:    "Download Finished",
			Message:  "file.bin",
			Urgency:  "normal",
			Category: "transfer.complete",
			Icon:     "hydra",
		}, lookPath, cmdFactory)
		if err != nil {
			t.Fatalf("sendNativeNotification error: %v", err)
		}
		if calledBin != "/usr/bin/notify-send" {
			t.Fatalf("called binary = %q, want /usr/bin/notify-send", calledBin)
		}
		if !slices.Contains(calledArgs, "string:desktop-entry:hydra") {
			t.Errorf("missing desktop-entry hint in args: %v", calledArgs)
		}
		if !slices.Contains(calledArgs, "transfer.complete") {
			t.Errorf("missing category in args: %v", calledArgs)
		}
	})

	t.Run("falls back to gdbus when notify-send is unavailable", func(t *testing.T) {
		var calledBin string
		var calledArgs []string

		lookPath := func(name string) (string, error) {
			if name == "gdbus" {
				return "/usr/bin/gdbus", nil
			}
			return "", exec.ErrNotFound
		}
		cmdFactory := func(ctx context.Context, name string, args ...string) *exec.Cmd {
			calledBin = name
			calledArgs = append([]string(nil), args...)
			return exec.CommandContext(ctx, "true")
		}

		err := sendNativeNotification(DesktopNotification{
			Event:    "failed",
			Title:    "Download Failed",
			Message:  "timeout",
			Urgency:  "critical",
			Category: "transfer.error",
			Icon:     "hydra",
		}, lookPath, cmdFactory)
		if err != nil {
			t.Fatalf("sendNativeNotification gdbus fallback error: %v", err)
		}
		if calledBin != "/usr/bin/gdbus" {
			t.Fatalf("called binary = %q, want /usr/bin/gdbus", calledBin)
		}
		if !slices.Contains(calledArgs, "org.freedesktop.Notifications.Notify") {
			t.Errorf("unexpected gdbus args: %v", calledArgs)
		}
	})

	t.Run("missing notification daemons degrades gracefully", func(t *testing.T) {
		lookPath := func(string) (string, error) {
			return "", exec.ErrNotFound
		}
		cmdFactory := func(ctx context.Context, name string, args ...string) *exec.Cmd {
			t.Fatalf("unexpected command execution: %s", name)
			return nil
		}

		err := sendNativeNotification(DesktopNotification{
			Event:   "queued",
			Title:   "Download Queued",
			Message: "test.zip",
		}, lookPath, cmdFactory)
		if err != nil {
			t.Fatalf("expected nil error when notification tools unavailable, got %v", err)
		}
	})
}

func TestQueueManager_PermanentFailureEmitsSingleNotification(t *testing.T) {
	store := newTestDBStore(t)

	job := &models.UIJob{
		ID:       "job-fail-once",
		FileName: "broken-archive.tar.gz",
		URL:      "https://example.com/broken.tar.gz",
		SavePath: filepath.Join(t.TempDir(), "broken-archive.tar.gz"),
		Status:   "QUEUED",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	var mu sync.Mutex
	var failedEvents []DesktopNotification
	SetNotificationHandler(func(n DesktopNotification) {
		if n.Event == "failed" {
			mu.Lock()
			failedEvents = append(failedEvents, n)
			mu.Unlock()
		}
	})
	t.Cleanup(func() {
		SetNotificationHandler(nil)
	})

	attempts := 0
	qm := NewQueueManager(1, func(url, savePath, jobID string, headers map[string]string) error {
		attempts++
		return fmt.Errorf("HTTP 503 service unavailable")
	})
	qm.db = store
	qm.initialBackoff = 5 * time.Millisecond
	t.Cleanup(qm.Stop)

	qm.processJobWithRetry(*job)

	if attempts != 3 {
		t.Fatalf("expected 3 retry attempts, got %d", attempts)
	}

	saved, exists := store.GetJob(job.ID)
	if !exists || saved.Status != "FAILED" {
		t.Fatalf("expected FAILED status, got exists=%v status=%q", exists, saved.Status)
	}
	if !strings.Contains(saved.ErrorMessage, "503") {
		t.Fatalf("expected persisted error message, got %q", saved.ErrorMessage)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(failedEvents) != 1 {
		t.Fatalf("expected exactly 1 failure notification after retries exhausted, got %d", len(failedEvents))
	}
	if !strings.Contains(failedEvents[0].Message, "broken-archive.tar.gz") {
		t.Errorf("unexpected notification body: %q", failedEvents[0].Message)
	}
}

func TestQueueManager_PausedJobDoesNotEmitFailureNotification(t *testing.T) {
	store := newTestDBStore(t)

	job := &models.UIJob{
		ID:       "job-pause-test",
		FileName: "movie.mkv",
		URL:      "https://example.com/movie.mkv",
		SavePath: filepath.Join(t.TempDir(), "movie.mkv"),
		Status:   "QUEUED",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	var mu sync.Mutex
	var failedCount int
	SetNotificationHandler(func(n DesktopNotification) {
		if n.Event == "failed" {
			mu.Lock()
			failedCount++
			mu.Unlock()
		}
	})
	t.Cleanup(func() {
		SetNotificationHandler(nil)
	})

	qm := NewQueueManager(1, func(url, savePath, jobID string, headers map[string]string) error {
		_ = store.UpdateStatus(jobID, "PAUSED")
		return context.Canceled
	})
	qm.db = store
	qm.initialBackoff = 5 * time.Millisecond
	t.Cleanup(qm.Stop)

	qm.processJobWithRetry(*job)

	mu.Lock()
	defer mu.Unlock()
	if failedCount != 0 {
		t.Fatalf("paused job emitted %d failure notifications, want 0", failedCount)
	}
}

func TestQueueManager_SchedulerPromotionEmitsQueuedNotification(t *testing.T) {
	store := newTestDBStore(t)
	past := time.Now().Add(-5 * time.Second)
	job := &models.UIJob{
		ID:          "job-sched-due",
		FileName:    "report.pdf",
		URL:         "https://example.com/report.pdf",
		SavePath:    filepath.Join(t.TempDir(), "report.pdf"),
		Status:      "SCHEDULED",
		ScheduledAt: &past,
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	var mu sync.Mutex
	var queuedEvents []DesktopNotification
	SetNotificationHandler(func(n DesktopNotification) {
		if n.Event == "queued" {
			mu.Lock()
			queuedEvents = append(queuedEvents, n)
			mu.Unlock()
		}
	})
	t.Cleanup(func() {
		SetNotificationHandler(nil)
	})

	qm := NewQueueManager(1, nil)
	qm.db = store
	t.Cleanup(qm.Stop)

	qm.promoteScheduledJobs()

	updated, exists := store.GetJob(job.ID)
	if !exists || updated.Status != "QUEUED" {
		t.Fatalf("expected SCHEDULED job to promote to QUEUED, got exists=%v status=%q", exists, updated.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(queuedEvents) != 1 {
		t.Fatalf("expected 1 queued notification, got %d", len(queuedEvents))
	}
	if !strings.Contains(queuedEvents[0].Message, "report.pdf") {
		t.Errorf("unexpected queued notification body: %q", queuedEvents[0].Message)
	}
}

func TestServer_EnqueueEmitsQueuedNotificationAndPauseResumeAll(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "test-notify-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	// Ensure QueueManager slots look occupied so ProcessNext won't spawn workers during HTTP enqueue test.
	originalQM := GlobalQueueManager
	busyQM := NewQueueManager(1, nil)
	atomic.StoreInt32(&busyQM.active, 1)
	GlobalQueueManager = busyQM
	t.Cleanup(func() {
		atomic.StoreInt32(&busyQM.active, 0)
		busyQM.Stop()
		GlobalQueueManager = originalQM
	})

	store := newTestDBStore(t)
	server := NewServer(nil)
	server.db = store

	var mu sync.Mutex
	var events []DesktopNotification
	SetNotificationHandler(func(n DesktopNotification) {
		mu.Lock()
		events = append(events, n)
		mu.Unlock()
	})
	t.Cleanup(func() {
		SetNotificationHandler(nil)
	})

	destFile := filepath.Join(t.TempDir(), "linux-installer.iso")
	body := fmt.Sprintf(`{"url":"https://example.com/linux-installer.iso","save_path":%q}`, destFile)
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hydra-Token", "test-notify-token")

	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/jobs status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}

	mu.Lock()
	queuedCount := 0
	for _, ev := range events {
		if ev.Event == "queued" {
			queuedCount++
		}
	}
	mu.Unlock()
	if queuedCount != 1 {
		t.Fatalf("expected 1 queued notification from /api/jobs, got %d", queuedCount)
	}

	// Test pause-all authentication and behavior.
	unauthReq := httptest.NewRequest(http.MethodPost, "/api/jobs/pause-all", nil)
	unauthRec := httptest.NewRecorder()
	server.Router.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pause-all status = %d, want 401", unauthRec.Code)
	}

	pauseReq := httptest.NewRequest(http.MethodPost, "/api/jobs/pause-all", nil)
	pauseReq.Header.Set("X-Hydra-Token", "test-notify-token")
	pauseRec := httptest.NewRecorder()
	server.Router.ServeHTTP(pauseRec, pauseReq)
	if pauseRec.Code != http.StatusOK {
		t.Fatalf("pause-all status = %d, want 200 (%s)", pauseRec.Code, pauseRec.Body.String())
	}

	var pauseResp struct {
		Status string `json:"status"`
		Paused int    `json:"paused"`
	}
	if err := json.NewDecoder(pauseRec.Body).Decode(&pauseResp); err != nil {
		t.Fatalf("decode pause-all response: %v", err)
	}
	if pauseResp.Paused != 1 {
		t.Fatalf("paused count = %d, want 1", pauseResp.Paused)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/api/jobs/resume-all", nil)
	resumeReq.Header.Set("X-Hydra-Token", "test-notify-token")
	resumeRec := httptest.NewRecorder()
	server.Router.ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume-all status = %d, want 200 (%s)", resumeRec.Code, resumeRec.Body.String())
	}

	var resumeResp struct {
		Status  string `json:"status"`
		Resumed int    `json:"resumed"`
	}
	if err := json.NewDecoder(resumeRec.Body).Decode(&resumeResp); err != nil {
		t.Fatalf("decode resume-all response: %v", err)
	}
	if resumeResp.Resumed != 1 {
		t.Fatalf("resumed count = %d, want 1", resumeResp.Resumed)
	}
}

func TestQueueManager_PauseUnlocksWaitingQueuedJob(t *testing.T) {
	store := newTestDBStore(t)
	tmpDir := t.TempDir()

	job1 := &models.UIJob{
		ID:       "job-active-1",
		FileName: "first.bin",
		URL:      "https://example.com/first.bin",
		SavePath: filepath.Join(tmpDir, "first.bin"),
		Status:   "QUEUED",
	}
	job2 := &models.UIJob{
		ID:       "job-queued-2",
		FileName: "second.bin",
		URL:      "https://example.com/second.bin",
		SavePath: filepath.Join(tmpDir, "second.bin"),
		Status:   "QUEUED",
	}
	if err := store.SaveJob(job1); err != nil {
		t.Fatalf("SaveJob(job1): %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := store.SaveJob(job2); err != nil {
		t.Fatalf("SaveJob(job2): %v", err)
	}

	job1Started := make(chan struct{})
	releaseJob1 := make(chan struct{})
	job2Completed := make(chan struct{})

	qm := NewQueueManager(1, func(url, savePath, jobID string, headers map[string]string) error {
		if jobID == job1.ID {
			close(job1Started)
			<-releaseJob1
			return context.Canceled
		}
		return nil
	})
	qm.db = store
	qm.initialBackoff = 5 * time.Millisecond
	t.Cleanup(qm.Stop)

	qm.ProcessNext()

	select {
	case <-job1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("job1 did not start")
	}

	// Simulate pausing job1 while job2 is queued waiting on maxConcurrent=1.
	_ = store.UpdateStatus(job1.ID, "PAUSED")
	close(releaseJob1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got2, ok := store.GetJob(job2.ID)
		if ok && got2.Status == "COMPLETED" {
			close(job2Completed)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case <-job2Completed:
	default:
		got2, _ := store.GetJob(job2.ID)
		t.Fatalf("job2 stayed in status %q instead of completing after job1 paused", got2.Status)
	}
}

func TestQueueManager_NoDuplicateLaunchUnderConcurrentProcessNext(t *testing.T) {
	store := newTestDBStore(t)
	job := &models.UIJob{
		ID:       "job-dedup-launch",
		FileName: "single.mkv",
		URL:      "https://example.com/single.mkv",
		SavePath: filepath.Join(t.TempDir(), "single.mkv"),
		Status:   "QUEUED",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	var runs atomic.Int32
	gate := make(chan struct{})
	qm := NewQueueManager(2, func(url, savePath, jobID string, headers map[string]string) error {
		runs.Add(1)
		<-gate
		return nil
	})
	qm.db = store
	t.Cleanup(qm.Stop)

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			qm.ProcessNext()
		}()
	}
	wg.Wait()
	time.Sleep(30 * time.Millisecond)

	if got := runs.Load(); got != 1 {
		close(gate)
		t.Fatalf("expected job to run exactly once under concurrent ProcessNext calls, got %d", got)
	}
	close(gate)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if qm.ActiveCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if active := qm.ActiveCount(); active != 0 {
		t.Fatalf("expected active count to return to 0, got %d", active)
	}
}

func TestQueueManager_DeletedJobStopsRetryImmediately(t *testing.T) {
	store := newTestDBStore(t)
	job := &models.UIJob{
		ID:       "job-deleted-running",
		FileName: "ephemeral.zip",
		URL:      "https://example.com/ephemeral.zip",
		SavePath: filepath.Join(t.TempDir(), "ephemeral.zip"),
		Status:   "QUEUED",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	var failedNotifications atomic.Int32
	SetNotificationHandler(func(n DesktopNotification) {
		if n.Event == "failed" {
			failedNotifications.Add(1)
		}
	})
	t.Cleanup(func() {
		SetNotificationHandler(nil)
	})

	var attempts atomic.Int32
	qm := NewQueueManager(1, func(url, savePath, jobID string, headers map[string]string) error {
		attempts.Add(1)
		_ = store.DeleteJob(jobID)
		return context.Canceled
	})
	qm.db = store
	qm.initialBackoff = 5 * time.Millisecond
	t.Cleanup(qm.Stop)

	qm.processJobWithRetry(*job)

	if got := attempts.Load(); got != 1 {
		t.Fatalf("deleted job should stop after 1 attempt, got %d", got)
	}
	if got := failedNotifications.Load(); got != 0 {
		t.Fatalf("deleted job emitted %d failure notifications, want 0", got)
	}
}
