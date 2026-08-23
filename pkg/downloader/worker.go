package downloader

import (
	"context"
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

	const dynamicMinChunk int64 = 2 * 1024 * 1024

	for {
		writeOffset := atomic.LoadInt64(&me.CurrentPtr)
		endBoundary := atomic.LoadInt64(&me.EndBoundary)

		// Dynamic workload stealing (only for bounded chunks)
		if endBoundary > 0 && writeOffset >= endBoundary {
			newStart, newEnd, stolenFrom := StealWork(trackers, dynamicMinChunk)
			if stolenFrom == nil {
				return
			}

			atomic.StoreInt64(&me.CurrentPtr, newStart)
			atomic.StoreInt64(&me.EndBoundary, newEnd)
			writeOffset = newStart
			endBoundary = newEnd
		}

		var resp *http.Response
		var makeReqErr error
		maxAttempts := 5

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if ctx.Err() != nil {
				return
			}

			req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
			if err != nil {
				errChan <- fmt.Errorf("thread %d init failed: %v", myIndex, err)
				return
			}

			if endBoundary > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", writeOffset, endBoundary))
			} else if writeOffset > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", writeOffset))
			}

			for key, value := range headers {
				req.Header.Set(key, value)
			}
			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
			}

			resp, makeReqErr = client.Do(req)
			if makeReqErr != nil {
				sleepDuration := time.Duration(1<<attempt)*100*time.Millisecond + time.Duration(myIndex*50)*time.Millisecond
				if sleepDuration > 3*time.Second {
					sleepDuration = 3 * time.Second
				}
				time.Sleep(sleepDuration)
				continue
			}

			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfter := resp.Header.Get("Retry-After")
				sleepDuration := time.Duration(1<<attempt) * 200 * time.Millisecond
				if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
					sleepDuration = time.Duration(seconds) * time.Second
				}
				resp.Body.Close()
				time.Sleep(sleepDuration)
				continue
			}

			if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				makeReqErr = fmt.Errorf("status code %d", resp.StatusCode)
				sleepDuration := time.Duration(1<<attempt) * 100 * time.Millisecond
				time.Sleep(sleepDuration)
				continue
			}

			makeReqErr = nil
			break
		}

		if makeReqErr != nil {
			errChan <- fmt.Errorf("thread %d request failed after %d attempts: %v", myIndex, maxAttempts, makeReqErr)
			return
		}

		buffer := make([]byte, 64*1024)
		streamFailed := false

		for {
			if ctx.Err() != nil {
				resp.Body.Close()
				return
			}

			bytesRead, readErr := resp.Body.Read(buffer)
			if bytesRead > 0 {
				currentEnd := atomic.LoadInt64(&me.EndBoundary)

				// 🛡️ Rigid Boundary Clamping: prevent overrun past stolen midpoints
				effectiveBytes := bytesRead
				if currentEnd > 0 && writeOffset+int64(effectiveBytes) > currentEnd+1 {
					effectiveBytes = int(currentEnd + 1 - writeOffset)
					if effectiveBytes <= 0 {
						break
					}
				}

				_, writeErr := finalFile.WriteAt(buffer[:effectiveBytes], writeOffset)
				if writeErr != nil {
					resp.Body.Close()
					errChan <- fmt.Errorf("thread %d write failed: %v", myIndex, writeErr)
					return
				}
				writeOffset += int64(effectiveBytes)
				atomic.StoreInt64(&me.CurrentPtr, writeOffset)

				select {
				case stateUpdateChan <- Chunk{Index: myIndex, Start: writeOffset, End: currentEnd}:
				default:
				}

				progressChan <- int64(effectiveBytes)

				if currentEnd > 0 && writeOffset > currentEnd {
					break
				}
			}

			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				streamFailed = true
				break
			}
		}
		resp.Body.Close()

		if endBoundary <= 0 {
			return
		}

		if !streamFailed && writeOffset >= atomic.LoadInt64(&me.EndBoundary) {
			continue
		}
	}
}
