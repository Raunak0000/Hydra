package main

import (
	"fmt"

	"github.com/Raunak0000/Hydra/pkg/downloader"
)

func main() {

	testURL := "https://storage.googleapis.com/android-ndk-releases/android-ndk-r26b-linux.zip"
	numThreads := 4

	fmt.Println("=== Hydra Core Engine Validation ===")

	// Step 1: Handshake
	metadata, err := downloader.GetMetadata(testURL)
	if err != nil {
		fmt.Println("Handshake system error:", err)
		return
	}
	fmt.Printf("[✓] Remote File Size: %d bytes\n", metadata.Size)

	// Step 2: Slice calculation
	fmt.Printf("[⚙] Calculating boundary allocation for %d threads...\n", numThreads)
	chunks := downloader.CalculateChunks(metadata.Size, numThreads)

	// Print out the structural boundaries calculated for the workers
	for _, chunk := range chunks {
		fmt.Printf("   -> Thread %d target region: Bytes %d to %d\n", chunk.Index, chunk.Start, chunk.End)
	}
}
