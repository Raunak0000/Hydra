package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestServer_UpdateExistingJobDestinationPreservesHeadersAndGuardsState(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "test-update-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

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

	SetNotificationHandler(func(DesktopNotification) {})
	t.Cleanup(func() {
		SetNotificationHandler(nil)
	})

	// Seed a PENDING_PATH job captured with authenticated browser headers.
	initial := models.UIJob{
		ID:       "job_pending_1",
		FileName: "report.pdf",
		URL:      "https://example.com/report.pdf",
		SavePath: "PENDING",
		Status:   "PENDING_PATH",
		Headers: map[string]string{
			"Cookie":     "session=xyz",
			"User-Agent": "Firefox/130.0",
		},
	}
	if err := store.SaveJob(&initial); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	destDir := t.TempDir()
	reqBody, _ := json.Marshal(map[string]any{
		"job_id":    "job_pending_1",
		"save_path": destDir + "/",
		"filename":  "final-report.pdf",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hydra-Token", "test-update-token")

	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/jobs update status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	updated, exists := store.GetJob("job_pending_1")
	if !exists {
		t.Fatal("updated job disappeared")
	}
	expectedPath := filepath.Join(destDir, "final-report.pdf")
	if updated.SavePath != expectedPath {
		t.Fatalf("SavePath = %q, want %q", updated.SavePath, expectedPath)
	}
	if updated.FileName != "final-report.pdf" {
		t.Fatalf("FileName = %q, want final-report.pdf", updated.FileName)
	}
	if updated.Status != "QUEUED" {
		t.Fatalf("Status = %q, want QUEUED", updated.Status)
	}
	if updated.Headers["Cookie"] != "session=xyz" {
		t.Fatalf("existing headers were lost on destination update: %+v", updated.Headers)
	}

	// Reject destination updates while actively downloading.
	_ = store.UpdateStatus("job_pending_1", "DOWNLOADING")
	conflictReq := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(reqBody))
	conflictReq.Header.Set("Content-Type", "application/json")
	conflictReq.Header.Set("X-Hydra-Token", "test-update-token")
	conflictRec := httptest.NewRecorder()
	server.Router.ServeHTTP(conflictRec, conflictReq)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("active job update status = %d, want 409 Conflict (%s)", conflictRec.Code, conflictRec.Body.String())
	}
}

