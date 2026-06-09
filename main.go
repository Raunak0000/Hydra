package main

import (
	"fmt"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func main() {
	storage.InitializeDaemon()

	// Update the wrapper signature to accept both input streams dynamically
	executeDownloadJob := func(url string, savePath string) {

		// Step 1: Handshake
		metadata, err := downloader.GetMetadata(url)
		if err != nil {
			fmt.Println("Handshake system error:", err)
			return
		}
		fmt.Printf("[✓] Target download size: %d bytes\n", metadata.Size)

		// Step 2: Linux pre-allocation targeting your custom dynamic location!
		fmt.Printf("[⚙] Pre-allocating storage footprint at: %s\n", savePath)
		sharedFile, err := storage.PreallocateSpace(savePath, metadata.Size)
		if err != nil {
			fmt.Println("[X] Pre-allocation failed:", err)
			return
		}
		defer sharedFile.Close()

		// Step 3: Boundary slicing
		numThreads := 4
		chunks := downloader.CalculateChunks(metadata.Size, numThreads)

		// Step 4: Progress metrics engine
		tracker := downloader.NewProgressTracker(metadata.Size)
		stopProgress := make(chan bool)
		go tracker.Watch(stopProgress)

		// Step 5: Activate signal checker
		cancelChan := downloader.SetupSignalHandling(stopProgress)

		// Step 6: Concurrent parallel download workers
		var wg sync.WaitGroup
		downloadDone := make(chan bool, 1)

		go func() {
			for _, chunk := range chunks {
				wg.Add(1)
				go downloader.DownloadChunkParallel(url, chunk, sharedFile, tracker, &wg)
			}
			wg.Wait()
			downloadDone <- true
		}()

		// Step 7: Multi-channel monitoring block
		select {
		case <-downloadDone:
			stopProgress <- true
			fmt.Printf("=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)
		case <-cancelChan:
			fmt.Println("[🛑] Job execution stopped by kernel signature.")
			return
		}
	}

	storage.StartIPCServer(executeDownloadJob)
}
