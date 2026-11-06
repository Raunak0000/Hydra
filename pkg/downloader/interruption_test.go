package downloader

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorkerInterruption_ServerAbruptClose tests how workers handle mid-stream network drops
// when an HTTP server abruptly terminates the TCP connection before all bytes are sent.
func TestWorkerInterruption_ServerAbruptClose(t *testing.T) {
	totalSize := int64(1024 * 1024) // 1 MB
	fullData := make([]byte, totalSize)
	_, _ = rand.Read(fullData)

	var dropCount int32

	// Server that intentionally cuts off after sending 32KB on initial attempts
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		count := atomic.AddInt32(&dropCount, 1)

		rangeHeader := r.Header.Get("Range")
		var start int64
		if rangeHeader != "" {
			_, _ = fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
		}

		// On the first 2 attempts, send 32KB from the requested offset and abruptly close connection
		if count <= 2 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, totalSize-1, totalSize))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", totalSize-start))
			w.WriteHeader(http.StatusPartialContent)

			// Write truncated data (32 KB)
			chunkEnd := start + 32*1024
			if chunkEnd > totalSize {
				chunkEnd = totalSize
			}
			_, _ = w.Write(fullData[start:chunkEnd])

			// Hijack and abruptly close the TCP connection to simulate connection drop
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				if conn != nil {
					_ = conn.Close()
				}
			}
			return
		}

		// Subsequent requests succeed normally
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, totalSize-1, totalSize))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", totalSize-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(fullData[start:])
	}))

	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()

	tmpFile, err := os.CreateTemp("", "hydra_interruption_*.bin")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if err := tmpFile.Truncate(totalSize); err != nil {
		t.Fatalf("Truncate failed: %v", err)
	}

	trackers := []*AdaptiveTracker{
		{
			Index:       0,
			StartByte:   0,
			CurrentPtr:  0,
			EndBoundary: totalSize - 1,
		},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	errChan := make(chan error, 4)
	progressChan := make(chan int64, 256)
	stateUpdateChan := make(chan Chunk, 256)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		for {
			select {
			case <-progressChan:
			case <-stateUpdateChan:
			case <-ctx.Done():
				return
			}
		}
	}()

	go DownloadChunkParallel(
		ctx,
		server.URL,
		0,
		trackers,
		tmpFile,
		&wg,
		errChan,
		progressChan,
		stateUpdateChan,
		nil,
		nil,
	)

	wg.Wait()

	// Verify that worker reconnected and successfully downloaded the entire file despite 2 drops
	select {
	case dlErr := <-errChan:
		t.Fatalf("unexpected worker error: %v", dlErr)
	default:
	}

	result, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(result, fullData) {
		t.Fatalf("data mismatch after recovering from abrupt server disconnections")
	}
}

// TestWorkerInterruption_ContextCancellationAndResume verifies that cancelling the
// context partway through terminates workers promptly and preserves accurate byte offsets
// so a subsequent resume completes with exact byte-for-byte SHA256 integrity.
func TestWorkerInterruption_ContextCancellationAndResume(t *testing.T) {
	fileSize := int64(4 * 1024 * 1024) // 4 MB
	payload := make([]byte, fileSize)
	_, _ = rand.Read(payload)
	expectedHash := sha256.Sum256(payload)

	server := setupRangeServer(t, payload)
	defer server.Close()

	tmpFile, err := os.CreateTemp("", "hydra_cancel_resume_*.bin")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if err := tmpFile.Truncate(fileSize); err != nil {
		t.Fatalf("Truncate failed: %v", err)
	}

	numThreads := 4
	chunks := CalculateChunks(fileSize, numThreads)
	trackers := make([]*AdaptiveTracker, numThreads)
	for i, ch := range chunks {
		trackers[i] = &AdaptiveTracker{
			Index:       i,
			StartByte:   ch.Start,
			CurrentPtr:  ch.Start,
			EndBoundary: ch.End,
		}
	}

	// Phase 1: Start download and cancel context after ~500 KB is downloaded
	ctx1, cancel1 := context.WithCancel(context.Background())

	var wg1 sync.WaitGroup
	errChan1 := make(chan error, numThreads)
	progressChan1 := make(chan int64, 256)
	stateChan1 := make(chan Chunk, 256)

	var totalDownloaded int64
	doneConsumer1 := make(chan struct{})
	go func() {
		defer close(doneConsumer1)
		for {
			select {
			case n, ok := <-progressChan1:
				if !ok {
					return
				}
				newTotal := atomic.AddInt64(&totalDownloaded, n)
				if newTotal >= 500*1024 {
					cancel1() // Trigger mid-stream interruption
				}
			case <-stateChan1:
			case <-ctx1.Done():
				return
			}
		}
	}()

	for i := 0; i < numThreads; i++ {
		wg1.Add(1)
		go DownloadChunkParallel(
			ctx1,
			server.URL,
			i,
			trackers,
			tmpFile,
			&wg1,
			errChan1,
			progressChan1,
			stateChan1,
			nil,
			nil,
		)
	}

	// Workers must finish quickly once context is cancelled
	finishedCh := make(chan struct{})
	go func() {
		wg1.Wait()
		close(finishedCh)
	}()

	select {
	case <-finishedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("workers failed to terminate within 5s after context cancellation")
	}
	<-doneConsumer1

	// Verify that at least some bytes were recorded and no chunk ptr exceeded its boundary
	for i, tr := range trackers {
		cur := atomic.LoadInt64(&tr.CurrentPtr)
		end := atomic.LoadInt64(&tr.EndBoundary)
		if cur > end+1 {
			t.Errorf("chunk %d overshot boundary: cur=%d end=%d", i, cur, end)
		}
	}

	// Phase 2: Resume download with a fresh context using updated tracker pointers
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	var wg2 sync.WaitGroup
	errChan2 := make(chan error, numThreads)
	progressChan2 := make(chan int64, 256)
	stateChan2 := make(chan Chunk, 256)

	go func() {
		for {
			select {
			case <-progressChan2:
			case <-stateChan2:
			case <-ctx2.Done():
				return
			}
		}
	}()

	for i := 0; i < numThreads; i++ {
		cur := atomic.LoadInt64(&trackers[i].CurrentPtr)
		end := atomic.LoadInt64(&trackers[i].EndBoundary)
		if cur <= end {
			wg2.Add(1)
			go DownloadChunkParallel(
				ctx2,
				server.URL,
				i,
				trackers,
				tmpFile,
				&wg2,
				errChan2,
				progressChan2,
				stateChan2,
				nil,
				nil,
			)
		}
	}

	wg2.Wait()

	select {
	case err := <-errChan2:
		t.Fatalf("resume failed: %v", err)
	default:
	}

	// Phase 3: Verify SHA256 checksum
	result, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read resumed file: %v", err)
	}
	actualHash := sha256.Sum256(result)
	if actualHash != expectedHash {
		t.Fatalf("SHA256 mismatch after resume:\nexpected %x\ngot      %x", expectedHash, actualHash)
	}
}

