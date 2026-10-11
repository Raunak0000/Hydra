package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathReturnsAbsolutePathWithoutTruncation(t *testing.T) {
	dir := t.TempDir()
	filename := strings.Repeat("a", 130) + ".zip"
	input := filepath.Join(dir, filename)

	got, err := ResolvePath(input)
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}

	if got != input {
		t.Fatalf("ResolvePath() = %q, want %q", got, input)
	}
}

func TestResolvePathCreatesParentDirectory(t *testing.T) {
	input := filepath.Join(t.TempDir(), "nested", "downloads", "file.zip")

	got, err := ResolvePath(input)
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}

	if got != input {
		t.Fatalf("ResolvePath() = %q, want %q", got, input)
	}

	if info, err := os.Stat(filepath.Dir(input)); err != nil {
		t.Fatalf("parent directory was not created: %v", err)
	} else if !info.IsDir() {
		t.Fatal("destination parent is not a directory")
	}
}

func TestResolvePathCreatesDirectoryDestination(t *testing.T) {
	input := filepath.Join(t.TempDir(), "new-downloads") + string(filepath.Separator)

	got, err := ResolvePath(input)
	if err != nil {
		t.Fatalf("ResolvePath() error = %v", err)
	}

	if got != filepath.Clean(input) {
		t.Fatalf("ResolvePath() = %q, want %q", got, filepath.Clean(input))
	}

	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("resolved destination is not a directory")
	}
}

func TestResolvePathRejectsEmptyPath(t *testing.T) {
	for _, input := range []string{"", "   "} {
		if _, err := ResolvePath(input); err == nil {
			t.Errorf("ResolvePath(%q) expected an error", input)
		}
	}
}

func TestResolvePathRejectsUnsupportedTildeSyntax(t *testing.T) {
	if _, err := ResolvePath("~otheruser/downloads"); err == nil {
		t.Fatal("expected unsupported tilde syntax to be rejected")
	}
}
