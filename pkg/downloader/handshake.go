package downloader

import (
	"fmt"
	"net/http"
)

type HandshakeResult struct {
	Size         int64
	AcceptRanges bool
	FinalURL     string // Add this so we can pass the real, resolved URL to the workers
}

func GetMetadata(url string, headers map[string]string) (HandshakeResult, error) { // 👈 UPDATE SIGNATURE
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { // cite: file(2).txt
			return nil // cite: file(2).txt
		},
	}

	req, err := http.NewRequest("GET", url, nil) // cite: file(2).txt
	if err != nil {                              // cite: file(2).txt
		return HandshakeResult{}, err // cite: file(2).txt
	}

	req.Header.Set("Range", "bytes=0-0") // cite: file(2).txt

	// 🚨 NEW: Inject browser authentication headers dynamically into the handshake frame
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	// If user-agent wasn't explicitly captured by extension, apply default fallback safeguard
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36") // cite: file(2).txt
	}

	response, err := client.Do(req) // cite: file(2).txt
	// ... remainder stays identical to file(2).txt completely ...
	if err != nil {
		return HandshakeResult{}, err
	}
	defer response.Body.Close()

	// Handle standard HTTP failure codes
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return HandshakeResult{}, fmt.Errorf("server returned status code: %d", response.StatusCode)
	}

	// Look for the standard Content-Range header to parse the TRUE file size
	var trueSize int64
	contentRange := response.Header.Get("Content-Range")
	if contentRange != "" {
		// Content-Range looks like "bytes 0-0/143284902" -> parse the number after the slash
		_, err := fmt.Sscanf(contentRange, "bytes 0-0/%d", &trueSize)
		if err != nil {
			// Fallback to regular ContentLength if parsing fails
			trueSize = response.ContentLength
		}
	} else {
		trueSize = response.ContentLength
	}

	acceptsBytes := response.Header.Get("Accept-Ranges") == "bytes" || contentRange != ""

	return HandshakeResult{
		Size:         trueSize,
		AcceptRanges: acceptsBytes,
		FinalURL:     response.Request.URL.String(), // Lock down the final destination link
	}, nil
}
