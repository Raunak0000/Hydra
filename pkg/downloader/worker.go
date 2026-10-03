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
	BufferSize          = 256 * 1024
	DynamicMinChunkSize = 2 * 1024 * 1024

	MaxStreamRetries = 8

	// If an active HTTP stream produces no data for this long,
	// cancel the request and reconnect from the latest file offset.
	StreamIdleTimeout = 10 * time.Second
)

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, BufferSize)
		return &b
	},
}

// SharedHTTPClient is tuned for high-speed multi-threaded downloads.
var SharedHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true,
		WriteBufferSize:       BufferSize,
		ReadBufferSize:        BufferSize,
		ForceAttemptHTTP2:     true,
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

	bufPtr := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufPtr)

	buffer := *bufPtr

	for {
		if ctx.Err() != nil {
			return
		}

		writeOffset := atomic.LoadInt64(&me.CurrentPtr)
		endBoundary := atomic.LoadInt64(&me.EndBoundary)

		if endBoundary > 0 && writeOffset > endBoundary {
			return
		}

		var resp *http.Response
		var streamErr error
		connected := false

		/*
			Connection establishment.

			We keep trying while the parent context is alive.
			This is important for network interruptions that last
			longer than MaxStreamRetries attempts.
		*/
		for attempt := 1; ; attempt++ {
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

			// Reload the latest offset before EVERY reconnect.
			writeOffset = atomic.LoadInt64(&me.CurrentPtr)
			endBoundary = atomic.LoadInt64(&me.EndBoundary)

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
				sleepDuration := time.Duration(attempt*150) * time.Millisecond

				if sleepDuration > time.Second {
					sleepDuration = time.Second
				}

				select {
				case <-ctx.Done():
					return

				case <-time.After(sleepDuration):
				}

				continue
			}

			/*
				HTTP 429.

				Retry without treating it as a successful connection.
			*/
			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfter := 2 * time.Second

				if retryHeader := resp.Header.Get("Retry-After"); retryHeader != "" {
					if seconds, err := strconv.Atoi(retryHeader); err == nil && seconds > 0 {
						retryAfter = time.Duration(seconds) * time.Second
					}
				}

				resp.Body.Close()

				select {
				case <-ctx.Done():
					return

				case <-time.After(retryAfter):
				}

				continue
			}

			/*
				Transient server errors.
			*/
			if resp.StatusCode >= 500 {
				resp.Body.Close()

				sleepDuration :=
					time.Duration(1<<min(attempt, 5)) *
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

			/*
				Validate ranged responses.
			*/
			if hasRange {
				if resp.StatusCode != http.StatusPartialContent {
					status := resp.StatusCode
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d expected HTTP 206 for ranged request, got HTTP %d",
						myIndex,
						status,
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

					status := resp.StatusCode
					resp.Body.Close()

					errChan <- fmt.Errorf(
						"worker %d received invalid HTTP %d",
						myIndex,
						status,
					)

					return
				}
			}

			connected = true
			streamErr = nil
			break
		}

		if !connected {
			return
		}

		/*
			Active stream.

			The child context allows us to cancel ONLY this HTTP
			request when the connection becomes idle.

			The parent ctx remains alive, allowing the worker to
			reconnect.
		*/
		streamCtx, cancelStream := context.WithCancel(ctx)

		/*
			Idle watchdog.

			If no data arrives for StreamIdleTimeout, cancelStream()
			forces the blocked Body.Read() to return.
		*/
		idleTimer := time.NewTimer(StreamIdleTimeout)
		idleReset := make(chan struct{}, 1)
		streamDone := make(chan struct{})

		go func() {
			defer close(streamDone)

			for {
				select {
				case <-idleTimer.C:
					cancelStream()
					return

				case <-idleReset:
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}

					idleTimer.Reset(StreamIdleTimeout)

				case <-ctx.Done():
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}

					return
				}
			}
		}()

		streamAborted := false
		streamCompleted := false

		for {
			if ctx.Err() != nil {
				cancelStream()
				resp.Body.Close()
				<-streamDone
				return
			}

			bytesRead, readErr := resp.Body.Read(buffer)

			if bytesRead > 0 {
				/*
					We received data, so reset the idle watchdog.
				*/
				select {
				case idleReset <- struct{}{}:
				default:
				}

				if limiter != nil {
					if err := limiter.WaitN(ctx, bytesRead); err != nil {
						cancelStream()
						resp.Body.Close()
						<-streamDone
						return
					}
				}

				currentEnd := atomic.LoadInt64(&me.EndBoundary)

				effectiveBytes := bytesRead

				/*
					Never write beyond the chunk boundary.
				*/
				if currentEnd > 0 &&
					writeOffset+int64(effectiveBytes) > currentEnd+1 {

					effectiveBytes = int(
						currentEnd + 1 - writeOffset,
					)

					if effectiveBytes <= 0 {
						streamCompleted = true
						break
					}
				}

				_, writeErr := finalFile.WriteAt(
					buffer[:effectiveBytes],
					writeOffset,
				)

				if writeErr != nil {
					cancelStream()
					resp.Body.Close()
					<-streamDone

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

				select {
				case stateUpdateChan <- Chunk{
					Index: myIndex,
					Start: writeOffset,
					End:   currentEnd,
				}:
				default:
				}

				select {
				case progressChan <- int64(effectiveBytes):
				case <-ctx.Done():
					cancelStream()
					resp.Body.Close()
					<-streamDone
					return
				}

				if currentEnd > 0 &&
					writeOffset > currentEnd {

					streamCompleted = true
					break
				}
			}

			if readErr != nil {
				/*
					Normal EOF.
				*/
				if errors.Is(readErr, io.EOF) {
					streamCompleted = true
				} else {
					/*
						The request was cancelled by the idle watchdog,
						or the underlying network connection died.

						Do NOT report this as a fatal error.
						Reconnect from CurrentPtr.
					*/
					if streamCtx.Err() != nil && ctx.Err() == nil {
						streamAborted = true
					} else {
						streamAborted = true
					}
				}

				break
			}
		}

		cancelStream()

		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}

		resp.Body.Close()
		<-streamDone

		/*
			Completed download.
		*/
		if streamCompleted {
			if endBoundary <= 0 {
				return
			}

			if writeOffset > atomic.LoadInt64(&me.EndBoundary) {
				return
			}
		}

		/*
			Network interruption.

			Loop back to connection establishment.

			CurrentPtr already contains the last successfully
			written byte, so the next request resumes from there.
		*/
		if streamAborted {
			continue
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
