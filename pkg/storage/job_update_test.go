package storage

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

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

