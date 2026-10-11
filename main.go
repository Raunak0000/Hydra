package main

import (
	"context"

	"fmt"

	"log"

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

		// A directory can be left in place when a previous job used the

		// selected filename as its download directory. Keep retries stable

		// by choosing a sibling download path instead of nesting repeatedly.

		if info, statErr := os.Stat(savePath); statErr == nil && info.IsDir() {

			filename := filepath.Base(job.FileName)

			if filename == "" {

				filename = filepath.Base(url)

			}

			targetDir := filepath.Dir(savePath)

			for filepath.Base(targetDir) == filename {

				targetDir = filepath.Dir(targetDir)

			}

			candidate := filepath.Join(

				targetDir,

				fmt.Sprintf("%s.hydra-%s.part", filename, jobID),
			)

			for suffix := 1; ; suffix++ {

				candidateInfo, candidateErr := os.Stat(candidate)

				if os.IsNotExist(candidateErr) ||

					(candidateErr == nil && !candidateInfo.IsDir()) {

					break

				}

				candidate = filepath.Join(

					targetDir,

					fmt.Sprintf("%s.hydra-%s.part-%d", filename, jobID, suffix),
				)

			}

			savePath = candidate

			job.SavePath = savePath

			_ = dbStore.SaveJob(&job)

		}

		if info, statErr := os.Stat(savePath); statErr == nil && info.IsDir() {

			return fmt.Errorf("download target is a directory: %s", savePath)

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

		// Validate available disk space before creating the destination file.

		if meta.Size > 0 {

			diskInfo, err := storage.GetDiskSpace(savePath, meta.Size)

			if err != nil {

				errMsg := fmt.Sprintf("disk space check failed: %v", err)

				_ = dbStore.UpdateErrorMessage(jobID, errMsg)

				storage.NotifyDownloadFailed(filepathBase(savePath), errMsg)

				return fmt.Errorf("%s", errMsg)

			}

			if !diskInfo.EnoughSpace {

				errMsg := fmt.Sprintf(

					"insufficient disk space: required %.2f GB, available %.2f GB",

					float64(meta.Size)/(1024*1024*1024),

					float64(diskInfo.AvailableBytes)/(1024*1024*1024),
				)

				_ = dbStore.UpdateErrorMessage(jobID, errMsg)

				storage.NotifyDownloadFailed(filepathBase(savePath), errMsg)

				return fmt.Errorf("%s", errMsg)

			}

		}

		var reservedFile *os.File

		// Resolve destination conflicts while preserving resumed jobs.

		if len(job.Chunks) == 0 {

			if info, statErr := os.Stat(savePath); statErr == nil && info.IsDir() {

				filename := filepath.Base(job.FileName)

				if filename == "" || filename == "." {

					filename = filepath.Base(url)

				}

				targetDir := filepath.Dir(savePath)

				for filepath.Base(targetDir) == filename {

					targetDir = filepath.Dir(targetDir)

				}

				savePath = filepath.Join(targetDir, filename)

			}

			reservedPath, file, err := storage.ReserveConflict(savePath, 0666)

			if err != nil {

				return fmt.Errorf("reserve download destination: %w", err)

			}

			reservedFile = file

			savePath = reservedPath

			job.SavePath = savePath

			if err := dbStore.SaveJob(&job); err != nil {

				reservedFile.Close()

				os.Remove(savePath)

				return fmt.Errorf("save resolved download destination: %w", err)

			}

		} else {

			// A resumed job must retain its persisted destination.

			savePath = job.SavePath

		}

		file := reservedFile

		if file == nil {

			// Resumed jobs or jobs without a new reservation use the persisted path.

			file, err = storage.PreallocateSpace(savePath, meta.Size)

			if err != nil {

				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())

				return err

			}

		} else {

			// The new download already owns this file through atomic reservation.

			if err := storage.PreallocateFile(file, meta.Size); err != nil {

				_ = file.Close()

				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())

				return err

			}

		}

		defer file.Close()

		numThreads := 8

		if !meta.AcceptRanges || meta.Size <= 0 {

			numThreads = 1

		}

		var chunks []downloader.Chunk

		if len(job.Chunks) > 0 {

			// Resuming an existing download: use the persisted chunk configuration.

			chunks = make([]downloader.Chunk, len(job.Chunks))

			for i, sc := range job.Chunks {

				chunks[i] = downloader.Chunk{

					Index: sc.Index,

					Start: sc.Start,

					End: sc.End,
				}

			}

			numThreads = len(chunks)

		} else {

			// Fresh download: calculate initial chunks.

			chunks = downloader.CalculateChunks(meta.Size, numThreads)

		}

		trackers := make([]*downloader.AdaptiveTracker, len(chunks))

		chunkStates := make([]models.ChunkState, len(chunks))

		// Build trackers from persisted state when available.

		for i, ch := range chunks {

			state := models.ChunkState{

				Index: i,

				Start: ch.Start,

				CurrentOffset: ch.Start,

				End: ch.End,

				Completed: false,
			}

			if len(job.Chunks) > 0 && i < len(job.Chunks) {

				state = job.Chunks[i]

				if state.Completed {

					state.CurrentOffset = state.End + 1

				} else {

					if state.CurrentOffset < state.Start {

						state.CurrentOffset = state.Start

					}

					if state.End > 0 && state.CurrentOffset > state.End+1 {

						state.CurrentOffset = state.End + 1

						state.Completed = true

					}

				}

			}

			chunkStates[i] = state

			trackers[i] = &downloader.AdaptiveTracker{

				Index: state.Index,

				StartByte: state.Start,

				CurrentPtr: state.CurrentOffset,

				EndBoundary: state.End,
			}

		}

		if info, statErr := os.Stat(savePath); statErr == nil && info.IsDir() {

			return fmt.Errorf("download target is a directory: %s", savePath)

		}

		var wg sync.WaitGroup

		errChan := make(chan error, numThreads)

		progressChan := make(chan int64, 1024)

		stateChan := make(chan downloader.Chunk, 1024)

		// ----------------------------------------------------------

		// Restore aggregate download progress from persisted chunks.

		// ----------------------------------------------------------

		var downloadedBytes int64

		for _, state := range chunkStates {

			current := state.CurrentOffset

			if state.End > 0 && current > state.End+1 {

				current = state.End + 1

			}

			if current > state.Start {

				downloadedBytes += current - state.Start

			}

		}

		// Never allow restored progress to exceed the actual file size.

		if meta.Size > 0 && downloadedBytes > meta.Size {

			downloadedBytes = meta.Size

		}

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

		var downloaded atomic.Int64

		downloaded.Store(downloadedBytes)

		// Dedicated drain: never blocked by DB work.

		go func() {

			for {

				select {

				case <-done:

					return

				case p, ok := <-progressChan:

					if !ok {

						return

					}

					downloaded.Add(p)

				}

			}

		}()

		go func() {

			ticker := time.NewTicker(500 * time.Millisecond)

			defer ticker.Stop()

			lastDownloaded := downloaded.Load()

			lastTime := time.Now()

			var smoothed float64

			var lastChunkSave time.Time

			for {

				select {

				case <-done:

					return

				case <-ticker.C:

					now := time.Now()

					cur := downloaded.Load()

					dt := now.Sub(lastTime).Seconds()

					if dt < 0.1 {

						continue // skip a too-close tick instead of reporting garbage

					}

					instant := float64(cur-lastDownloaded) / dt

					lastDownloaded, lastTime = cur, now

					// Exponential moving average to keep the display steady.

					if smoothed == 0 {

						smoothed = instant

					} else {

						smoothed = 0.3*instant + 0.7*smoothed

					}

					// Sync chunk positions from the atomic trackers (same as before).

					for i, tr := range trackers {

						c, end := tr.GetCurrent(), tr.GetEnd()

						chunkStates[i].CurrentOffset = c

						if end > 0 && c > end {

							chunkStates[i].Completed = true

							chunkStates[i].CurrentOffset = end + 1

						}

					}

					progressPct := 0.0

					if meta.Size > 0 {

						progressPct = float64(cur) / float64(meta.Size) * 100.0

						if progressPct > 100.0 {

							progressPct = 100.0

						}

					}

					etaStr := "--"

					if smoothed > 0 && meta.Size > 0 {

						if rem := meta.Size - cur; rem > 0 {

							etaStr = formatETA(float64(rem) / smoothed)

						}

					}

					_ = dbStore.UpdateProgress(jobID, progressPct, formatBytes(cur),

						formatSpeed(smoothed), etaStr, "", "DOWNLOADING")

					// Chunk checkpoints don't need to be written twice a second.

					if now.Sub(lastChunkSave) >= 2*time.Second {

						_ = dbStore.UpdateJobChunks(jobID, chunkStates)

						lastChunkSave = now

					}

				}

			}

		}()

		wg.Wait()

		close(done)

		close(progressChan)

		close(stateChan)

		// Sync final chunk offsets directly from trackers and persist to SQLite

		var finalDownloaded int64

		for i, tr := range trackers {

			cur := tr.GetCurrent()

			end := tr.GetEnd()

			start := tr.GetStart()

			chunkStates[i].CurrentOffset = cur

			chunkStates[i].End = end

			chunkStates[i].Start = start

			if end > 0 && cur > end {

				chunkStates[i].Completed = true

				chunkStates[i].CurrentOffset = end + 1

			}

			if cur > start {

				chunkBytes := cur - start

				if end > 0 && cur > end+1 {

					chunkBytes = end + 1 - start

				}

				finalDownloaded += chunkBytes

			}

		}

		_ = dbStore.UpdateJobChunks(jobID, chunkStates)

		if meta.Size > 0 && finalDownloaded > meta.Size {

			finalDownloaded = meta.Size

		}

		finalProgressPct := 0.0

		if meta.Size > 0 {

			finalProgressPct = (float64(finalDownloaded) / float64(meta.Size)) * 100.0

			if finalProgressPct > 100.0 {

				finalProgressPct = 100.0

			}

		}

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

			_ = dbStore.UpdateProgress(

				jobID,

				finalProgressPct,

				formatBytes(finalDownloaded),

				"0.00 KB/s",

				"--",

				"",

				"PAUSED",
			)

			storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())

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

		Addr: ":9000",

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
