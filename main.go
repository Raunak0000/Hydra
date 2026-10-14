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

	executeDownloadJob := func(url, savePath, jobID string, headers map[string]string) error {

		job, exists := dbStore.GetJob(jobID)

		if !exists {

			return fmt.Errorf("job %s not found", jobID)

		}

		if job.URL != "" {

			url = job.URL

		}

		if job.SavePath != "" {

			savePath = job.SavePath

		}

		if job.Headers != nil {

			headers = job.Headers

		}

		if info, statErr := os.Stat(savePath); statErr == nil && info.IsDir() {

			filename := filepath.Base(job.FileName)

			if filename == "" {

				filename = filepath.Base(url)

			}

			targetDir := filepath.Dir(savePath)

			for filepath.Base(targetDir) == filename {

				targetDir = filepath.Dir(targetDir)

			}

			candidate := filepath.Join(targetDir, fmt.Sprintf("%s.hydra-%s.part", filename, jobID))

			for suffix := 1; ; suffix++ {

				candidateInfo, candidateErr := os.Stat(candidate)

				if os.IsNotExist(candidateErr) || (candidateErr == nil && !candidateInfo.IsDir()) {

					break

				}

				if candidateErr != nil && !os.IsNotExist(candidateErr) {

					return fmt.Errorf("checking candidate destination: %w", candidateErr)

				}

				candidate = filepath.Join(targetDir, fmt.Sprintf("%s.hydra-%s.part-%d", filename, jobID, suffix))

			}

			savePath = candidate

			job.SavePath = savePath

			if err := dbStore.SaveJob(&job); err != nil {

				return fmt.Errorf("saving resolved download path: %w", err)

			}

		}

		if info, statErr := os.Stat(savePath); statErr == nil && info.IsDir() {

			return fmt.Errorf("download target is a directory: %s", savePath)

		} else if statErr != nil && !os.IsNotExist(statErr) {

			return fmt.Errorf("checking download target: %w", statErr)

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

		totalSizeStr := "Unknown Size"

		if meta.Size > 0 {

			totalSizeStr = fmt.Sprintf("%.2f MB", float64(meta.Size)/(1024*1024))

		}

		if err := dbStore.UpdateTotalSize(jobID, totalSizeStr); err != nil {

			return fmt.Errorf("updating total size: %w", err)

		}

		resuming := len(job.Chunks) > 0
		var file *os.File

		// Resolve the destination path before opening or reserving it.
		resolvedPath, err := storage.ResolvePath(savePath)
		if err != nil {
			return fmt.Errorf("resolving destination path: %w", err)
		}

		if info, statErr := os.Stat(resolvedPath); statErr == nil && info.IsDir() {
			return fmt.Errorf("download target is a directory: %s", resolvedPath)
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("checking download target: %w", statErr)
		}

		if resuming {
			if job.SavePath == "" {
				return fmt.Errorf("cannot resume job without a saved destination path")
			}

			resolvedPath, err = storage.ResolvePath(job.SavePath)
			if err != nil {
				return fmt.Errorf("resolving resume destination: %w", err)
			}

			info, statErr := os.Lstat(resolvedPath)
			if statErr != nil {
				return fmt.Errorf("resume destination is unavailable: %w", statErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("resume destination must be a regular, non-symlink file")
			}

			file, err = os.OpenFile(resolvedPath, os.O_RDWR, 0)
			if err != nil {
				return fmt.Errorf("opening resume destination: %w", err)
			}
		} else {
			resolvedPath, file, err = storage.ReserveConflict(resolvedPath, 0666)
			if err != nil {
				return fmt.Errorf("reserving destination: %w", err)
			}

			job.SavePath = resolvedPath
			if err := dbStore.SaveJob(&job); err != nil {
				_ = file.Close()
				_ = os.Remove(resolvedPath)
				return fmt.Errorf("saving reserved destination: %w", err)
			}
		}

		savePath = resolvedPath
		defer file.Close()

		// Check available space on the filesystem containing the final path.
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

		// Only preallocate a newly reserved file. Reallocating a resumed
		// file could affect already-downloaded data.
		if !resuming {
			if err := storage.PreallocateFile(file, meta.Size); err != nil {
				_ = dbStore.UpdateErrorMessage(jobID, err.Error())
				storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())
				return fmt.Errorf("preallocating destination: %w", err)
			}
		}

		numThreads := 8

		if !meta.AcceptRanges || meta.Size <= 0 {

			numThreads = 1

		}

		var chunks []downloader.Chunk

		if len(job.Chunks) > 0 {

			if err := validateChunkStates(job.Chunks, meta.Size); err != nil {

				err = fmt.Errorf("invalid persisted chunk state: %w", err)

				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				_ = dbStore.UpdateStatus(jobID, "FAILED")

				return err

			}

			// Restore persisted chunk ranges.

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

			chunks = downloader.CalculateChunks(meta.Size, numThreads)

			if len(chunks) == 0 {

				return fmt.Errorf("could not calculate download chunks for job %s", jobID)

			}

		}

		trackers := make([]*downloader.AdaptiveTracker, len(chunks))

		chunkStates := make([]models.ChunkState, len(chunks))

		for i, ch := range chunks {

			state := models.ChunkState{

				Index: ch.Index,

				Start: ch.Start,

				CurrentOffset: ch.Start,

				End: ch.End,

				Completed: false,
			}

			if len(job.Chunks) > 0 {

				state = job.Chunks[i]

				if state.Completed {

					state.CurrentOffset = state.End + 1

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

		if len(job.Chunks) == 0 {

			if err := dbStore.UpdateJobChunks(jobID, chunkStates); err != nil {

				return fmt.Errorf("persisting initial chunk state: %w", err)

			}

		}

		var downloadedBytes int64

		for _, state := range chunkStates {

			current := state.CurrentOffset

			if state.End >= 0 && current > state.End+1 {

				current = state.End + 1

			}

			if current > state.Start {

				downloadedBytes += current - state.Start

			}

		}

		if meta.Size > 0 && downloadedBytes > meta.Size {

			return fmt.Errorf("persisted progress exceeds expected file size")

		}

		cfg := storage.GetConfig()

		speedLimit := job.MaxSpeedBytes

		if speedLimit <= 0 {

			speedLimit = cfg.SpeedLimitBytes

		}

		var limiter *downloader.RateLimiter

		if speedLimit > 0 {

			limiter = downloader.NewRateLimiter(speedLimit)

		}

		var wg sync.WaitGroup

		errChan := make(chan error, len(chunks))

		progressChan := make(chan int64, 1024)

		stateChan := make(chan downloader.Chunk, 1024)

		for i := range chunks {

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

		reporterDone := make(chan struct{})

		go func() {

			defer close(reporterDone)

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

						continue

					}

					instant := float64(cur-lastDownloaded) / dt

					lastDownloaded, lastTime = cur, now

					if smoothed == 0 {

						smoothed = instant

					} else {

						smoothed = 0.3*instant + 0.7*smoothed

					}

					for i, tr := range trackers {

						current, end := tr.GetCurrent(), tr.GetEnd()

						chunkStates[i].CurrentOffset = current

						if current >= end+1 {

							chunkStates[i].Completed = true

							chunkStates[i].CurrentOffset = end + 1

						}

					}

					progressPct := 0.0

					if meta.Size > 0 {

						progressPct = float64(cur) / float64(meta.Size) * 100

						if progressPct > 100 {

							progressPct = 100

						}

					}

					eta := "--"

					if smoothed > 0 && meta.Size > 0 {

						if remaining := meta.Size - cur; remaining > 0 {

							eta = formatETA(float64(remaining) / smoothed)

						}

					}

					if err := dbStore.UpdateProgress(

						jobID, progressPct, formatBytes(cur),

						formatSpeed(smoothed), eta, "", "DOWNLOADING",
					); err != nil {

						log.Printf("updating progress for job %s: %v", jobID, err)

					}

					if now.Sub(lastChunkSave) >= 2*time.Second {

						if err := dbStore.UpdateJobChunks(jobID, chunkStates); err != nil {

							log.Printf("saving chunk checkpoint for job %s: %v", jobID, err)

						}

						lastChunkSave = now

					}

				}

			}

		}()

		wg.Wait()

		close(done)

		<-reporterDone

		close(progressChan)

		close(stateChan)

		var workerErr error

		for {

			select {

			case e := <-errChan:

				if e != nil && workerErr == nil {

					workerErr = e

				}

			default:

				goto workersDrained

			}

		}

	workersDrained:

		var finalDownloaded int64

		for i, tr := range trackers {

			current := tr.GetCurrent()

			end := tr.GetEnd()

			start := tr.GetStart()

			chunkStates[i].CurrentOffset = current

			chunkStates[i].End = end

			chunkStates[i].Start = start

			chunkStates[i].Completed = current >= end+1

			if current > start {

				chunkBytes := current - start

				if end >= 0 && current > end+1 {

					chunkBytes = end + 1 - start

				}

				finalDownloaded += chunkBytes

			}

		}

		if err := dbStore.UpdateJobChunks(jobID, chunkStates); err != nil {

			return fmt.Errorf("persisting final chunk state: %w", err)

		}

		if ctx.Err() != nil {

			_ = dbStore.UpdateProgress(

				jobID,

				0,

				formatBytes(finalDownloaded),

				"0.00 KB/s",

				"--",

				"",

				"PAUSED",
			)

			storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())

			return ctx.Err()

		}

		if workerErr != nil {

			_ = dbStore.UpdateErrorMessage(jobID, workerErr.Error())

			_ = dbStore.UpdateStatus(jobID, "FAILED")

			storage.NotifyDownloadFailed(filepathBase(savePath), workerErr.Error())

			return workerErr

		}

		if meta.Size > 0 {

			for i, tracker := range trackers {

				expected := tracker.GetEnd() + 1

				if tracker.GetCurrent() < expected {

					err := fmt.Errorf(

						"download incomplete: chunk %d stopped at %d, expected %d",

						i, tracker.GetCurrent(), expected,
					)

					_ = dbStore.UpdateErrorMessage(jobID, err.Error())

					_ = dbStore.UpdateStatus(jobID, "FAILED")

					storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())

					return err

				}

			}

			if finalDownloaded != meta.Size {

				err := fmt.Errorf(

					"download incomplete: received %d of %d bytes",

					finalDownloaded, meta.Size,
				)

				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				_ = dbStore.UpdateStatus(jobID, "FAILED")

				storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())

				return err

			}

			info, err := file.Stat()

			if err != nil {

				return fmt.Errorf("verifying downloaded file size: %w", err)

			}

			if info.Size() != meta.Size {

				err := fmt.Errorf(

					"file size mismatch: got %d bytes, expected %d",

					info.Size(), meta.Size,
				)

				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				_ = dbStore.UpdateStatus(jobID, "FAILED")

				storage.NotifyDownloadFailed(filepathBase(savePath), err.Error())

				return err

			}

		}

		if job.ExpectedChecksum != "" {

			result, err := downloader.VerifyFileChecksum(

				savePath,

				job.ExpectedChecksum,

				job.ChecksumAlgo,
			)

			if err != nil {

				_ = dbStore.UpdateChecksumVerified(jobID, false)

				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				return err

			}

			if !result.Matched {

				err := fmt.Errorf(

					"checksum mismatch: expected %s, computed %s (%s)",

					result.Expected, result.Computed, result.Algorithm,
				)

				_ = dbStore.UpdateChecksumVerified(jobID, false)

				_ = dbStore.UpdateErrorMessage(jobID, err.Error())

				return err

			}

			if err := dbStore.UpdateChecksumVerified(jobID, true); err != nil {

				return fmt.Errorf("saving checksum verification result: %w", err)

			}

		}

		if err := dbStore.UpdateProgress(

			jobID,

			100,

			formatBytes(finalDownloaded),

			"0.00 KB/s",

			"0s",

			"",

			"COMPLETED",
		); err != nil {

			return fmt.Errorf("marking job completed: %w", err)

		}

		storage.ClearJobState(savePath)

		storage.NotifyDownloadComplete(filepathBase(savePath), savePath)

		return nil

	}

	storage.InitQueueManager(2, executeDownloadJob)

	go storage.StartIPCServer(func(url, savePath, jobID string) {

		if err := executeDownloadJob(url, savePath, jobID, nil); err != nil {

			log.Printf("IPC download job %s failed: %v", jobID, err)

		}

	})

	server := storage.NewServer(executeDownloadJob)

	httpServer := &http.Server{

		Addr: "127.0.0.1:9000",

		Handler: server.Router,

		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {

		log.Printf("[Hydra-Daemon] Starting HTTP server on 127.0.0.1:9000")

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {

			log.Printf("[Hydra-Daemon] HTTP server error: %v", err)

		}

	}()

	sigChan := make(chan os.Signal, 1)

	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	defer signal.Stop(sigChan)

	<-sigChan

	log.Println("[Hydra-Daemon] Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)

	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {

		log.Printf("[Hydra-Daemon] HTTP graceful shutdown failed: %v", err)

		if closeErr := httpServer.Close(); closeErr != nil {

			log.Printf("[Hydra-Daemon] HTTP force close failed: %v", closeErr)

		}

	}

	storage.GlobalCancelMutex.Lock()

	for _, cancel := range storage.GlobalCancelMap {

		cancel()

	}

	storage.GlobalCancelMutex.Unlock()

	deadline := time.NewTimer(10 * time.Second)

	ticker := time.NewTicker(100 * time.Millisecond)

	defer deadline.Stop()

	defer ticker.Stop()

	for {

		storage.GlobalCancelMutex.Lock()

		active := len(storage.GlobalCancelMap)

		storage.GlobalCancelMutex.Unlock()

		if active == 0 {

			break

		}

		select {

		case <-ticker.C:

		case <-deadline.C:

			log.Printf("timed out waiting for %d active downloads", active)

			goto cleanup

		}

	}

cleanup:

	if err := os.Remove(storage.GetSocketPath()); err != nil && !os.IsNotExist(err) {

		log.Printf("removing IPC socket: %v", err)

	}

	log.Println("[Hydra-Daemon] Shutdown complete")

}

func validateChunkStates(chunks []models.ChunkState, fileSize int64) error {

	if len(chunks) == 0 {

		return fmt.Errorf("no persisted chunks to resume")

	}

	seen := make(map[int]bool, len(chunks))

	for _, chunk := range chunks {

		if chunk.Index < 0 || seen[chunk.Index] {

			return fmt.Errorf("invalid or duplicate chunk index %d", chunk.Index)

		}

		seen[chunk.Index] = true

		if chunk.Start < 0 {

			return fmt.Errorf("chunk %d has negative start offset", chunk.Index)

		}

		if fileSize > 0 {

			if chunk.End < chunk.Start || chunk.End >= fileSize {

				return fmt.Errorf(

					"chunk %d has invalid range [%d, %d] for file size %d",

					chunk.Index, chunk.Start, chunk.End, fileSize,
				)

			}

		} else if chunk.End >= 0 && chunk.End < chunk.Start {

			return fmt.Errorf("chunk %d has invalid range", chunk.Index)

		}

		if chunk.CurrentOffset < chunk.Start {

			return fmt.Errorf("chunk %d offset precedes its start", chunk.Index)

		}

		if chunk.End >= 0 && chunk.CurrentOffset > chunk.End+1 {

			return fmt.Errorf("chunk %d offset exceeds its end", chunk.Index)

		}

	}

	return nil

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

		return fmt.Sprintf("%.2f GB/s", bytesPerSec/(1024*1024*1024))

	}

	if bytesPerSec >= 1024*1024 {

		return fmt.Sprintf("%.2f MB/s", bytesPerSec/(1024*1024))

	}

	if bytesPerSec >= 1024 {

		return fmt.Sprintf("%.2f KB/s", bytesPerSec/1024)

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
