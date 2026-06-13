package main

import (
	"fmt"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func main() {
	// 1. COMMENT THIS OUT SO IT RUNS IN THE FOREGROUND LIVE:
	// storage.InitializeDaemon()

	executeDownloadJob := func(url string, savePath string) {
		metadata, err := downloader.GetMetadata(url)
		if err != nil {
			fmt.Println("Handshake system error:", err)
			return
		}
		fmt.Printf("[✓] Target download size: %d bytes\n", metadata.Size)

		fmt.Printf("[⚙] Pre-allocating storage footprint at: %s\n", savePath)
		sharedFile, err := storage.PreallocateSpace(savePath, metadata.Size)
		if err != nil {
			fmt.Println("[X] Pre-allocation failed:", err)
			return
		}
		defer sharedFile.Close()

		numThreads := 4
		chunks := downloader.CalculateChunks(metadata.Size, numThreads)

		tracker := downloader.NewProgressTracker(metadata.Size)
		stopProgress := make(chan bool)
		go tracker.Watch(stopProgress)

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

	// 2. SWAP OUT IPC SOCKETS FOR THE HTTP GATEWAY HERE:
	storage.StartHTTPServer(executeDownloadJob)
}
