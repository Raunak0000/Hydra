package storage

import (
	"testing"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestDBStoreUpdateJobURL(t *testing.T) {
	store := newTestDBStore(t)
	job := &models.UIJob{
		ID:       "job-test-1",
		FileName: "file.mkv",
		URL:      "https://old-example.com/file.mkv",
		SavePath: "/tmp/file.mkv",
		Status:   "PAUSED",
	}
	if err := store.SaveJob(job); err != nil {
		t.Fatalf("failed to save job: %v", err)
	}

	retrieved, exists := store.GetJob("job-test-1")
	if !exists {
		t.Fatal("expected job to exist")
	}
	if retrieved.URL != "https://old-example.com/file.mkv" {
		t.Fatalf("expected old url, got %s", retrieved.URL)
	}

	if err := store.UpdateJobURL("job-test-1", "https://new-example.com/file.mkv"); err != nil {
		t.Fatalf("failed to update job url: %v", err)
	}

	retrieved, exists = store.GetJob("job-test-1")
	if !exists {
		t.Fatal("expected job to exist after update")
	}
	if retrieved.URL != "https://new-example.com/file.mkv" {
		t.Fatalf("expected new url, got %s", retrieved.URL)
	}
}
