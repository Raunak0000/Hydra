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

func GetMetadata(url string) (HandshakeResult, error) {
	// Create a custom client that handles redirects properly
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Returning nil tells the Go client to follow the redirect automatically
			return nil
		},
	}

	// Use GET instead of HEAD because some CDN servers (like Discord/Cloudflare)
	// strip content-length headers or drop requests if they see a HEAD method.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return HandshakeResult{}, err
	}

	// Request just the first byte so we don't accidentally download the whole file during the handshake
	req.Header.Set("Range", "bytes=0-0")

	response, err := client.Do(req)
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
