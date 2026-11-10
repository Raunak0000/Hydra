package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const apiTokenFileName = "api.token"

// GetAPITokenPath returns the per-user API token file path ($XDG_CONFIG_HOME/hydra/api.token).
func GetAPITokenPath() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, apiTokenFileName), nil
}

// LoadAPIToken resolves the API token from HYDRA_API_TOKEN or the per-user token file.
func LoadAPIToken() (string, error) {
	if envToken := strings.TrimSpace(os.Getenv("HYDRA_API_TOKEN")); envToken != "" {
		return envToken, nil
	}

	tokenPath, err := GetAPITokenPath()
	if err != nil {
		return "", err
	}

	return readTokenFile(tokenPath)
}

// EnsureAPIToken returns an existing API token or creates and persists a new random
// per-user token with 0600 permissions, updating the server's active token.
func EnsureAPIToken() (string, error) {
	if token, err := LoadAPIToken(); err == nil && token != "" {
		SetAPIToken(token)
		return token, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	tokenPath, err := GetAPITokenPath()
	if err != nil {
		return "", err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate API token: %w", err)
	}
	token := hex.EncodeToString(raw)

	if err := writeTokenFileAtomic(tokenPath, token); err != nil {
		return "", err
	}

	SetAPIToken(token)
	return token, nil
}

// SetAPIToken configures the API token used by HTTP handlers.
func SetAPIToken(token string) {
	hydraAPIToken = strings.TrimSpace(token)
}

func readTokenFile(tokenPath string) (string, error) {
	fd, err := syscall.Open(tokenPath, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open API token file %q: %w", tokenPath, err)
	}
	file := os.NewFile(uintptr(fd), tokenPath)
	if file == nil {
		_ = syscall.Close(fd)
		return "", fmt.Errorf("wrap API token file descriptor")
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect API token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("API token path %q is not a regular file", tokenPath)
	}

	data, err := io.ReadAll(io.LimitReader(file, 4096))
	if err != nil {
		return "", fmt.Errorf("read API token file: %w", err)
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("API token file %q is empty", tokenPath)
	}
	return token, nil
}

func writeTokenFileAtomic(tokenPath string, token string) error {
	if info, err := os.Lstat(tokenPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to overwrite symbolic link token path %q", tokenPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect token path %q: %w", tokenPath, err)
	}

	dir := filepath.Dir(tokenPath)
	tmpFile, err := os.CreateTemp(dir, ".api.token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp API token file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmpFile.Chmod(0600); err != nil {
		return fmt.Errorf("set API token permissions: %w", err)
	}
	if _, err := tmpFile.WriteString(token + "\n"); err != nil {
		return fmt.Errorf("write temp API token: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp API token: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp API token: %w", err)
	}

	if err := os.Rename(tmpPath, tokenPath); err != nil {
		return fmt.Errorf("persist API token: %w", err)
	}
	cleanup = false
	return nil
}

