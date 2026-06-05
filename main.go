package main

import (
	"fmt"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
)

func main() {

	testURL := "https://storage.googleapis.com/android-ndk-releases/android-ndk-r26b-linux.zip"
	numThreads := 4
	finalOutputFile := "downloaded_result.html" // Target file mapping the redirect payload we capture

	fmt.Println("=== Hydra Core Engine Validation ===")

	// Step 1: Handshake
	metadata, err := downloader.GetMetadata(testURL)
	if err != nil {
		fmt.Println("Handshake system error:", err)
		return
	}
	fmt.Printf("[✓] Remote File Size: %d bytes\n", metadata.Size)

	// Step 2: Slice calculation
	fmt.Printf("[⚙] Calculating boundary allocation for %d threads...\n", numThreads)
	chunks := downloader.CalculateChunks(metadata.Size, numThreads)

	// Step 3: Concurrency Engine (The Workers)
	fmt.Println("[🚀] Launching parallel worker goroutines...")

	// Initialize our synchronization gate counter
	var wg sync.WaitGroup

	for _, chunk := range chunks {
		// Tell the gate counter: "We are spinning up 1 more worker thread"
		wg.Add(1)

		// The 'go' keyword turns the loop execution concurrent!
		// Instead of waiting for Thread 0 to finish, Go fires it off into the background
		// and instantly loops to fire Thread 1, 2, and 3 simultaneously.
		go downloader.DownloadChunk(testURL, chunk, &wg)
	}

	// Wait for background workers to execute completely
	wg.Wait()
	fmt.Println("[✓] All parallel segments successfully downloaded.")

	// Step 4: Stitcher Assembly Execution
	err = downloader.StitchChunks(chunks, finalOutputFile)
	if err != nil {
		fmt.Println("[X] Critical file processing error during assembly:", err)
		return
	}

	fmt.Println("\n=== SUCCESS: HYDRA PHASE 1 ENGINE COMPLETED COHESIVELY ===")
}
