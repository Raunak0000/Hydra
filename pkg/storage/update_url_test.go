package storage

import (
	"testing"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestMemoryStoreUpdateJobURL(t *testing.T) {
	store := &MemoryStore{
		Jobs: make(map[string]*models.UIJob),
	}
	job := &models.UIJob{
		ID:     "job-test-1",
		URL:    "https://old-example.com/file.mkv",
		Status: "PAUSED",
	}
	store.SetJob("job-test-1", job)

	retrieved, exists := store.GetJob("job-test-1")
	if !exists {
		t.Fatal("expected job to exist")
	}
	if retrieved.URL != "https://old-example.com/file.mkv" {
		t.Fatalf("expected old url, got %s", retrieved.URL)
	}

	store.UpdateJobURL("job-test-1", "https://new-example.com/file.mkv")

	retrieved, exists = store.GetJob("job-test-1")
	if !exists {
		t.Fatal("expected job to exist after update")
	}
	if retrieved.URL != "https://new-example.com/file.mkv" {
		t.Fatalf("expected new url, got %s", retrieved.URL)
	}
}
