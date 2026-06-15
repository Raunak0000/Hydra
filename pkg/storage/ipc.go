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

// Change the function signature so the callback accepts BOTH the url and savePath strings
func StartIPCServer(downloadTrigger func(string, string, string)) {
	if _, err := os.Stat(SocketPath); err == nil {
		_ = os.Remove(SocketPath)
	}

	listener, err := net.Listen("unix", SocketPath)
	if err != nil {
		fmt.Printf("[X] IPC Server failed to bind to socket: %v\n", err)
		return
	}
	defer listener.Close()
	_ = os.Chmod(SocketPath, 0666)

	fmt.Printf("[⚙] Hydra IPC Server listening silently on %s\n", SocketPath)

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

			if strings.HasPrefix(commandText, "DOWNLOAD|") {
				// Remove the command prefix
				payload := strings.TrimPrefix(commandText, "DOWNLOAD|")

				// Split the remaining payload by the pipe separator
				parts := strings.Split(payload, "|")
				if len(parts) < 2 {
					_, _ = c.Write([]byte("ERROR|Missing target save path string.\n"))
					return
				}

				targetURL := parts[0]
				savePath := parts[1]

				_, _ = c.Write([]byte("SUCCESS|Download job dispatched to background daemon.\n"))

				store := GetStore()
				jobID := fmt.Sprintf("job_%d", len(store.GetAllJobs())+1)
				fileName := savePath
				if lastIdx := len(savePath) - 1; lastIdx >= 0 {
					for i := lastIdx; i >= 0; i-- {
						if savePath[i] == '/' {
							fileName = savePath[i+1:]
							break
						}
					}
				}

				store.SetJob(jobID, &models.UIJob{
					ID:         jobID,
					FileName:   fileName,
					URL:        targetURL,
					Progress:   0.0,
					TotalSize:  "Calculating...",
					Downloaded: "0 B",
					Status:     "DOWNLOADING",
				})

				// Pass both parameters out to your engine execution block!
				downloadTrigger(targetURL, savePath, jobID)
			} else {
				_, _ = c.Write([]byte("ERROR|Unknown command structure.\n"))
			}
		}(conn)
	}
}
