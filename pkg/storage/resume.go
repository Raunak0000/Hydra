package storage

import (
	"fmt"
	"os"
	"syscall"
)

// OpenExistingDestination opens an existing regular file for resuming.
// O_NOFOLLOW prevents the final path component from being followed if it
// is a symbolic link.
func OpenExistingDestination(path string) (*os.File, error) {
	if path == "" {
		return nil, fmt.Errorf("resume destination path cannot be empty")
	}

	fd, err := syscall.Open(
		path,
		syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open existing resume destination: %w", err)
	}

	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("create file handle for resume destination")
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect opened resume destination: %w", err)
	}

	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("resume destination is not a regular file")
	}

	return file, nil
}
