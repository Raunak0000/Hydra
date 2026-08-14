package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Raunak0000/Hydra/pkg/models"
)

var socketPath = func() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return runtimeDir + "/hydra.sock"
	}
	return "/tmp/hydra.sock"
}()

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	action := os.Args[1]

	// 1. Establish low-level connection through the local Linux socket file
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Println("[X] Error: Could not connect to Hydra background daemon. Is the server running?")
		os.Exit(1)
	}
	defer conn.Close()

	switch action {
	case "status":
		executeStatusRequest(conn)
	case "pause", "resume", "delete":
		if len(os.Args) < 3 {
			fmt.Printf("[X] Error: The '%s' subcommand requires a target Job ID parameter.\n", action)
			os.Exit(1)
		}
		executeActionRequest(conn, action, os.Args[2])
	default:
		fmt.Printf("[X] Error: Unknown subcommand '%s'\n", action)
		printUsage()
	}
}

func executeStatusRequest(conn net.Conn) {
	// Write standard command token directly to the socket wire
	_, _ = conn.Write([]byte("STATUS\n"))

	// Decode incoming JSON array from the socket stream
	var jobs []models.UIJob
	if err := json.NewDecoder(conn).Decode(&jobs); err != nil {
		fmt.Println("[X] Error: Failed to parse active queue records:", err)
		return
	}

	if len(jobs) == 0 {
		fmt.Println("🐉 Hydra Queue is currently empty.")
		return
	}

	fmt.Println("\n🐉 HYDRA CORE DOWNLOAD ACTIVE QUEUE:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.Debug)
	fmt.Fprintln(w, "JOB ID\t FILE NAME\t PROGRESS\t SIZE / DOWNLOADED\t SPEED\t STATUS")
	fmt.Fprintln(w, "------\t ---------\t --------\t ─────────────────\t ─────\t ──────")

	for _, job := range jobs {
		fmt.Fprintf(w, "%s\t %s\t %.2f%%\t %s / %s\t %s\t %s\n",
			job.ID, job.FileName, job.Progress, job.Downloaded, job.TotalSize, job.Speed, job.Status)
	}
	w.Flush()
	fmt.Println()
}

func executeActionRequest(conn net.Conn, action string, jobID string) {
	// Format plaintext command payload string (e.g. PAUSE|job_1\n, DELETE|job_1\n)
	payload := fmt.Sprintf("%s|%s\n", strings.ToUpper(action), jobID)
	_, _ = conn.Write([]byte(payload))

	// Wait and read line confirmation reply string back from daemon
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		fmt.Printf("[X] Error: Failed to read daemon confirmation for %s action.\n", action)
		return
	}

	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "SUCCESS") {
		fmt.Printf("[✓] Command accepted: Job %s has been successfully %sd.\n", jobID, action)
	} else {
		fmt.Printf("[X] Daemon rejected command: %s\n", reply)
	}
}

func printUsage() {
	fmt.Println("🚀 HYDRA COMMAND LINE INTERFACE CONTROL UTILITY")
	fmt.Println("Usage:")
	fmt.Println("  hydra status          - Display live progress text grids for all managed tracking tasks")
	fmt.Println("  hydra pause [id]      - Contextually freeze parallel downloader channel pools for a job")
	fmt.Println("  hydra resume [id]     - Reload serialization snapshots and resume a specific job")
	fmt.Println("  hydra delete [id]     - Purge structural job trace files and records from the queue server")
}
