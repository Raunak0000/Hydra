package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestSecurityReview_HeaderIngestionAndQueueRedaction(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "queue-secret-token"
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

	destPath := filepath.Join(t.TempDir(), "auth-download.tar.gz")
	payload := fmt.Sprintf(`{
		"url": "https://example.com/private/auth-download.tar.gz",
		"save_path": %q,
		"start_paused": true,
		"headers": {
			"Cookie": "session=super-secret-cookie",
			"Authorization": "Bearer super-secret-jwt",
			"Proxy-Authorization": "Basic dXNlcjpwYXNz",
			"X-Auth-Token": "internal-auth",
			"X-Hydra-Token": "must-be-stripped",
			"Range": "bytes=999-1000",
			"Content-Length": "12345",
			"User-Agent": "Mozilla/5.0"
		}
	}`, destPath)

	createReq := httptest.NewRequest(http.MethodPost, "/download", bytes.NewBufferString(payload))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Hydra-Token", "queue-secret-token")
	createRec := httptest.NewRecorder()
	server.Router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("POST /download status = %d, want 202 (%s)", createRec.Code, createRec.Body.String())
	}

	var createResp struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&createResp); err != nil {
		t.Fatal(err)
	}

	// Stored job retains browser auth headers for workers, but stripped X-Hydra-Token/Range/Content-Length.
	stored, ok := store.GetJob(createResp.JobID)
	if !ok {
		t.Fatalf("job %q missing from DB", createResp.JobID)
	}
	if stored.Headers["Cookie"] != "session=super-secret-cookie" || stored.Headers["Authorization"] != "Bearer super-secret-jwt" {
		t.Fatalf("stored browser headers unexpectedly altered: %+v", stored.Headers)
	}
	for _, stripped := range []string{"X-Hydra-Token", "Range", "Content-Length"} {
		if _, exists := stored.Headers[stripped]; exists {
			t.Fatalf("expected %s stripped during ingestion, still present in %+v", stripped, stored.Headers)
		}
	}

	// Unauthenticated /api/queue/json redacts sensitive headers.
	unauthQueueReq := httptest.NewRequest(http.MethodGet, "/api/queue/json", nil)
	unauthQueueReq.Header.Set("Origin", "http://127.0.0.1:9000")
	unauthQueueRec := httptest.NewRecorder()
	server.Router.ServeHTTP(unauthQueueRec, unauthQueueReq)
	if unauthQueueRec.Code != http.StatusOK {
		t.Fatalf("GET /api/queue/json status = %d, want 200", unauthQueueRec.Code)
	}
	var queueJobs []models.UIJob
	if err := json.NewDecoder(unauthQueueRec.Body).Decode(&queueJobs); err != nil {
		t.Fatal(err)
	}
	if len(queueJobs) != 1 {
		t.Fatalf("expected 1 job from /api/queue/json, got %d", len(queueJobs))
	}
	for _, k := range []string{"Cookie", "Authorization", "Proxy-Authorization", "X-Auth-Token"} {
		if got := queueJobs[0].Headers[k]; got != "[REDACTED]" {
			t.Fatalf("expected unauthenticated /api/queue/json header %s=[REDACTED], got %q", k, got)
		}
	}
	if queueJobs[0].Headers["User-Agent"] != "Mozilla/5.0" {
		t.Fatalf("expected non-sensitive User-Agent preserved, got %q", queueJobs[0].Headers["User-Agent"])
	}
}

