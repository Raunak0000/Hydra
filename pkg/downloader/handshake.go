package downloader

import (
	"fmt"
	"io"
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

func newMetadataClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()

	// Be more compatible with public file servers.
	transport.ForceAttemptHTTP2 = true
	transport.DisableCompression = true

	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,

		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}
}

func GetMetadata(url string, headers map[string]string) (HandshakeResult, error) {
	client := newMetadataClient()

	// First try a small ranged GET.
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("create metadata request: %w", err)
	}

	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	if req.Header.Get("User-Agent") == "" {
		req.Header.Set(
			"User-Agent",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "+
				"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
		)
	}

	response, err := client.Do(req)
	if err != nil {
		// Some servers behave badly with Range requests.
		// Try a normal request before giving up.
		return getMetadataFallback(client, url, headers, err)
	}

	defer response.Body.Close()

	// Read a tiny amount so the connection is properly consumed.
	_, _ = io.CopyN(io.Discard, response.Body, 1)

	if response.StatusCode == http.StatusTooManyRequests {
		return HandshakeResult{}, fmt.Errorf(
			"HTTP 429: Host rate limit exceeded",
		)
	}

	if response.StatusCode == http.StatusForbidden ||
		response.StatusCode == http.StatusUnauthorized ||
		response.StatusCode == http.StatusGone {

		return HandshakeResult{}, fmt.Errorf(
			"HTTP %d: Access denied or token expired",
			response.StatusCode,
		)
	}

	if response.StatusCode >= 500 {
		return HandshakeResult{}, fmt.Errorf(
			"HTTP %d: Remote server error",
			response.StatusCode,
		)
	}

	// Server rejected the Range request.
	if response.StatusCode == http.StatusBadRequest ||
		response.StatusCode == http.StatusRequestedRangeNotSatisfiable {

		return getMetadataFallback(client, url, headers, nil)
	}

	contentType := strings.ToLower(response.Header.Get("Content-Type"))

	if response.StatusCode == http.StatusOK &&
		strings.Contains(contentType, "text/html") {

		return HandshakeResult{}, fmt.Errorf(
			"host returned HTML webpage instead of media stream",
		)
	}

	if response.StatusCode != http.StatusOK &&
		response.StatusCode != http.StatusPartialContent {

		return HandshakeResult{}, fmt.Errorf(
			"server returned status: %d",
			response.StatusCode,
		)
	}

	var trueSize int64

	// Best source: Content-Range.
	contentRange := response.Header.Get("Content-Range")

	if contentRange != "" {
		if idx := strings.LastIndex(contentRange, "/"); idx != -1 {
			totalStr := strings.TrimSpace(contentRange[idx+1:])

			if parsed, parseErr := strconv.ParseInt(totalStr, 10, 64); parseErr == nil &&
				parsed > 0 {

				trueSize = parsed
			}
		}
	}

	// Some download servers expose their own size header.
	if trueSize <= 0 {
		if xSize := response.Header.Get("X-File-Size"); xSize != "" {
			if parsed, parseErr := strconv.ParseInt(xSize, 10, 64); parseErr == nil &&
				parsed > 0 {

				trueSize = parsed
			}
		}
	}

	// Last resort.
	if trueSize <= 0 && response.ContentLength > 0 {
		trueSize = response.ContentLength
	}

	acceptsBytes :=
		response.StatusCode == http.StatusPartialContent ||
			response.Header.Get("Accept-Ranges") == "bytes" ||
			contentRange != ""

	return HandshakeResult{
		Size:         trueSize,
		AcceptRanges: acceptsBytes,
		FinalURL:     response.Request.URL.String(),
	}, nil
}

func getMetadataFallback(
	client *http.Client,
	url string,
	headers map[string]string,
	originalErr error,
) (HandshakeResult, error) {

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf(
			"create fallback request: %w",
			err,
		)
	}

	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")

	for key, value := range headers {
		req.Header.Set(key, value)
	}

	if req.Header.Get("User-Agent") == "" {
		req.Header.Set(
			"User-Agent",
			"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "+
				"(KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
		)
	}

	response, err := client.Do(req)
	if err != nil {
		if originalErr != nil {
			return HandshakeResult{}, fmt.Errorf(
				"network connection error: %w (fallback also failed: %v)",
				originalErr,
				err,
			)
		}

		return HandshakeResult{}, fmt.Errorf(
			"network connection error: %w",
			err,
		)
	}

	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests {
		return HandshakeResult{}, fmt.Errorf(
			"HTTP 429: Host rate limit exceeded",
		)
	}

	if response.StatusCode == http.StatusForbidden ||
		response.StatusCode == http.StatusUnauthorized ||
		response.StatusCode == http.StatusGone {

		return HandshakeResult{}, fmt.Errorf(
			"HTTP %d: Access denied or token expired",
			response.StatusCode,
		)
	}

	if response.StatusCode != http.StatusOK {
		return HandshakeResult{}, fmt.Errorf(
			"fallback returned HTTP %d",
			response.StatusCode,
		)
	}

	contentType := strings.ToLower(response.Header.Get("Content-Type"))

	if strings.Contains(contentType, "text/html") {
		return HandshakeResult{}, fmt.Errorf(
			"fallback returned HTML instead of a file",
		)
	}

	trueSize := response.ContentLength

	if xSize := response.Header.Get("X-File-Size"); xSize != "" {
		if parsed, parseErr := strconv.ParseInt(xSize, 10, 64); parseErr == nil &&
			parsed > 0 {

			trueSize = parsed
		}
	}

	return HandshakeResult{
		Size:         trueSize,
		AcceptRanges: response.Header.Get("Accept-Ranges") == "bytes",
		FinalURL:     response.Request.URL.String(),
	}, nil
}
