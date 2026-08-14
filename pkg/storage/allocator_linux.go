// pkg/storage/allocator_linux.go

package storage

import (
	"fmt"
	"os"
	"syscall"
)

// PreallocateSpace claims a continuous physical block layout footprint on your storage drive
func PreallocateSpace(filePath string, size int64) (*os.File, error) {
	// Standard high-speed Read/Write file handles work seamlessly with network chunk streaming
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return nil, fmt.Errorf("failed to create target storage file: %v", err)
	}

	// If size is unknown (0 or negative), skip kernel preallocation
	if size <= 0 {
		fmt.Println("[⚠️] File size unknown. Skipping Linux kernel pre-allocation.")
		return file, nil
	}

	// Grab the low-level Linux File Descriptor (int)
	fd := int(file.Fd())

	// Pre-allocate space continuous block footprint on disk via kernel syscall
	err = syscall.Fallocate(fd, 0, 0, size)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("linux kernel fallocate failed: %v", err)
	}

	fmt.Printf("[✓] Linux Kernel successfully pre-allocated %d bytes on SSD.\n", size)
	return file, nil
}
