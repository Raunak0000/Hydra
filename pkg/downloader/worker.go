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

	// Maximum time an active HTTP stream can produce no data
	// before it is considered stalled and reconnected.
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

		// Chunk is already complete.
		if endBoundary > 0 && writeOffset > endBoundary {
			return
		}

		/*
			========================================================
			CONNECTION LOOP
			========================================================

			This loop intentionally has no fixed retry limit.

			If the network disappears, the worker waits and keeps
			trying until the parent context is cancelled.
		*/

		for attempt := 1; ; attempt++ {
			if ctx.Err() != nil {
				return
			}

			/*
				Always reload the latest offset before reconnecting.

				This is what allows:

				    network dies
				    ↓
				    current bytes remain on disk
				    ↓
				    connection retries
				    ↓
				    Range starts at CurrentPtr
			*/
			writeOffset = atomic.LoadInt64(&me.CurrentPtr)
			endBoundary = atomic.LoadInt64(&me.EndBoundary)

			/*
				Create a context specifically for this HTTP stream.

				The watchdog can cancel this stream without killing
				the entire download worker.
			*/
			streamCtx, cancelStream := context.WithCancel(ctx)

			req, err := http.NewRequestWithContext(
				streamCtx,
				http.MethodGet,
				url,
				nil,
			)

			if err != nil {
				cancelStream()

				select {
				case errChan <- fmt.Errorf(
					"worker %d request build failed: %w",
					myIndex,
					err,
				):
				case <-ctx.Done():
				}

				return
			}

			hasRange := false

			/*
				Set the correct Range header.
			*/
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
					fmt.Sprintf(
						"bytes=%d-",
						writeOffset,
					),
				)

				hasRange = true
			}

			/*
				Apply caller-provided headers.
			*/
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

			/*
				IMPORTANT:
				resp is declared here, in the same scope where it
				will be used by the validation logic below.
			*/
			resp, err := SharedHTTPClient.Do(req)

			if err != nil {
				cancelStream()

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
				====================================================
				HTTP 429
				====================================================
			*/
			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfter := 2 * time.Second

				if retryHeader := resp.Header.Get("Retry-After"); retryHeader != "" {
					if seconds, err := strconv.Atoi(retryHeader); err == nil && seconds > 0 {
						retryAfter = time.Duration(seconds) * time.Second
					}
				}

				resp.Body.Close()
				cancelStream()

				select {
				case <-ctx.Done():
					return

				case <-time.After(retryAfter):
				}

				continue
			}

			/*
				====================================================
				HTTP 5xx
				====================================================
			*/
			if resp.StatusCode >= 500 {
				resp.Body.Close()
				cancelStream()

				backoff := time.Duration(1<<min(attempt, 5)) *
					150 *
					time.Millisecond

				if backoff > 4*time.Second {
					backoff = 4 * time.Second
				}

				select {
				case <-ctx.Done():
					return

				case <-time.After(backoff):
				}

				continue
			}

			/*
				====================================================
				RANGE VALIDATION
				====================================================
			*/
			if hasRange {
				/*
					When requesting a range, the server MUST return
					206 Partial Content.
				*/
				if resp.StatusCode != http.StatusPartialContent {
					status := resp.StatusCode

					resp.Body.Close()
					cancelStream()

					select {
					case errChan <- fmt.Errorf(
						"worker %d expected HTTP 206 for ranged request, got HTTP %d",
						myIndex,
						status,
					):
					case <-ctx.Done():
					}

					return
				}

				contentRange := resp.Header.Get("Content-Range")

				if contentRange == "" {
					resp.Body.Close()
					cancelStream()

					select {
					case errChan <- fmt.Errorf(
						"worker %d received HTTP 206 without Content-Range",
						myIndex,
					):
					case <-ctx.Done():
					}

					return
				}

				var rangeStart int64

				if _, err := fmt.Sscanf(
					contentRange,
					"bytes %d-",
					&rangeStart,
				); err != nil {
					resp.Body.Close()
					cancelStream()

					select {
					case errChan <- fmt.Errorf(
						"worker %d received malformed Content-Range %q",
						myIndex,
						contentRange,
					):
					case <-ctx.Done():
					}

					return
				}

				/*
					Prevent duplicate/corrupt writes after reconnect.
				*/
				if rangeStart != writeOffset {
					resp.Body.Close()
					cancelStream()

					select {
					case errChan <- fmt.Errorf(
						"worker %d received incorrect Content-Range start: expected %d, got %d",
						myIndex,
						writeOffset,
						rangeStart,
					):
					case <-ctx.Done():
					}

					return
				}
			} else {
				/*
					Initial request without a Range header.
				*/
				if resp.StatusCode != http.StatusOK &&
					resp.StatusCode != http.StatusPartialContent {

					status := resp.StatusCode

					resp.Body.Close()
					cancelStream()

					select {
					case errChan <- fmt.Errorf(
						"worker %d received invalid HTTP %d",
						myIndex,
						status,
					):
					case <-ctx.Done():
					}

					return
				}
			}

			/*
				====================================================
				STREAM WATCHDOG
				====================================================
			*/

			idleTimer := time.NewTimer(StreamIdleTimeout)

			/*
				The reader sends activity whenever bytes arrive.
				The watchdog owns the timer, avoiding concurrent
				timer Reset/Stop races.
			*/
			activityChan := make(chan struct{}, 1)

			watchdogDone := make(chan struct{})

			go func() {
				defer close(watchdogDone)

				for {
					select {
					case <-idleTimer.C:
						/*
							The stream has stopped producing data.

							Close the response body so a blocked
							Read() is guaranteed to wake up.
						*/
						cancelStream()
						resp.Body.Close()

						return

					case <-activityChan:
						if !idleTimer.Stop() {
							select {
							case <-idleTimer.C:
							default:
							}
						}

						idleTimer.Reset(StreamIdleTimeout)

					case <-streamCtx.Done():
						return
					}
				}
			}()

			streamCompleted := false
			streamAborted := false

			/*
				====================================================
				READ LOOP
				====================================================
			*/
			for {
				if ctx.Err() != nil {
					cancelStream()
					resp.Body.Close()

					<-watchdogDone

					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}

					return
				}

				bytesRead, readErr := resp.Body.Read(buffer)

				if bytesRead > 0 {
					/*
						Tell watchdog that data arrived.
					*/
					select {
					case activityChan <- struct{}{}:
					default:
					}

					/*
						Rate limiting.
					*/
					if limiter != nil {
						if err := limiter.WaitN(ctx, bytesRead); err != nil {
							cancelStream()
							resp.Body.Close()

							<-watchdogDone

							return
						}
					}

					currentEnd := atomic.LoadInt64(
						&me.EndBoundary,
					)

					effectiveBytes := bytesRead

					/*
						Never write past the chunk boundary.
					*/
					if currentEnd > 0 &&
						writeOffset+int64(effectiveBytes) >
							currentEnd+1 {

						effectiveBytes = int(
							currentEnd + 1 - writeOffset,
						)

						if effectiveBytes <= 0 {
							streamCompleted = true
							break
						}
					}

					/*
						Write downloaded bytes.
					*/
					_, writeErr := finalFile.WriteAt(
						buffer[:effectiveBytes],
						writeOffset,
					)

					if writeErr != nil {
						cancelStream()
						resp.Body.Close()

						<-watchdogDone

						select {
						case errChan <- fmt.Errorf(
							"worker %d write failed at offset %d: %w",
							myIndex,
							writeOffset,
							writeErr,
						):
						case <-ctx.Done():
						}

						return
					}

					/*
						Update the authoritative resume offset.
					*/
					writeOffset += int64(effectiveBytes)

					atomic.StoreInt64(
						&me.CurrentPtr,
						writeOffset,
					)

					/*
						Checkpoint state.
					*/
					select {
					case stateUpdateChan <- Chunk{
						Index: myIndex,
						Start: writeOffset,
						End:   currentEnd,
					}:
					default:
					}

					/*
						Report progress.
					*/
					select {
					case progressChan <- int64(effectiveBytes):

					case <-ctx.Done():
						cancelStream()
						resp.Body.Close()

						<-watchdogDone

						return
					}

					/*
						Chunk boundary reached.
					*/
					if currentEnd > 0 &&
						writeOffset > currentEnd {

						streamCompleted = true
						break
					}
				}

				if readErr != nil {
					if errors.Is(readErr, io.EOF) {
						/*
							Normal HTTP completion.
						*/
						streamCompleted = true
					} else {
						/*
							Connection dropped or watchdog
							cancelled the stream.

							Resume from CurrentPtr.
						*/
						streamAborted = true
					}

					break
				}
			}

			/*
				====================================================
				CLEANUP CURRENT STREAM
				====================================================
			*/

			cancelStream()
			resp.Body.Close()

			<-watchdogDone

			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}

			/*
				====================================================
				COMPLETION
				====================================================
			*/

			if streamCompleted {
				/*
					Dynamic/unbounded download.
				*/
				if endBoundary <= 0 {
					return
				}

				/*
					Bounded chunk.
				*/
				if writeOffset > atomic.LoadInt64(
					&me.EndBoundary,
				) {
					return
				}
			}

			/*
				====================================================
				NETWORK INTERRUPTION
				====================================================

				The current stream died.

				Break back to the connection loop.

				CurrentPtr contains the last successfully written
				byte, so the next request starts exactly there.
			*/
			if streamAborted {
				break
			}

			/*
				Unexpected termination.

				Reconnect instead of leaving the worker stuck.
			*/
			break
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}
