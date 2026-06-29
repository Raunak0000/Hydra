package downloader

import (
	"context" // Added for loop cancellation monitoring
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

func DownloadChunkParallel(ctx context.Context, url string, myIndex int, trackers []*AdaptiveTracker, finalFile *os.File, wg *sync.WaitGroup, errChan chan error, progressChan chan int64, stateUpdateChan chan<- Chunk, headers map[string]string) {
	defer wg.Done()

	client := &http.Client{}
	me := trackers[myIndex]

	// Minimum threshold slice size to make stealing worth the round-trip latency overhead (e.g. 2MB)
	const dynamicMinChunk int64 = 2 * 1024 * 1024

	for {
		// 1. Read current dynamic workload targets
		writeOffset := atomic.LoadInt64(&me.CurrentPtr)
		endBoundary := atomic.LoadInt64(&me.EndBoundary)

		// If our target boundary has collapsed or completed, attempt to steal work from a slow sibling!
		if writeOffset >= endBoundary {
			newStart, newEnd, stolenFrom := StealWork(trackers, dynamicMinChunk)
			if stolenFrom == nil {
				// No viable work left to steal across any channels. Exit gracefully.
				return
			}

			fmt.Printf("[⚡] Thread #%d dynamically STOLE %d MB from Channel #%d!\n",
				myIndex+1, (newEnd-newStart)/(1024*1024), stolenFrom.Index+1)

			// Initialize our local tracker pointers to manage the stolen byte array range
			atomic.StoreInt64(&me.CurrentPtr, newStart)
			atomic.StoreInt64(&me.EndBoundary, newEnd)
			writeOffset = newStart
			endBoundary = newEnd
		}

		// 2. Perform standard segment slice streaming download with retry handling
		var resp *http.Response
		var makeReqErr error
		maxAttempts := 15

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if ctx.Err() != nil {
				return
			}

			req, err := http.NewRequestWithContext(ctx, "GET", url, nil) // cite: file(1).txt
			if err != nil {
				errChan <- fmt.Errorf("Thread %d init failed: %v", myIndex, err) // cite: file(1).txt
				return
			}

			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", writeOffset, endBoundary))
			for key, value := range headers { // cite: file(1).txt
				req.Header.Set(key, value) // cite: file(1).txt
			}
			if req.Header.Get("User-Agent") == "" { // cite: file(1).txt
				req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36") // cite: file(1).txt
			}

			resp, makeReqErr = client.Do(req) // cite: file(1).txt
			if makeReqErr != nil {
				// Retry with exponential backoff + jitter
				sleepDuration := time.Duration(1<<attempt)*100*time.Millisecond + time.Duration(myIndex*50)*time.Millisecond
				if sleepDuration > 5*time.Second {
					sleepDuration = 5*time.Second
				}
				time.Sleep(sleepDuration)
				continue
			}

			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfter := resp.Header.Get("Retry-After")
				sleepDuration := time.Duration(1<<attempt)*200*time.Millisecond + time.Duration(myIndex*100)*time.Millisecond
				if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
					sleepDuration = time.Duration(seconds)*time.Second
				}
				resp.Body.Close()
				time.Sleep(sleepDuration)
				continue
			}

			if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
				resp.Body.Close() // cite: file(1).txt
				makeReqErr = fmt.Errorf("status code %d", resp.StatusCode)
				sleepDuration := time.Duration(1<<attempt)*100*time.Millisecond
				if sleepDuration > 5*time.Second {
					sleepDuration = 5*time.Second
				}
				time.Sleep(sleepDuration)
				continue
			}

			makeReqErr = nil
			break
		}

		if makeReqErr != nil {
			errChan <- fmt.Errorf("Thread %d request failed after %d attempts: %v", myIndex, maxAttempts, makeReqErr)
			return
		}

		buffer := make([]byte, 32*1024) // cite: file(1).txt
		streamFailed := false

		for {
			if ctx.Err() != nil {
				resp.Body.Close() // cite: file(1).txt
				return
			}

			bytesRead, readErr := resp.Body.Read(buffer) // cite: file(1).txt
			if bytesRead > 0 {
				// Defensive verification step: Make sure we don't bleed past our dynamic boundary cap
				currentEnd := atomic.LoadInt64(&me.EndBoundary)
				if writeOffset >= currentEnd {
					break // Our boundary shifted inward due to theft or realignment. Break out to re-evaluate loops.
				}

				_, writeErr := finalFile.WriteAt(buffer[:bytesRead], writeOffset) // cite: file(1).txt
				if writeErr != nil {
					resp.Body.Close() // cite: file(1).txt
					errChan <- fmt.Errorf("Thread %d write failed: %v", myIndex, writeErr)
					return
				}
				writeOffset += int64(bytesRead) // cite: file(1).txt
				atomic.StoreInt64(&me.CurrentPtr, writeOffset)

				// Push updates up to UI serialization states safely
				select {
				case stateUpdateChan <- Chunk{Index: myIndex, Start: writeOffset, End: currentEnd}:
				default:
				}

				progressChan <- int64(bytesRead) // cite: file(1).txt
			}

			if readErr == io.EOF { // cite: file(1).txt
				break
			}
			if readErr != nil { // cite: file(1).txt
				streamFailed = true
				break
			}
		}
		resp.Body.Close() // cite: file(1).txt

		if !streamFailed && writeOffset >= atomic.LoadInt64(&me.EndBoundary) {
			// We successfully finished our current sub-slice! Loop back up to check for more work to steal.
			continue
		}
	}
}
