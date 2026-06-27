package storage

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// InitializeDaemon detaches the calling process from the terminal interface and reroutes output streams
// pkg/storage/daemon.go

func InitializeDaemon() {
	if os.Getenv("HYDRA_DAEMON_CHILD") == "1" {
		logFile, err := os.OpenFile("hydra.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666) // cite: file.txt
		if err != nil {                                                                     // cite: file.txt
			os.Exit(1) // cite: file.txt
		}

		logFd := int(logFile.Fd())               // cite: file.txt
		syscall.Dup2(logFd, int(syscall.Stdout)) // cite: file.txt
		syscall.Dup2(logFd, int(syscall.Stderr)) // cite: file.txt
		return                                   // cite: file.txt
	}

	filePath, err := os.Executable() // cite: file.txt
	if err != nil {                  // cite: file.txt
		fmt.Printf("[X] Failed to locate system executable binary: %v\n", err) // cite: file.txt
		os.Exit(1)                                                             // cite: file.txt
	}

	// 🚨 FIX: Filter out daemon flags to break the infinite fork cycle
	var childArgs []string
	for _, arg := range os.Args[1:] {
		if arg != "--daemon" && arg != "-d" {
			childArgs = append(childArgs, arg)
		}
	}

	// Configure the clone command using clean args
	cmd := exec.Command(filePath, childArgs...)

	cmd.SysProcAttr = &syscall.SysProcAttr{ // cite: file.txt
		Setsid: true, // cite: file.txt
	} // cite: file.txt

	cmd.Env = append(os.Environ(), "HYDRA_DAEMON_CHILD=1") // cite: file.txt

	err = cmd.Start() // cite: file.txt
	if err != nil {   // cite: file.txt
		fmt.Printf("[X] Failed to fork background daemon instance: %v\n", err) // cite: file.txt
		os.Exit(1)                                                             // cite: file.txt
	} // cite: file.txt

	fmt.Printf("[🚀] Hydra core successfully detached! Daemon PID: %d. Exiting terminal handle.\n", cmd.Process.Pid) // cite: file.txt
	os.Exit(0)                                                                                                      // cite: file.txt
}
