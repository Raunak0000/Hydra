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
		Timeout: 30 * time.Second,
	}

	maxAttempts := 10
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return HandshakeResult{}, err
		}

		req.Header.Set("Range", "bytes=0-0")

		for key, value := range headers {
			req.Header.Set(key, value)
		}
		if req.Header.Get("User-Agent") == "" {
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		}

		response, err := client.Do(req)
		if err != nil {
			lastErr = err
			sleepDuration := time.Duration(1<<attempt) * 500 * time.Millisecond
			if sleepDuration > 10*time.Second {
				sleepDuration = 10 * time.Second
			}
			time.Sleep(sleepDuration)
			continue
		}

		// Handle Rate Limiting (429) & Temporary Server Errors (500, 502, 503, 504)
		if response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode == http.StatusInternalServerError ||
			response.StatusCode == http.StatusBadGateway ||
			response.StatusCode == http.StatusServiceUnavailable ||
			response.StatusCode == http.StatusGatewayTimeout {

			retryAfter := response.Header.Get("Retry-After")
			response.Body.Close()

			sleepDuration := time.Duration(1<<attempt) * 1 * time.Second
			if seconds, parseErr := strconv.Atoi(retryAfter); parseErr == nil && seconds > 0 {
				sleepDuration = time.Duration(seconds) * time.Second
			}
			if sleepDuration > 60*time.Second {
				sleepDuration = 60 * time.Second
			}

			fmt.Printf("[⚠️] Handshake attempt %d/%d: Server returned HTTP %d. Retrying in %v...\n",
				attempt, maxAttempts, response.StatusCode, sleepDuration)

			lastErr = fmt.Errorf("server returned status code: %d", response.StatusCode)
			time.Sleep(sleepDuration)
			continue
		}

		// If server rejects Range: bytes=0-0 (400 or 416), attempt fallback GET without Range header
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			response.Body.Close()
			return getMetadataFallback(client, url, headers)
		}

		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
			response.Body.Close()
			return HandshakeResult{}, fmt.Errorf("server returned status code: %d", response.StatusCode)
		}

		defer response.Body.Close()

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

	return HandshakeResult{}, fmt.Errorf("handshake failed after %d attempts: %v", maxAttempts, lastErr)
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
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	}

	response, err := client.Do(req)
	if err != nil {
		return HandshakeResult{}, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return HandshakeResult{}, fmt.Errorf("fallback GET returned status code: %d", response.StatusCode)
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
