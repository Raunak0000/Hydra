package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenExistingDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "download.bin")

	if err := os.WriteFile(path, []byte("existing data"), 0600); err != nil {
		t.Fatal(err)
	}

	file, err := OpenExistingDestination(path)
	if err != nil {
		t.Fatalf("expected regular file to open: %v", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("opened destination is not a regular file")
	}
}

func TestOpenExistingDestinationRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	link := filepath.Join(dir, "link.bin")

	if err := os.WriteFile(target, []byte("keep this data"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, err := OpenExistingDestination(link)
	if err == nil {
		_ = file.Close()
		t.Fatal("expected symlink to be rejected")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep this data" {
		t.Fatal("target file was unexpectedly modified")
	}
}
