package downloader

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDownloadPath(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		_, err := ValidateDownloadPath(
			"  ",
			PathValidationOptions{},
		)
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("expected ErrInvalidPath, got %v", err)
		}
	})

	t.Run("missing parent directory", func(t *testing.T) {
		path := filepath.Join(
			t.TempDir(),
			"missing",
			"file.zip",
		)

		_, err := ValidateDownloadPath(
			path,
			PathValidationOptions{},
		)
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("expected ErrInvalidPath, got %v", err)
		}
	})

	t.Run("create parent directory", func(t *testing.T) {
		path := filepath.Join(
			t.TempDir(),
			"created",
			"file.zip",
		)

		result, err := ValidateDownloadPath(
			path,
			PathValidationOptions{
				CreateParentDirs: true,
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.CreatedParent {
			t.Fatal("expected parent directory to be created")
		}
		if result.FinalPath != path {
			t.Fatalf("got path %q, want %q", result.FinalPath, path)
		}
	})

	t.Run("existing destination rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file.zip")
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}

		_, err := ValidateDownloadPath(
			path,
			PathValidationOptions{},
		)
		if !errors.Is(err, ErrDestinationExists) {
			t.Fatalf("expected ErrDestinationExists, got %v", err)
		}
	})

	t.Run("existing destination allowed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file.zip")
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}

		result, err := ValidateDownloadPath(
			path,
			PathValidationOptions{
				AllowExisting: true,
			},
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Exists {
			t.Fatal("expected destination to exist")
		}
	})

	t.Run("destination is a directory", func(t *testing.T) {
		dir := t.TempDir()

		_, err := ValidateDownloadPath(
			dir,
			PathValidationOptions{
				AllowExisting: true,
			},
		)
		if !errors.Is(err, ErrDestinationDir) {
			t.Fatalf("expected ErrDestinationDir, got %v", err)
		}
	})

	t.Run("symlink destination", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.zip")
		link := filepath.Join(dir, "link.zip")

		if err := os.WriteFile(target, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(target, link); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		_, err := ValidateDownloadPath(
			link,
			PathValidationOptions{
				AllowExisting: true,
			},
		)

		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("expected ErrInvalidPath, got %v", err)
		}
	})

	t.Run("symlink parent directory", func(t *testing.T) {
		root := t.TempDir()
		realDir := filepath.Join(root, "real")
		linkDir := filepath.Join(root, "linked")

		if err := os.Mkdir(realDir, 0700); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(realDir, linkDir); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}

		path := filepath.Join(linkDir, "file.zip")

		_, err := ValidateDownloadPath(
			path,
			PathValidationOptions{},
		)
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("expected ErrInvalidPath, got %v", err)
		}
	})
}
