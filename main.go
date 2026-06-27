package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

// Global map to track active cancel functions per job ID
var (
	activeCancellations = make(map[string]context.CancelFunc)
	cancelMutex         sync.Mutex
)

func main() {
	// 1. DEFINE THE CORE MULTI-THREADED PIPELINE ENGINE
	executeDownloadJob := func(url string, savePath string, jobID string) {
		store := storage.GetStore()

		// Set up context cancellation tracking for this active execution job run
		cancelMutex.Lock()
		jobCtx, jobCancel := context.WithCancel(context.Background())
		activeCancellations[jobID] = jobCancel
		cancelMutex.Unlock()

		defer func() {
			cancelMutex.Lock()
			delete(activeCancellations, jobID)
			cancelMutex.Unlock()
		}()

		// Phase A: Handshake & Multi-stage redirect verification
		metadata, err := downloader.GetMetadata(url)
		if err != nil {
			fmt.Printf("[X] Handshake system error for %s: %v\n", url, err)
			store.UpdateStatus(jobID, "FAILED")
			return
		}

		var totalSizeStr string
		if metadata.Size > 0 {
			totalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))
		} else {
			totalSizeStr = "Unknown"
		}
		store.UpdateTotalSize(jobID, totalSizeStr)

		var cleanName string
		if parts := strings.Split(savePath, "/"); len(parts) > 0 {
			cleanName = parts[len(parts)-1]
		}
		if cleanName != "" {
			store.UpdateProgress(jobID, 0.0, "0.00 MB", "0.00 KB/s", cleanName, "DOWNLOADING")
		}

		// Load state if exists
		var chunks []downloader.Chunk
		var totalDownloaded int64 = 0
		stateLoaded := false

		jobState, loadErr := storage.LoadJobState(savePath)
		if loadErr == nil && len(jobState.Chunks) > 0 {
			stateLoaded = true
			fmt.Printf("[⚙] Resuming download for job %s from saved state...\n", jobID)
			for _, cs := range jobState.Chunks {
				chunks = append(chunks, downloader.Chunk{
					Index: cs.Index,
					Start: cs.CurrentOffset,
					End:   cs.End,
				})
				// Accumulate already downloaded bytes
				totalDownloaded += (cs.CurrentOffset - cs.Start)
			}
		}

		var sharedFile *os.File
		if stateLoaded {
			sharedFile, err = os.OpenFile(savePath, os.O_RDWR, 0666)
			if err != nil {
				fmt.Printf("[X] Failed to open target file for resume: %v\n", err)
				store.UpdateStatus(jobID, "FAILED")
				return
			}
		} else {
			// Phase B: Low-Level continuous Linux kernel storage pre-allocation
			fmt.Printf("[⚙] Pre-allocating continuous physical space footprint at: %s\n", savePath)
			sharedFile, err = storage.PreallocateSpace(savePath, metadata.Size)
			if err != nil {
				fmt.Println("[X] Pre-allocation allocation failed:", err)
				store.UpdateStatus(jobID, "FAILED")
				return
			}
		}
		defer sharedFile.Close()

		numThreads := 4
		if !metadata.AcceptRanges || metadata.Size <= 0 {
			numThreads = 1
		}

		if !stateLoaded {
			chunks = downloader.CalculateChunks(metadata.Size, numThreads)
		}

		// Initialize the chunk states in memory for serialization
		var chunkStates []models.ChunkState
		if stateLoaded {
			chunkStates = jobState.Chunks
		} else {
			for _, chunk := range chunks {
				chunkStates = append(chunkStates, models.ChunkState{
					Index:         chunk.Index,
					Start:         chunk.Start,
					CurrentOffset: chunk.Start,
					End:           chunk.End,
					Completed:     false,
				})
			}
		}

		// Create native communication primitives for safe data routing
		downloadDone := make(chan bool, 1)
		workerErrors := make(chan error, numThreads)
		progressChan := make(chan int64, 2000)
		tempStateChan := make(chan downloader.Chunk, numThreads*2)

		var wg sync.WaitGroup

		// Draining tempStateChan loop to serialize state periodically
		var stateWg sync.WaitGroup
		stateWg.Add(1)
		go func() {
			defer stateWg.Done()
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			dirty := false

			for {
				select {
				case chunkUpdate, ok := <-tempStateChan:
					if !ok {
						if dirty {
							jobState := models.UIJob{
								ID:         jobID,
								FileName:   cleanName,
								URL:        url,
								SavePath:   savePath,
								Progress:   (float64(totalDownloaded) / float64(metadata.Size)) * 100,
								TotalSize:  totalSizeStr,
								Downloaded: fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024)),
								Status:     "DOWNLOADING",
								Chunks:     chunkStates,
							}
							if metadata.Size <= 0 {
								jobState.Progress = 0.0
							}
							_ = storage.SaveJobState(jobState)
						}
						return
					}
					if chunkUpdate.Index >= 0 && chunkUpdate.Index < len(chunkStates) {
						chunkStates[chunkUpdate.Index].CurrentOffset = chunkUpdate.Start
						if chunkUpdate.Start >= chunkUpdate.End {
							chunkStates[chunkUpdate.Index].Completed = true
						}
						dirty = true
					}
				case <-ticker.C:
					if dirty {
						jobState := models.UIJob{
							ID:         jobID,
							FileName:   cleanName,
							URL:        url,
							SavePath:   savePath,
							Progress:   (float64(totalDownloaded) / float64(metadata.Size)) * 100,
							TotalSize:  totalSizeStr,
							Downloaded: fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024)),
							Status:     "DOWNLOADING",
							Chunks:     chunkStates,
						}
						if metadata.Size <= 0 {
							jobState.Progress = 0.0
						}
						_ = storage.SaveJobState(jobState)
						dirty = false
					}
				}
			}
		}()

		// ── UNIFORM AND SECURE METRIC TRACKING PIPELINE LOOP ──
		go func() {
			var totalDownloaded int64 = 0
			var lastDownloaded int64 = 0

			// Compute a 1-second interval delta window tick
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			var speedStr string = "0.00 KB/s"

			for {
				select {
				case bytes, ok := <-progressChan:
					if !ok {
						close(tempStateChan) // cite: 297
						stateWg.Wait()       // cite: 297
						downloadDone <- true // cite: 297
						return
					}
					totalDownloaded += bytes

					// Format ongoing values instantly
					downloadedStr := fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))
					var cleanFilename string
					if parts := strings.Split(savePath, "/"); len(parts) > 0 {
						cleanFilename = parts[len(parts)-1]
					}

					if metadata.Size > 0 {
						percentage := (float64(totalDownloaded) / float64(metadata.Size)) * 100
						store.UpdateProgress(jobID, percentage, downloadedStr, speedStr, cleanFilename, "DOWNLOADING")
					} else {
						store.UpdateProgress(jobID, 0.0, downloadedStr, speedStr, cleanFilename, "DOWNLOADING")
					}

				case <-ticker.C:
					// Calculate differential bytes processed over the past 1 second window
					deltaBytes := totalDownloaded - lastDownloaded
					lastDownloaded = totalDownloaded

					// Generate human readable speed metrics
					if deltaBytes > 1024*1024 {
						speedStr = fmt.Sprintf("%.2f MB/s", float64(deltaBytes)/(1024*1024))
					} else {
						speedStr = fmt.Sprintf("%.2f KB/s", float64(deltaBytes)/1024)
					}
				}
			}
		}()

		// Phase C: Launch Parallel Thread Worker Pools
		go func() {
			for _, chunk := range chunks {
				// Only download chunks that are not completed yet
				if stateLoaded && chunk.Start >= chunk.End {
					continue
				}
				wg.Add(1)
				go downloader.DownloadChunkParallel(jobCtx, metadata.FinalURL, chunk, sharedFile, &wg, workerErrors, progressChan, tempStateChan)
			}
			wg.Wait()
			close(progressChan)
		}()

		cancelChan := downloader.SetupSignalHandling(make(chan bool))

		// Phase E: Coordinate the Finish Line Status States
		select {
		case <-downloadDone:
			close(workerErrors)

			if jobCtx.Err() != nil {
				fmt.Printf("[⏸] Job %s successfully suspended by runtime intervention.\n", jobID)
				store.UpdateStatus(jobID, "PAUSED")
				return
			}

			if len(workerErrors) > 0 {
				firstErr := <-workerErrors
				fmt.Printf("\n[X] CRITICAL ABORT: Thread failure detected: %v\n", firstErr)
				store.UpdateStatus(jobID, "FAILED")
				os.Remove(savePath)
				storage.ClearJobState(savePath)
				return
			}

			var finalSizeStr string
			if metadata.Size > 0 {
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))
			} else {
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))
			}
			var cleanFilename string
			if parts := strings.Split(savePath, "/"); len(parts) > 0 {
				cleanFilename = parts[len(parts)-1]
			}

			storage.GetStore().UpdateProgress(jobID, 100.0, finalSizeStr, "0.00 KB/s", cleanFilename, "COMPLETED")
			storage.ClearJobState(savePath)
			fmt.Printf("\n=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)

		case workerErr := <-workerErrors:
			if jobCtx.Err() == nil {
				fmt.Printf("\n[X] PIPELINE CRASHED: Intercepted thread panic: %v\n", workerErr)
				store.UpdateStatus(jobID, "FAILED")
				os.Remove(savePath)
				storage.ClearJobState(savePath)
			}
			return

		case <-cancelChan:
			fmt.Println("[🛑] Job signature canceled by hardware kernel interrupt.")
			return
		}
	}

	// Expose our internal cancel map hook directly to the storage server layout structure
	storage.GlobalCancelMap = activeCancellations
	storage.GlobalCancelMutex = &cancelMutex

	fmt.Println("[⚙] Hydra UI Dashboard Server running on http://localhost:9000")
	server := storage.NewServer(executeDownloadJob)
	if err := http.ListenAndServe(":9000", server.Router); err != nil {
		fmt.Printf("Server runtime exception error: %v\n", err)
	}
}
