package downloader

import (
	"fmt"
	"io"
	"os"
)

// StitchChunks reads the temporary .tmp fragments and merges them sequentially
func StitchChunks(chunks []Chunk, destinationPath string) error {
	fmt.Printf("[⚙] Stitcher initialized: Assembly path targeting %s...\n", destinationPath)

	// 1. Create or truncate the final clean file on your SSD
	// os.O_CREATE: Makes it if missing | os.O_WRONLY: Write-only mode | os.O_APPEND: Tail-end focus
	finalFile, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return fmt.Errorf("failed to initialize final file layout: %v", err)
	}
	defer finalFile.Close()

	// 2. Loop through chunks in strict sequential order (0 -> 1 -> 2 -> 3)
	for _, chunk := range chunks {
		partFileName := fmt.Sprintf("part_%d.tmp", chunk.Index)
		fmt.Printf("   -> Merging fragment payload: %s\n", partFileName)

		// Open the individual target temporary fragment
		partFile, err := os.Open(partFileName)
		if err != nil {
			return fmt.Errorf("failed to open fragment block %s: %v", partFileName, err)
		}

		// Stream bytes from the individual fragment straight to the tail end of our output file
		_, err = io.Copy(finalFile, partFile)
		partFile.Close() // Close immediately after reading to avoid holding stale system descriptors
		if err != nil {
			return fmt.Errorf("io streaming assembly failed on fragment %s: %v", partFileName, err)
		}

		// 3. Clear storage space: Delete the temporary file from disk immediately after stitching
		err = os.Remove(partFileName)
		if err != nil {
			fmt.Printf("[!] Warning: Could not remove temporary artifact %s: %v\n", partFileName, err)
		}
	}

	return nil
}
