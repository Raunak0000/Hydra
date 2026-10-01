package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func main() {
	dbStore, err := storage.GetDBStore()
	if err != nil {
		log.Fatalf("[X] Critical: Failed to initialize SQLite storage: %v", err)
	}
	defer dbStore.Close()

	storage.GlobalCancelMap = make(map[string]context.CancelFunc)
	storage.GlobalCancelMutex = &sync.Mutex{}

	executeDownloadJob := func(url string, savePath string, jobID string, headers map[string]string) error {
		// Load the complete persisted job configuration.
		job, exists := dbStore.GetJob(jobID)
		if !exists {
			return fmt.Errorf("job %s not found", jobID)
		}

		// The persisted job configuration is authoritative.
		// Keep the function arguments as a fallback for legacy callers.
		if job.URL != "" {
			url = job.URL
		}
		if job.SavePath != "" {
			savePath = job.SavePath
		}
		if job.Headers != nil {
			headers = job.Headers
		}

		ctx, cancel := context.WithCancel(context.Background())

		storage.GlobalCancelMutex.Lock()
		storage.GlobalCancelMap[jobID] = cancel
		storage.GlobalCancelMutex.Unlock()

		defer func() {
			storage.GlobalCancelMutex.Lock()
			delete(storage.GlobalCancelMap, jobID)
			storage.GlobalCancelMutex.Unlock()
			cancel()
		}()

		meta, err := downloader.GetMetadata(url, headers)
		if err != nil {
			_ = dbStore.UpdateErrorMessage(jobID, err.Error())
			storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())
			return err
		}

		totalSizeStr := fmt.Sprintf("%.2f MB", float64(meta.Size)/(1024*1024))
		if meta.Size <= 0 {
			totalSizeStr = "Unknown Size"
		}
		_ = dbStore.UpdateTotalSize(jobID, totalSizeStr)

		file, err := storage.PreallocateSpace(savePath, meta.Size)
		if err != nil {
			_ = dbStore.UpdateErrorMessage(jobID, err.Error())
			storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())
			return err
		}
		defer file.Close()

		numThreads := 8
		if !meta.AcceptRanges || meta.Size <= 0 {
			numThreads = 1
		}

		chunks := downloader.CalculateChunks(meta.Size, numThreads)

		trackers := make([]*downloader.AdaptiveTracker, len(chunks))
		chunkStates := make([]models.ChunkState, len(chunks))

		// ----------------------------------------------------------
		// Restore persisted chunk state when resuming.
		// A fresh download has no chunk state yet.
		// ----------------------------------------------------------
		if len(job.Chunks) == len(chunks) {
			copy(chunkStates, job.Chunks)
		}

		// Build trackers from persisted state when available.
		for i, ch := range chunks {
			state := models.ChunkState{
				Index:         i,
				Start:         ch.Start,
				CurrentOffset: ch.Start,
				End:           ch.End,
				Completed:     false,
			}

			if len(job.Chunks) == len(chunks) {
				state = job.Chunks[i]

				// Validate persisted state before using it.
				if state.Start < 0 || state.End < state.Start {
					state.Start = ch.Start
					state.End = ch.End
					state.CurrentOffset = ch.Start
					state.Completed = false
				}

				if state.CurrentOffset < state.Start ||
					state.CurrentOffset > state.End+1 {
					state.CurrentOffset = state.Start
					state.Completed = false
				}

				// Completed chunks resume at the byte immediately
				// after their inclusive end boundary.
				if state.Completed {
					state.CurrentOffset = state.End + 1
				}
			}

			chunkStates[i] = state

			trackers[i] = &downloader.AdaptiveTracker{
				Index:       state.Index,
				StartByte:   state.Start,
				CurrentPtr:  state.CurrentOffset,
				EndBoundary: state.End,
			}
		}

		// Persist initial state only for a brand-new download.
		// NEVER overwrite an existing resume checkpoint.
		if len(job.Chunks) != len(chunks) {
			_ = dbStore.UpdateJobChunks(jobID, chunkStates)
		}

		var wg sync.WaitGroup
		errChan := make(chan error, numThreads)
		progressChan := make(chan int64, 1024)
		stateChan := make(chan downloader.Chunk, 1024)

		var downloadedBytes int64

		// Prefer the per-job speed limit.
		// Fall back to the global Hydra configuration if no
		// per-job limit was specified.
		cfg := storage.GetConfig()

		speedLimit := job.MaxSpeedBytes
		if speedLimit <= 0 {
			speedLimit = cfg.SpeedLimitBytes
		}

		var limiter *downloader.RateLimiter
		if speedLimit > 0 {
			limiter = downloader.NewRateLimiter(speedLimit)
		}

		for i := 0; i < numThreads; i++ {
			wg.Add(1)

			go downloader.DownloadChunkParallel(
				ctx,
				url,
				i,
				trackers,
				file,
				&wg,
				errChan,
				progressChan,
				stateChan,
				headers,
				limiter,
			)
		}

		done := make(chan struct{})

		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()

			lastDownloaded := int64(0)
			lastTime := time.Now()

			for {
				select {
				case <-done:
					return

				case p := <-progressChan:
					downloadedBytes += p

				case st := <-stateChan:
					if st.Index >= 0 && st.Index < len(chunkStates) {
						chunkStates[st.Index].CurrentOffset = st.Start

						if st.End > 0 && st.Start >= st.End {
							chunkStates[st.Index].Completed = true
						}
					}

				case <-ticker.C:
					now := time.Now()

					duration := now.Sub(lastTime).Seconds()
					if duration <= 0 {
						duration = 0.25
					}

					diff := downloadedBytes - lastDownloaded
					speedBytesPerSec := float64(diff) / duration

					lastDownloaded = downloadedBytes
					lastTime = now

					speedStr := formatSpeed(speedBytesPerSec)
					downloadedStr := formatBytes(downloadedBytes)

					progressPct := 0.0

					if meta.Size > 0 {
						progressPct =
							(float64(downloadedBytes) / float64(meta.Size)) * 100.0

						if progressPct > 100.0 {
							progressPct = 100.0
						}
					}

					etaStr := "--"

					if speedBytesPerSec > 0 && meta.Size > 0 {
						remainingBytes := meta.Size - downloadedBytes

						if remainingBytes > 0 {
							secs := float64(remainingBytes) / speedBytesPerSec
							etaStr = formatETA(secs)
						}
					}

					_ = dbStore.UpdateProgress(
						jobID,
						progressPct,
						downloadedStr,
						speedStr,
						etaStr,
						"",
						"DOWNLOADING",
					)

					_ = dbStore.UpdateJobChunks(jobID, chunkStates)
				}
			}
		}()

		wg.Wait()

		close(done)
		close(progressChan)
		close(stateChan)

		select {
		case err := <-errChan:
			if err != nil && ctx.Err() == nil {
				_ = dbStore.UpdateErrorMessage(jobID, err.Error())
				_ = dbStore.UpdateStatus(jobID, "FAILED")

				storage.NotifyDownloadFailed(
					filepathBase(savePath),
					err.Error(),
				)

				return err
			}

		default:
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// ----------------------------------------------------------
		// Checksum verification
		// ----------------------------------------------------------
		//
		// Only perform verification when the user supplied an
		// expected checksum.
		//
		if job.ExpectedChecksum != "" {
			result, checksumErr := downloader.VerifyFileChecksum(
				savePath,
				job.ExpectedChecksum,
				job.ChecksumAlgo,
			)

			if checksumErr != nil {
				_ = dbStore.UpdateChecksumVerified(jobID, false)
				_ = dbStore.UpdateErrorMessage(jobID, checksumErr.Error())

				storage.NotifyDownloadFailed(
					filepathBase(savePath),
					checksumErr.Error(),
				)

				return checksumErr
			}

			if !result.Matched {
				err := fmt.Errorf(
					"checksum mismatch: expected %s, computed %s (%s)",
					result.Expected,
					result.Computed,
					result.Algorithm,
				)

				_ = dbStore.UpdateChecksumVerified(jobID, false)
				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				storage.NotifyDownloadFailed(
					filepathBase(savePath),
					err.Error(),
				)

				return err
			}

			_ = dbStore.UpdateChecksumVerified(jobID, true)
		}

		// ----------------------------------------------------------
		// Download successfully completed
		// ----------------------------------------------------------

		_ = dbStore.UpdateProgress(
			jobID,
			100.0,
			formatBytes(meta.Size),
			"0.00 KB/s",
			"0s",
			"",
			"COMPLETED",
		)

		storage.ClearJobState(savePath)

		storage.NotifyDownloadComplete(
			filepathBase(savePath),
			savePath,
		)

		return nil
	}

	storage.InitQueueManager(2, executeDownloadJob)

	go storage.StartIPCServer(func(url string, savePath string, jobID string) {
		executeDownloadJob(url, savePath, jobID, nil)
	})

	// Start HTTP server on port 9000
	server := storage.NewServer(executeDownloadJob)

	httpServer := &http.Server{
		Addr:    ":9000",
		Handler: server.Router,
	}

	go func() {
		log.Printf("[Hydra-Daemon] Starting HTTP server on :9000")

		if err := httpServer.ListenAndServe(); err != nil &&
			err != http.ErrServerClosed {
			log.Fatalf("[Hydra-Daemon] Server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan

	log.Println("[Hydra-Daemon] Shutting down...")

	_ = os.Remove(storage.GetSocketPath())

	os.Exit(0)
}

func filepathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}

	return path
}

func formatBytes(bytes int64) string {
	if bytes >= 1024*1024*1024 {
		return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
	}

	if bytes >= 1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	}

	if bytes >= 1024 {
		return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
	}

	return fmt.Sprintf("%d B", bytes)
}

func formatSpeed(bytesPerSec float64) string {
	if bytesPerSec >= 1024*1024*1024 {
		return fmt.Sprintf(
			"%.2f GB/s",
			bytesPerSec/(1024*1024*1024),
		)
	}

	if bytesPerSec >= 1024*1024 {
		return fmt.Sprintf(
			"%.2f MB/s",
			bytesPerSec/(1024*1024),
		)
	}

	if bytesPerSec >= 1024 {
		return fmt.Sprintf(
			"%.2f KB/s",
			bytesPerSec/1024,
		)
	}

	return fmt.Sprintf("%.2f B/s", bytesPerSec)
}

func formatETA(seconds float64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", int(seconds))
	}

	if seconds < 3600 {
		mins := int(seconds) / 60
		secs := int(seconds) % 60

		return fmt.Sprintf("%dm %ds", mins, secs)
	}

	hours := int(seconds) / 3600
	mins := (int(seconds) % 3600) / 60

	return fmt.Sprintf("%dh %dm", hours, mins)
}
