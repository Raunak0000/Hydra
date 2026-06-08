package storage

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// InitializeDaemon detaches the calling process from the terminal interface and reroutes output streams
func InitializeDaemon() {
	// 1. Look for our custom internal environment flag
	if os.Getenv("HYDRA_DAEMON_CHILD") == "1" {
		// =========================================================
		// GRANDCHILD DESCRIPTOR REDIRECTION
		// We are inside the background daemon instance. Reroute streams now!
		// =========================================================
		logFile, err := os.OpenFile("hydra.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			os.Exit(1) // Exit if we cannot open or initialize the log tracker
		}

		// Grab the raw underlying OS file descriptor integer for our log file
		logFd := int(logFile.Fd())

		// syscall.Dup2 duplicates file descriptors.
		// We override standard Output (1) and standard Error (2) handles to point directly to the log file.
		syscall.Dup2(logFd, int(syscall.Stdout))
		syscall.Dup2(logFd, int(syscall.Stderr))
		return
	}

	// 2. Locate the active executable binary
	filePath, err := os.Executable()
	if err != nil {
		fmt.Printf("[X] Failed to locate system executable binary: %v\n", err)
		os.Exit(1)
	}

	// 3. Configure the clone command using original arguments
	cmd := exec.Command(filePath, os.Args[1:]...)

	// 4. Set custom Linux operating system execution attributes to sever TTY bond
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	// 5. Inject our custom tracking flag into the environment profile
	cmd.Env = append(os.Environ(), "HYDRA_DAEMON_CHILD=1")

	// 6. Start the process asynchronously in the background
	err = cmd.Start()
	if err != nil {
		fmt.Printf("[X] Failed to fork background daemon instance: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[🚀] Hydra core successfully detached! Daemon PID: %d. Exiting terminal handle.\n", cmd.Process.Pid)

	// 7. Terminate the original interactive terminal process instantly
	os.Exit(0)
}
