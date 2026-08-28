package storage

import (
	"fmt"
	"os/exec"
)

// SendDesktopNotification triggers a native Linux notification via notify-send
func SendDesktopNotification(title, message, urgency string) {
	// Urgency can be: low, normal, critical
	if urgency == "" {
		urgency = "normal"
	}

	go func() {
		cmd := exec.Command("notify-send",
			"-a", "Hydra Downloader",
			"-i", "download",
			"-u", urgency,
			title,
			message,
		)
		_ = cmd.Run()
	}()
}

func NotifyDownloadComplete(filename, savePath string) {
	SendDesktopNotification(
		"Download Finished ✅",
		fmt.Sprintf("%s\nSaved to: %s", filename, savePath),
		"normal",
	)
}

func NotifyDownloadFailed(filename, reason string) {
	SendDesktopNotification(
		"Download Failed ❌",
		fmt.Sprintf("%s\nReason: %s", filename, reason),
		"critical",
	)
}

func NotifyChecksumFailed(filename string) {
	SendDesktopNotification(
		"Checksum Mismatch ⚠️",
		fmt.Sprintf("%s failed integrity check!", filename),
		"critical",
	)
}

func NotifyPendingPath(filename string) {
	SendDesktopNotification(
		"Action Required ⚙️",
		fmt.Sprintf("Select save destination for: %s", filename),
		"normal",
	)
}
