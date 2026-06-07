package main

import (
	"fmt"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func main() {
	testURL := "https://www.fastly.com/static/test_file_10MB.bin"
	numThreads := 4
	finalOutputFile := "hydra_optimized_test.bin"

	fmt.Println("=== Hydra Phase 2: Native Linux Optimization Engine ===")

	// Step 1: Handshake
	metadata, err := downloader.GetMetadata(testURL)
	if err != nil {
		fmt.Println("Handshake system error:", err)
		return
	}
	fmt.Printf("[✓] Remote File Size Detected: %d bytes\n", metadata.Size)

	// Step 2: Linux Allocation optimization
	fmt.Println("[⚙] Communicating with Linux kernel for space pre-allocation...")
	sharedFile, err := storage.PreallocateSpace(finalOutputFile, metadata.Size)
	if err != nil {
		fmt.Println("[X] Kernel pre-allocation failed:", err)
		return
	}
	defer sharedFile.Close()

	// Step 3: Slice boundary calculations
	chunks := downloader.CalculateChunks(metadata.Size, numThreads)

	// Step 4: Initialize the Atomic Progress Engine
	tracker := downloader.NewProgressTracker(metadata.Size)
	stopProgress := make(chan bool)

	// Launch the progress bar loop inside its own dedicated background goroutine
	go tracker.Watch(stopProgress)

	// Step 5: Spawn download routines
	var wg sync.WaitGroup
	for _, chunk := range chunks {
		wg.Add(1)
		// Pass tracker pointer down into the concurrent worker pipelines
		go downloader.DownloadChunkParallel(testURL, chunk, sharedFile, tracker, &wg)
	}

	// Block here until all downloads wrap up cleanly
	wg.Wait()

	// Shut down the monitoring ticker loop safely
	stopProgress <- true

	fmt.Println("\n=== SUCCESS: HYDRA HIGH-PERFORMANCE NATIVE LINUX ENGINE CONCLUDED ===")
}
