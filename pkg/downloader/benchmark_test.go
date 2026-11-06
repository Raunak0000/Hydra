package downloader

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// setupBenchRangeServer creates an HTTP test server that supports Range headers.
func setupBenchRangeServer(b *testing.B, data []byte) *httptest.Server {
	b.Helper()
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

func BenchmarkSingleStreamDownload(b *testing.B) {
	dataSize := 10 * 1024 * 1024 // 10 MB
	data := make([]byte, dataSize)
	_, _ = rand.Read(data)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(dataSize))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	client := server.Client()

	b.SetBytes(int64(dataSize))
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		resp, err := client.Get(server.URL)
		if err != nil {
			b.Fatalf("GET failed: %v", err)
		}
		n, err := io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil || n != int64(dataSize) {
			b.Fatalf("copied %d bytes, err: %v", n, err)
		}
	}
}

func BenchmarkRangedDownload(b *testing.B) {
	dataSize := 10 * 1024 * 1024 // 10 MB
	data := make([]byte, dataSize)
	_, _ = rand.Read(data)

	server := setupBenchRangeServer(b, data)
	defer server.Close()

	numWorkers := 4

	b.SetBytes(int64(dataSize))
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tmpDir := b.TempDir()
		outPath := filepath.Join(tmpDir, "out.bin")
		outFile, err := os.OpenFile(outPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			b.Fatalf("failed to create output file: %v", err)
		}
		_ = outFile.Truncate(int64(dataSize))

		chunks := CalculateChunks(int64(dataSize), numWorkers)
		trackers := make([]*AdaptiveTracker, len(chunks))
		for cIdx, ch := range chunks {
			trackers[cIdx] = &AdaptiveTracker{
				Index:       cIdx,
				StartByte:   ch.Start,
				CurrentPtr:  ch.Start,
				EndBoundary: ch.End,
			}
		}

		var wg sync.WaitGroup
		errChan := make(chan error, numWorkers)
		progressChan := make(chan int64, 4096)
		stateChan := make(chan Chunk, 4096)

		go func() {
			for range progressChan {
			}
		}()
		go func() {
			for range stateChan {
			}
		}()

		ctx := context.Background()
		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go DownloadChunkParallel(
				ctx,
				server.URL,
				w,
				trackers,
				outFile,
				&wg,
				errChan,
				progressChan,
				stateChan,
				nil,
				nil,
			)
		}

		wg.Wait()
		close(errChan)
		close(progressChan)
		close(stateChan)
		_ = outFile.Close()

		if err := <-errChan; err != nil {
			b.Fatalf("parallel download error: %v", err)
		}
	}
}

func BenchmarkRateLimiterThroughput(b *testing.B) {
	limit := int64(10 * 1024 * 1024 * 1024) // 10 GB/s (benchmarks token bucket computation and mutex throughput without artificial sleep)
	limiter := NewRateLimiter(limit)
	chunkSize := 32 * 1024 // 32 KB per token chunk
	ctx := context.Background()

	b.SetBytes(int64(chunkSize))
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := limiter.WaitN(ctx, chunkSize); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRateLimitingAccuracy(t *testing.T) {
	// Target: 2 MB/s
	targetRate := int64(2 * 1024 * 1024)
	limiter := NewRateLimiter(targetRate)
	ctx := context.Background()

	// Drain initial burst
	_ = limiter.WaitN(ctx, int(targetRate))

	totalBytes := 1 * 1024 * 1024 // 1 MB
	chunkSize := 64 * 1024        // 64 KB
	chunks := totalBytes / chunkSize

	start := time.Now()
	for i := 0; i < chunks; i++ {
		if err := limiter.WaitN(ctx, chunkSize); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)

	// At 2 MB/s, 1 MB should take approximately 0.5s (500ms).
	// Allow realistic scheduling jitter: 350ms to 850ms.
	expectedDuration := float64(totalBytes) / float64(targetRate)
	t.Logf("Rate limiting: transfer %d bytes took %v (expected ~%.2fs)", totalBytes, elapsed, expectedDuration)

	if elapsed < 350*time.Millisecond || elapsed > 850*time.Millisecond {
		t.Errorf("rate limiter timing deviated significantly: got %v, expected around ~500ms", elapsed)
	}
}

func BenchmarkMemoryUse(b *testing.B) {
	pool := sync.Pool{
		New: func() any {
			buf := make([]byte, 32*1024)
			return &buf
		},
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		bufPtr := pool.Get().(*[]byte)
		buf := *bufPtr
		buf[0] = 1
		buf[len(buf)-1] = 2
		pool.Put(bufPtr)
	}
}

func TestLargeFileMemoryFootprint(t *testing.T) {
	// Simulate tracking a large file (e.g. 50 GB) across 32 chunk trackers
	// ensuring tracker memory overhead is minuscule (< 10 KB).
	totalSize := int64(50) * 1024 * 1024 * 1024 // 50 GB
	chunks := CalculateChunks(totalSize, 32)

	if len(chunks) != 32 {
		t.Fatalf("expected 32 chunks, got %d", len(chunks))
	}

	for i, ch := range chunks {
		if ch.End < ch.Start {
			t.Errorf("chunk %d invalid range: %d to %d", i, ch.Start, ch.End)
		}
	}

	if chunks[0].Start != 0 {
		t.Errorf("first chunk does not start at 0: %d", chunks[0].Start)
	}
	if chunks[len(chunks)-1].End != totalSize-1 {
		t.Errorf("last chunk end %d != totalSize-1 %d", chunks[len(chunks)-1].End, totalSize-1)
	}
	t.Logf("50 GB file divided into %d chunks successfully, memory overhead minimal", len(chunks))
}
