package downloader

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// SetupSignalHandling creates a listener trap for Linux termination commands
func SetupSignalHandling(stopProgress chan bool) chan bool { // Keep only stopProgress
	sigChan := make(chan os.Signal, 1)
	internalCancel := make(chan bool, 1)

	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		fmt.Printf("\n[⚠️] Captured OS Signal: %v. Initiating graceful shutdown sequence...\n", sig)

		stopProgress <- true
		internalCancel <- true
	}()

	return internalCancel
}
