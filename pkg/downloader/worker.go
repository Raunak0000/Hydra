package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// DownloadChunk handles a single segment network stream
func DownloadChunk(url string, chunk Chunk, wg *sync.WaitGroup) {
	// Crucial: Decrement the WaitGroup counter by 1 when this worker function exits
	defer wg.Done()

	fmt.Printf("[+] Thread %d started: Requesting bytes %d to %d...\n", chunk.Index, chunk.Start, chunk.End)

	// 1. Create an isolated HTTP request client
	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Printf("[X] Thread %d failed to create request: %v\n", chunk.Index, err)
		return
	}

	// 2. HTTP Magic: Inject the Range Header to pull ONLY this chunk's byte window
	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
	req.Header.Set("Range", rangeHeader)

	// 3. Execute the network connection stream
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[X] Thread %d network error: %v\n", chunk.Index, err)
		return
	}
	defer resp.Body.Close()

	// 4. Create a temporary file on your SSD to hold this chunk's incoming data bytes
	partFileName := fmt.Sprintf("part_%d.tmp", chunk.Index)
	file, err := os.Create(partFileName)
	if err != nil {
		fmt.Printf("[X] Thread %d disk write error: %v\n", chunk.Index, err)
		return
	}
	defer file.Close()

	// 5. Stream the incoming network data packets straight into your SSD part file
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		fmt.Printf("[X] Thread %d streaming failed: %v\n", chunk.Index, err)
		return
	}

	fmt.Printf("[✓] Thread %d finished downloading segment!\n", chunk.Index)
}
