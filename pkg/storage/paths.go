// pkg/storage/paths.go

package storage

import (
	"fmt"
	"os"
	"os/exec"
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

	// Determine destination directory whether path ends in a file or directory
	targetDir := cleaned
	if strings.HasSuffix(rawPath, "/") || strings.HasSuffix(rawPath, string(filepath.Separator)) {
		targetDir = cleaned
	} else {
		targetDir = filepath.Dir(cleaned)
	}

	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create target directory: %w", err)
		}
	}

	return TruncateFilename(cleaned, 120), nil
}

// TruncateFilename ensures path base filenames do not exceed system length limits
func TruncateFilename(path string, maxLen int) string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	if len(base) <= maxLen {
		return path
	}

	ext := filepath.Ext(base)
	if len(ext) > 10 {
		ext = ""
	}

	nameLen := maxLen - len(ext)
	if nameLen <= 0 {
		nameLen = maxLen
		ext = ""
	}

	truncatedBase := base[:nameLen] + ext
	return filepath.Join(dir, truncatedBase)
}

// ChooseFolderDialog opens the native desktop directory picker (zenity / kdialog)
func ChooseFolderDialog(initialPath string) (string, error) {
	initial, _ := ResolvePath(initialPath)

	// 1. Try zenity (GNOME / GTK / Standard Linux desktop environments)
	if _, err := exec.LookPath("zenity"); err == nil {
		cmd := exec.Command("zenity", "--file-selection", "--directory", "--title=Hydra: Choose Download Directory")
		if initial != "" {
			cmd.Args = append(cmd.Args, fmt.Sprintf("--filename=%s/", initial))
		}
		out, err := cmd.Output()
		if err == nil {
			selected := strings.TrimSpace(string(out))
			if selected != "" {
				return selected, nil
			}
		}
	}

	// 2. Try kdialog (KDE Plasma)
	if _, err := exec.LookPath("kdialog"); err == nil {
		cmd := exec.Command("kdialog", "--getexistingdirectory", initial, "--title", "Hydra: Choose Download Directory")
		out, err := cmd.Output()
		if err == nil {
			selected := strings.TrimSpace(string(out))
			if selected != "" {
				return selected, nil
			}
		}
	}

	return "", fmt.Errorf("no native file dialog utility found (install 'zenity' or 'kdialog')")
}
