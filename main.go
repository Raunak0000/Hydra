package main

import (
	"context"
	"flag"
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

// main.go -> Update the top of main() to parse daemon flags

func main() {
	// 1. CHOOSE BACKGROUND EXECUTION FLAGS
	daemonMode := flag.Bool("daemon", false, "Run Hydra core server as a detached background Linux daemon process")
	shortDaemonMode := flag.Bool("d", false, "Run Hydra core server as a detached background Linux daemon process (shortcut)")
	flag.Parse()

	// 2. CHECK IF DETACHMENT IS REQUESTED
	if *daemonMode || *shortDaemonMode {
		fmt.Println("[⚙] Detaching process from terminal session context...")
		storage.InitializeDaemon() // 🚀 Sever the TTY bond and fork grandchild log streams!
	}

	// main.go -> Updated executeDownloadJob with Phase 7 Atomic Work-Stealing Grid

	executeDownloadJob := func(url string, savePath string, jobID string, headers map[string]string) { // cite: file(1).txt
		store := storage.GetStore() // cite: file(1).txt

		cancelMutex.Lock()                                            // cite: file(1).txt
		jobCtx, jobCancel := context.WithCancel(context.Background()) // cite: file(1).txt
		activeCancellations[jobID] = jobCancel                        // cite: file(1).txt
		cancelMutex.Unlock()                                          // cite: file(1).txt

		defer func() { // cite: file(1).txt
			cancelMutex.Lock()                 // cite: file(1).txt
			delete(activeCancellations, jobID) // cite: file(1).txt
			cancelMutex.Unlock()               // cite: file(1).txt
		}() // cite: file(1).txt

		metadata, err := downloader.GetMetadata(url, headers) // cite: file(1).txt
		if err != nil {                                       // cite: file(1).txt
			fmt.Printf("[X] Handshake system error for %s: %v\n", url, err) // cite: file(1).txt
			store.UpdateStatus(jobID, "FAILED")                             // cite: file(1).txt
			return                                                          // cite: file(1).txt
		}

		var totalSizeStr string // cite: file(1).txt
		if metadata.Size > 0 {  // cite: file(1).txt
			totalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024)) // cite: file(1).txt
		} else { // cite: file(1).txt
			totalSizeStr = "Unknown" // cite: file(1).txt
		} // cite: file(1).txt
		store.UpdateTotalSize(jobID, totalSizeStr) // cite: file(1).txt

		var cleanName string                                       // cite: file(1).txt
		if parts := strings.Split(savePath, "/"); len(parts) > 0 { // cite: file(1).txt
			cleanName = parts[len(parts)-1] // cite: file(1).txt
		} // cite: file(1).txt
		if cleanName != "" { // cite: file(1).txt
			store.UpdateProgress(jobID, 0.0, "0.00 MB", "0.00 KB/s", cleanName, "DOWNLOADING") // cite: file(1).txt
		}

		var trackers []*downloader.AdaptiveTracker
		var totalDownloaded int64 = 0
		stateLoaded := false
		numThreads := 4 // cite: file(1).txt

		if !metadata.AcceptRanges || metadata.Size <= 0 { // cite: file(1).txt
			numThreads = 1 // cite: file(1).txt
		}

		// 1. STATE LOAD ENHANCEMENT: Rebuild dynamic trackers from .hydra file configs if they exist
		jobState, loadErr := storage.LoadJobState(savePath) // cite: file(1).txt
		if loadErr == nil && len(jobState.Chunks) > 0 {     // cite: file(1).txt
			stateLoaded = true // cite: file(1).txt
			numThreads = len(jobState.Chunks)
			fmt.Printf("[⚙] Resuming download for job %s via Adaptive Grid snapshot...\n", jobID) // cite: file(1).txt
			for _, cs := range jobState.Chunks {                                                  // cite: file(1).txt
				trackers = append(trackers, &downloader.AdaptiveTracker{
					Index:       cs.Index,
					CurrentPtr:  cs.CurrentOffset,
					EndBoundary: cs.End,
				})
				totalDownloaded += (cs.CurrentOffset - cs.Start) // cite: file(1).txt
			}
		}

		var sharedFile *os.File // cite: file(1).txt
		if stateLoaded {        // cite: file(1).txt
			sharedFile, err = os.OpenFile(savePath, os.O_RDWR, 0666) // cite: file(1).txt
			if err != nil {                                          // cite: file(1).txt
				fmt.Printf("[X] Failed to open target file for resume: %v\n", err) // cite: file(1).txt
				store.UpdateStatus(jobID, "FAILED")                                // cite: file(1).txt
				return                                                             // cite: file(1).txt
			}
		} else { // cite: file(1).txt
			fmt.Printf("[⚙] Pre-allocating continuous physical space footprint at: %s\n", savePath) // cite: file(1).txt
			sharedFile, err = storage.PreallocateSpace(savePath, metadata.Size)                     // cite: file(1).txt
			if err != nil {                                                                         // cite: file(1).txt
				fmt.Println("[X] Pre-allocation allocation failed:", err) // cite: file(1).txt
				store.UpdateStatus(jobID, "FAILED")                       // cite: file(1).txt
				return                                                    // cite: file(1).txt
			}
		} // cite: file(1).txt
		defer sharedFile.Close() // cite: file(1).txt

		// 2. FRESH INITIALIZATION: If no snapshot, calculate standard balanced base offsets
		if !stateLoaded {
			initialChunks := downloader.CalculateChunks(metadata.Size, numThreads) // cite: file(1).txt
			for _, ch := range initialChunks {
				trackers = append(trackers, &downloader.AdaptiveTracker{
					Index:       ch.Index,
					CurrentPtr:  ch.Start,
					EndBoundary: ch.End,
				})
			}
		}

		downloadDone := make(chan bool, 1)                         // cite: file(1).txt
		workerErrors := make(chan error, numThreads)               // cite: file(1).txt
		progressChan := make(chan int64, 2000)                     // cite: file(1).txt
		tempStateChan := make(chan downloader.Chunk, numThreads*2) // cite: file(1).txt

		var wg sync.WaitGroup // cite: file(1).txt

		// 3. PERSISTENCE ENGINE Realignment
		var stateWg sync.WaitGroup // cite: file(1).txt
		stateWg.Add(1)             // cite: file(1).txt
		go func() {                // cite: file(1).txt
			defer stateWg.Done()                      // cite: file(1).txt
			ticker := time.NewTicker(1 * time.Second) // cite: file(1).txt
			defer ticker.Stop()                       // cite: file(1).txt
			dirty := false                            // cite: file(1).txt

			// Remap dynamic trackers into serializable array elements
			buildChunkStates := func() []models.ChunkState {
				var states []models.ChunkState
				for _, tr := range trackers {
					current := tr.GetCurrent()
					end := tr.GetEnd()
					states = append(states, models.ChunkState{
						Index:         tr.Index,
						Start:         current - (current - tr.GetCurrent()), // Retain baseline reference markers
						CurrentOffset: current,
						End:           end,
						Completed:     current >= end,
					})
				}
				return states
			}

			for { // cite: file(1).txt
				select { // cite: file(1).txt
				case _, ok := <-tempStateChan: // cite: file(1).txt
					if !ok { // cite: file(1).txt
						if dirty { // cite: file(1).txt
							jobState := models.UIJob{ // cite: file(1).txt
								ID:         jobID,                                                        // cite: file(1).txt
								FileName:   cleanName,                                                    // cite: file(1).txt
								URL:        url,                                                          // cite: file(1).txt
								SavePath:   savePath,                                                     // cite: file(1).txt
								Progress:   (float64(totalDownloaded) / float64(metadata.Size)) * 100,    // cite: file(1).txt
								TotalSize:  totalSizeStr,                                                 // cite: file(1).txt
								Downloaded: fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024)), // cite: file(1).txt
								Status:     "DOWNLOADING",                                                // cite: file(1).txt
								Chunks:     buildChunkStates(),
							} // cite: file(1).txt
							if metadata.Size <= 0 { // cite: file(1).txt
								jobState.Progress = 0.0 // cite: file(1).txt
							} // cite: file(1).txt
							_ = storage.SaveJobState(jobState) // cite: file(1).txt
						} // cite: file(1).txt
						return // cite: file(1).txt
					} // cite: file(1).txt
					dirty = true // cite: file(1).txt
				case <-ticker.C: // cite: file(1).txt
					if dirty { // cite: file(1).txt
						jobState := models.UIJob{ // cite: file(1).txt
							ID:         jobID,                                                        // cite: file(1).txt
							FileName:   cleanName,                                                    // cite: file(1).txt
							URL:        url,                                                          // cite: file(1).txt
							SavePath:   savePath,                                                     // cite: file(1).txt
							Progress:   (float64(totalDownloaded) / float64(metadata.Size)) * 100,    // cite: file(1).txt
							TotalSize:  totalSizeStr,                                                 // cite: file(1).txt
							Downloaded: fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024)), // cite: file(1).txt
							Status:     "DOWNLOADING",                                                // cite: file(1).txt
							Chunks:     buildChunkStates(),
						} // cite: file(1).txt
						if metadata.Size <= 0 { // cite: file(1).txt
							jobState.Progress = 0.0 // cite: file(1).txt
						} // cite: file(1).txt
						_ = storage.SaveJobState(jobState) // cite: file(1).txt
						dirty = false                      // cite: file(1).txt
					} // cite: file(1).txt
				} // cite: file(1).txt
			} // cite: file(1).txt
		}() // cite: file(1).txt

		// 4. TELEMETRY CONTROLLER PIPELINE (Channel delta metric accumulator)
		go func() {
			var lastDownloaded int64 = 0              // cite: file(1).txt
			ticker := time.NewTicker(1 * time.Second) // cite: file(1).txt
			defer ticker.Stop()                       // cite: file(1).txt
			speedStr := "0.00 KB/s"                   // cite: file(1).txt

			go func() { // cite: file(1).txt
				for range ticker.C { // cite: file(1).txt
					deltaBytes := totalDownloaded - lastDownloaded // cite: file(1).txt
					lastDownloaded = totalDownloaded               // cite: file(1).txt

					if deltaBytes > 1024*1024 { // cite: file(1).txt
						speedStr = fmt.Sprintf("%.2f MB/s", float64(deltaBytes)/(1024*1024)) // cite: file(1).txt
					} else if deltaBytes > 1024 { // cite: file(1).txt
						speedStr = fmt.Sprintf("%.2f KB/s", float64(deltaBytes)/1024) // cite: file(1).txt
					} else { // cite: file(1).txt
						speedStr = "0.00 KB/s" // cite: file(1).txt
					} // cite: file(1).txt
				} // cite: file(1).txt
			}() // cite: file(1).txt

			for bytes := range progressChan { // cite: file(1).txt
				totalDownloaded += bytes                                                      // cite: file(1).txt
				downloadedStr := fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024)) // cite: file(1).txt

				var cleanFilename string                                   // cite: file(1).txt
				if parts := strings.Split(savePath, "/"); len(parts) > 0 { // cite: file(1).txt
					cleanFilename = parts[len(parts)-1] // cite: file(1).txt
				} // cite: file(1).txt

				// Push live structural updates to sync memory structures
				var activeChunksForUI []models.ChunkState
				for _, tr := range trackers {
					current := tr.GetCurrent()
					activeChunksForUI = append(activeChunksForUI, models.ChunkState{
						Index:         tr.Index,
						CurrentOffset: current,
						End:           tr.GetEnd(),
						Completed:     current >= tr.GetEnd(),
					})
				}

				globalStore := storage.GetStore()
				globalStore.UpdateJobChunks(jobID, activeChunksForUI)

				if metadata.Size > 0 { // cite: file(1).txt
					percentage := (float64(totalDownloaded) / float64(metadata.Size)) * 100                        // cite: file(1).txt
					store.UpdateProgress(jobID, percentage, downloadedStr, speedStr, cleanFilename, "DOWNLOADING") // cite: file(1).txt
				} else { // cite: file(1).txt
					store.UpdateProgress(jobID, 0.0, downloadedStr, speedStr, cleanFilename, "DOWNLOADING") // cite: file(1).txt
				} // cite: file(1).txt
			} // cite: file(1).txt

			close(tempStateChan) // cite: file(1).txt
			stateWg.Wait()       // cite: file(1).txt
			downloadDone <- true // cite: file(1).txt
		}() // cite: file(1).txt

		// 5. RUN ASYNC WORK-STEALING POOLS
		go func() {
			for i := 0; i < numThreads; i++ {
				wg.Add(1)
				go downloader.DownloadChunkParallel(jobCtx, metadata.FinalURL, i, trackers, sharedFile, &wg, workerErrors, progressChan, tempStateChan, headers)
			}
			wg.Wait()           // cite: file(1).txt
			close(progressChan) // cite: file(1).txt
		}() // cite: file(1).txt

		cancelChan := downloader.SetupSignalHandling(make(chan bool)) // cite: file(1).txt

		select { // cite: file(1).txt
		case <-downloadDone: // cite: file(1).txt
			close(workerErrors) // cite: file(1).txt

			if jobCtx.Err() != nil { // cite: file(1).txt
				fmt.Printf("[⏸] Job %s successfully suspended by adaptive runtime intervention.\n", jobID) // cite: file(1).txt
				store.UpdateStatus(jobID, "PAUSED")                                                        // cite: file(1).txt
				return                                                                                     // cite: file(1).txt
			} // cite: file(1).txt

			if len(workerErrors) > 0 { // cite: file(1).txt
				firstErr := <-workerErrors                                                  // cite: file(1).txt
				fmt.Printf("\n[X] CRITICAL ABORT: Thread failure detected: %v\n", firstErr) // cite: file(1).txt
				store.UpdateStatus(jobID, "FAILED")                                         // cite: file(1).txt
				os.Remove(savePath)                                                         // cite: file(1).txt
				storage.ClearJobState(savePath)                                             // cite: file(1).txt
				return                                                                      // cite: file(1).txt
			} // cite: file(1).txt

			var finalSizeStr string // cite: file(1).txt
			if metadata.Size > 0 {  // cite: file(1).txt
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024)) // cite: file(1).txt
			} else { // cite: file(1).txt
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024)) // cite: file(1).txt
			} // cite: file(1).txt
			var cleanFilename string                                   // cite: file(1).txt
			if parts := strings.Split(savePath, "/"); len(parts) > 0 { // cite: file(1).txt
				cleanFilename = parts[len(parts)-1] // cite: file(1).txt
			} // cite: file(1).txt

			storage.GetStore().UpdateProgress(jobID, 100.0, finalSizeStr, "--", cleanFilename, "COMPLETED") // cite: file(1).txt
			storage.ClearJobState(savePath)                                                                 // cite: file(1).txt
			fmt.Printf("\n=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)                            // cite: file(1).txt

		case workerErr := <-workerErrors: // cite: file(1).txt
			if jobCtx.Err() == nil { // cite: file(1).txt
				fmt.Printf("\n[X] PIPELINE CRASHED: Intercepted thread panic: %v\n", workerErr) // cite: file(1).txt
				store.UpdateStatus(jobID, "FAILED")                                             // cite: file(1).txt
				os.Remove(savePath)                                                             // cite: file(1).txt
				storage.ClearJobState(savePath)                                                 // cite: file(1).txt
			} // cite: file(1).txt
			return // cite: file(1).txt

		case <-cancelChan: // cite: file(1).txt
			fmt.Println("[🛑] Job signature canceled by hardware kernel interrupt.") // cite: file(1).txt
			return                                                                  // cite: file(1).txt
		} // cite: file(1).txt
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
