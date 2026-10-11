// pkg/storage/paths.go

package storage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
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

var (
	ErrInvalidFilename = fmt.Errorf("invalid filename")
)

const MaxFilenameBytes = 255

// ValidateFilename validates a single filesystem filename.
//
// A filename must:
//   - be valid UTF-8
//   - not be empty
//   - not be "." or ".."
//   - not contain path separators
//   - not contain NUL bytes
//   - not contain control characters
//   - not exceed the Linux 255-byte component limit
func ValidateFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("%w: filename is empty", ErrInvalidFilename)
	}

	if !utf8.ValidString(filename) {
		return fmt.Errorf(
			"%w: filename contains invalid UTF-8",
			ErrInvalidFilename,
		)
	}

	if filename == "." || filename == ".." {
		return fmt.Errorf(
			"%w: filename cannot be %q",
			ErrInvalidFilename,
			filename,
		)
	}

	if strings.ContainsRune(filename, '\x00') {
		return fmt.Errorf(
			"%w: filename contains NUL byte",
			ErrInvalidFilename,
		)
	}

	if strings.ContainsAny(filename, `/\`) {
		return fmt.Errorf(
			"%w: filename contains a path separator",
			ErrInvalidFilename,
		)
	}

	for _, r := range filename {
		if unicode.IsControl(r) {
			return fmt.Errorf(
				"%w: filename contains control characters",
				ErrInvalidFilename,
			)
		}
	}

	if len([]byte(filename)) > MaxFilenameBytes {
		return fmt.Errorf(
			"%w: filename exceeds %d bytes",
			ErrInvalidFilename,
			MaxFilenameBytes,
		)
	}

	if strings.TrimSpace(filename) == "" {
		return fmt.Errorf(
			"%w: filename contains only whitespace",
			ErrInvalidFilename,
		)
	}

	return nil
}

// ResolvePath expands ~, normalizes the path, makes it absolute,
// and ensures the target directory exists.
func ResolvePath(rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	// Expand only the current user's home directory.
	if rawPath == "~" || strings.HasPrefix(rawPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve home directory: %w", err)
		}

		if rawPath == "~" {
			rawPath = home
		} else {
			rawPath = filepath.Join(home, rawPath[2:])
		}
	} else if strings.HasPrefix(rawPath, "~") {
		return "", fmt.Errorf("unsupported home-directory syntax: %q", rawPath)
	}

	cleaned := filepath.Clean(rawPath)

	if !filepath.IsAbs(cleaned) {
		absolute, err := filepath.Abs(cleaned)
		if err != nil {
			return "", fmt.Errorf("failed to resolve absolute path: %w", err)
		}
		cleaned = absolute
	}

	// A trailing separator explicitly identifies a directory.
	isDirectory := strings.HasSuffix(rawPath, string(filepath.Separator))

	// Existing directories are also treated as directory destinations.
	info, statErr := os.Stat(cleaned)
	switch {
	case statErr == nil:
		if info.IsDir() {
			isDirectory = true
		}
	case !os.IsNotExist(statErr):
		return "", fmt.Errorf("failed to inspect destination: %w", statErr)
	}

	targetDir := cleaned
	if !isDirectory {
		targetDir = filepath.Dir(cleaned)
	}

	// Create the destination directory when necessary.
	info, err := os.Stat(targetDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("failed to inspect destination directory: %w", err)
		}

		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create destination directory: %w", err)
		}
	} else if !info.IsDir() {
		return "", fmt.Errorf("destination parent is not a directory: %s", targetDir)
	}

	// Never truncate the complete path or silently change its filename.
	return cleaned, nil
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