func TestServer_DesktopLifecycleActions(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "lifecycle-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

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
	SetNotificationHandler(func(DesktopNotification) {})
	t.Cleanup(func() { SetNotificationHandler(nil) })

	// 1. Delete with keep_file=true removes job record while preserving downloaded file on disk.
	dir := t.TempDir()
	keptFile := filepath.Join(dir, "kept.iso")
	if err := os.WriteFile(keptFile, []byte("complete-iso"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = store.SaveJob(&models.UIJob{
		ID: "job_keep", FileName: "kept.iso", URL: "https://example.com/kept.iso", SavePath: keptFile, Status: "COMPLETED", Progress: 100,
	})

	delReq := httptest.NewRequest(http.MethodDelete, "/api/jobs/job_keep?keep_file=true", nil)
	delReq.Header.Set("X-Hydra-Token", "lifecycle-token")
	delRec := httptest.NewRecorder()
	server.Router.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("DELETE keep_file=true status = %d: %s", delRec.Code, delRec.Body.String())
	}
	if _, exists := store.GetJob("job_keep"); exists {
		t.Fatal("expected job_keep record to be deleted")
	}
	if _, err := os.Stat(keptFile); err != nil {
		t.Fatalf("expected keptFile on disk when keep_file=true: %v", err)
	}

	// 2. Clear completed removes only COMPLETED jobs and keeps files intact.
	compFile := filepath.Join(dir, "done.zip")
	if err := os.WriteFile(compFile, []byte("zipdata"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = store.SaveJob(&models.UIJob{
		ID: "job_done", FileName: "done.zip", URL: "https://example.com/done.zip", SavePath: compFile, Status: "COMPLETED", Progress: 100,
	})
	_ = store.SaveJob(&models.UIJob{
		ID: "job_paused", FileName: "part.zip", URL: "https://example.com/part.zip", SavePath: filepath.Join(dir, "part.zip"), Status: "PAUSED",
	})

	clrReq := httptest.NewRequest(http.MethodPost, "/api/jobs/clear-completed", nil)
	clrReq.Header.Set("X-Hydra-Token", "lifecycle-token")
	clrRec := httptest.NewRecorder()
	server.Router.ServeHTTP(clrRec, clrReq)
	if clrRec.Code != http.StatusOK {
		t.Fatalf("clear-completed status = %d: %s", clrRec.Code, clrRec.Body.String())
	}
	if _, exists := store.GetJob("job_done"); exists {
		t.Fatal("expected COMPLETED job to be removed")
	}
	if _, exists := store.GetJob("job_paused"); !exists {
		t.Fatal("expected PAUSED job to remain")
	}
	if _, err := os.Stat(compFile); err != nil {
		t.Fatalf("expected completed file preserved on disk: %v", err)
	}

	// 3. Batch download honors start_paused=true.
	batchBody, _ := json.Marshal(map[string]any{
		"urls":         []string{"https://example.com/a.bin", "https://example.com/b.bin"},
		"save_path":    dir,
		"start_paused": true,
	})
	batchReq := httptest.NewRequest(http.MethodPost, "/api/batch/download", bytes.NewReader(batchBody))
	batchReq.Header.Set("Content-Type", "application/json")
	batchReq.Header.Set("X-Hydra-Token", "lifecycle-token")
	batchRec := httptest.NewRecorder()
	server.Router.ServeHTTP(batchRec, batchReq)
	if batchRec.Code != http.StatusAccepted {
		t.Fatalf("batch start_paused status = %d: %s", batchRec.Code, batchRec.Body.String())
	}
	var batchResp struct {
		DispatchedJobs []string `json:"dispatched_jobs"`
	}
	_ = json.NewDecoder(batchRec.Body).Decode(&batchResp)
	if len(batchResp.DispatchedJobs) != 2 {
		t.Fatalf("expected 2 dispatched batch jobs, got %d", len(batchResp.DispatchedJobs))
	}
	for _, id := range batchResp.DispatchedJobs {
		j, ok := store.GetJob(id)
		if !ok || j.Status != "PAUSED" {
			t.Fatalf("expected batch job %s PAUSED, got %+v", id, j)
		}
	}

	// 4. Clear schedule + reset speed limit + promote SCHEDULED via resume ("Start Now").
	future := time.Now().Add(2 * time.Hour)
	_ = store.SaveJob(&models.UIJob{
		ID:            "job_sched",
		FileName:      "sched.bin",
		URL:           "https://example.com/sched.bin",
		SavePath:      filepath.Join(dir, "sched.bin"),
		Status:        "SCHEDULED",
		ScheduledAt:   &future,
		MaxSpeedBytes: 102400,
	})

	updBody, _ := json.Marshal(map[string]any{
		"job_id":            "job_sched",
		"save_path":         filepath.Join(dir, "sched.bin"),
		"clear_schedule":    true,
		"reset_speed_limit": true,
	})
	updReq := httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(updBody))
	updReq.Header.Set("Content-Type", "application/json")
	updReq.Header.Set("X-Hydra-Token", "lifecycle-token")
	updRec := httptest.NewRecorder()
	server.Router.ServeHTTP(updRec, updReq)
	if updRec.Code != http.StatusOK {
		t.Fatalf("update clear_schedule status = %d: %s", updRec.Code, updRec.Body.String())
	}
	updatedSched, _ := store.GetJob("job_sched")
	if updatedSched.Status != "QUEUED" || updatedSched.ScheduledAt != nil || updatedSched.MaxSpeedBytes != 0 {
		t.Fatalf("expected cleared schedule & reset speed limit, got %+v", updatedSched)
	}
}

func TestUpdateProgressGuardsAndQueueImmediateResume(t *testing.T) {
	dir := t.TempDir()
	store := newTestDBStore(t)

	_ = store.SaveJob(&models.UIJob{
		ID:       "job_guard",
		FileName: "guard.bin",
		URL:      "https://example.com/guard.bin",
		SavePath: filepath.Join(dir, "guard.bin"),
		Status:   "PAUSED",
	})

	// Reporter update with DOWNLOADING must not revert PAUSED or QUEUED.
	_ = store.UpdateProgress("job_guard", 25.0, "25.00 MB", "0.00 KB/s", "--", "", "DOWNLOADING")
	if j, _ := store.GetJob("job_guard"); j.Status != "PAUSED" {
		t.Fatalf("expected PAUSED to survive late DOWNLOADING UpdateProgress, got %q", j.Status)
	}

	_ = store.UpdateStatus("job_guard", "QUEUED")
	_ = store.UpdateProgress("job_guard", 25.0, "25.00 MB", "0.00 KB/s", "--", "", "PAUSED")
	if j, _ := store.GetJob("job_guard"); j.Status != "QUEUED" {
		t.Fatalf("expected QUEUED to survive late PAUSED UpdateProgress, got %q", j.Status)
	}

	// Simulate Pause -> immediate Resume while previous run is exiting with context.Canceled.
	var attempts int32
	doneCh := make(chan struct{})
	var qm *QueueManager
	qm = NewQueueManager(1, func(url, savePath, jobID string, headers map[string]string) error {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			_ = store.UpdateStatus(jobID, "PAUSED")
			_ = store.UpdateStatus(jobID, "QUEUED")
			return context.Canceled
		}
		close(doneCh)
		return nil
	})
	qm.db = store
	qm.initialBackoff = 5 * time.Second
	defer qm.Stop()

	_ = store.SaveJob(&models.UIJob{
		ID:       "job_race",
		FileName: "race.bin",
		URL:      "https://example.com/race.bin",
		SavePath: filepath.Join(dir, "race.bin"),
		Status:   "QUEUED",
	})
	qm.ProcessNext()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected immediate resume without backoff after cancel race; attempts=%d", atomic.LoadInt32(&attempts))
	}
}

