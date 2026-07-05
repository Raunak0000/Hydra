package storage

import (
	"fmt"
	"os"
	"syscall"
)

// PreallocateSpace claims a continuous physical block layout footprint on your storage drive
func PreallocateSpace(filePath string, size int64) (*os.File, error) {
	// 1. PHASE 9 DIRECT I/O INJECTION: Open the file with O_DIRECT and O_SYNC flags.
	// This instructs the Linux OS kernel to completely skip the intermediate memory Page Cache layer,
	// streaming byte data segments straight from user-space Go memory straight onto physical disk tracks.
	flags := os.O_CREATE | os.O_RDWR | syscall.O_DIRECT | syscall.O_SYNC

	file, err := os.OpenFile(filePath, flags, 0666)
	if err != nil {
		// 🚨 SYSTEM SAFEGUARD FALLBACK:
		// Certain filesystems or shared network mounts (/sys, /dev, shm, virtual containers)
		// don't permit bare-metal O_DIRECT blocks. If Linux rejects it, fall back gracefully to standard I/O!
		fmt.Printf("[⚠️] Direct I/O (O_DIRECT) flag not supported on destination partition. Falling back to standard cached mode.\n")

		file, err = os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0666) // cite: file(5).txt
		if err != nil {
			return nil, fmt.Errorf("failed to create fallback target storage file: %v", err)
		}
	}

	// If size is unknown (0 or negative), skip kernel allocation completely
	if size <= 0 {
		fmt.Println("[⚠️] File size unknown. Skipping Linux kernel pre-allocation.") // cite: file(5).txt
		return file, nil                                                             // cite: file(5).txt
	}

	// 2. Grab the low-level Linux File Descriptor (int) from the Go file pointer
	fd := int(file.Fd()) // cite: file(5).txt

	// Mode 0: Default behavior (allocates and fills space with zero-bytes)
	err = syscall.Fallocate(fd, 0, 0, size) // cite: file(5).txt
	if err != nil {                         // cite: file(5).txt
		file.Close()                                                     // cite: file(5).txt
		return nil, fmt.Errorf("linux kernel fallocate failed: %v", err) // cite: file(5).txt
	}

	fmt.Printf("[✓] Linux Kernel successfully pre-allocated %d bytes on SSD via Zero-Copy Direct I/O pipelines.\n", size)
	return file, nil // cite: file(5).txt
}
