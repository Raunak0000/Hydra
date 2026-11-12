package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	BufferSize          = 256 * 1024
	DynamicMinChunkSize = 2 * 1024 * 1024

	MaxStreamRetries      = 8
	maxZeroProgressStalls = 8

	// Maximum time an active HTTP stream can produce no data before it is
	// considered stalled and reconnected.
	StreamIdleTimeout = 15 * time.Second
)

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, BufferSize)
		return &b
	},
}

// SharedHTTPClient is tuned for high-speed multi-threaded downloads.
var SharedHTTPClient = &http.Client{
	CheckRedirect: SafeCheckRedirect,
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
	initialIndex int,
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

	bufPtr := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(bufPtr)

	buffer := *bufPtr

	var me *AdaptiveTracker
	defer func() {
		if me != nil {
			me.Release()
		}
	}()

	// Stagger initial worker connections slightly (50ms per worker index)
	// to avoid triggering CDN / Cloudflare burst rate limits on initial connection.
	if initialIndex > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(initialIndex*50) * time.Millisecond):
		}
	}

	targetIndex := initialIndex
	stall429Count := 0
	stallCount := 0

	for {
		if ctx.Err() != nil {
			return
		}

		if me != nil {
			me.Release()
			me = nil
		}

		// Claim an unfinished chunk from trackers (preferred: targetIndex)
		me = ClaimNextUnfinishedTracker(trackers, targetIndex)
		if me == nil {
			// No tracker available to claim right now. Check if all chunks are finished.
			if AllTrackersCompleted(trackers) {
				return // Entire download is complete
			}

			// Some chunks are still in progress by other workers.
			// Wait briefly to see if another worker finishes, drops, or yields a chunk.
			select {
			case <-ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
				targetIndex = -1
				continue
			}
		}

		myIndex := me.Index
		chunkCompleted := false

		/*
			========================================================
			CONNECTION LOOP FOR CLAIMED CHUNK
			========================================================

			This loop intentionally has no fixed retry limit.

			If the network disappears, the worker waits and keeps
			trying until the parent context is cancelled.
		*/

		for attempt := 1; !chunkCompleted; attempt++ {
			if ctx.Err() != nil {
				me.Release()
				return
			}

			writeOffset := atomic.LoadInt64(&me.CurrentPtr)
			endBoundary := atomic.LoadInt64(&me.EndBoundary)

			// Chunk is already complete.
			if endBoundary > 0 && writeOffset > endBoundary {
				chunkCompleted = true
				break
			}

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
				me.Release()

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
				Apply caller-provided headers.

				Range and Content-Length are owned by the worker. Allowing
				a browser-supplied value to survive here can make a resumed
				worker request the wrong byte window and stall at a chunk
				boundary.
			*/
			for key, value := range headers {
				if strings.EqualFold(key, "Range") ||
					strings.EqualFold(key, "Content-Length") ||
					strings.EqualFold(key, "X-Hydra-Token") {
					continue
				}
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
				Set the correct Range header after caller headers so the
				worker always owns the byte window.
				Skip Range when a single worker starts from byte 0 so servers
				without byte-range support can stream HTTP 200 OK cleanly.
			*/
			useRange := len(trackers) > 1 || writeOffset > 0
			if useRange && endBoundary > 0 {
				req.Header.Set(
					"Range",
					fmt.Sprintf(
						"bytes=%d-%d",
						writeOffset,
						endBoundary,
					),
				)

				hasRange = true
			} else if useRange && writeOffset > 0 {
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

				if !waitOrFailStalledWorker(
					ctx,
					sleepDuration,
					trackers,
					&stallCount,
					me,
					errChan,
					fmt.Sprintf("worker %d connection failed after %d stalled attempts: %v", myIndex, maxZeroProgressStalls, err),
				) {
					return
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

				// Cap retry delay between 1s and 3s (plus jitter).
				// Servers/CDNs send Retry-After: 1800+ (30 mins) on concurrency limit.
				// A long sleep causes download speed to drop to 0.
				if retryAfter > 3*time.Second {
					retryAfter = 3 * time.Second
				}
				if retryAfter < 1*time.Second {
					retryAfter = 1 * time.Second
				}
				retryAfter += time.Duration(rand.Intn(400)+100) * time.Millisecond

				bodySnippet := readBodySnippet(resp.Body)
				resp.Body.Close()
				cancelStream()

				// Release the claim on this chunk during backoff so other workers can coordinate
				me.Release()

				progressBefore := sumTrackersCurrent(trackers)

				timer := time.NewTimer(retryAfter)
				select {
				case <-ctx.Done():
					timer.Stop()
					return

				case <-timer.C:
				}

				progressAfter := sumTrackersCurrent(trackers)
				if progressAfter > progressBefore {
					// Other streams are actively transferring bytes; this 429 is a concurrency cap.
					stall429Count = 0
				} else {
					stall429Count++
					if (isHardRateLimitMessage(bodySnippet) && stall429Count >= 2) || stall429Count >= 4 {
						errMsg := fmt.Sprintf("worker %d HTTP 429: host rate limit exceeded", myIndex)
						if bodySnippet != "" {
							errMsg = fmt.Sprintf("worker %d HTTP 429: %s", myIndex, bodySnippet)
						}
						select {
						case errChan <- fmt.Errorf("%s", errMsg):
						case <-ctx.Done():
						}
						return
					}
				}

				targetIndex = -1
				break
			}

			/*
				====================================================
				HTTP 5xx
				====================================================
			*/
			if resp.StatusCode >= 500 {
				status := resp.StatusCode
				resp.Body.Close()
				cancelStream()

				backoff := time.Duration(1<<min(attempt, 4)) *
					150 *
					time.Millisecond

				if backoff > 2*time.Second {
					backoff = 2 * time.Second
				}

				if !waitOrFailStalledWorker(
					ctx,
					backoff,
					trackers,
					&stallCount,
					me,
					errChan,
					fmt.Sprintf("worker %d server returned HTTP %d across %d stalled attempts", myIndex, status, maxZeroProgressStalls),
				) {
					return
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
					me.Release()

					errMsg := fmt.Sprintf(
						"worker %d expected HTTP 206 for ranged request, got HTTP %d",
						myIndex,
						status,
					)
					if status == http.StatusNotFound {
						errMsg = fmt.Sprintf(
							"worker %d: server returned HTTP 404 (download link expired or resource not found)",
							myIndex,
						)
					}

					select {
					case errChan <- fmt.Errorf("%s", errMsg):
					case <-ctx.Done():
					}

					return
				}

				contentRange := resp.Header.Get("Content-Range")

				if contentRange == "" {
					resp.Body.Close()
					cancelStream()
					me.Release()

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
					me.Release()

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
					me.Release()

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
					me.Release()

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
			streamBytes := int64(0)

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
					stall429Count = 0
					stallCount = 0

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
					streamBytes += int64(effectiveBytes)

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
							HTTP completion is only normal when this stream
							reached its assigned boundary. A CDN can close
							a range response early; reconnect from the
							authoritative offset instead of leaving the
							worker finished with a partial chunk.
						*/
						streamCompleted = endBoundary <= 0 ||
							writeOffset > endBoundary
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

				if streamBytes == 0 {
					streamAborted = true
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
					me.Release()
					return
				}

				/*
					Bounded chunk.
				*/
				if writeOffset > atomic.LoadInt64(
					&me.EndBoundary,
				) {
					chunkCompleted = true
					break
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
			if streamBytes == 0 {
				retryDelay := time.Duration(min(attempt, 5)*150) * time.Millisecond
				if !waitOrFailStalledWorker(
					ctx,
					retryDelay,
					trackers,
					&stallCount,
					me,
					errChan,
					fmt.Sprintf("worker %d stream stalled with zero bytes across %d attempts", myIndex, maxZeroProgressStalls),
				) {
					return
				}
				continue
			}

			stallCount = 0
			if streamAborted {
				break
			}

			/*
				Unexpected termination.

				Reconnect instead of leaving the worker stuck.
			*/
			break
		}

		// Release claim on completed chunk and claim the next unfinished chunk
		if me != nil {
			me.Release()
			me = nil
		}
		targetIndex = -1
	}
}

// waitOrFailStalledWorker backs off via waitForZeroProgressRetry and either returns
// true to retry or releases the chunk claim, closes idle connections, reports
// errMsg on persistent zero-progress stalls, and returns false.
func waitOrFailStalledWorker(
	ctx context.Context,
	delay time.Duration,
	trackers []*AdaptiveTracker,
	stallCount *int,
	me *AdaptiveTracker,
	errChan chan<- error,
	errMsg string,
) bool {
	shouldRetry, stalledOut := waitForZeroProgressRetry(ctx, delay, trackers, stallCount)
	if shouldRetry {
		return true
	}
	me.Release()
	if stalledOut {
		SharedHTTPClient.CloseIdleConnections()
		select {
		case errChan <- fmt.Errorf("%s", errMsg):
		case <-ctx.Done():
		}
	}
	return false
}

// waitForZeroProgressRetry sleeps for delay while checking whether any chunk
// made progress across the job. It stops immediately on cancellation or once
// stallCount reaches maxZeroProgressStalls without global progress.
func waitForZeroProgressRetry(
	ctx context.Context,
	delay time.Duration,
	trackers []*AdaptiveTracker,
	stallCount *int,
) (shouldRetry bool, stalledOut bool) {
	progressBefore := sumTrackersCurrent(trackers)

	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, false
		case <-timer.C:
		}
	} else if ctx.Err() != nil {
		return false, false
	}

	if sumTrackersCurrent(trackers) > progressBefore {
		*stallCount = 0
		return true, false
	}

	*stallCount++
	if *stallCount >= maxZeroProgressStalls {
		return false, true
	}
	return true, false
}

func sumTrackersCurrent(trackers []*AdaptiveTracker) int64 {
	var total int64
	for _, tr := range trackers {
		total += tr.GetCurrent()
	}
	return total
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}
