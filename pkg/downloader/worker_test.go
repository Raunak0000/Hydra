package downloader

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// setupRangeServer creates a test HTTP server supporting Range requests.
func setupRangeServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		totalSize := int64(len(data))

		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}

		// Parse Range: bytes=start-end or bytes=start-
		rangeHeader = strings.TrimPrefix(rangeHeader, "bytes=")
		parts := strings.Split(rangeHeader, "-")
		if len(parts) != 2 {
			http.Error(w, "invalid range", http.StatusBadRequest)
			return
		}

		start, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || start >= totalSize {
			http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}

		end := totalSize - 1
		if parts[1] != "" {
			parsedEnd, pErr := strconv.ParseInt(parts[1], 10, 64)
			if pErr == nil && parsedEnd < totalSize {
				end = parsedEnd
			}
		}

		chunkLen := end - start + 1
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
		w.Header().Set("Content-Length", strconv.FormatInt(chunkLen, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
}

func TestPauseAndResumeDownload(t *testing.T) {
	// Create 10 MB of random test data
	fileSize := int64(10 * 1024 * 1024)
	payload := make([]byte, fileSize)
	_, err := rand.Read(payload)
	if err != nil {
		t.Fatalf("failed to generate random test data: %v", err)
	}

	server := setupRangeServer(t, payload)
	defer server.Close()

	// Temporary output file
	tmpFile, err := os.CreateTemp("", "hydra_test_*.bin")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if err := tmpFile.Truncate(fileSize); err != nil {
		t.Fatalf("failed to truncate temp file: %v", err)
	}

	numThreads := 4
	chunks := CalculateChunks(fileSize, numThreads)
	if len(chunks) != numThreads {
		t.Fatalf("expected %d chunks, got %d", numThreads, len(chunks))
	}

	trackers := make([]*AdaptiveTracker, numThreads)
	for i, ch := range chunks {
		trackers[i] = &AdaptiveTracker{
			Index:       i,
			StartByte:   ch.Start,
			CurrentPtr:  ch.Start,
			EndBoundary: ch.End,
		}
	}

	// ── Phase 1: Start download and cancel midway ──
	ctx1, cancel1 := context.WithCancel(context.Background())
	var wg1 sync.WaitGroup
	errChan1 := make(chan error, numThreads)
	progressChan1 := make(chan int64, 1024)
	stateChan1 := make(chan Chunk, 1024)

	// Use rate limiter to slow down in-memory transfer for test
	limiter := NewRateLimiter(5 * 1024 * 1024) // 5 MB/s

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
			limiter,
		)
	}

	// Let workers download some bytes, then simulate Pause (P key)
	var downloadedSoFar int64
	doneReading := make(chan struct{})
	go func() {
		for p := range progressChan1 {
			atomic.AddInt64(&downloadedSoFar, p)
		}
		close(doneReading)
	}()

	// Wait until at least 1 MB is downloaded, then cancel
	timeout := time.After(3 * time.Second)
	for {
		if atomic.LoadInt64(&downloadedSoFar) > 1024*1024 {
			cancel1()
			break
		}
		select {
		case <-timeout:
			cancel1()
			t.Fatalf("timeout waiting for partial download progress")
		case <-time.After(10 * time.Millisecond):
		}
	}

	wg1.Wait()
	close(progressChan1)
	close(stateChan1)
	<-doneReading

	// Verify that download stopped midway
	pausedBytes := int64(0)
	for _, tr := range trackers {
		cur := tr.GetCurrent()
		start := tr.GetStart()
		if cur > start {
			pausedBytes += cur - start
		}
	}

	if pausedBytes == 0 {
		t.Fatalf("expected some bytes downloaded before pause, got 0")
	}
	if pausedBytes >= fileSize {
		t.Fatalf("download finished before it could be paused (got %d / %d)", pausedBytes, fileSize)
	}

	t.Logf("Successfully paused at %d / %d bytes (%.2f%%)",
		pausedBytes, fileSize, float64(pausedBytes)/float64(fileSize)*100.0)

	// ── Phase 2: Resume download from persisted tracker offsets ──
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	var wg2 sync.WaitGroup
	errChan2 := make(chan error, numThreads)
	progressChan2 := make(chan int64, 1024)
	stateChan2 := make(chan Chunk, 1024)

	var resumedBytes int64
	doneResumed := make(chan struct{})
	go func() {
		for p := range progressChan2 {
			atomic.AddInt64(&resumedBytes, p)
		}
		close(doneResumed)
	}()

	for i := 0; i < numThreads; i++ {
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

	wg2.Wait()
	close(progressChan2)
	close(stateChan2)
	<-doneResumed

	// Verify no worker reported errors
	select {
	case err := <-errChan2:
		t.Fatalf("worker error on resume: %v", err)
	default:
	}

	// Total newly downloaded bytes should equal the remaining portion
	expectedRemaining := fileSize - pausedBytes
	if resumedBytes != expectedRemaining {
		t.Errorf("resumed bytes mismatch: expected %d, got %d", expectedRemaining, resumedBytes)
	}

	// ── Phase 3: Integrity verification ──
	// The final file on disk MUST match the original payload exactly
	writtenData, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if int64(len(writtenData)) != fileSize {
		t.Fatalf("final file size mismatch: expected %d, got %d", fileSize, len(writtenData))
	}

	if !bytes.Equal(writtenData, payload) {
		t.Fatalf("corrupted file: final data on disk does not match original payload")
	}

	t.Logf("✓ File matches original payload byte-for-byte after pause and resume!")
}
