package downloader

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ProgressTracker manages thread-safe byte collection metrics
type ProgressTracker struct {
	TotalBytes     int64
	Downloaded     int64 // Must be modified ONLY via sync/atomic
	UpdateInterval time.Duration
}

// NewProgressTracker initializes the metric monitor
func NewProgressTracker(totalSize int64) *ProgressTracker {
	return &ProgressTracker{
		TotalBytes:     totalSize,
		UpdateInterval: 100 * time.Millisecond,
	}
}

// Increment adds bytes downloaded to our counter safely across threads
func (p *ProgressTracker) Increment(bytes int) {
	atomic.AddInt64(&p.Downloaded, int64(bytes))
}

// Watch Rendering Loop paints the live visual layout onto the terminal
func (p *ProgressTracker) Watch(stopChan chan bool) {
	ticker := time.NewTicker(p.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Read the counter atomically to avoid data races
			currentDownloaded := atomic.LoadInt64(&p.Downloaded)
			if currentDownloaded > p.TotalBytes {
				currentDownloaded = p.TotalBytes
			}

			// Prevent division by zero if metadata handshake was small
			if p.TotalBytes <= 0 {
				continue
			}

			// Calculate progress metrics
			percentage := float64(currentDownloaded) / float64(p.TotalBytes) * 100
			if percentage > 100 {
				percentage = 100
			}

			// Construct a 30-character wide visual terminal fill-bar
			barWidth := 30
			filledLength := int((percentage / 100) * float64(barWidth))
			if filledLength > barWidth {
				filledLength = barWidth
			}

			bar := strings.Repeat("█", filledLength) + strings.Repeat("-", barWidth-filledLength)

			// \r moves the cursor back to the start of the current terminal line.
			// This overwrites the previous tick instead of spamming new lines!
			fmt.Printf("\r[⚡] Hydra Downloading: [%s] %.2f%% (%d/%d bytes)",
				bar, percentage, currentDownloaded, p.TotalBytes)

		case <-stopChan:
			// Simply print a newline to lock the last painted bar position, then exit
			fmt.Println()
			return
		}
	}
}
