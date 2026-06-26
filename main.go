// main.go

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func main() {
	// 1. DEFINE THE CORE MULTI-THREADED PIPELINE ENGINE
	executeDownloadJob := func(url string, savePath string, jobID string) {
		store := storage.GetStore() // cite: 184

		// Phase A: Handshake & Multi-stage redirect verification
		metadata, err := downloader.GetMetadata(url) // cite: 184
		if err != nil {                              // cite: 184
			fmt.Printf("[X] Handshake system error for %s: %v\n", url, err) // cite: 184
			store.UpdateStatus(jobID, "FAILED")                             // cite: 184
			return                                                          // cite: 184
		} // cite: 184

		var totalSizeStr string
		if metadata.Size > 0 {
			totalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024)) // cite: 184
		} else {
			totalSizeStr = "Unknown"
		}
		store.UpdateTotalSize(jobID, totalSizeStr) // cite: 184

		var cleanName string                                       // cite: 184
		if parts := strings.Split(savePath, "/"); len(parts) > 0 { // cite: 184
			cleanName = parts[len(parts)-1] // cite: 184
		} // cite: 184
		if cleanName != "" { // cite: 184
			store.UpdateProgress(jobID, 0.0, "0.00 MB", cleanName, "DOWNLOADING") // cite: 184
		} // cite: 184

		// Phase B: Low-Level continuous Linux kernel storage pre-allocation
		fmt.Printf("[⚙] Pre-allocating continuous physical space footprint at: %s\n", savePath) // cite: 185
		sharedFile, err := storage.PreallocateSpace(savePath, metadata.Size)                    // cite: 185
		if err != nil {                                                                         // cite: 185
			fmt.Println("[X] Pre-allocation allocation failed:", err) // cite: 185
			store.UpdateStatus(jobID, "FAILED")                       // cite: 185
			return                                                    // cite: 185
		} // cite: 185
		defer sharedFile.Close() // cite: 185

		numThreads := 4                                   // cite: 185
		if !metadata.AcceptRanges || metadata.Size <= 0 { // cite: 185
			numThreads = 1 // cite: 185
		} // cite: 185
		chunks := downloader.CalculateChunks(metadata.Size, numThreads) // cite: 185

		// Create native communication primitives for safe data routing
		downloadDone := make(chan bool, 1)           // cite: 185
		workerErrors := make(chan error, numThreads) // cite: 185

		// Expanded buffer capacity depth to cushion backpressure spikes on gigabit pipelines
		progressChan := make(chan int64, 2000)

		var wg sync.WaitGroup // cite: 185

		// ── UNIFORM AND SECURE METRIC TRACKING PIPELINE LOOP ──
		var totalDownloaded int64 = 0

		go func() {
			// Safely read values continuously until progressChan is cleanly closed by the waiter thread
			for bytes := range progressChan {
				totalDownloaded += bytes
				downloadedStr := fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))
				globalStore := storage.GetStore()
				var cleanFilename string
				if parts := strings.Split(savePath, "/"); len(parts) > 0 {
					cleanFilename = parts[len(parts)-1]
				}

				if metadata.Size > 0 {
					percentage := (float64(totalDownloaded) / float64(metadata.Size)) * 100
					globalStore.UpdateProgress(jobID, percentage, downloadedStr, cleanFilename, "DOWNLOADING")
				} else {
					globalStore.UpdateProgress(jobID, 0.0, downloadedStr, cleanFilename, "DOWNLOADING")
				}
			}
			// Let the primary coordinator select state know every single update byte has been flushed out
			downloadDone <- true
		}()

		// Phase C: Launch Parallel Thread Worker Pools
		// Create a temporary placeholder channel to satisfy the signature requirement
		tempStateChan := make(chan downloader.Chunk, numThreads*2)

		go func() {
			for _, chunk := range chunks { // cite: 185
				wg.Add(1) // cite: 186

				// Pass context.Background() and tempStateChan to match worker.go exactly
				go downloader.DownloadChunkParallel(
					context.Background(),
					metadata.FinalURL,
					chunk,
					sharedFile,
					&wg,
					workerErrors,
					progressChan,
					tempStateChan,
				)
			} // cite: 186
			wg.Wait()           // cite: 186
			close(progressChan) // This line breaks the tracking range loop safely once threads exit!
		}()

		cancelChan := downloader.SetupSignalHandling(make(chan bool)) // cite: 187

		// Phase E: Coordinate the Finish Line Status States
		select {
		case <-downloadDone:
			close(workerErrors) // cite: 187

			if len(workerErrors) > 0 { // cite: 187
				firstErr := <-workerErrors                                                  // cite: 187
				fmt.Printf("\n[X] CRITICAL ABORT: Thread failure detected: %v\n", firstErr) // cite: 187
				store.UpdateStatus(jobID, "FAILED")                                         // cite: 187
				os.Remove(savePath)                                                         // cite: 187
				return                                                                      // cite: 187
			}

			var finalSizeStr string
			if metadata.Size > 0 {
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024)) // cite: 187
			} else {
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))
			}
			var cleanFilename string
			if parts := strings.Split(savePath, "/"); len(parts) > 0 { // cite: 187
				cleanFilename = parts[len(parts)-1] // cite: 187
			} // cite: 187

			storage.GetStore().UpdateProgress(jobID, 100.0, finalSizeStr, cleanFilename, "COMPLETED") // cite: 188
			fmt.Printf("\n=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)                      // cite: 188

		case workerErr := <-workerErrors: // cite: 188
			fmt.Printf("\n[X] PIPELINE CRASHED: Intercepted thread panic: %v\n", workerErr) // cite: 188
			store.UpdateStatus(jobID, "FAILED")                                             // cite: 188
			os.Remove(savePath)                                                             // cite: 188
			return                                                                          // cite: 188

		case <-cancelChan: // cite: 188
			fmt.Println("[🛑] Job signature canceled by hardware kernel interrupt.") // cite: 188
			return                                                                  // cite: 188
		}
	}

	// 2. BOOT THE EMBEDDED NATIVE GOTTH WEB MANAGEMENT GATEWAY
	fmt.Println("[⚙] Hydra UI Dashboard Server running on http://localhost:9000") // cite: 188
	server := storage.NewServer(executeDownloadJob)                               // cite: 188
	if err := http.ListenAndServe(":9000", server.Router); err != nil {           // cite: 188
		fmt.Printf("Server runtime exception error: %v\n", err) // cite: 189
	}
}
