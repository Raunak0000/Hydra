package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func getSocketPath() string {
	// 1. Check user runtime directory ($XDG_RUNTIME_DIR/hydra.sock)
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		sock := filepath.Join(runtimeDir, "hydra.sock")
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
	}

	// 2. Check /run/user/<UID>/hydra.sock
	uidSock := fmt.Sprintf("/run/user/%d/hydra.sock", os.Getuid())
	if _, err := os.Stat(uidSock); err == nil {
		return uidSock
	}

	// 3. Check /tmp/hydra.sock
	if _, err := os.Stat("/tmp/hydra.sock"); err == nil {
		return "/tmp/hydra.sock"
	}

	// Default fallback to XDG_RUNTIME_DIR or /tmp/hydra.sock
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "hydra.sock")
	}
	return "/tmp/hydra.sock"
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	action := os.Args[1]
	socketPath := getSocketPath()

	// Establish low-level connection through the local Linux socket file
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Printf("[X] Error: Could not connect to Hydra background daemon at %s. Is the server running?\n", socketPath)
		os.Exit(1)
	}
	defer conn.Close()

	switch action {
	case "status":
		executeStatusRequest(conn)
	case "pause-all":
		executeBulkActionRequest(conn, "PAUSE_ALL")
	case "resume-all":
		executeBulkActionRequest(conn, "RESUME_ALL")
	case "pause", "resume", "delete":
		if len(os.Args) < 3 {
			fmt.Printf("[X] Error: The '%s' subcommand requires a target Job ID parameter.\n", action)
			os.Exit(1)
		}
		executeActionRequest(conn, action, os.Args[2])
	case "update-url":
		if len(os.Args) < 4 {
			fmt.Println("[X] Error: 'update-url' requires both a target Job ID and a new URL.")
			fmt.Println("Usage: hydra update-url <JOB_ID> <NEW_URL>")
			os.Exit(1)
		}
		executeUpdateURLRequest(conn, os.Args[2], os.Args[3])
	case "rules":
		executeRulesRequest(conn)
	case "set-rule":
		if len(os.Args) < 5 {
			fmt.Println("[X] Error: 'set-rule' requires category name, destination path, and extensions.")
			fmt.Println("Usage: hydra set-rule <CATEGORY> <PATH> <ext1,ext2,...>")
			os.Exit(1)
		}
		executeSetRuleRequest(conn, os.Args[2], os.Args[3], os.Args[4])
	case "delete-rule":
		if len(os.Args) < 3 {
			fmt.Println("[X] Error: 'delete-rule' requires a category name parameter.")
			fmt.Println("Usage: hydra delete-rule <CATEGORY>")
			os.Exit(1)
		}
		executeDeleteRuleRequest(conn, os.Args[2])
	default:
		fmt.Printf("[X] Error: Unknown subcommand '%s'\n", action)
		printUsage()
	}
}

func executeStatusRequest(conn net.Conn) {
	_, _ = conn.Write([]byte("STATUS\n"))

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
	fmt.Fprintln(w, "JOB ID\t FILE NAME\t PROGRESS\t SIZE / DOWNLOADED\t SPEED\t ETA\t STATUS")
	fmt.Fprintln(w, "------\t ---------\t --------\t ─────────────────\t ─────\t ---\t ──────")

	for _, job := range jobs {
		eta := job.ETA
		if eta == "" {
			eta = "--"
		}
		fmt.Fprintf(w, "%s\t %s\t %.2f%%\t %s / %s\t %s\t %s\t %s\n",
			job.ID, job.FileName, job.Progress, job.Downloaded, job.TotalSize, job.Speed, eta, job.Status)
	}
	w.Flush()
	fmt.Println()
}

func executeActionRequest(conn net.Conn, action string, jobID string) {
	payload := fmt.Sprintf("%s|%s\n", strings.ToUpper(action), jobID)
	_, _ = conn.Write([]byte(payload))

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

func executeBulkActionRequest(conn net.Conn, command string) {
	_, _ = conn.Write([]byte(command + "\n"))

	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		fmt.Printf("[X] Error: Failed to read daemon confirmation for %s.\n", command)
		return
	}

	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "SUCCESS|") {
		fmt.Printf("[✓] %s\n", strings.TrimPrefix(reply, "SUCCESS|"))
	} else {
		fmt.Printf("[X] Daemon rejected command: %s\n", reply)
	}
}

