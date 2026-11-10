package storage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DesktopNotification describes a native desktop notification emitted by Hydra.
type DesktopNotification struct {
	Event    string
	Title    string
	Message  string
	Urgency  string
	Category string
	Icon     string
}

var (
	notifyMu           sync.RWMutex
	notificationHook   func(DesktopNotification)
	lookPathFn         = exec.LookPath
	execCommandContext = exec.CommandContext
)

// SetNotificationHandler registers an optional test/observer callback that
// intercepts desktop notifications instead of spawning external commands.
// Pass nil to restore native notification delivery.
func SetNotificationHandler(fn func(DesktopNotification)) {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	notificationHook = fn
}

// resolveNotificationIcon selects the Hydra application icon when installed,
// falling back to the standard XDG icon name "hydra".
func resolveNotificationIcon() string {
	candidates := []string{
		filepath.Join(os.Getenv("XDG_DATA_HOME"), "icons", "hicolor", "scalable", "apps", "hydra.svg"),
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "icons", "hicolor", "scalable", "apps", "hydra.svg"))
	}
	candidates = append(candidates, "/usr/share/icons/hicolor/scalable/apps/hydra.svg")

	for _, candidate := range candidates {
		if candidate == "" || strings.HasPrefix(candidate, string(filepath.Separator)+"icons") {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return "hydra"
}

func notificationsDisabledByEnv() bool {
	val := strings.TrimSpace(strings.ToLower(os.Getenv("HYDRA_DISABLE_NOTIFICATIONS")))
	return val == "1" || val == "true" || val == "yes"
}

func dispatchNotification(n DesktopNotification) {
	if n.Urgency == "" {
		n.Urgency = "normal"
	}
	if n.Icon == "" {
		n.Icon = resolveNotificationIcon()
	}

	notifyMu.RLock()
	hook := notificationHook
	notifyMu.RUnlock()

	if hook != nil {
		hook(n)
		return
	}

	if notificationsDisabledByEnv() {
		return
	}

	notifyMu.RLock()
	lookPath := lookPathFn
	cmdFactory := execCommandContext
	notifyMu.RUnlock()

	go func(notif DesktopNotification) {
		_ = sendNativeNotification(notif, lookPath, cmdFactory)
	}(n)
}

func sendNativeNotification(
	n DesktopNotification,
	lookPath func(string) (string, error),
	cmdFactory func(context.Context, string, ...string) *exec.Cmd,
) error {
	if n.Urgency == "" {
		n.Urgency = "normal"
	}
	if n.Icon == "" {
		n.Icon = "hydra"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if notifySendPath, err := lookPath("notify-send"); err == nil && notifySendPath != "" {
		args := []string{
			"-a", "Hydra Downloader",
			"-i", n.Icon,
			"-u", n.Urgency,
			"-h", "string:desktop-entry:hydra",
		}
		if n.Category != "" {
			args = append(args, "-c", n.Category)
		}
		args = append(args, n.Title, n.Message)
		return cmdFactory(ctx, notifySendPath, args...).Run()
	}

	if gdbusPath, err := lookPath("gdbus"); err == nil && gdbusPath != "" {
		urgencyByte := byte(1)
		switch n.Urgency {
		case "low":
			urgencyByte = 0
		case "critical":
			urgencyByte = 2
		}
		category := n.Category
		if category == "" {
			category = "transfer"
		}
		hints := fmt.Sprintf(
			"{'urgency': <byte %d>, 'desktop-entry': <'hydra'>, 'category': <'%s'>}",
			urgencyByte,
			escapeGVariantString(category),
		)
		return cmdFactory(ctx, gdbusPath,
			"call",
			"--session",
			"--dest", "org.freedesktop.Notifications",
			"--object-path", "/org/freedesktop/Notifications",
			"--method", "org.freedesktop.Notifications.Notify",
			"Hydra Downloader",
			"0",
			n.Icon,
			n.Title,
			n.Message,
			"[]",
			hints,
			"-1",
		).Run()
	}

	return nil
}

func escapeGVariantString(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// SendDesktopNotification triggers a native Linux notification via notify-send
// (or org.freedesktop.Notifications over D-Bus if notify-send is unavailable).
func SendDesktopNotification(title, message, urgency string) {
	dispatchNotification(DesktopNotification{
		Event:   "custom",
		Title:   title,
		Message: message,
		Urgency: urgency,
	})
}

// NotifyDownloadQueued emits a native notification when a job enters the queue.
func NotifyDownloadQueued(filename, savePath string) {
	if strings.TrimSpace(filename) == "" {
		if savePath != "" && savePath != "PENDING" && savePath != "DEFAULT" {
			filename = filepath.Base(savePath)
		} else {
			filename = "Download"
		}
	}
	msg := filename
	if savePath != "" && savePath != "PENDING" && savePath != "DEFAULT" {
		msg = fmt.Sprintf("%s\nDestination: %s", filename, savePath)
	}
	dispatchNotification(DesktopNotification{
		Event:    "queued",
		Title:    "Download Queued",
		Message:  msg,
		Urgency:  "low",
		Category: "transfer",
	})
}

// NotifyBatchQueued emits a summary notification when multiple downloads are queued together.
func NotifyBatchQueued(count int, destinationDir string) {
	if count <= 0 {
		return
	}
	msg := fmt.Sprintf("%d downloads added to queue", count)
	if destinationDir != "" {
		msg = fmt.Sprintf("%s\nDestination: %s", msg, destinationDir)
	}
	dispatchNotification(DesktopNotification{
		Event:    "queued",
		Title:    "Batch Downloads Queued",
		Message:  msg,
		Urgency:  "low",
		Category: "transfer",
	})
}

// NotifyDownloadScheduled emits a native notification when a download is scheduled for later.
func NotifyDownloadScheduled(filename, scheduledFor string) {
	if strings.TrimSpace(filename) == "" {
		filename = "Download"
	}
	msg := filename
	if scheduledFor != "" {
		msg = fmt.Sprintf("%s\nScheduled for: %s", filename, scheduledFor)
	}
	dispatchNotification(DesktopNotification{
		Event:    "queued",
		Title:    "Download Scheduled",
		Message:  msg,
		Urgency:  "low",
		Category: "transfer",
	})
}

// NotifyDownloadComplete emits a native notification when a download finishes successfully.
func NotifyDownloadComplete(filename, savePath string) {
	dispatchNotification(DesktopNotification{
		Event:    "completed",
		Title:    "Download Finished ✅",
		Message:  fmt.Sprintf("%s\nSaved to: %s", filename, savePath),
		Urgency:  "normal",
		Category: "transfer.complete",
	})
}

// NotifyDownloadFailed emits a critical native notification when a download fails permanently.
func NotifyDownloadFailed(filename, reason string) {
	if strings.TrimSpace(filename) == "" {
		filename = "Download"
	}
	dispatchNotification(DesktopNotification{
		Event:    "failed",
		Title:    "Download Failed ❌",
		Message:  fmt.Sprintf("%s\nReason: %s", filename, reason),
		Urgency:  "critical",
		Category: "transfer.error",
	})
}

// NotifyChecksumFailed emits a critical notification on integrity mismatch.
func NotifyChecksumFailed(filename string) {
	if strings.TrimSpace(filename) == "" {
		filename = "Download"
	}
	dispatchNotification(DesktopNotification{
		Event:    "failed",
		Title:    "Checksum Mismatch ⚠️",
		Message:  fmt.Sprintf("%s failed integrity check!", filename),
		Urgency:  "critical",
		Category: "transfer.error",
	})
}

// NotifyPendingPath emits a notification when user confirmation of save location is needed.
func NotifyPendingPath(filename string) {
	if strings.TrimSpace(filename) == "" {
		filename = "Download"
	}
	dispatchNotification(DesktopNotification{
		Event:    "pending_path",
		Title:    "Action Required ⚙️",
		Message:  fmt.Sprintf("Select save destination for: %s", filename),
		Urgency:  "normal",
		Category: "transfer",
	})
}
