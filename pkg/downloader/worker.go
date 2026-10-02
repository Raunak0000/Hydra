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
	MaxStreamRetries    = 8               // Max connection attempts per reconnect cycle

	// If a stream produces no data for this long, the stream is
	// considered stalled and is automatically reconnected.
	StreamStallTimeout = 15 * time.Second
)

// bufferPool recycles 256 KB byte slices across workers.
var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, BufferSize)
		return &b
	},
}

// SharedHTTPClient is tuned for high-speed multi-threaded downloads.
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

// DownloadChunkParallel downloads data for a single adaptive worker.
//
// Important behavior:
//   - Resumes from CurrentPtr after reconnects.
//   - Detects stalled HTTP streams.
//   - Automatically reconnects after a stall.
//   - Validates HTTP Range responses.
//   - Never writes beyond the assigned chunk boundary.
//   - Preserves pause/cancel behavior through ctx.
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

		// Assigned bounded chunk has already been completely downloaded.
		if endBoundary > 0 && writeOffset > endBoundary {
			return
		}

		var resp *http.Response
		var streamErr error

		// ------------------------------------------------------------
		// Establish HTTP connection
		// ------------------------------------------------------------

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

			// Bounded chunk.
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

				// Unbounded resume.
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

			// --------------------------------------------------------
			// Connection failed
			// --------------------------------------------------------

			if streamErr != nil {
				sleepDuration := time.Duration(attempt*150) * time.Millisecond

				// Stagger workers slightly.
				sleepDuration += time.Duration(myIndex*20) * time.Millisecond

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

			// --------------------------------------------------------
			// HTTP 429
			// --------------------------------------------------------

			if resp.StatusCode == http.StatusTooManyRequests {
				retryAfterSecs := 2

				if retryHeader := resp.Header.Get("Retry-After"); retryHeader != "" {
					if parsed, err := strconv.Atoi(retryHeader); err == nil && parsed > 0 {
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

			// --------------------------------------------------------
			// HTTP 5xx
			// --------------------------------------------------------

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

			// --------------------------------------------------------
			// Range response validation
			// --------------------------------------------------------

			if hasRange {
				// A ranged request must return 206.
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

				// Expected:
				//
				// bytes START-END/TOTAL
				//
				// Example:
				//
				// bytes 1048576-2097151/10485760

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
				// Initial non-ranged request.
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

		// ------------------------------------------------------------
		// All connection attempts failed.
		// ------------------------------------------------------------

		if streamErr != nil {
			errChan <- fmt.Errorf(
				"worker %d exhausted %d retries: %w",
				myIndex,
				MaxStreamRetries,
				streamErr,
			)

			return
		}

		// ------------------------------------------------------------
		// Stream watchdog
		// ------------------------------------------------------------

		// The normal HTTP client timeout does NOT protect us from a
		// connection that remains open but stops producing data.
		//
		// Therefore we track the timestamp of the last successful read.
		streamCtx, cancelStream := context.WithCancel(ctx)

		// Rebuild the request using the stream-specific context.
		//
		// The existing response is already tied to the original request,
		// so cancellation of the original context is not enough here.
		//
		// Instead, the watchdog below directly closes the response body
		// when a stall is detected.
		_ = streamCtx

		var lastReadAt int64
		atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())

		watchdogDone := make(chan struct{})

		go func() {
			ticker := time.NewTicker(1 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ticker.C:
					lastRead := atomic.LoadInt64(&lastReadAt)

					if time.Since(time.Unix(0, lastRead)) >= StreamStallTimeout {
						// Closing the body forcibly unblocks Read().
						//
						// The read loop will detect the resulting error
						// and reconnect from CurrentPtr.
						_ = resp.Body.Close()
						return
					}

				case <-watchdogDone:
					return

				case <-ctx.Done():
					return
				}
			}
		}()

		// ------------------------------------------------------------
		// Read/write loop
		// ------------------------------------------------------------

		streamAborted := false

		for {
			if ctx.Err() != nil {
				close(watchdogDone)
				cancelStream()
				resp.Body.Close()
				return
			}

			bytesRead, readErr := resp.Body.Read(buffer)

			if bytesRead > 0 {
				// Reset the stall timer whenever data is successfully
				// received.
				atomic.StoreInt64(
					&lastReadAt,
					time.Now().UnixNano(),
				)

				// ----------------------------------------------------
				// Bandwidth limiter
				// ----------------------------------------------------

				if limiter != nil {
					if err := limiter.WaitN(ctx, bytesRead); err != nil {
						close(watchdogDone)
						cancelStream()
						resp.Body.Close()
						return
					}
				}

				currentEnd := atomic.LoadInt64(&me.EndBoundary)

				effectiveBytes := bytesRead

				// Never write past assigned chunk boundary.
				if currentEnd > 0 &&
					writeOffset+int64(effectiveBytes) > currentEnd+1 {

					effectiveBytes = int(
						currentEnd + 1 - writeOffset,
					)

					if effectiveBytes <= 0 {
						break
					}
				}

				// ----------------------------------------------------
				// Direct offset write
				// ----------------------------------------------------

				_, writeErr := finalFile.WriteAt(
					buffer[:effectiveBytes],
					writeOffset,
				)

				if writeErr != nil {
					close(watchdogDone)
					cancelStream()
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

				// ----------------------------------------------------
				// State checkpoint
				// ----------------------------------------------------

				select {
				case stateUpdateChan <- Chunk{
					Index: myIndex,
					Start: writeOffset,
					End:   currentEnd,
				}:
				default:
				}

				// ----------------------------------------------------
				// Progress telemetry
				// ----------------------------------------------------

				progressChan <- int64(effectiveBytes)

				// Assigned bounded chunk finished.
				if currentEnd > 0 &&
					writeOffset > currentEnd {

					break
				}
			}

			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					// Clean server-side completion.
					streamAborted = false
				} else {
					// Network disconnect OR watchdog-triggered
					// stalled stream.
					streamAborted = true
				}

				break
			}
		}

		close(watchdogDone)
		cancelStream()
		resp.Body.Close()

		// ------------------------------------------------------------
		// Completion handling
		// ------------------------------------------------------------

		// For an unbounded stream, EOF means the download is complete.
		if endBoundary <= 0 && !streamAborted {
			return
		}

		// Bounded chunk completed.
		if !streamAborted &&
			writeOffset > atomic.LoadInt64(&me.EndBoundary) {

			return
		}

		// ------------------------------------------------------------
		// Automatic reconnect
		// ------------------------------------------------------------
		//
		// If we arrive here because:
		//
		//   - network disappeared
		//   - HTTP connection died
		//   - stream stalled for 15 seconds
		//
		// CurrentPtr already contains the last successfully written
		// byte, so the next iteration sends a Range request starting
		// exactly there.
		//
		// No pause/resume action is required from the user.
		continue
	}
}
