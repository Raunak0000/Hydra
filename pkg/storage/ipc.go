package storage

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
)

// SocketPath defines where our local file system communication pipe lives
const SocketPath = "/tmp/hydra.sock"

// StartIPCServer listens on a Unix domain socket for incoming download commands
func StartIPCServer(downloadTrigger func(string)) {
	// 1. Clean up old socket files if Hydra previously shut down abruptly
	if _, err := os.Stat(SocketPath); err == nil {
		_ = os.Remove(SocketPath)
	}

	// 2. Bind and listen to the local file system socket path
	listener, err := net.Listen("unix", SocketPath)
	if err != nil {
		fmt.Printf("[X] IPC Server failed to bind to socket: %v\n", err)
		return
	}
	defer listener.Close()

	// Ensure the socket file permissions allow communication
	_ = os.Chmod(SocketPath, 0666)

	fmt.Printf("[⚙] Hydra IPC Server listening silently on %s\n", SocketPath)

	for {
		// Block here until a client connects (like hydra-cli)
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		// Handle the client packet inside a short background goroutine
		go func(c net.Conn) {
			defer c.Close()

			// Read incoming text line sent over the socket pipe
			reader := bufio.NewReader(c)
			message, err := reader.ReadString('\n')
			if err != nil {
				return
			}

			// Clean up extra whitespace and newlines
			commandText := strings.TrimSpace(message)

			// Parse custom command contract: "DOWNLOAD|<URL>"
			if strings.HasPrefix(commandText, "DOWNLOAD|") {
				targetURL := strings.TrimPrefix(commandText, "DOWNLOAD|")

				// Send feedback confirmation byte back over the socket to the client
				_, _ = c.Write([]byte("SUCCESS|Download job dispatched to background daemon.\n"))

				// Pass the captured URL back to Hydra's downloading engine loop
				downloadTrigger(targetURL)
			} else {
				_, _ = c.Write([]byte("ERROR|Unknown command structure.\n"))
			}
		}(conn)
	}
}
