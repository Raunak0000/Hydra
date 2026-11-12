package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSafeCheckRedirectPolicy(t *testing.T) {
	t.Run("stops after 10 redirects", func(t *testing.T) {
		via := make([]*http.Request, 10)
		for i := range via {
			r, _ := http.NewRequest(http.MethodGet, "https://example.com/hop", nil)
			via[i] = r
		}
		nextReq, _ := http.NewRequest(http.MethodGet, "https://example.com/hop-11", nil)
		if err := SafeCheckRedirect(nextReq, via); err == nil {
			t.Fatal("expected error when redirect count reaches 10")
		}
	})

	t.Run("strips sensitive auth headers on HTTPS to HTTP downgrade even for same host", func(t *testing.T) {
		prevReq, _ := http.NewRequest(http.MethodGet, "https://files.example.com/start", nil)
		nextReq, _ := http.NewRequest(http.MethodGet, "http://files.example.com/final", nil)
		nextReq.Header.Set("Authorization", "Bearer top-secret")
		nextReq.Header.Set("Cookie", "session=xyz")
		nextReq.Header.Set("X-Auth-Token", "tok-123")
		nextReq.Header.Set("X-Api-Key", "key-123")
		nextReq.Header.Set("Proxy-Authorization", "Basic abc")
		nextReq.Header.Set("User-Agent", "Hydra-Test/1.0")
		nextReq.Header.Set("Referer", "https://files.example.com/")

		if err := SafeCheckRedirect(nextReq, []*http.Request{prevReq}); err != nil {
			t.Fatalf("unexpected SafeCheckRedirect error: %v", err)
		}
		for _, stripped := range []string{"Authorization", "Cookie", "X-Auth-Token", "X-Api-Key", "Proxy-Authorization"} {
			if got := nextReq.Header.Get(stripped); got != "" {
				t.Fatalf("expected %s to be stripped on HTTPS->HTTP downgrade, got %q", stripped, got)
			}
		}
		if nextReq.Header.Get("User-Agent") != "Hydra-Test/1.0" {
			t.Fatalf("expected non-credential User-Agent preserved, got %q", nextReq.Header.Get("User-Agent"))
		}
	})

	t.Run("same origin preserves credentials", func(t *testing.T) {
		u1, _ := url.Parse("https://files.example.com:443/a")
		u2, _ := url.Parse("https://files.example.com/b")
		if !sameHTTPOrigin(u1, u2) {
			t.Fatal("expected explicit :443 and implicit https :443 to match origin")
		}
	})
}

