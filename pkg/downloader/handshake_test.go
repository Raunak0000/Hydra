package downloader

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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
