package downloader

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidPath       = errors.New("invalid path")
	ErrPermissionDenied  = errors.New("permission denied")
	ErrDestinationExists = errors.New("destination already exists")
	ErrDestinationDir    = errors.New("destination is a directory")
)

type PathValidationResult struct {
	FinalPath     string
	ParentDir     string
	Exists        bool
	IsDirectory   bool
	CreatedParent bool
}

type PathValidationOptions struct {
	CreateParentDirs bool
	AllowExisting    bool
}

func ValidateDownloadPath(
	path string,
	opts PathValidationOptions,
) (*PathValidationResult, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: path is empty", ErrInvalidPath)
	}

	if !utf8.ValidString(path) {
		return nil, fmt.Errorf("%w: path contains invalid UTF-8", ErrInvalidPath)
	}

	// Expand "~" and "~/".
	path, err := expandHome(path)
	if err != nil {
		return nil, err
	}

	// Clean the path first.
	path = filepath.Clean(path)

	// Convert to absolute path.
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf(
			"%w: unable to resolve absolute path: %v",
			ErrInvalidPath,
			err,
		)
	}

	// Reject obvious traversal components after normalization.
	if containsTraversal(path) {
		return nil, fmt.Errorf(
			"%w: path traversal is not allowed",
			ErrInvalidPath,
		)
	}

	parentDir := filepath.Dir(path)

	// Validate/create parent directory.
	parentCreated := false

	info, statErr := os.Stat(parentDir)

	if statErr != nil {
		if !os.IsNotExist(statErr) {
			return nil, fmt.Errorf(
				"%w: cannot inspect parent directory %q: %v",
				ErrInvalidPath,
				parentDir,
				statErr,
			)
		}

		if !opts.CreateParentDirs {
			return nil, fmt.Errorf(
				"%w: parent directory does not exist: %s",
				ErrInvalidPath,
				parentDir,
			)
		}

		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return nil, fmt.Errorf(
				"%w: cannot create parent directory %q: %v",
				ErrPermissionDenied,
				parentDir,
				err,
			)
		}

		parentCreated = true

		info, err = os.Stat(parentDir)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: cannot verify parent directory %q: %v",
				ErrInvalidPath,
				parentDir,
				err,
			)
		}
	}

	if !info.IsDir() {
		return nil, fmt.Errorf(
			"%w: parent path is not a directory: %s",
			ErrInvalidPath,
			parentDir,
		)
	}

	// Validate symlinks in the existing parent path.
	if _, err := filepath.EvalSymlinks(parentDir); err != nil {
		return nil, fmt.Errorf(
			"%w: unable to resolve parent directory symlinks: %v",
			ErrInvalidPath,
			err,
		)
	}

	// Verify that the parent directory is writable.
	if err := checkDirectoryWritable(parentDir); err != nil {
		return nil, err
	}

	result := &PathValidationResult{
		FinalPath:     path,
		ParentDir:     parentDir,
		CreatedParent: parentCreated,
	}

	// Check destination.
	destinationInfo, err := os.Lstat(path)

	if err == nil {
		result.Exists = true
		result.IsDirectory = destinationInfo.IsDir()

		if destinationInfo.IsDir() {
			return nil, fmt.Errorf(
				"%w: %s",
				ErrDestinationDir,
				path,
			)
		}

		if !opts.AllowExisting {
			return nil, fmt.Errorf(
				"%w: %s",
				ErrDestinationExists,
				path,
			)
		}

		return result, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf(
			"%w: cannot inspect destination %q: %v",
			ErrInvalidPath,
			path,
			err,
		)
	}

	return result, nil
}

func expandHome(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf(
				"%w: unable to determine home directory: %v",
				ErrInvalidPath,
				err,
			)
		}

		return home, nil
	}

	if strings.HasPrefix(path, "~/") ||
		strings.HasPrefix(path, "~"+string(os.PathSeparator)) {

		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf(
				"%w: unable to determine home directory: %v",
				ErrInvalidPath,
				err,
			)
		}

		return filepath.Join(
			home,
			path[2:],
		), nil
	}

	return path, nil
}

func containsTraversal(path string) bool {
	parts := strings.FieldsFunc(
		filepath.Clean(path),
		func(r rune) bool {
			return r == '/' || r == '\\'
		},
	)

	for _, part := range parts {
		if part == ".." {
			return true
		}
	}

	return false
}

func checkDirectoryWritable(dir string) error {
	testFile, err := os.CreateTemp(
		dir,
		".hydra-write-test-*",
	)

	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf(
				"%w: directory %q is not writable",
				ErrPermissionDenied,
				dir,
			)
		}

		return fmt.Errorf(
			"%w: unable to verify write permission for %q: %v",
			ErrPermissionDenied,
			dir,
			err,
		)
	}

	testPath := testFile.Name()

	if err := testFile.Close(); err != nil {
		_ = os.Remove(testPath)

		return fmt.Errorf(
			"%w: unable to close write test file: %v",
			ErrPermissionDenied,
			err,
		)
	}

	if err := os.Remove(testPath); err != nil {
		return fmt.Errorf(
			"%w: unable to clean write test file: %v",
			ErrPermissionDenied,
			err,
		)
	}

	return nil
}
