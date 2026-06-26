package storage

import (
	"fmt"
	"os"
	"syscall"
)

// pkg/storage/allocator_linux.go

func PreallocateSpace(filePath string, size int64) (*os.File, error) {
	// 1. Create or open the final target file with Read/Write access permissions
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0666) // cite: file(2).txt
	if err != nil {                                                 // cite: file(2).txt
		return nil, fmt.Errorf("failed to create target storage file: %v", err) // cite: file(2).txt
	}

	// 🚨 FIX: If size is unknown (0 or negative), skip kernel allocation completely
	if size <= 0 {
		fmt.Println("[⚠️] File size unknown. Skipping Linux kernel pre-allocation.")
		return file, nil
	}

	// 2. Grab the low-level Linux File Descriptor (int) from the Go file pointer
	fd := int(file.Fd()) // cite: file(2).txt

	// Mode 0: Default behavior (allocates and fills space with zero-bytes)
	err = syscall.Fallocate(fd, 0, 0, size) // cite: file(2).txt
	if err != nil {                         // cite: file(2).txt
		file.Close()                                                     // cite: file(2).txt
		return nil, fmt.Errorf("linux kernel fallocate failed: %v", err) // cite: file(2).txt
	}

	fmt.Printf("[✓] Linux Kernel successfully pre-allocated %d bytes on SSD.\n", size) // cite: file(2).txt
	return file, nil                                                                   // cite: file(2).txt
}
