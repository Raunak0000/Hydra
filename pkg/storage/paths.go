// pkg/storage/paths.go

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GetConfigDir returns $XDG_CONFIG_HOME/hydra (defaults to ~/.config/hydra)
func GetConfigDir() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get user home dir: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}

	appConfig := filepath.Join(configHome, "hydra")
	if err := os.MkdirAll(appConfig, 0755); err != nil {
		return "", fmt.Errorf("failed to create config dir: %w", err)
	}
	return appConfig, nil
}

// GetDataDir returns $XDG_DATA_HOME/hydra (defaults to ~/.local/share/hydra)
func GetDataDir() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get user home dir: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}

	appData := filepath.Join(dataHome, "hydra")
	if err := os.MkdirAll(appData, 0755); err != nil {
		return "", fmt.Errorf("failed to create data dir: %w", err)
	}
	return appData, nil
}

// GetSocketPath returns $XDG_RUNTIME_DIR/hydra.sock with fallback to /tmp/hydra.sock
func GetSocketPath() string {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir != "" {
		return filepath.Join(runtimeDir, "hydra.sock")
	}
	return "/tmp/hydra.sock"
}

// GetDefaultDownloadsDir returns the user's Downloads directory (~/Downloads)
func GetDefaultDownloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return filepath.Join(home, "Downloads")
}

// ResolvePath expands ~ and ensures the directory path is absolute and valid
func ResolvePath(rawPath string) (string, error) {
	cleaned := filepath.Clean(rawPath)

	if strings.HasPrefix(cleaned, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to expand home dir: %w", err)
		}
		cleaned = filepath.Join(home, strings.TrimPrefix(cleaned, "~"))
	}

	if !filepath.IsAbs(cleaned) {
		var err error
		cleaned, err = filepath.Abs(cleaned)
		if err != nil {
			return "", fmt.Errorf("invalid path: %w", err)
		}
	}

	dir := filepath.Dir(cleaned)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create target directory: %w", err)
		}
	}

	return TruncateFilename(cleaned, 120), nil
}
