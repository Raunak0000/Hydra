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

	fmt.Println("=== Hydra Phase 3: Daemon Signal Engine Validation ===")

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
	go tracker.Watch(stopProgress)

	// ==========================================
	// NEW: Step 5: Activate OS Signal Interceptor Trap
	// ==========================================
	cancelChan := downloader.SetupSignalHandling(stopProgress)

	// Step 6: Spawn download routines
	var wg sync.WaitGroup

	// We create an internal completion gate to monitor downloads independently of signals
	downloadDone := make(chan bool, 1)

	go func() {
		for _, chunk := range chunks {
			wg.Add(1)
			go downloader.DownloadChunkParallel(testURL, chunk, sharedFile, tracker, &wg)
		}
		wg.Wait()
		downloadDone <- true
	}()

	// Step 7: Multi-channel Multiplexing (Wait for completion OR a kernel kill signature)
	select {
	case <-downloadDone:
		// Download wrapped up perfectly without any OS hardware interruptions
		stopProgress <- true
		fmt.Println("\n=== SUCCESS: HYDRA DOWNLOAD ENGINE CONCLUDED ===")

	case <-cancelChan:
		// The user hit Ctrl+C or system manager fired a termination call mid-stream
		fmt.Println("[🛑] Hydra safely intercepted termination. Disposing descriptors and shutting down.")
		return
	}
}
