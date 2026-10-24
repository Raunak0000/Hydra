package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReserveConflictCreatesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.zip")

	gotPath, file, err := ReserveConflict(path, 0600)
	if err != nil {
		t.Fatalf("ReserveConflict() error: %v", err)
	}
	defer file.Close()

	if gotPath != path {
		t.Fatalf("got path %q, want %q", gotPath, path)
	}

	info, err := os.Stat(gotPath)
	if err != nil {
		t.Fatalf("reserved destination does not exist: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("reserved destination is not a regular file")
	}
}

func TestReserveConflictUsesUniqueName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.zip")

	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	gotPath, file, err := ReserveConflict(path, 0600)
	if err != nil {
		t.Fatalf("ReserveConflict() error: %v", err)
	}
	defer file.Close()

	wantPath := filepath.Join(dir, "download (1).zip")
	if gotPath != wantPath {
		t.Fatalf("got path %q, want %q", gotPath, wantPath)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatal("existing destination was modified")
	}
}

func TestReserveConflictRejectsEmptyPath(t *testing.T) {
	_, file, err := ReserveConflict("  ", 0600)
	if file != nil {
		_ = file.Close()
		t.Fatal("expected no file for empty path")
	}
	if err == nil {
		t.Fatal("expected an error for empty path")
	}
	if !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReserveConflictRejectsSymlinkParent(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	linkDir := filepath.Join(root, "linked")

	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	path := filepath.Join(linkDir, "download.zip")

	_, file, err := ReserveConflict(path, 0600)
	if file != nil {
		_ = file.Close()
		t.Fatal("expected no file to be reserved")
	}
	if err == nil {
		t.Fatal("expected symlink parent to be rejected")
	}
}
