package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReserveConflict atomically reserves an unused destination file.
// The returned file must be closed by the caller and can be used for writing.
func ReserveConflict(path string, perm os.FileMode) (string, *os.File, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, fmt.Errorf("destination path cannot be empty")
	}

	path = filepath.Clean(path)

	parentDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return "", nil, fmt.Errorf("resolve destination directory: %w", err)
	}

	resolvedParent, err := filepath.EvalSymlinks(parentDir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve destination directory symlinks: %w", err)
	}

	if resolvedParent != parentDir {
		return "", nil, fmt.Errorf(
			"destination parent directory must not contain symbolic links: %s",
			parentDir,
		)
	}

	path = filepath.Join(parentDir, filepath.Base(path))

	dir := filepath.Dir(path)
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)

	for i := 0; ; i++ {
		candidate := path
		if i > 0 {
			candidate = filepath.Join(
				dir,
				fmt.Sprintf("%s (%d)%s", base, i, ext),
			)
		}

		file, err := os.OpenFile(
			candidate,
			os.O_CREATE|os.O_EXCL|os.O_RDWR,
			perm,
		)
		if err == nil {
			return candidate, file, nil
		}

		if !os.IsExist(err) {
			return "", nil, fmt.Errorf("reserve destination %q: %w", candidate, err)
		}
	}
}
