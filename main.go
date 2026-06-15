package main

import (
	"fmt"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func main() {
	// Define how a download handles background operations when an API call comes in
	executeDownloadJob := func(url string, savePath string, jobID string) {
		store := storage.GetStore()

		metadata, err := downloader.GetMetadata(url)
		if err != nil {
			fmt.Println("Handshake system error:", err)
			return
		}
		fmt.Printf("[✓] Target download size: %d bytes\n", metadata.Size)
		store.UpdateTotalSize(jobID, downloader.FormatBytes(metadata.Size))

		fmt.Printf("[⚙] Pre-allocating storage footprint at: %s\n", savePath)
		sharedFile, err := storage.PreallocateSpace(savePath, metadata.Size)
		if err != nil {
			fmt.Println("[X] Pre-allocation failed:", err)
			return
		}
		defer sharedFile.Close()

		numThreads := 4
		if !metadata.AcceptRanges {
			numThreads = 1
		}
		chunks := downloader.CalculateChunks(metadata.Size, numThreads)

		tracker := downloader.NewProgressTracker(metadata.Size)
		stopProgress := make(chan bool)

		// 2. PASS THE CALLBACK ATTACHING THE EXTRACTED jobID VARIABLES SAFELY
		go tracker.WatchWithUI(stopProgress, func(progress float64, downloaded string) {
			store.UpdateProgress(jobID, progress, downloaded)
		})

		cancelChan := downloader.SetupSignalHandling(stopProgress)

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

		select {
		case <-downloadDone:
			stopProgress <- true
			fmt.Printf("=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)
		case <-cancelChan:
			fmt.Println("[🛑] Job execution stopped by kernel signature.")
			return
		}
	}

	// Hand the download function pointer to the UI server wrapper
	storage.StartHTTPServer(executeDownloadJob)
}
