package downloader

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// We completely removed *ProgressTracker and added progressChan chan int64
func DownloadChunkParallel(url string, chunk Chunk, finalFile *os.File, wg *sync.WaitGroup, errChan chan error, progressChan chan int64) {
	defer wg.Done()

	client := &http.Client{}
	var resp *http.Response
	maxAttempts := 15
	var attemptErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			errChan <- fmt.Errorf("Thread %d init failed: %v", chunk.Index, err)
			return
		}

		if chunk.End >= chunk.Start {
			rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
			req.Header.Set("Range", rangeHeader)
		} else if chunk.Start > 0 {
			rangeHeader := fmt.Sprintf("bytes=%d-", chunk.Start)
			req.Header.Set("Range", rangeHeader)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

		resp, err = client.Do(req)
		if err != nil {
			attemptErr = fmt.Errorf("Thread %d network link dropped: %v", chunk.Index, err)
			
			// Exponential backoff + randomized jitter to avoid thundering herd lockstep
			jitter := time.Duration(rand.Intn(500)) * time.Millisecond
			sleepDuration := time.Duration(1<<attempt)*100*time.Millisecond + jitter
			if sleepDuration > 5*time.Second {
				sleepDuration = 5*time.Second + jitter
			}
			
			fmt.Printf("[⚠️] Thread %d connection error: %v. Retrying in %v (attempt %d/%d)...\n", chunk.Index, err, sleepDuration, attempt, maxAttempts)
			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := resp.Header.Get("Retry-After")
			
			// Exponential backoff + randomized jitter to avoid thundering herd lockstep
			jitter := time.Duration(rand.Intn(500)) * time.Millisecond
			sleepDuration := time.Duration(1<<attempt)*100*time.Millisecond + jitter
			if sleepDuration > 5*time.Second {
				sleepDuration = 5*time.Second + jitter
			}
			
			if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
				sleepDuration = time.Duration(seconds)*time.Second + jitter
			}
			resp.Body.Close()
			attemptErr = fmt.Errorf("Thread %d server exception status: 429 (Too Many Requests)", chunk.Index)
			fmt.Printf("[⚠️] Thread %d rate limited (429). Retrying in %v (attempt %d/%d)...\n", chunk.Index, sleepDuration, attempt, maxAttempts)
			time.Sleep(sleepDuration)
			continue
		}

		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			errChan <- fmt.Errorf("Thread %d server exception status: %d", chunk.Index, resp.StatusCode)
			return
		}

		// Request succeeded
		attemptErr = nil
		break
	}

	if attemptErr != nil {
		errChan <- fmt.Errorf("Thread %d failed after %d attempts: %v", chunk.Index, maxAttempts, attemptErr)
		return
	}
	defer resp.Body.Close()

	buffer := make([]byte, 32*1024) // 32KB processing buffer blocks
	writeOffset := chunk.Start

	for {
		bytesRead, readErr := resp.Body.Read(buffer)
		if bytesRead > 0 {
			_, writeErr := finalFile.WriteAt(buffer[:bytesRead], writeOffset)
			if writeErr != nil {
				errChan <- fmt.Errorf("Thread %d disk write exception: %v", chunk.Index, writeErr)
				return
			}
			writeOffset += int64(bytesRead)

			// Send the raw number of bytes read straight into the progress channel!
			progressChan <- int64(bytesRead)
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			errChan <- fmt.Errorf("Thread %d stream terminated: %v", chunk.Index, readErr)
			return
		}
	}
}

