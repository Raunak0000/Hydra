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
func (pt *ProgressTracker) Increment(bytes int) {
	atomic.AddInt64(&pt.Downloaded, int64(bytes))
}

// FormatBytes helper converts raw bytes into a human-readable string
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// Change the signature so it doesn't import or depend on storage directly:
func (pt *ProgressTracker) WatchWithUI(stopChan chan bool, onTick func(progress float64, downloaded string)) {
	ticker := time.NewTicker(pt.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopChan:
			currentDownloaded := atomic.LoadInt64(&pt.Downloaded)
			onTick(100.0, FormatBytes(currentDownloaded))
			return
		case <-ticker.C:
			currentDownloaded := atomic.LoadInt64(&pt.Downloaded)
			if pt.TotalBytes <= 0 {
				continue
			}

			percentage := (float64(currentDownloaded) / float64(pt.TotalBytes)) * 100
			if percentage > 100 {
				percentage = 100
			}

			// 1. Keep your visual terminal bar cooking
			barWidth := 30
			filledLength := int((percentage / 100) * float64(barWidth))
			if filledLength > barWidth {
				filledLength = barWidth
			}
			bar := strings.Repeat("█", filledLength) + strings.Repeat("-", barWidth-filledLength)
			fmt.Printf("\r[⚡] Hydra Downloading: [%s] %.2f%% (%d/%d bytes)", bar, percentage, currentDownloaded, pt.TotalBytes)

			// 2. Fire the callback function to pass data back out across packages safely!
			onTick(percentage, FormatBytes(currentDownloaded))
		}
	}
}
