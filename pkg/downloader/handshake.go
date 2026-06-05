package downloader

import (
	"net/http"
)

type HandshakeResult struct {
	Size         int64
	AcceptRanges bool
}

func GetMetadata(url string) (HandshakeResult, error) {
	response, err := http.Head(url)
	if err != nil {
		return HandshakeResult{}, err
	}
	defer response.Body.Close()

	size := response.ContentLength
	acceptsBytes := response.Header.Get("Accept-Ranges") == "bytes"

	return HandshakeResult{
		Size:         size,
		AcceptRanges: acceptsBytes,
	}, nil
}
