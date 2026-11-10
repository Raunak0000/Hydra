package downloader

import (
	"context"
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

const (
	metadataRateLimitRetries = 3
	maxMetadataRetryDelay    = 5 * time.Second
	maxErrorBodySnippet      = 512
)

func readBodySnippet(body io.Reader) string {
	if body == nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBodySnippet))
	if err != nil && len(raw) == 0 {
		return ""
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

func isHardRateLimitMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "id busy") ||
		strings.Contains(lower, "generate a new") ||
		strings.Contains(lower, "link expired")
}

func formatRateLimitError(bodySnippet string) error {
	if bodySnippet != "" {
		return fmt.Errorf("HTTP 429: %s", bodySnippet)
	}
	return fmt.Errorf(
		"HTTP 429: Host rate limit exceeded; provide the browser Cookie header in download options if the link only works in your browser",
	)
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

func doMetadataRequest(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, string, error) {
	for attempt := 0; ; attempt++ {
		response, err := client.Do(req.Clone(ctx))
		if err != nil || response.StatusCode != http.StatusTooManyRequests {
			return response, "", err
		}

		snippet := readBodySnippet(response.Body)
		retryAfter := parseRetryAfter(response.Header.Get("Retry-After"))
		if attempt >= metadataRateLimitRetries ||
			retryAfter > maxMetadataRetryDelay ||
			isHardRateLimitMessage(snippet) {
			return response, snippet, nil
		}

		response.Body.Close()

		if retryAfter <= 0 {
			retryAfter = time.Duration(attempt+1) * time.Second
		}

		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, "", ctx.Err()
		case <-timer.C:
		}
	}
}

func setBrowserHeaders(req *http.Request) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	}
	if req.Header.Get("Sec-Fetch-Dest") == "" {
		req.Header.Set("Sec-Fetch-Dest", "document")
	}
	if req.Header.Get("Sec-Fetch-Mode") == "" {
		req.Header.Set("Sec-Fetch-Mode", "navigate")
	}
	if req.Header.Get("Sec-Fetch-Site") == "" {
		req.Header.Set("Sec-Fetch-Site", "none")
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}

	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}

	delay := time.Until(when)
	if delay < 0 {
		return 0
	}

	return delay
}

func applyMetadataHeaders(req *http.Request, headers map[string]string) {
	req.Header.Set("Connection", "keep-alive")
	setBrowserHeaders(req)

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
}

func GetMetadata(url string, headers map[string]string) (HandshakeResult, error) {
	return GetMetadataContext(context.Background(), url, headers)
}

func GetMetadataContext(ctx context.Context, url string, headers map[string]string) (HandshakeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	client := newMetadataClient()

	// First try a small ranged GET.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf("create metadata request: %w", err)
	}

	req.Header.Set("Range", "bytes=0-0")
	applyMetadataHeaders(req, headers)

	response, rateLimitSnippet, err := doMetadataRequest(ctx, client, req)
	if err != nil {
		if ctx.Err() != nil {
			return HandshakeResult{}, ctx.Err()
		}
		// Some servers behave badly with Range requests.
		// Try a normal request before giving up.
		return getMetadataFallback(ctx, client, url, headers, err)
	}

	defer response.Body.Close()

	if response.StatusCode == http.StatusTooManyRequests {
		return HandshakeResult{}, formatRateLimitError(rateLimitSnippet)
	}

	// Read a tiny amount so the connection is properly consumed.
	_, _ = io.CopyN(io.Discard, response.Body, 1)

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

		return getMetadataFallback(ctx, client, url, headers, nil)
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

		if response.StatusCode == http.StatusNotFound {
			return HandshakeResult{}, fmt.Errorf(
				"server returned HTTP 404: download link expired - regenerate link",
			)
		}

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
	ctx context.Context,
	client *http.Client,
	url string,
	headers map[string]string,
	originalErr error,
) (HandshakeResult, error) {

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return HandshakeResult{}, fmt.Errorf(
			"create fallback request: %w",
			err,
		)
	}

	applyMetadataHeaders(req, headers)

	response, rateLimitSnippet, err := doMetadataRequest(ctx, client, req)
	if err != nil {
		if ctx.Err() != nil {
			return HandshakeResult{}, ctx.Err()
		}
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
		return HandshakeResult{}, formatRateLimitError(rateLimitSnippet)
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
