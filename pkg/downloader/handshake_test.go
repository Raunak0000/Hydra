package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetMetadataRetriesRateLimit(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}

		w.Header().Set("Content-Range", "bytes 0-0/4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("t"))
	}))
	defer server.Close()

	metadata, err := GetMetadata(server.URL, nil)
	if err != nil {
		t.Fatalf("GetMetadata returned an error: %v", err)
	}

	if metadata.Size != 4 {
		t.Fatalf("expected size 4, got %d", metadata.Size)
	}

	if !metadata.AcceptRanges {
		t.Fatal("expected ranged downloads to be enabled")
	}

	if requests.Load() != 2 {
		t.Fatalf("expected 2 requests, got %d", requests.Load())
	}
}

func TestGetMetadataContext_FailsFastOnHard429(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("id busy - generate a new download link if stuck"))
	}))
	defer server.Close()

	start := time.Now()
	_, err := GetMetadataContext(context.Background(), server.URL, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for hard 429 lock, got nil")
	}
	if !strings.Contains(err.Error(), "id busy - generate a new download link if stuck") {
		t.Fatalf("expected host error message in %q", err.Error())
	}
	if elapsed > time.Second {
		t.Fatalf("expected fast failure on Retry-After: 60 id-busy lock, took %v", elapsed)
	}
	if requests.Load() != 1 {
		t.Fatalf("expected 1 attempt without sleeping 60s, got %d", requests.Load())
	}
}

func TestGetMetadataContext_RespectsCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := GetMetadataContext(ctx, server.URL, nil)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("GetMetadataContext ignored context cancellation (took %v)", time.Since(start))
	}
}
