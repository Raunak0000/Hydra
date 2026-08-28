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
	"sync/atomic"
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

func formatETA(sec int64) string {
	if sec <= 0 {
		return "0s"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	m := sec / 60
	s := sec % 60
	if m < 60 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := m / 60
	m = m % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

func calculateDynamicThreads(size int64, acceptRanges bool) int {
	if !acceptRanges || size <= 0 {
		return 1
	}
	switch {
	case size < 10*1024*1024: // < 10 MB
		return 4
	case size < 100*1024*1024: // < 100 MB
		return 8
	case size < 1024*1024*1024: // < 1 GB
		return 16
	default: // >= 1 GB
		return 32
	}
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

	var executeDownloadJob func(url string, savePath string, jobID string, headers map[string]string)

	executeDownloadJob = func(url string, savePath string, jobID string, headers map[string]string) {
		jobCtx, jobCancel := context.WithCancel(rootCtx)

		cancelMutex.Lock()
		activeCancellations[jobID] = jobCancel
		cancelMutex.Unlock()

		defer func() {
			jobCancel() // Ensure context is always cleaned up on exit
			cancelMutex.Lock()
			delete(activeCancellations, jobID)
			cancelMutex.Unlock()

			storage.GetQueueManager().ProcessNext()
		}()

		// 1. Resolve destination path
		resolvedPath, err := storage.ResolvePath(savePath)
		if err != nil {
			fmt.Printf("[X] Invalid destination path '%s' for job %s: %v\n", savePath, jobID, err)
			_ = dbStore.UpdateStatus(jobID, "FAILED")
			storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
			storage.NotifyDownloadFailed(filepath.Base(savePath), "Invalid destination path")
			return
		}
		savePath = resolvedPath
		cleanName := filepath.Base(savePath)

		// 2. Fetch Metadata
		metadata, err := downloader.GetMetadata(url, headers)
		if err != nil {
			fmt.Printf("[X] Handshake error for %s (%s): %v\n", jobID, url, err)
			_ = dbStore.UpdateStatus(jobID, "FAILED")
			storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
			storage.NotifyDownloadFailed(cleanName, err.Error())
			return
		}

		var totalSizeStr string
		if metadata.Size > 0 {
			totalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))
		} else {
			totalSizeStr = "Dynamic Stream"
		}
		_ = dbStore.UpdateTotalSize(jobID, totalSizeStr)

		var trackers []*downloader.AdaptiveTracker
		var totalDownloaded int64 = 0
		stateLoaded := false
		numThreads := calculateDynamicThreads(metadata.Size, metadata.AcceptRanges)

		savedJob, hasSavedJob := dbStore.GetJob(jobID)
		if hasSavedJob && len(savedJob.Chunks) > 0 {
			stateLoaded = true
			numThreads = len(savedJob.Chunks) // Sync worker count
			fmt.Printf("[⚙] Resuming task %s via database chunk snapshot...\n", jobID)
			for _, cs := range savedJob.Chunks {
				trackers = append(trackers, &downloader.AdaptiveTracker{
					Index:       cs.Index,
					StartByte:   cs.Start,
					CurrentPtr:  cs.CurrentOffset,
					EndBoundary: cs.End,
				})
				if cs.CurrentOffset > cs.Start {
					totalDownloaded += (cs.CurrentOffset - cs.Start)
				}
			}
		}

		initialPercentage := 0.0
		initialDownloadedStr := "0.00 MB"
		if stateLoaded {
			initialDownloadedStr = fmt.Sprintf("%.2f MB", float64(totalDownloaded)/(1024*1024))
			if metadata.Size > 0 {
				initialPercentage = (float64(totalDownloaded) / float64(metadata.Size)) * 100
			}
		}
		_ = dbStore.UpdateProgress(jobID, initialPercentage, initialDownloadedStr, "0.00 KB/s", "--", cleanName, "DOWNLOADING")
		storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())

		var sharedFile *os.File
		if stateLoaded {
			sharedFile, err = os.OpenFile(savePath, os.O_RDWR, 0666)
			if err != nil {
				fmt.Printf("[X] Failed to open target file for resume: %v\n", err)
				_ = dbStore.UpdateStatus(jobID, "FAILED")
				storage.NotifyDownloadFailed(cleanName, "Could not open target file for resume")
				return
			}
		} else {
			sharedFile, err = storage.PreallocateSpace(savePath, metadata.Size)
			if err != nil {
				fmt.Println("[X] Pre-allocation failed:", err)
				_ = dbStore.UpdateStatus(jobID, "FAILED")
				storage.NotifyDownloadFailed(cleanName, "Disk pre-allocation failed")
				return
			}
			initialChunks := downloader.CalculateChunks(metadata.Size, numThreads)
			for _, ch := range initialChunks {
				trackers = append(trackers, &downloader.AdaptiveTracker{
					Index:       ch.Index,
					StartByte:   ch.Start,
					CurrentPtr:  ch.Start,
					EndBoundary: ch.End,
				})
			}
		}
		defer sharedFile.Close()

		downloadDone := make(chan struct{})
		workerErrors := make(chan error, numThreads)
		progressChan := make(chan int64, 4000)
		tempStateChan := make(chan downloader.Chunk, numThreads*4)

		// 3. State Persistence Routine
		var stateWg sync.WaitGroup
		stateWg.Add(1)
		go func() {
			defer stateWg.Done()
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()
			dirty := false

			buildChunkStates := func() []models.ChunkState {
				states := make([]models.ChunkState, 0, len(trackers))
				for _, tr := range trackers {
					start := tr.GetStart()
					current := tr.GetCurrent()
					end := tr.GetEnd()
					states = append(states, models.ChunkState{
						Index:         tr.Index,
						Start:         start,
						CurrentOffset: current,
						End:           end,
						Completed:     end > 0 && current >= end,
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

		// 4. Telemetry Controller Routine
		go func() {
			var lastDownloaded int64 = totalDownloaded
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()

			telemetryCtx, cancelTelemetry := context.WithCancel(jobCtx)
			defer cancelTelemetry()

			go func() {
				for {
					select {
					case <-telemetryCtx.Done():
						return
					case <-ticker.C:
						currentDownloaded := atomic.LoadInt64(&totalDownloaded)
						deltaBytes := currentDownloaded - lastDownloaded
						lastDownloaded = currentDownloaded

						speedBytesPerSec := deltaBytes * 4
						var speedStr string
						if speedBytesPerSec > 1024*1024 {
							speedStr = fmt.Sprintf("%.2f MB/s", float64(speedBytesPerSec)/(1024*1024))
						} else if speedBytesPerSec > 1024 {
							speedStr = fmt.Sprintf("%.2f KB/s", float64(speedBytesPerSec)/1024)
						} else {
							speedStr = "0.00 KB/s"
						}

						etaStr := "--"
						if metadata.Size > 0 && speedBytesPerSec > 0 {
							remaining := metadata.Size - currentDownloaded
							if remaining > 0 {
								etaStr = formatETA(remaining / speedBytesPerSec)
							} else {
								etaStr = "0s"
							}
						}

						downloadedStr := fmt.Sprintf("%.2f MB", float64(currentDownloaded)/(1024*1024))
						percentage := 0.0
						if metadata.Size > 0 {
							percentage = (float64(currentDownloaded) / float64(metadata.Size)) * 100
						}

						_ = dbStore.UpdateProgress(jobID, percentage, downloadedStr, speedStr, etaStr, cleanName, "DOWNLOADING")
						storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
					}
				}
			}()

			for bytes := range progressChan {
				atomic.AddInt64(&totalDownloaded, bytes)
			}

			cancelTelemetry()
			close(tempStateChan)
			stateWg.Wait()
			close(downloadDone)
		}()

		// 5. Rate Limiter
		var limiter *downloader.RateLimiter
		if savedJob.MaxSpeedBytes > 0 {
			limiter = downloader.NewRateLimiter(savedJob.MaxSpeedBytes)
		}

		// 6. Launch Worker Pool
		var workerWg sync.WaitGroup
		for i := 0; i < numThreads; i++ {
			workerWg.Add(1)
			go downloader.DownloadChunkParallel(jobCtx, metadata.FinalURL, i, trackers, sharedFile, &workerWg, workerErrors, progressChan, tempStateChan, headers, limiter)
		}

		// Close progress channel once all workers return
		go func() {
			workerWg.Wait()
			close(progressChan)
		}()

		// 7. Await completion or worker error
		select {
		case err := <-workerErrors:
			jobCancel() // Stop peer workers immediately
			if jobCtx.Err() == nil || jobCtx.Err() == context.Canceled {
				fmt.Printf("[X] Task %s worker error: %v\n", jobID, err)
				_ = dbStore.UpdateStatus(jobID, "FAILED")
				storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
				storage.NotifyDownloadFailed(cleanName, err.Error())
			}
			<-downloadDone // Ensure channels flush before exiting
			return

		case <-downloadDone:
			if jobCtx.Err() != nil {
				fmt.Printf("[⏸] Job %s suspended.\n", jobID)
				_ = dbStore.UpdateStatus(jobID, "PAUSED")
				storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
				return
			}

			finalSizeStr := fmt.Sprintf("%.2f MB", float64(atomic.LoadInt64(&totalDownloaded))/(1024*1024))
			if metadata.Size > 0 {
				finalSizeStr = fmt.Sprintf("%.2f MB", float64(metadata.Size)/(1024*1024))
			}

			// Checksum validation
			job, _ := dbStore.GetJob(jobID)
			if job.ExpectedChecksum != "" && job.ChecksumAlgo != "" {
				res, err := downloader.VerifyFileChecksum(savePath, job.ExpectedChecksum, job.ChecksumAlgo)
				if err != nil || !res.Matched {
					fmt.Printf("[X] Checksum verification failed for %s! Expected %s, got %s\n", jobID, res.Expected, res.Computed)
					_ = dbStore.UpdateStatus(jobID, "CHECKSUM_FAILED")
					storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
					storage.NotifyChecksumFailed(cleanName)
					return
				}
				fmt.Printf("[✓] Checksum (%s) verified successfully for %s\n", res.Algorithm, cleanName)
				_ = dbStore.UpdateChecksumVerified(jobID, true)
			}

			_ = dbStore.UpdateProgress(jobID, 100.0, finalSizeStr, "--", "--", cleanName, "COMPLETED")
			storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
			storage.ClearJobState(savePath)
			storage.NotifyDownloadComplete(cleanName, savePath)
			fmt.Printf("\n=== SUCCESS: FILE SAVED SAFELY TO %s ===\n", savePath)
		}
	}

	storage.InitQueueManager(2, executeDownloadJob)

	go storage.StartIPCServer(func(url string, savePath string, jobID string) {
		executeDownloadJob(url, savePath, jobID, make(map[string]string))
	})

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
