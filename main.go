package main

import (
	"fmt"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/storage" // Import our new system storage allocator
)

func main() {
	testURL := "https://storage.googleapis.com/android-ndk-releases/android-ndk-r26b-linux.zip"
	numThreads := 4
	finalOutputFile := "hydra_optimized_output.zip"

	fmt.Println("=== Hydra Phase 2: Native Linux Optimization Validation ===")

	// Step 1: Handshake
	metadata, err := downloader.GetMetadata(testURL)
	if err != nil {
		fmt.Println("Handshake system error:", err)
		return
	}
	fmt.Printf("[✓] Remote File Size Detected: %d bytes\n", metadata.Size)

	// Step 2: Linux Allocation optimization (fallocate)
	fmt.Println("[⚙] Communicating with Linux kernel for space pre-allocation...")
	sharedFile, err := storage.PreallocateSpace(finalOutputFile, metadata.Size)
	if err != nil {
		fmt.Println("[X] Kernel pre-allocation failed:", err)
		return
	}
	defer sharedFile.Close() // Close the single file when main finishes

	// Step 3: Slice boundary calculations
	chunks := downloader.CalculateChunks(metadata.Size, numThreads)

	// Step 4: True Parallel Writing Engine (pwrite via WriteAt)
	fmt.Println("[🚀] Spawning concurrent workers targeting single file descriptors...")
	var wg sync.WaitGroup

	for _, chunk := range chunks {
		wg.Add(1)
		// Pass the exact same sharedFile pointer to every single background worker
		go downloader.DownloadChunkParallel(testURL, chunk, sharedFile, &wg)
	}

	// Wait for execution completion
	wg.Wait()

	fmt.Println("\n=== SUCCESS: HYDRA HIGH-PERFORMANCE NATIVE LINUX ENGINE CONCLUDED ===")
}