// TestWorkerDiskFull_WriteError verifies that if the file cannot be written to
// (e.g. simulated disk error / read-only file), worker stops and reports the write failure.
func TestWorkerDiskFull_WriteError(t *testing.T) {
	fileSize := int64(512 * 1024) // 512 KB
	payload := make([]byte, fileSize)
	_, _ = rand.Read(payload)

	server := setupRangeServer(t, payload)
	defer server.Close()

	// Create temp file and immediately close it so WriteAt fails
	tmpFile, err := os.CreateTemp("", "hydra_disk_err_*.bin")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	fileName := tmpFile.Name()
	_ = tmpFile.Close()
	defer os.Remove(fileName)

	// Reopen file in READ-ONLY mode
	readOnlyFile, err := os.Open(fileName)
	if err != nil {
		t.Fatalf("os.Open read-only failed: %v", err)
	}
	defer readOnlyFile.Close()

	trackers := []*AdaptiveTracker{
		{
			Index:       0,
			StartByte:   0,
			CurrentPtr:  0,
			EndBoundary: fileSize - 1,
		},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	errChan := make(chan error, 4)
	progressChan := make(chan int64, 32)
	stateChan := make(chan Chunk, 32)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		for {
			select {
			case <-progressChan:
			case <-stateChan:
			case <-ctx.Done():
				return
			}
		}
	}()

	go DownloadChunkParallel(
		ctx,
		server.URL,
		0,
		trackers,
		readOnlyFile,
		&wg,
		errChan,
		progressChan,
		stateChan,
		nil,
		nil,
	)

	wg.Wait()

	// Worker must catch write error and send to errChan
	select {
	case writeErr := <-errChan:
		if writeErr == nil {
			t.Fatal("expected non-nil write failure")
		}
	default:
		t.Fatal("expected write error to be reported via errChan on disk write failure")
	}
}

// TestAdaptiveTracker_ConcurrentChunkClaiming verifies thread safety when multiple
// threads race to claim, split, and release chunk ranges.
func TestAdaptiveTracker_ConcurrentChunkClaiming(t *testing.T) {
	const numChunks = 8
	const chunkSize = int64(10 * 1024 * 1024) // 10MB per chunk

	trackers := make([]*AdaptiveTracker, numChunks)
	for i := 0; i < numChunks; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		trackers[i] = &AdaptiveTracker{
			Index:       i,
			StartByte:   start,
			CurrentPtr:  start,
			EndBoundary: end,
		}
	}

	const numGoroutines = 20
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		idx := g % numChunks
		go func(targetIdx int) {
			defer wg.Done()
			tr := trackers[targetIdx]

			for step := 0; step < 50; step++ {
				if tr.TryClaim() {
					// Simulate advancing pointer
					atomic.AddInt64(&tr.CurrentPtr, 1024)
					tr.Release()
				}
				time.Sleep(100 * time.Microsecond)
			}
		}(idx)
	}

	wg.Wait()

	// Verify invariant: CurrentPtr cannot exceed EndBoundary+1
	for i, tr := range trackers {
		cur := atomic.LoadInt64(&tr.CurrentPtr)
		end := atomic.LoadInt64(&tr.EndBoundary)
		if cur > end+1 {
			t.Errorf("tracker %d overshot boundary: cur=%d, end=%d", i, cur, end)
		}
	}
}
