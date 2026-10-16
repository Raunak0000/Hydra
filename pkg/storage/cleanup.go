package storage

import (
	"fmt"
	"os"
)

// CleanupNewDestination removes a newly reserved destination after
// initialization fails. It must never be used for resumed downloads.
func CleanupNewDestination(path string, newlyReserved bool) error {
	if !newlyReserved {
		return nil
	}

	if path == "" {
		return fmt.Errorf("destination path is empty")
	}

	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove failed destination %q: %w", path, err)
	}

	return nil
}
