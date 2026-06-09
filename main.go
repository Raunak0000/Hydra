package main

import (
	"fmt"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func main() {
	// Initialize Daemon Mode to detach process and route logs into hydra.log
	storage.InitializeDaemon()

	// Define our download manager engine logic inside a callback function wrapper
	executeDownloadJob := func(url string) {
		finalOutputFile := "hydra_cli_output.bin"

		// Step 1: Handshake
		metadata, err := downloader.GetMetadata(url)
		if err != nil {
			fmt.Println("Handshake system error:", err)
			return
		}
		fmt.Printf("[✓] Target download size: %d bytes\n", metadata.Size)

		// Step 2: Linux pre-allocation
		sharedFile, err := storage.PreallocateSpace(finalOutputFile, metadata.Size)
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
			fmt.Println("=== SUCCESS: JOB CONCLUDED EXTENSIVELY ===")
		case <-cancelChan:
			fmt.Println("[🛑] Job execution stopped by kernel signature.")
			return
		}
	}

	// Start the IPC local network socket listening interface loop inside your daemon.
	// Whenever hydra-cli sends a URL link, this server runs our callback downloader engine.
	storage.StartIPCServer(executeDownloadJob)
}
