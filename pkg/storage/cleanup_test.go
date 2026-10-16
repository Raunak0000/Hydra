package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupNewDestinationRemovesReservedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.bin")

	if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := CleanupNewDestination(path, true); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected destination to be removed, stat error: %v", err)
	}
}

func TestCleanupNewDestinationPreservesResumeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.bin")
	want := []byte("keep partial download")

	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}

	if err := CleanupNewDestination(path, false); err != nil {
		t.Fatalf("cleanup returned error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("resume file was removed: %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("resume data changed: got %q, want %q", got, want)
	}
}

func TestCleanupNewDestinationMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.bin")

	if err := CleanupNewDestination(path, true); err != nil {
		t.Fatalf("cleanup should tolerate a missing file: %v", err)
	}
}

func TestCleanupNewDestinationRejectsEmptyPath(t *testing.T) {
	if err := CleanupNewDestination("", true); err == nil {
		t.Fatal("expected empty path to be rejected")
	}
}
