package storage

import (
	"fmt"
	"os"
	"syscall"
)

// PreallocateSpace talks directly to the Linux Kernel to reserve continuous SSD blocks
func PreallocateSpace(filePath string, size int64) (*os.File, error) {
	// 1. Create or open the final target file with Read/Write access permissions
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return nil, fmt.Errorf("failed to create target storage file: %v", err)
	}

	// 2. Grab the low-level Linux File Descriptor (int) from the Go file pointer
	fd := int(file.Fd())

	// 3. Invoke native Linux fallocate system call
	// Mode 0: Default behavior (allocates and fills space with zero-bytes)
	// Offset 0: Start allocating right from the beginning of the file layout
	err = syscall.Fallocate(fd, 0, 0, size)
	if err != nil {
		file.Close() // Clean up descriptor if kernel allocation drops
		return nil, fmt.Errorf("linux kernel fallocate failed: %v", err)
	}

	fmt.Printf("[✓] Linux Kernel successfully pre-allocated %d bytes on SSD.\n", size)
	return file, nil
}
