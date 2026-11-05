package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func newTestDBStore(t *testing.T) *DBStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hydra_test.db")
	store, err := openDBStore(dbPath)
	if err != nil {
		t.Fatalf("openDBStore: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = os.Remove(dbPath)
	})
	return store
}

func TestRecoverStaleJobs(t *testing.T) {
	store := newTestDBStore(t)

	jobs := []models.UIJob{
		{ID: "job-dl-1", FileName: "a.mkv", URL: "https://example.com/a.mkv", SavePath: "/tmp/a.mkv", Status: "DOWNLOADING"},
		{ID: "job-dl-2", FileName: "b.mkv", URL: "https://example.com/b.mkv", SavePath: "/tmp/b.mkv", Status: "DOWNLOADING"},
		{ID: "job-queued", FileName: "c.mkv", URL: "https://example.com/c.mkv", SavePath: "/tmp/c.mkv", Status: "QUEUED"},
		{ID: "job-paused", FileName: "d.mkv", URL: "https://example.com/d.mkv", SavePath: "/tmp/d.mkv", Status: "PAUSED"},
		{ID: "job-done", FileName: "e.mkv", URL: "https://example.com/e.mkv", SavePath: "/tmp/e.mkv", Status: "COMPLETED"},
	}

	for i := range jobs {
		if err := store.SaveJob(&jobs[i]); err != nil {
			t.Fatalf("SaveJob %s: %v", jobs[i].ID, err)
		}
	}

	n, err := store.RecoverStaleJobs()
	if err != nil {
		t.Fatalf("RecoverStaleJobs: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 recovered jobs, got %d", n)
	}

	// DOWNLOADING jobs must become PAUSED
	for _, id := range []string{"job-dl-1", "job-dl-2"} {
		job, exists := store.GetJob(id)
		if !exists {
			t.Errorf("job %s not found after recovery", id)
			continue
		}
		if job.Status != "PAUSED" {
			t.Errorf("job %s: expected PAUSED, got %s", id, job.Status)
		}
	}

	// Other statuses must be untouched
	unchanged := map[string]string{
		"job-queued": "QUEUED",
		"job-paused": "PAUSED",
		"job-done":   "COMPLETED",
	}
	for id, want := range unchanged {
		job, exists := store.GetJob(id)
		if !exists {
			t.Errorf("job %s not found", id)
			continue
		}
		if job.Status != want {
			t.Errorf("job %s: expected %s, got %s", id, want, job.Status)
		}
	}
}

func TestRecoverStaleJobsIdempotent(t *testing.T) {
	store := newTestDBStore(t)

	job := models.UIJob{
		ID: "job-dl", FileName: "a.mkv", URL: "https://example.com/a.mkv",
		SavePath: "/tmp/a.mkv", Status: "DOWNLOADING",
	}
	if err := store.SaveJob(&job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	n1, err := store.RecoverStaleJobs()
	if err != nil || n1 != 1 {
		t.Fatalf("first recovery: n=%d err=%v", n1, err)
	}

	// Running again must recover nothing (job is already PAUSED)
	n2, err := store.RecoverStaleJobs()
	if err != nil || n2 != 0 {
		t.Errorf("second recovery should recover 0: n=%d err=%v", n2, err)
	}
}

