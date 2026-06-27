package storage

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/Raunak0000/Hydra/pkg/models"
)

const SocketPath = "/tmp/hydra.sock"

// pkg/storage/ipc.go

func StartIPCServer(downloadTrigger func(string, string, string)) {
	if _, err := os.Stat(SocketPath); err == nil { // cite: 207
		_ = os.Remove(SocketPath) // cite: 207
	}

	listener, err := net.Listen("unix", SocketPath) // cite: 207
	if err != nil {                                 // cite: 207
		fmt.Printf("[X] IPC Server failed to bind to socket: %v\n", err) // cite: 207
		return                                                           // cite: 207
	}
	defer listener.Close() // cite: 207

	// ── SECURE SOCKET FILESYSTEM BOUNDARY PERMISSIONS ──
	// Change 0666 to 0600 so only the process owner can trigger commands
	_ = os.Chmod(SocketPath, 0600)

	fmt.Printf("[⚙] Hydra IPC Server listening silently on %s\n", SocketPath) // cite: 207

	for {
		conn, err := listener.Accept() // cite: 207
		if err != nil {                // cite: 207
			continue // cite: 207
		}

		go func(c net.Conn) {
			defer c.Close() // cite: 208

			reader := bufio.NewReader(c)            // cite: 208
			message, err := reader.ReadString('\n') // cite: 208
			if err != nil {                         // cite: 208
				return // cite: 208
			}

			commandText := strings.TrimSpace(message) // cite: 208

			if strings.HasPrefix(commandText, "DOWNLOAD|") { // cite: 208
				payload := strings.TrimPrefix(commandText, "DOWNLOAD|") // cite: 208

				parts := strings.Split(payload, "|") // cite: 208
				if len(parts) < 2 {                  // cite: 208
					_, _ = c.Write([]byte("ERROR|Missing target save path string.\n")) // cite: 208
					return                                                             // cite: 208
				}

				targetURL := parts[0] // cite: 208
				unsafePath := parts[1]

				// Sanitize raw incoming input from local socket strings
				securedPath, err := SanitizeDownloadPath(unsafePath)
				if err != nil {
					_, _ = c.Write([]byte(fmt.Sprintf("ERROR|%v\n", err)))
					return
				}

				_, _ = c.Write([]byte("SUCCESS|Download job dispatched to background daemon.\n")) // cite: 208

				store := GetStore() // cite: 208
				jobID := fmt.Sprintf("job_%d", len(store.GetAllJobs())+1)

				var fileName string
				if parts := strings.Split(securedPath, "/"); len(parts) > 0 {
					fileName = parts[len(parts)-1]
				}

				store.SetJob(jobID, &models.UIJob{ // cite: 208
					ID:         jobID,            // cite: 208
					FileName:   fileName,         // cite: 208
					URL:        targetURL,        // cite: 208
					Progress:   0.0,              // cite: 208
					TotalSize:  "Calculating...", // cite: 208
					Downloaded: "0 B",            // cite: 208
					Speed:      "0.00 KB/s",
					Status:     "DOWNLOADING",    // cite: 208
				}) // cite: 208

				downloadTrigger(targetURL, securedPath, jobID) // cite: 211
			} else {
				_, _ = c.Write([]byte("ERROR|Unknown command structure.\n")) // cite: 211
			}
		}(conn)
	}
}
