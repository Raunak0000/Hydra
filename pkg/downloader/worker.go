package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// DownloadChunkParallel connects network sockets and tracks streams atomically
func DownloadChunkParallel(url string, chunk Chunk, finalFile *os.File, tracker *ProgressTracker, wg *sync.WaitGroup) {
	defer wg.Done()

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Printf("\n[X] Thread %d init failed: %v\n", chunk.Index, err)
		return
	}

	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
	req.Header.Set("Range", rangeHeader)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("\n[X] Thread %d network link dropped: %v\n", chunk.Index, err)
		return
	}
	defer resp.Body.Close()

	buffer := make([]byte, 32*1024) // 32KB streaming chunks
	writeOffset := chunk.Start

	for {
		bytesRead, readErr := resp.Body.Read(buffer)
		if bytesRead > 0 {
			_, writeErr := finalFile.WriteAt(buffer[:bytesRead], writeOffset)
			if writeErr != nil {
				fmt.Printf("\n[X] Thread %d disk write exception: %v\n", chunk.Index, writeErr)
				return
			}
			writeOffset += int64(bytesRead)

			// REPORT metrics to the atomic counter instantly
			tracker.Increment(bytesRead)
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			fmt.Printf("\n[X] Thread %d connection terminated: %v\n", chunk.Index, readErr)
			return
		}
	}
}
