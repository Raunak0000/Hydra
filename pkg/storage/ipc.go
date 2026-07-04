package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Raunak0000/Hydra/pkg/models"
)

const SocketPath = "/tmp/hydra.sock" // cite: 60

// StartIPCServer listens on a UNIX domain socket and handles incoming control strings
func StartIPCServer(downloadTrigger func(string, string, string)) { // cite: 60
	// 1. Unlink stale socket ghosts securely on startup
	if _, err := os.Stat(SocketPath); err == nil { // cite: 60
		_ = os.Remove(SocketPath) // cite: 60
	}

	listener, err := net.Listen("unix", SocketPath) // cite: 60
	if err != nil {
		fmt.Printf("[X] IPC Server failed to bind to socket: %v\n", err) // cite: 60
		return
	}
	defer listener.Close() // cite: 60

	// 2. Lock down filesystem access boundaries to owner-only read/write
	_ = os.Chmod(SocketPath, 0600)

	fmt.Printf("[⚙] Hydra IPC Server listening silently on %s\n", SocketPath) // cite: 60

	for {
		conn, err := listener.Accept() // cite: 61
		if err != nil {
			continue // cite: 61
		}

		go func(c net.Conn) {
			defer c.Close() // cite: 61

			reader := bufio.NewReader(c)            // cite: 61
			message, err := reader.ReadString('\n') // cite: 61
			if err != nil {
				return // cite: 61
			}

			commandText := strings.TrimSpace(message) // cite: 208

			// ── 📊 HANDLE STATUS COMMAND ──
			if commandText == "STATUS" {
				jobs := GetStore().GetAllJobs()     // Thread-safe fetch from core storage map cache [cite: 83]
				_ = json.NewEncoder(c).Encode(jobs) // Stream raw JSON bytes directly down the socket line
				return
			}

			// ── ⏸ HANDLE PAUSE COMMAND ──
			if strings.HasPrefix(commandText, "PAUSE|") {
				jobID := strings.TrimPrefix(commandText, "PAUSE|")

				if GlobalCancelMutex != nil && GlobalCancelMap != nil {
					GlobalCancelMutex.Lock()                              // cite: 74
					if cancel, exists := GlobalCancelMap[jobID]; exists { // cite: 74
						cancel() // Instantly abort matching thread pipelines safely
					}
					GlobalCancelMutex.Unlock()
				}

				store := GetStore()
				store.UpdateStatus(jobID, "PAUSED")

				_, _ = c.Write([]byte("SUCCESS|Job successfully paused.\n"))
				return
			}

			// ── ▶ HANDLE RESUME COMMAND ──
			if strings.HasPrefix(commandText, "RESUME|") {
				jobID := strings.TrimPrefix(commandText, "RESUME|")
				store := GetStore()

				store.mu.RLock()                     // cite: 75
				job, exists := store.Jobs[jobID]     // cite: 75
				var targetURL, targetSavePath string // cite: 75
				if exists && job != nil {            // cite: 75
					targetURL = job.URL           // cite: 75
					targetSavePath = job.SavePath // cite: 75
				}
				store.mu.RUnlock() // cite: 75

				if !exists || job == nil { // cite: 75
					_, _ = c.Write([]byte("ERROR|Target job profile not found in cache.\n"))
					return
				}

				store.UpdateStatus(jobID, "DOWNLOADING") // cite: 75
				// Note: downloadTrigger binds directly to the executeDownloadJob hook injected from main.go [cite: 65, 76]
				go downloadTrigger(targetURL, targetSavePath, jobID)

				_, _ = c.Write([]byte("SUCCESS|Job successfully resumed.\n"))
				return
			}
			// pkg/storage/ipc.go

			// ── 🗑️ NEW: HANDLE JOB DELETION AND DRIVE STORAGE CLEANUP OVER RAW IPC ──
			if strings.HasPrefix(commandText, "DELETE|") {
				jobID := strings.TrimPrefix(commandText, "DELETE|")
				store := GetStore()

				// 1. Thread-safely extract target details inside a read lock to identify cleanup files
				store.mu.RLock()
				job, exists := store.Jobs[jobID]
				var targetSavePath string
				if exists && job != nil {
					targetSavePath = job.SavePath
				}
				store.mu.RUnlock()

				if !exists || job == nil {
					_, _ = c.Write([]byte("ERROR|Target job profile not found in cache.\n"))
					return
				}

				// 2. INTERCEPT TIMELINES: Trigger context cancellation to stop active worker network loops instantly
				if GlobalCancelMutex != nil && GlobalCancelMap != nil {
					GlobalCancelMutex.Lock()
					if cancel, active := GlobalCancelMap[jobID]; active {
						cancel()
					}
					GlobalCancelMutex.Unlock()
				}

				// 3. STORAGE DISK SCRUBBING: Clean out all file payloads and temporary tracking artifacts
				_ = os.Remove(targetSavePath) // Final binary target download file
				ClearJobState(targetSavePath) // Stale metadata state file (.hydra configuration)

				// 4. MEMORY MAP REGISTRY PURGE: Evict the job tracking data profile index entirely
				store.DeleteJob(jobID)

				_, _ = c.Write([]byte("SUCCESS|Job and disk footprints successfully purged.\n"))
				return
			}

			// ── 🚀 HANDLE INCOMING BROWSER HOOK DOWNLOAD DISPATCH ──
			if strings.HasPrefix(commandText, "DOWNLOAD|") { // cite: 62
				payload := strings.TrimPrefix(commandText, "DOWNLOAD|") // cite: 62
				parts := strings.Split(payload, "|")                    // cite: 62
				if len(parts) < 2 {                                     // cite: 63
					_, _ = c.Write([]byte("ERROR|Missing target save path string.\n")) // cite: 63
					return
				}

				targetURL := parts[0] // cite: 208
				unsafePath := parts[1]

				securedPath, err := SanitizeDownloadPath(unsafePath) // cite: 64
				if err != nil {
					_, _ = c.Write([]byte(fmt.Sprintf("ERROR|%v\n", err)))
					return
				}

				store := GetStore()                                       // cite: 208
				jobID := fmt.Sprintf("job_%d", len(store.GetAllJobs())+1) // cite: 64

				var fileName string
				if parts := strings.Split(securedPath, "/"); len(parts) > 0 { // cite: 64
					fileName = parts[len(parts)-1] // cite: 64
				}

				store.SetJob(jobID, &models.UIJob{ // cite: 64
					ID:         jobID,            // cite: 64
					FileName:   fileName,         // cite: 64
					URL:        targetURL,        // cite: 64
					Progress:   0.0,              // cite: 64
					TotalSize:  "Calculating...", // cite: 64
					Downloaded: "0 B",            // cite: 65
					Speed:      "0.00 KB/s",
					Status:     "DOWNLOADING", // cite: 65
				})

				_, _ = c.Write([]byte("SUCCESS|Download job dispatched to background daemon.\n")) // cite: 64
				downloadTrigger(targetURL, securedPath, jobID)                                    // cite: 65
				return
			}

			_, _ = c.Write([]byte("ERROR|Unknown command structure.\n")) // cite: 65
		}(conn)
	}
}
