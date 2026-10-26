package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestSaveJobState(t *testing.T) {
	dir := t.TempDir()
	savePath := filepath.Join(dir, "download.bin")

	job := models.UIJob{
		ID:       "test-job",
		FileName: "download.bin",
		URL:      "https://example.com/download.bin",
		SavePath: savePath,
		Status:   "PAUSED",
	}

	if err := SaveJobState(job); err != nil {
		t.Fatalf("SaveJobState() error: %v", err)
	}

	statePath := savePath + ".hydra"

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}

	var got models.UIJob
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("state file contains invalid JSON: %v", err)
	}

	if got.ID != job.ID || got.SavePath != job.SavePath {
		t.Fatalf("saved job mismatch: got %+v, want %+v", got, job)
	}

	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}

	if info.Mode().Perm() != 0600 {
		t.Fatalf("state file permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveJobStateRejectsEmptyPath(t *testing.T) {
	err := SaveJobState(models.UIJob{})
	if err == nil {
		t.Fatal("expected empty save path to be rejected")
	}
}

func TestSaveJobStateLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	savePath := filepath.Join(dir, "download.bin")

	job := models.UIJob{
		ID:       "test-job",
		SavePath: savePath,
	}

	if err := SaveJobState(job); err != nil {
		t.Fatalf("SaveJobState() error: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, ".hydra-state-*.tmp"))
	if err != nil {
		t.Fatalf("find temporary files: %v", err)
	}

	if len(matches) != 0 {
		t.Fatalf("temporary state files remain: %v", matches)
	}
}