func TestBrowserAuthHeadersOnSameAndCrossOriginRedirects(t *testing.T) {
	payload := []byte("0123456789abcdef0123456789abcdef")

	var crossMu sync.Mutex
	var crossSeenHeaders []http.Header
	crossTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		crossMu.Lock()
		crossSeenHeaders = append(crossSeenHeaders, r.Header.Clone())
		crossMu.Unlock()

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", "32")
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/32")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:1])
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer crossTarget.Close()

	var originMu sync.Mutex
	var originFinalHeaders []http.Header
	var originServer *httptest.Server
	originServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/same-start":
			http.Redirect(w, r, originServer.URL+"/same-final", http.StatusFound)
		case "/same-final":
			originMu.Lock()
			originFinalHeaders = append(originFinalHeaders, r.Header.Clone())
			originMu.Unlock()
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "32")
			if r.Header.Get("Range") == "bytes=0-0" {
				w.Header().Set("Content-Range", "bytes 0-0/32")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(payload[:1])
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		case "/cross-start":
			http.Redirect(w, r, crossTarget.URL+"/blob.bin", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer originServer.Close()

	suppliedHeaders := map[string]string{
		"Cookie":              "session=browser-cookie",
		"Authorization":       "Bearer browser-bearer",
		"Proxy-Authorization": "Basic proxy-cred",
		"X-Api-Key":           "secret-api-key",
		"X-Auth-Token":        "secret-auth-tok",
		"X-Hydra-Token":       "local-daemon-token-must-never-leak",
		"Referer":             "https://portal.example.org/file",
		"User-Agent":          "HydraBrowserBridge/1.0",
	}

	// 1. Metadata handshake cross-origin redirect must NOT leak auth headers or X-Hydra-Token.
	metaCross, err := GetMetadataContext(context.Background(), originServer.URL+"/cross-start", suppliedHeaders)
	if err != nil {
		t.Fatalf("cross-origin GetMetadataContext failed: %v", err)
	}
	if metaCross.Size != int64(len(payload)) {
		t.Fatalf("cross-origin meta.Size = %d, want %d", metaCross.Size, len(payload))
	}

	// 2. Chunk worker cross-origin redirect must NOT leak auth headers or X-Hydra-Token.
	destPath := filepath.Join(t.TempDir(), "cross.bin")
	outFile, err := os.Create(destPath)
	if err != nil {
		t.Fatal(err)
	}
	defer outFile.Close()

	trackers := []*AdaptiveTracker{{
		Index:       0,
		StartByte:   0,
		CurrentPtr:  0,
		EndBoundary: int64(len(payload) - 1),
	}}
	var wg sync.WaitGroup
	errChan := make(chan error, 1)
	progressChan := make(chan int64, 16)
	stateChan := make(chan Chunk, 16)
	wg.Add(1)
	DownloadChunkParallel(
		context.Background(),
		originServer.URL+"/cross-start",
		0,
		trackers,
		outFile,
		&wg,
		errChan,
		progressChan,
		stateChan,
		suppliedHeaders,
		NewRateLimiter(0),
	)
	wg.Wait()
	close(errChan)
	if workerErr := <-errChan; workerErr != nil {
		t.Fatalf("DownloadChunkParallel cross-origin failed: %v", workerErr)
	}

	crossMu.Lock()
	defer crossMu.Unlock()
	if len(crossSeenHeaders) < 2 {
		t.Fatalf("expected metadata + worker requests at crossTarget, got %d", len(crossSeenHeaders))
	}
	for i, hdr := range crossSeenHeaders {
		for _, forbidden := range []string{
			"Cookie", "Authorization", "Proxy-Authorization", "X-Api-Key", "X-Auth-Token", "X-Hydra-Token",
		} {
			if got := hdr.Get(forbidden); got != "" {
				t.Fatalf("cross-origin request %d leaked header %s=%q", i, forbidden, got)
			}
		}
		if hdr.Get("User-Agent") != "HydraBrowserBridge/1.0" {
			t.Fatalf("cross-origin request %d lost User-Agent: %q", i, hdr.Get("User-Agent"))
		}
	}

	// 3. Same-origin redirect preserves Cookie/Authorization while STILL omitting X-Hydra-Token.
	metaSame, err := GetMetadataContext(context.Background(), originServer.URL+"/same-start", suppliedHeaders)
	if err != nil {
		t.Fatalf("same-origin GetMetadataContext failed: %v", err)
	}
	if metaSame.Size != int64(len(payload)) {
		t.Fatalf("same-origin meta.Size = %d, want %d", metaSame.Size, len(payload))
	}

	originMu.Lock()
	defer originMu.Unlock()
	if len(originFinalHeaders) != 1 {
		t.Fatalf("expected 1 same-origin final request, got %d", len(originFinalHeaders))
	}
	hdr := originFinalHeaders[0]
	if hdr.Get("Cookie") != "session=browser-cookie" || hdr.Get("Authorization") != "Bearer browser-bearer" {
		t.Fatalf("same-origin redirect failed to preserve auth headers: Cookie=%q Auth=%q", hdr.Get("Cookie"), hdr.Get("Authorization"))
	}
	if got := hdr.Get("X-Hydra-Token"); got != "" {
		t.Fatalf("X-Hydra-Token leaked even on same-origin target: %q", got)
	}
}

