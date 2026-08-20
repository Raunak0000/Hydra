package downloader

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HandshakeResult struct {
	Size         int64
	AcceptRanges bool
	FinalURL     string
}

func GetMetadata(url string, headers map[string]string) (HandshakeResult, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return HandshakeResult{}, err
	}

	req.Header.Set("Range", "bytes=0-0")

	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	}

	response, err := client.Do(req)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("network connection error: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests {
		bodyBuf := make([]byte, 256)
		n, _ := response.Body.Read(bodyBuf)
		msg := strings.TrimSpace(string(bodyBuf[:n]))
		if msg != "" {
			return HandshakeResult{}, fmt.Errorf("HTTP 429: %s", msg)
		}
		return HandshakeResult{}, fmt.Errorf("HTTP 429: Host rate limit exceeded")
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusGone {
		return HandshakeResult{}, fmt.Errorf("HTTP %d: Access denied or token expired", response.StatusCode)
	}
	if response.StatusCode >= 500 {
		return HandshakeResult{}, fmt.Errorf("HTTP %d: Remote server error", response.StatusCode)
	}

	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return getMetadataFallback(client, url, headers)
	}

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if response.StatusCode == http.StatusOK && strings.Contains(contentType, "text/html") {
		return HandshakeResult{}, fmt.Errorf("host returned HTML webpage instead of media stream (Cloudflare challenge or expired token)")
	}

	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return HandshakeResult{}, fmt.Errorf("server returned status: %d", response.StatusCode)
	}

	var trueSize int64
	contentRange := response.Header.Get("Content-Range")
	if contentRange != "" {
		if idx := strings.Index(contentRange, "/"); idx != -1 {
			totalStr := strings.TrimSpace(contentRange[idx+1:])
			if parsed, err := strconv.ParseInt(totalStr, 10, 64); err == nil && parsed > 0 {
				trueSize = parsed
			}
		}
	}

	if trueSize <= 0 {
		if xSize := response.Header.Get("X-File-Size"); xSize != "" {
			if parsed, err := strconv.ParseInt(xSize, 10, 64); err == nil {
				trueSize = parsed
			}
		}
	}

	if trueSize <= 0 {
		trueSize = response.ContentLength
	}

	acceptsBytes := response.Header.Get("Accept-Ranges") == "bytes" || contentRange != ""

	return HandshakeResult{
		Size:         trueSize,
		AcceptRanges: acceptsBytes,
		FinalURL:     response.Request.URL.String(),
	}, nil
}

func getMetadataFallback(client *http.Client, url string, headers map[string]string) (HandshakeResult, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return HandshakeResult{}, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	}

	response, err := client.Do(req)
	if err != nil {
		return HandshakeResult{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return HandshakeResult{}, fmt.Errorf("fallback returned HTTP %d", response.StatusCode)
	}

	trueSize := response.ContentLength
	if xSize := response.Header.Get("X-File-Size"); xSize != "" {
		if parsed, err := strconv.ParseInt(xSize, 10, 64); err == nil {
			trueSize = parsed
		}
	}

	return HandshakeResult{
		Size:         trueSize,
		AcceptRanges: false,
		FinalURL:     response.Request.URL.String(),
	}, nil
}
