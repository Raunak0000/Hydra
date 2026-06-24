package main

import (
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
		store := storage.GetStore()

		// Phase A: Handshake & Multi-stage redirect verification
		metadata, err := downloader.GetMetadata(url)
		if err != nil {
			fmt.Printf("[X] Handshake system error for %s: %v\n", url, err)
			store.UpdateStatus(jobID, "FAILED")
			return
		}

		// Update file size and name in memory store so they display in the dashboard
		totalSizeStr := fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))
		store.UpdateTotalSize(jobID, totalSizeStr)

		var cleanName string
		if parts := strings.Split(savePath, "/"); len(parts) > 0 {
			cleanName = parts[len(parts)-1]
		}
		if cleanName != "" {
			store.UpdateProgress(jobID, 0.0, "0.00 MB", cleanName, "DOWNLOADING")
		}

		// Phase B: Low-Level continuous Linux kernel storage pre-allocation
		fmt.Printf("[⚙] Pre-allocating continuous physical space footprint at: %s\n", savePath)
		sharedFile, err := storage.PreallocateSpace(savePath, metadata.Size)
		if err != nil {
			fmt.Println("[X] Pre-allocation allocation failed:", err)
			store.UpdateStatus(jobID, "FAILED")
			return
		}
		defer sharedFile.Close()

		numThreads := 4
		if !metadata.AcceptRanges {
			numThreads = 1
		}
		chunks := downloader.CalculateChunks(metadata.Size, numThreads)

		// Create native communication primitives for safe data routing
		downloadDone := make(chan bool, 1)
		workerErrors := make(chan error, numThreads)
		progressChan := make(chan int64, 100)

		var wg sync.WaitGroup

		// Phase C: Launch Parallel Thread Worker Pools
		go func() {
			for _, chunk := range chunks {
				wg.Add(1)
				// MATCH THE ORDER EXACTLY: workerErrors (chan error) first, then progressChan (chan int64)
				go downloader.DownloadChunkParallel(metadata.FinalURL, chunk, sharedFile, &wg, workerErrors, progressChan)
			}
			wg.Wait()
			downloadDone <- true
		}()

		// ── UPDATED TRACKING LOOP IN MAIN.GO ──
		// ── DIRECT MEMORY POINTER TRACKING LOOP ──
		var totalDownloaded int64 = 0
		stopMonitoring := make(chan bool)

		go func() {
			for {
				select {
				case bytes := <-progressChan:
					totalDownloaded += bytes
					if metadata.Size > 0 {
						percentage := (float64(totalDownloaded) / float64(metadata.Size)) * 100
						downloadedStr := fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))

						// 1. Fetch the storage cache instance directly
						globalStore := storage.GetStore()

						// Extract clean filename from path string
						var cleanName string
						if parts := strings.Split(savePath, "/"); len(parts) > 0 {
							cleanName = parts[len(parts)-1]
						}

						// 2. Safely update the struct fields in a thread-safe manner
						globalStore.UpdateProgress(jobID, percentage, downloadedStr, cleanName, "DOWNLOADING")
					}
				case <-stopMonitoring:
					return
				}
			}
		}()

		cancelChan := downloader.SetupSignalHandling(make(chan bool))

		// Phase E: Coordinate the Finish Line Status States
		select {
		case <-downloadDone:
			close(stopMonitoring)
			close(workerErrors)

			// Inspect the thread error pipeline channel queue
			if len(workerErrors) > 0 {
				firstErr := <-workerErrors
				fmt.Printf("\n[X] CRITICAL ABORT: Thread failure detected: %v\n", firstErr)
				store.UpdateStatus(jobID, "FAILED")
				os.Remove(savePath) // Wipe partial file artifacts
				return
			}

			// ── DIRECT FINALIZE (NO STITCHER CALL) ──
			finalSizeStr := fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))

			var cleanName string
			if parts := strings.Split(savePath, "/"); len(parts) > 0 {
				cleanName = parts[len(parts)-1]
			}

			// Smoothly transition status straight to COMPLETED inside our memory cache vault
			storage.GetStore().UpdateProgress(jobID, 100.0, finalSizeStr, cleanName, "COMPLETED")
			fmt.Printf("\n=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)

		case workerErr := <-workerErrors:
			close(stopMonitoring)
			fmt.Printf("\n[X] PIPELINE CRASHED: Intercepted thread panic: %v\n", workerErr)
			store.UpdateStatus(jobID, "FAILED")
			os.Remove(savePath)
			return

		case <-cancelChan:
			close(stopMonitoring)
			fmt.Println("[🛑] Job signature canceled by hardware kernel interrupt.")
			return
		}
	}

	// 2. BOOT THE EMBEDDED NATIVE GOTTH WEB MANAGEMENT GATEWAY
	fmt.Println("[⚙] Hydra UI Dashboard Server running on http://localhost:9000")
	server := storage.NewServer(executeDownloadJob)
	if err := http.ListenAndServe(":9000", server.Router); err != nil {
		fmt.Printf("Server runtime exception error: %v\n", err)
	}
}
