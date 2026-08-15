package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

var (
	activeCancellations = make(map[string]context.CancelFunc)
	cancelMutex         sync.Mutex
)

// formatETA converts remaining seconds into human-readable strings
func formatETA(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		mins := seconds / 60
		secs := seconds % 60
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	if seconds < 86400 {
		hours := seconds / 3600
		mins := (seconds % 3600) / 60
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	return fmt.Sprintf("%dd %dh", days, hours)
}

func main() {
	daemonMode := flag.Bool("daemon", false, "Run Hydra as a detached background Linux daemon")
	shortDaemonMode := flag.Bool("d", false, "Run Hydra as a detached background Linux daemon (shortcut)")
	flag.Parse()

	if *daemonMode || *shortDaemonMode {
		fmt.Println("[⚙] Detaching process from terminal session...")
		storage.InitializeDaemon()
	}

	// 1. Initialize SQLite Database Engine
	dbStore, err := storage.GetDBStore()
	if err != nil {
		fmt.Printf("[X] Critical: Failed to initialize SQLite storage: %v\n", err)
		os.Exit(1)
	}
	defer dbStore.Close()
	fmt.Println("[✓] SQLite database initialized successfully at ~/.local/share/hydra/hydra.db")

	// 2. Reconcile Interrupted Tasks on Boot
	allJobs := dbStore.GetAllJobs()
	for _, job := range allJobs {
		if job.Status == "DOWNLOADING" {
			_ = dbStore.UpdateStatus(job.ID, "PAUSED")
			fmt.Printf("[⚙] Recovered task %s (%s) -> marked PAUSED\n", job.ID, job.FileName)
		}
	}

	// 3. Root Context for Graceful Shutdown
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	storage.GlobalCancelMap = activeCancellations
	storage.GlobalCancelMutex = &cancelMutex

	executeDownloadJob := func(url string, savePath string, jobID string, headers map[string]string) {
		cancelMutex.Lock()
		jobCtx, jobCancel := context.WithCancel(rootCtx)
		activeCancellations[jobID] = jobCancel
		cancelMutex.Unlock()

		defer func() {
			cancelMutex.Lock()
			delete(activeCancellations, jobID)
			cancelMutex.Unlock()
		}()

		metadata, err := downloader.GetMetadata(url, headers)
		if err != nil {
			fmt.Printf("[X] Handshake error for %s: %v\n", url, err)
			_ = dbStore.UpdateStatus(jobID, "FAILED")
			return
		}

		var totalSizeStr string
		if metadata.Size > 0 {
			totalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))
		} else {
			totalSizeStr = "Unknown"
		}
		_ = dbStore.UpdateTotalSize(jobID, totalSizeStr)

		cleanName := filepath.Base(savePath)
		_ = dbStore.UpdateProgress(jobID, 0.0, "0.00 MB", "0.00 KB/s", "--", cleanName, "DOWNLOADING")

		var trackers []*downloader.AdaptiveTracker
		var totalDownloaded int64 = 0
		stateLoaded := false
		numThreads := 4

		if !metadata.AcceptRanges || metadata.Size <= 0 {
			numThreads = 1
		}

		savedJob, hasSavedJob := dbStore.GetJob(jobID)
		if hasSavedJob && len(savedJob.Chunks) > 0 {
			stateLoaded = true
			numThreads = len(savedJob.Chunks)
			fmt.Printf("[⚙] Resuming task %s via database chunk snapshot...\n", jobID)
			for _, cs := range savedJob.Chunks {
				trackers = append(trackers, &downloader.AdaptiveTracker{
					Index:       cs.Index,
					CurrentPtr:  cs.CurrentOffset,
					EndBoundary: cs.End,
				})
				totalDownloaded += (cs.CurrentOffset - cs.Start)
			}
		}

		var sharedFile *os.File
		if stateLoaded {
			sharedFile, err = os.OpenFile(savePath, os.O_RDWR, 0666)
			if err != nil {
				fmt.Printf("[X] Failed to open target file for resume: %v\n", err)
				_ = dbStore.UpdateStatus(jobID, "FAILED")
				return
			}
		} else {
			sharedFile, err = storage.PreallocateSpace(savePath, metadata.Size)
			if err != nil {
				fmt.Println("[X] Pre-allocation failed:", err)
				_ = dbStore.UpdateStatus(jobID, "FAILED")
				return
			}
		}
		defer sharedFile.Close()

		if !stateLoaded {
			initialChunks := downloader.CalculateChunks(metadata.Size, numThreads)
			for _, ch := range initialChunks {
				trackers = append(trackers, &downloader.AdaptiveTracker{
					Index:       ch.Index,
					CurrentPtr:  ch.Start,
					EndBoundary: ch.End,
				})
			}
		}

		downloadDone := make(chan bool, 1)
		workerErrors := make(chan error, numThreads)
		progressChan := make(chan int64, 2000)
		tempStateChan := make(chan downloader.Chunk, numThreads*2)

		var wg sync.WaitGroup

		// State Persistence Routine
		var stateWg sync.WaitGroup
		stateWg.Add(1)
		go func() {
			defer stateWg.Done()
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			dirty := false

			buildChunkStates := func() []models.ChunkState {
				var states []models.ChunkState
				for _, tr := range trackers {
					current := tr.GetCurrent()
					end := tr.GetEnd()
					states = append(states, models.ChunkState{
						Index:         tr.Index,
						Start:         current,
						CurrentOffset: current,
						End:           end,
						Completed:     current >= end,
					})
				}
				return states
			}

			for {
				select {
				case _, ok := <-tempStateChan:
					if !ok {
						if dirty {
							_ = dbStore.UpdateJobChunks(jobID, buildChunkStates())
						}
						return
					}
					dirty = true
				case <-ticker.C:
					if dirty {
						_ = dbStore.UpdateJobChunks(jobID, buildChunkStates())
						dirty = false
					}
				}
			}
		}()

		// Telemetry Controller Routine (Calculates Speed & ETA)
		go func() {
			var lastDownloaded int64 = 0
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			speedStr := "0.00 KB/s"
			etaStr := "--"

			go func() {
				for range ticker.C {
					deltaBytes := totalDownloaded - lastDownloaded
					lastDownloaded = totalDownloaded

					if deltaBytes > 1024*1024 {
						speedStr = fmt.Sprintf("%.2f MB/s", float64(deltaBytes)/(1024*1024))
					} else if deltaBytes > 1024 {
						speedStr = fmt.Sprintf("%.2f KB/s", float64(deltaBytes)/1024)
					} else {
						speedStr = "0.00 KB/s"
					}

					// Compute ETA dynamically
					if metadata.Size > 0 && deltaBytes > 0 {
						remainingBytes := metadata.Size - totalDownloaded
						if remainingBytes > 0 {
							secs := remainingBytes / deltaBytes
							etaStr = formatETA(secs)
						} else {
							etaStr = "0s"
						}
					} else {
						etaStr = "--"
					}
				}
			}()

			for bytes := range progressChan {
				totalDownloaded += bytes
				downloadedStr := fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))

				var percentage float64 = 0.0
				if metadata.Size > 0 {
					percentage = (float64(totalDownloaded) / float64(metadata.Size)) * 100
				}

				_ = dbStore.UpdateProgress(jobID, percentage, downloadedStr, speedStr, etaStr, cleanName, "DOWNLOADING")
			}

			close(tempStateChan)
			stateWg.Wait()
			downloadDone <- true
		}()

		// Launch Worker Threads
		go func() {
			for i := 0; i < numThreads; i++ {
				wg.Add(1)
				go downloader.DownloadChunkParallel(jobCtx, metadata.FinalURL, i, trackers, sharedFile, &wg, workerErrors, progressChan, tempStateChan, headers)
			}
			wg.Wait()
			close(progressChan)
		}()

		select {
		case <-downloadDone:
			close(workerErrors)

			if jobCtx.Err() != nil {
				fmt.Printf("[⏸] Job %s suspended.\n", jobID)
				_ = dbStore.UpdateStatus(jobID, "PAUSED")
				return
			}

			if len(workerErrors) > 0 {
				firstErr := <-workerErrors
				fmt.Printf("[X] Task %s failed: %v\n", jobID, firstErr)
				_ = dbStore.UpdateStatus(jobID, "FAILED")
				return
			}

			var finalSizeStr string
			if metadata.Size > 0 {
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))
			} else {
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))
			}

			_ = dbStore.UpdateProgress(jobID, 100.0, finalSizeStr, "--", "--", cleanName, "COMPLETED")
			storage.ClearJobState(savePath)
			fmt.Printf("\n=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)

		case workerErr := <-workerErrors:
			if jobCtx.Err() == nil {
				fmt.Printf("\n[X] Thread panic: %v\n", workerErr)
				_ = dbStore.UpdateStatus(jobID, "FAILED")
			}
			return
		}
	}

	// 4. Start IPC Server
	go storage.StartIPCServer(func(url string, savePath string, jobID string) {
		executeDownloadJob(url, savePath, jobID, make(map[string]string))
	})

	// 5. Start HTTP Server
	server := storage.NewServer(executeDownloadJob)
	httpServer := &http.Server{
		Addr:    "127.0.0.1:9000",
		Handler: server.Router,
	}

	go func() {
		fmt.Println("[⚙] Hydra UI Dashboard Server running on http://127.0.0.1:9000")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("HTTP Server error: %v\n", err)
		}
	}()

	// 6. Graceful Shutdown Signal Trap
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	fmt.Printf("\n[🛑] Captured signal %v: Shutting down Hydra gracefully...\n", sig)

	rootCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)

	_ = os.Remove(storage.GetSocketPath())
	fmt.Println("[✓] All resources flushed. Goodbye!")
}
