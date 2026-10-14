package storage

import (
	"fmt"
	"os"
)

func OpenExistingDestination(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect resume destination: %w", err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("resume destination must not be a symbolic link")
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("resume destination is not a regular file")
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open existing resume destination: %w", err)
	}

	return file, nil
}