func executeUpdateURLRequest(conn net.Conn, jobID string, newURL string) {
	payload := fmt.Sprintf("UPDATE_URL|%s|%s\n", jobID, newURL)
	_, _ = conn.Write([]byte(payload))

	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		fmt.Println("[X] Error: Failed to read daemon confirmation for update-url action.")
		return
	}

	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "SUCCESS") {
		fmt.Printf("[✓] URL for job %s updated successfully. You can now resume it!\n", jobID)
	} else {
		fmt.Printf("[X] Daemon rejected update-url: %s\n", reply)
	}
}

func executeRulesRequest(conn net.Conn) {
	_, _ = conn.Write([]byte("RULES\n"))

	var categories map[string]storage.CategoryRule
	if err := json.NewDecoder(conn).Decode(&categories); err != nil {
		fmt.Println("[X] Error: Failed to parse category rules:", err)
		return
	}

	if len(categories) == 0 {
		fmt.Println("🐉 No download category rules configured.")
		return
	}

	fmt.Println("\n📂 HYDRA DEFAULT DOWNLOAD ROUTING RULES:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.Debug)
	fmt.Fprintln(w, "CATEGORY\t DESTINATION PATH\t EXTENSIONS")
	fmt.Fprintln(w, "────────\t ────────────────\t ──────────")

	var names []string
	for name := range categories {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		rule := categories[name]
		exts := strings.Join(rule.Extensions, ", ")
		fmt.Fprintf(w, "%s\t %s\t %s\n", name, rule.Path, exts)
	}
	w.Flush()
	fmt.Println()
}

func executeSetRuleRequest(conn net.Conn, name string, path string, extensions string) {
	payload := fmt.Sprintf("SET_RULE|%s|%s|%s\n", name, path, extensions)
	_, _ = conn.Write([]byte(payload))

	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		fmt.Println("[X] Error: Failed to read daemon response for set-rule action.")
		return
	}

	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "SUCCESS") {
		fmt.Printf("[✓] Category rule '%s' successfully configured for '%s'.\n", name, path)
	} else {
		fmt.Printf("[X] Daemon rejected set-rule: %s\n", reply)
	}
}

func executeDeleteRuleRequest(conn net.Conn, name string) {
	payload := fmt.Sprintf("DELETE_RULE|%s\n", name)
	_, _ = conn.Write([]byte(payload))

	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		fmt.Println("[X] Error: Failed to read daemon response for delete-rule action.")
		return
	}

	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "SUCCESS") {
		fmt.Printf("[✓] Category rule '%s' successfully deleted.\n", name)
	} else {
		fmt.Printf("[X] Daemon rejected delete-rule: %s\n", reply)
	}
}

func printUsage() {
	fmt.Println("🚀 HYDRA COMMAND LINE INTERFACE CONTROL UTILITY")
	fmt.Println("Usage:")
	fmt.Println("  hydra status                   - Display live progress text grids for all managed tracking tasks")
	fmt.Println("  hydra pause [id]               - Contextually freeze parallel downloader channel pools for a job")
	fmt.Println("  hydra pause-all                - Pause all active and queued downloads")
	fmt.Println("  hydra resume [id]              - Reload serialization snapshots and resume a specific job")
	fmt.Println("  hydra resume-all               - Resume all paused downloads")
	fmt.Println("  hydra update-url [id] [new_url] - Update download URL for a job (e.g. after link expires)")
	fmt.Println("  hydra delete [id]              - Purge structural job trace files and records from the queue server")
	fmt.Println("  hydra rules                    - View all file category routing rules and target folders")
	fmt.Println("  hydra set-rule <name> <path> <exts> - Configure target destination for file extensions")
	fmt.Println("  hydra delete-rule <name>       - Remove a file category routing rule")
}
