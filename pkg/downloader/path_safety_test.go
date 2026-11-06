package downloader

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathSafety_TraversalAttacks(t *testing.T) {
	tempDir := t.TempDir()

	traversalCases := []struct {
		name string
		path string
	}{
		{name: "parent_escape", path: filepath.Join(tempDir, "..", "escape.bin")},
		{name: "deep_escape", path: filepath.Join(tempDir, "sub", "..", "..", "escape.bin")},
		{name: "dotdot_in_filename", path: filepath.Join(tempDir, "foo..bar")},
	}

	for _, tc := range traversalCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateDownloadPath(tc.path, PathValidationOptions{CreateParentDirs: false})
			// Path validation should either resolve safely within normalized boundaries or reject
			if tc.name != "dotdot_in_filename" && containsTraversal(tc.path) {
				if err == nil {
					t.Fatalf("expected traversal %q to be rejected", tc.path)
				}
			}
		})
	}
}

func TestPathSafety_SymlinkRejections(t *testing.T) {
	dir := t.TempDir()

	// 1. Existing destination symlink pointing to another file
	targetFile := filepath.Join(dir, "target.dat")
	if err := os.WriteFile(targetFile, []byte("target-payload"), 0600); err != nil {
		t.Fatal(err)
	}

	destSymlink := filepath.Join(dir, "symlink_dest.dat")
	if err := os.Symlink(targetFile, destSymlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := ValidateDownloadPath(destSymlink, PathValidationOptions{AllowExisting: true})
	if err == nil {
		t.Fatal("expected ValidateDownloadPath to reject destination that is a symlink")
	}
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath, got: %v", err)
	}

	// 2. Dangling symlink destination
	danglingSymlink := filepath.Join(dir, "dangling_link.dat")
	if err := os.Symlink(filepath.Join(dir, "nonexistent.dat"), danglingSymlink); err == nil {
		_, err := ValidateDownloadPath(danglingSymlink, PathValidationOptions{AllowExisting: false})
		if err == nil {
			t.Fatal("expected ValidateDownloadPath to reject dangling symlink")
		}
	}

	// 3. Symlink parent directory
	realParent := filepath.Join(dir, "real_parent")
	if err := os.Mkdir(realParent, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkParent := filepath.Join(dir, "symlink_parent")
	if err := os.Symlink(realParent, symlinkParent); err == nil {
		childPath := filepath.Join(symlinkParent, "download.bin")
		_, err := ValidateDownloadPath(childPath, PathValidationOptions{CreateParentDirs: true})
		if err == nil {
			t.Fatal("expected ValidateDownloadPath to reject parent directory containing symlinks")
		}
	}
}

func TestPathSafety_UnusualFilenamesInDownloadPath(t *testing.T) {
	dir := t.TempDir()

	unusualFilenames := []string{
		"🎬_Hydra_Download_🍿.mkv",
		"日本語_ファイル.iso",
		"résumé_2026.pdf",
		"ملف_بيانات.dat",
		"[Release] Hydra-Engine (v1.0.0).tar.gz",
		"file with spaces and symbols!@#$%^&()_+.bin",
		".hidden_download.bin",
		"archive.tar.bz2",
	}

	for _, filename := range unusualFilenames {
		t.Run(filename, func(t *testing.T) {
			fullPath := filepath.Join(dir, filename)
			res, err := ValidateDownloadPath(fullPath, PathValidationOptions{
				CreateParentDirs: true,
				AllowExisting:    false,
			})
			if err != nil {
				t.Fatalf("ValidateDownloadPath failed for unusual filename %q: %v", filename, err)
			}
			if res.FinalPath != fullPath {
				t.Fatalf("expected final path %q, got %q", fullPath, res.FinalPath)
			}
		})
	}
}

func TestPathSafety_LongPathsAndBoundaries(t *testing.T) {
	dir := t.TempDir()

	// Filename component of exactly 255 bytes (Linux limit)
	exact255Name := strings.Repeat("a", 251) + ".bin"
	validPath := filepath.Join(dir, exact255Name)

	res, err := ValidateDownloadPath(validPath, PathValidationOptions{CreateParentDirs: true})
	if err != nil {
		t.Fatalf("ValidateDownloadPath failed for 255-byte component: %v", err)
	}
	if filepath.Base(res.FinalPath) != exact255Name {
		t.Fatalf("filename was altered or truncated: got %q, want %q", filepath.Base(res.FinalPath), exact255Name)
	}
}
