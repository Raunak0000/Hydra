package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// DownloadChunkParallel writes directly into a specific offset of a single shared file
func DownloadChunkParallel(url string, chunk Chunk, finalFile *os.File, wg *sync.WaitGroup) {
	defer wg.Done()

	fmt.Printf("[+] Thread %d started: Requesting bytes %d to %d...\n", chunk.Index, chunk.Start, chunk.End)

	client := &http.Client{}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Printf("[X] Thread %d failed request initialization: %v\n", chunk.Index, err)
		return
	}

	// Inject Range header for segment capture
	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.Start, chunk.End)
	req.Header.Set("Range", rangeHeader)

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[X] Thread %d network error: %v\n", chunk.Index, err)
		return
	}
	defer resp.Body.Close()

	// Buffer to stream network chunks sequentially before writing to disk
	buffer := make([]byte, 32*1024) // 32KB processing window
	writeOffset := chunk.Start

	for {
		bytesRead, readErr := resp.Body.Read(buffer)
		if bytesRead > 0 {
			// WriteAt invokes the low-level Linux 'pwrite' system call.
			// It safely writes data at the exact 'writeOffset' without shifting the file pointer.
			_, writeErr := finalFile.WriteAt(buffer[:bytesRead], writeOffset)
			if writeErr != nil {
				fmt.Printf("[X] Thread %d parallel write crash: %v\n", chunk.Index, writeErr)
				return
			}
			// Shift our local tracking offset forward by the number of bytes written
			writeOffset += int64(bytesRead)
		}

		if readErr == io.EOF {
			break // Streaming successfully concluded
		}
		if readErr != nil {
			fmt.Printf("[X] Thread %d network streaming dropped: %v\n", chunk.Index, readErr)
			return
		}
	}

	fmt.Printf("[✓] Thread %d completed parallel write layout!\n", chunk.Index)
}
