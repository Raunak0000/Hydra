package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// We completely removed *ProgressTracker and added progressChan chan int64
func DownloadChunkParallel(url string, chunk Chunk, finalFile *os.File, wg *sync.WaitGroup, errChan chan error, progressChan chan int64) {
	defer wg.Done()

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		errChan <- fmt.Errorf("Thread %d init failed: %v", chunk.Index, err)
		return
	}

	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
	req.Header.Set("Range", rangeHeader)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		errChan <- fmt.Errorf("Thread %d network link dropped: %v", chunk.Index, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		errChan <- fmt.Errorf("Thread %d server exception status: %d", chunk.Index, resp.StatusCode)
		return
	}

	buffer := make([]byte, 32*1024) // 32KB processing buffer blocks
	writeOffset := chunk.Start

	for {
		bytesRead, readErr := resp.Body.Read(buffer)
		if bytesRead > 0 {
			_, writeErr := finalFile.WriteAt(buffer[:bytesRead], writeOffset)
			if writeErr != nil {
				errChan <- fmt.Errorf("Thread %d disk write exception: %v", chunk.Index, writeErr)
				return
			}
			writeOffset += int64(bytesRead)

			// Send the raw number of bytes read straight into the progress channel!
			progressChan <- int64(bytesRead)
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			errChan <- fmt.Errorf("Thread %d stream terminated: %v", chunk.Index, readErr)
			return
		}
	}
}
