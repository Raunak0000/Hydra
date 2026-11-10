package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/Raunak0000/Hydra/pkg/models"
)

// StartIPCServer listens on the dynamic UNIX domain socket and handles incoming commands
func StartIPCServer(downloadTrigger func(string, string, string)) {
	socketPath := GetSocketPath()

	// 1. Clean up stale socket file if present
	if _, err := os.Stat(socketPath); err == nil {
		_ = os.Remove(socketPath)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Printf("[X] IPC Server failed to bind to socket: %v\n", err)
		return
	}
	defer listener.Close()

	// 2. Restrict permissions to owner-only
	_ = os.Chmod(socketPath, 0600)

	fmt.Printf("[⚙] Hydra IPC Server listening silently on %s\n", socketPath)

	dbStore, err := GetDBStore()
	if err != nil {
		fmt.Printf("[X] IPC Server could not connect to SQLite: %v\n", err)
		return
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go func(c net.Conn) {
			defer c.Close()

			reader := bufio.NewReader(c)
			message, err := reader.ReadString('\n')
			if err != nil {
				return
			}

			commandText := strings.TrimSpace(message)

			// ── 📊 HANDLE STATUS COMMAND ──
			if commandText == "STATUS" {
				jobs := dbStore.GetAllJobs()
				_ = json.NewEncoder(c).Encode(jobs)
				return
			}

			// ── ⏸ HANDLE PAUSE COMMAND ──
			if strings.HasPrefix(commandText, "PAUSE|") {
				jobID := strings.TrimPrefix(commandText, "PAUSE|")

				_ = dbStore.UpdateStatus(jobID, "PAUSED")

				if GlobalCancelMutex != nil && GlobalCancelMap != nil {
					GlobalCancelMutex.Lock()
					if cancel, exists := GlobalCancelMap[jobID]; exists {
						cancel()
					}
					GlobalCancelMutex.Unlock()
				}

				GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
				_, _ = c.Write([]byte("SUCCESS|Job successfully paused.\n"))
				return
			}

			// ── ⏸ HANDLE PAUSE_ALL COMMAND ──
			if commandText == "PAUSE_ALL" {
				paused := PauseAllJobs(dbStore)
				_, _ = c.Write([]byte(fmt.Sprintf("SUCCESS|Paused %d active/queued job(s).\n", paused)))
				return
			}

			// ── ▶ HANDLE RESUME_ALL COMMAND ──
			if commandText == "RESUME_ALL" {
				resumed := ResumeAllJobs(dbStore)
				_, _ = c.Write([]byte(fmt.Sprintf("SUCCESS|Queued %d paused job(s) to resume.\n", resumed)))
				return
			}

			// ── ▶ HANDLE RESUME COMMAND ──
			if strings.HasPrefix(commandText, "RESUME|") {
				jobID := strings.TrimPrefix(commandText, "RESUME|")

				job, exists := dbStore.GetJob(jobID)
				if !exists {
					_, _ = c.Write([]byte("ERROR|Target job profile not found in database.\n"))
					return
				}

				_ = dbStore.UpdateStatus(jobID, "DOWNLOADING")
				go downloadTrigger(job.URL, job.SavePath, jobID)

				_, _ = c.Write([]byte("SUCCESS|Job successfully resumed.\n"))
				return
			}

			// ── 🔗 HANDLE UPDATE URL COMMAND ──
			if strings.HasPrefix(commandText, "UPDATE_URL|") {
				payload := strings.TrimPrefix(commandText, "UPDATE_URL|")
				parts := strings.SplitN(payload, "|", 2)
				if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
					_, _ = c.Write([]byte("ERROR|Usage: UPDATE_URL|<jobID>|<newURL>\n"))
					return
				}
				jobID := parts[0]
				newURL := parts[1]

				if _, exists := dbStore.GetJob(jobID); !exists {
					_, _ = c.Write([]byte("ERROR|Target job profile not found in database.\n"))
					return
				}

				if err := dbStore.UpdateJobURL(jobID, newURL); err != nil {
					_, _ = c.Write([]byte(fmt.Sprintf("ERROR|Failed to update job URL: %v\n", err)))
					return
				}

				_, _ = c.Write([]byte("SUCCESS|Job URL successfully updated.\n"))
				return
			}

			// ── 🗑️ HANDLE DELETE COMMAND ──
			if strings.HasPrefix(commandText, "DELETE|") {
				jobID := strings.TrimPrefix(commandText, "DELETE|")

				job, exists := dbStore.GetJob(jobID)
				if !exists {
					_, _ = c.Write([]byte("ERROR|Target job profile not found in database.\n"))
					return
				}

				if GlobalCancelMutex != nil && GlobalCancelMap != nil {
					GlobalCancelMutex.Lock()
					if cancel, active := GlobalCancelMap[jobID]; active {
						cancel()
					}
					GlobalCancelMutex.Unlock()
				}

				_ = os.Remove(job.SavePath)
				ClearJobState(job.SavePath)
				_ = dbStore.DeleteJob(jobID)

				_, _ = c.Write([]byte("SUCCESS|Job and disk footprints successfully purged.\n"))
				return
			}

			// ── 📂 HANDLE RULES COMMAND ──
			if commandText == "RULES" {
				cfg := GetConfig()
				_ = json.NewEncoder(c).Encode(cfg.Categories)
				return
			}

			// ── ➕ HANDLE SET_RULE COMMAND ──
			if strings.HasPrefix(commandText, "SET_RULE|") {
				payload := strings.TrimPrefix(commandText, "SET_RULE|")
				parts := strings.SplitN(payload, "|", 3)
				if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
					_, _ = c.Write([]byte("ERROR|Usage: SET_RULE|<category>|<destination_path>|<ext1,ext2...>\n"))
					return
				}
				name := parts[0]
				destPath := parts[1]
				extList := strings.Split(parts[2], ",")

				if err := SetCategoryRule(name, destPath, extList); err != nil {
					_, _ = c.Write([]byte(fmt.Sprintf("ERROR|Failed to save rule: %v\n", err)))
					return
				}
				_, _ = c.Write([]byte("SUCCESS|Category rule successfully saved.\n"))
				return
			}

			// ── ❌ HANDLE DELETE_RULE COMMAND ──
			if strings.HasPrefix(commandText, "DELETE_RULE|") {
				name := strings.TrimSpace(strings.TrimPrefix(commandText, "DELETE_RULE|"))
				if name == "" {
					_, _ = c.Write([]byte("ERROR|Missing category name parameter.\n"))
					return
				}
				if err := DeleteCategoryRule(name); err != nil {
					_, _ = c.Write([]byte(fmt.Sprintf("ERROR|Failed to delete rule: %v\n", err)))
					return
				}
				_, _ = c.Write([]byte("SUCCESS|Category rule successfully removed.\n"))
				return
			}

			// ── 🚀 HANDLE DOWNLOAD DISPATCH ──
			if strings.HasPrefix(commandText, "DOWNLOAD|") {
				payload := strings.TrimPrefix(commandText, "DOWNLOAD|")
				parts := strings.Split(payload, "|")
				if len(parts) < 2 {
					_, _ = c.Write([]byte("ERROR|Missing target save path string.\n"))
					return
				}

				targetURL := parts[0]
				unsafePath := parts[1]

				securedPath, err := ResolvePath(unsafePath)
				if err != nil {
					_, _ = c.Write([]byte(fmt.Sprintf("ERROR|%v\n", err)))
					return
				}

				jobID := fmt.Sprintf("job_%d", len(dbStore.GetAllJobs())+1)
				fileName := filepath.Base(securedPath)

				newJob := models.UIJob{
					ID:         jobID,
					FileName:   fileName,
					URL:        targetURL,
					SavePath:   securedPath,
					Progress:   0.0,
					TotalSize:  "Calculating...",
					Downloaded: "0 B",
					Speed:      "0.00 KB/s",
					Status:     "DOWNLOADING",
				}
				_ = dbStore.SaveJob(&newJob)
				NotifyDownloadQueued(fileName, securedPath)

				_, _ = c.Write([]byte("SUCCESS|Download job dispatched to background daemon.\n"))
				go downloadTrigger(targetURL, securedPath, jobID)
				return
			}

			_, _ = c.Write([]byte("ERROR|Unknown command structure.\n"))
		}(conn)
	}
}
