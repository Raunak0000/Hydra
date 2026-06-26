package downloader

import (
	"context" // Added for loop cancellation monitoring
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// DownloadChunkParallel accepts a context parameter to watch for pause interruptions safely
func DownloadChunkParallel(ctx context.Context, url string, chunk Chunk, finalFile *os.File, wg *sync.WaitGroup, errChan chan error, progressChan chan int64, stateUpdateChan chan<- Chunk) {
	defer wg.Done()

	client := &http.Client{}
	var resp *http.Response
	maxAttempts := 15
	var attemptErr error

	// We compute starting offset dynamically based on past progress if resuming
	writeOffset := chunk.Start

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Check context before making an outbound network request
		if err := ctx.Err(); err != nil {
			return
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			errChan <- fmt.Errorf("Thread %d init failed: %v", chunk.Index, err)
			return
		}

		if chunk.End >= writeOffset {
			rangeHeader := fmt.Sprintf("bytes=%d-%d", writeOffset, chunk.End)
			req.Header.Set("Range", rangeHeader)
		} else if writeOffset > 0 {
			rangeHeader := fmt.Sprintf("bytes=%d-", writeOffset)
			req.Header.Set("Range", rangeHeader)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

		resp, err = client.Do(req)
		if err != nil {
			attemptErr = fmt.Errorf("Thread %d network link dropped: %v", chunk.Index, err)
			jitter := time.Duration(rand.Intn(500)) * time.Millisecond
			sleepDuration := time.Duration(1<<attempt)*100*time.Millisecond + jitter
			if sleepDuration > 5*time.Second {
				sleepDuration = 5*time.Second + jitter
			}
			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			jitter := time.Duration(rand.Intn(500)) * time.Millisecond
			sleepDuration := time.Duration(1<<attempt)*100*time.Millisecond + jitter
			if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
				sleepDuration = time.Duration(seconds)*time.Second + jitter
			}
			resp.Body.Close()
			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			errChan <- fmt.Errorf("Thread %d server exception status: %d", chunk.Index, resp.StatusCode)
			return
		}

		attemptErr = nil
		break
	}

	if attemptErr != nil {
		errChan <- fmt.Errorf("Thread %d failed after %d attempts: %v", chunk.Index, maxAttempts, attemptErr)
		return
	}
	defer resp.Body.Close()

	buffer := make([]byte, 32*1024)

	for {
		// Intercept context pause signals cleanly right before evaluating buffer reads
		select {
		case <-ctx.Done():
			return
		default:
		}

		bytesRead, readErr := resp.Body.Read(buffer)
		if bytesRead > 0 {
			_, writeErr := finalFile.WriteAt(buffer[:bytesRead], writeOffset)
			if writeErr != nil {
				errChan <- fmt.Errorf("Thread %d disk write exception: %v", chunk.Index, writeErr)
				return
			}
			writeOffset += int64(bytesRead)

			// Update the chunk's active position state
			chunk.Start = writeOffset
			stateUpdateChan <- chunk

			progressChan <- int64(bytesRead)
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			// Ignore read cancellations caused explicitly by forcing a pause network drop
			if ctx.Err() != nil {
				return
			}
			errChan <- fmt.Errorf("Thread %d stream terminated: %v", chunk.Index, readErr)
			return
		}
	}
}
