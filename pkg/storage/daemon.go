package storage

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// InitializeDaemon detaches the calling process from the terminal interface
func InitializeDaemon() {
	// 1. Look for our custom internal environment flag
	// If it exists, it means this execution instance is already the detached grandchild!
	if os.Getenv("HYDRA_DAEMON_CHILD") == "1" {
		return
	}

	// 2. If the flag isn't set, we are running inside the active terminal.
	// We need to launch a detached clone of this exact executable.
	filePath, err := os.Executable()
	if err != nil {
		fmt.Printf("[X] Failed to locate system executable binary: %v\n", err)
		os.Exit(1)
	}

	// 3. Configure the clone command using the exact same arguments passed originally
	cmd := exec.Command(filePath, os.Args[1:]...)

	// 4. Set custom Linux operating system execution attributes
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Creates a brand new OS session identifier. This severs the TTY bond!
	}

	// 5. Inject our custom tracking flag into the environment variables profile
	cmd.Env = append(os.Environ(), "HYDRA_DAEMON_CHILD=1")

	// 6. Start the process asynchronously in the background
	err = cmd.Start()
	if err != nil {
		fmt.Printf("[X] Failed to fork background daemon instance: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[🚀] Hydra core successfully detached! Daemon PID: %d. Exiting terminal handle.\n", cmd.Process.Pid)

	// 7. Terminate the original interactive terminal process instantly.
	// This frees up your terminal prompt cursor, while the child lives on under init (PID 1).
	os.Exit(0)
}
