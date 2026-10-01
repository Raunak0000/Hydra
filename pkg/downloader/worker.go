package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	BufferSize          = 256 * 1024      // 256 KB read buffer per worker
	DynamicMinChunkSize = 2 * 1024 * 1024 // 2 MB floor for work-stealing
	MaxStreamRetries    = 8               // Max connection attempts for dropped streams
)

// bufferPool recycles 256 KB byte slices across workers to eliminate heap GC pressure.
var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, BufferSize)
		return &b
	},
}

// SharedHTTPClient is tuned specifically for high-speed multi-threaded downloads.
var SharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     120 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  true,
		WriteBufferSize:     BufferSize,
		ReadBufferSize:      BufferSize,
		ForceAttemptHTTP2:   true,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	},
}

func DownloadChunkParallel(
	ctx context.Context,
	url string,
	myIndex int,
	trackers []*AdaptiveTracker,
	finalFile *os.File,
	wg *sync.WaitGroup,
	errChan chan error,
	progressChan chan int64,
	stateUpdateChan chan<- Chunk,
	headers map[string]string,
	limiter *RateLimiter,
) {
	defer wg.Done()

	me := trackers[myIndex]

	// Acquire a pooled 256 KB buffer for this worker.
	bufPtr := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufPtr)

	buffer := *bufPtr

	for {
		writeOffset := atomic.LoadInt64(&me.CurrentPtr)
		endBoundary := atomic.LoadInt64(&me.EndBoundary)

		// Dynamic workload stealing applies to bounded chunks.
		// If this chunk has already completed, exit cleanly without corrupting boundaries.
		if endBoundary > 0 && writeOffset > endBoundary {
			return
		}

		var resp *http.Response
		var streamErr error

		// Connection establishment with exponential backoff.
		for attempt := 1; attempt <= MaxStreamRetries; attempt++ {
			if ctx.Err() != nil {
				return
			}

			req, err := http.NewRequestWithContext(
				ctx,
				http.MethodGet,
				url,
				nil,
			)

			if err != nil {
				errChan <- fmt.Errorf(
					"worker %d request build failed: %w",
					myIndex,
					err,
				)
				return
			}

			hasRange := false

			if endBoundary > 0 {
				req.Header.Set(
					"Range",
					fmt.Sprintf(
						"bytes=%d-%d",
						writeOffset,
						endBoundary,
					),
				)
				hasRange = true
			} else if writeOffset > 0 {
				req.Header.Set(
					"Range",
					fmt.Sprintf("bytes=%d-", writeOffset),
				)
				hasRange = true
			}

			for key, value := range headers {
				req.Header.Set(key, value)
			}

			if req.Header.Get("User-Agent") == "" {
				req.Header.Set(
					"User-Agent",
					"Mozilla/5.0 (X11; Linux x86_64) "+
						"AppleWebKit/537.36 "+
						"(KHTML, like Gecko) "+
						"Chrome/126.0.0.0 Safari/537.36",
				)
			}

			resp, streamErr = SharedHTTPClient.Do(req)

			if streamErr != nil {
				sleepDuration :=
					time.Duration(1<<attempt)*100*time.Millisecond +
						time.Duration(myIndex*25)*time.Millisecond

				if sleepDuration > 4*time.Second {
					sleepDuration = 4 * time.Second
				}

				select {
				case <-ctx.Done():
					return

				case <-time.After(sleepDuration):
				}

				continue
			}

			// Handle HTTP 429 (Rate Limited).
			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfterSecs := 2

				if retryHeader := resp.Header.Get("Retry-After"); retryHeader != "" {
					if parsed, pErr := strconv.Atoi(retryHeader); pErr == nil && parsed > 0 {
						retryAfterSecs = parsed
					}
				}

				resp.Body.Close()

				select {
				case <-ctx.Done():
					return

				case <-time.After(
					time.Duration(retryAfterSecs) * time.Second,
				):
				}

				continue
			}

			// Handle transient server errors.
			if resp.StatusCode >= 500 {
				resp.Body.Close()

				streamErr = fmt.Errorf(
					"remote server error HTTP %d",
					resp.StatusCode,
				)

				sleepDuration :=
					time.Duration(1<<attempt) *
						150 *
						time.Millisecond

				if sleepDuration > 4*time.Second {
					sleepDuration = 4 * time.Second
				}

				select {
				case <-ctx.Done():
					return

				case <-time.After(sleepDuration):
				}

				continue
			}

			// ------------------------------------------------------------
			// Range response validation.
			//
			// A request containing Range MUST receive 206 Partial Content.
			// Accepting 200 OK is unsafe because the server may have
			// ignored the Range header and returned the entire file.
			// ------------------------------------------------------------
			if hasRange {
				if resp.StatusCode != http.StatusPartialContent {
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d expected HTTP 206 for ranged request, got HTTP %d",
						myIndex,
						resp.StatusCode,
					)

					return
				}

				contentRange := resp.Header.Get("Content-Range")

				if contentRange == "" {
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d received HTTP 206 without Content-Range",
						myIndex,
					)

					return
				}

				// Expected format:
				//
				//	bytes START-END/TOTAL
				//
				// Example:
				//
				//	bytes 1048576-2097151/10485760
				var rangeStart int64

				if _, err := fmt.Sscanf(
					contentRange,
					"bytes %d-",
					&rangeStart,
				); err != nil {
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d received malformed Content-Range %q",
						myIndex,
						contentRange,
					)

					return
				}

				// Make sure the server returned the range we actually requested.
				if rangeStart != writeOffset {
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d received incorrect Content-Range start: expected %d, got %d",
						myIndex,
						writeOffset,
						rangeStart,
					)

					return
				}
			} else {
				if resp.StatusCode != http.StatusOK &&
					resp.StatusCode != http.StatusPartialContent {
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d received invalid HTTP %d",
						myIndex,
						resp.StatusCode,
					)

					return
				}
			}

			streamErr = nil
			break
		}

		if streamErr != nil {
			errChan <- fmt.Errorf(
				"worker %d exhausted %d retries: %w",
				myIndex,
				MaxStreamRetries,
				streamErr,
			)
			return
		}

		// Read and write loop for the active connection stream.
		streamAborted := false

		for {
			if ctx.Err() != nil {
				resp.Body.Close()
				return
			}

			bytesRead, readErr := resp.Body.Read(buffer)

			if bytesRead > 0 {
				// Apply token-bucket bandwidth limiter.
				if limiter != nil {
					if err := limiter.WaitN(ctx, bytesRead); err != nil {
						resp.Body.Close()
						return
					}
				}

				currentEnd := atomic.LoadInt64(&me.EndBoundary)

				effectiveBytes := bytesRead

				// Never write beyond the current chunk boundary.
				if currentEnd > 0 &&
					writeOffset+int64(effectiveBytes) > currentEnd+1 {

					effectiveBytes = int(
						currentEnd + 1 - writeOffset,
					)

					if effectiveBytes <= 0 {
						break
					}
				}

				// Direct offset write avoids POSIX file-pointer locking.
				_, writeErr := finalFile.WriteAt(
					buffer[:effectiveBytes],
					writeOffset,
				)

				if writeErr != nil {
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d write failed at offset %d: %w",
						myIndex,
						writeOffset,
						writeErr,
					)

					return
				}

				writeOffset += int64(effectiveBytes)

				atomic.StoreInt64(
					&me.CurrentPtr,
					writeOffset,
				)

				// Non-blocking telemetry checkpoint update.
				select {
				case stateUpdateChan <- Chunk{
					Index: myIndex,
					Start: writeOffset,
					End:   currentEnd,
				}:
				default:
				}

				progressChan <- int64(effectiveBytes)

				if currentEnd > 0 && writeOffset > currentEnd {
					break
				}
			}

			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					// Mid-stream disconnection.
					// The outer loop reconnects at writeOffset.
					streamAborted = true
				}

				break
			}
		}

		resp.Body.Close()

		// Dynamic single-stream completed cleanly.
		if endBoundary <= 0 && !streamAborted {
			return
		}

		// Bounded chunk completed cleanly.
		if !streamAborted &&
			writeOffset > atomic.LoadInt64(&me.EndBoundary) {
			return
		}
	}
}
