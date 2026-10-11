package storage

import (
	"fmt"
	"os"
	"syscall"
)

// PreallocateFile reserves disk space for an already-open destination file.
// The caller retains ownership of the file and must close it.
func PreallocateFile(file *os.File, size int64) error {
	if file == nil {
		return fmt.Errorf("destination file is nil")
	}

	if size <= 0 {
		return nil
	}

	if err := syscall.Fallocate(int(file.Fd()), 0, 0, size); err != nil {
		return fmt.Errorf("Linux kernel fallocate failed: %w", err)
	}

	return nil
}

// PreallocateSpace creates a new destination file and preallocates its space.
func PreallocateSpace(filePath string, size int64) (*os.File, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return nil, fmt.Errorf("failed to open target storage file: %w", err)
	}

	if err := PreallocateFile(file, size); err != nil {
		file.Close()
		return nil, err
	}

	return file, nil
}
