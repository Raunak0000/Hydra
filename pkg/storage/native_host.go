package storage

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MaxNativeMessageBytes limits WebExtension native messaging payloads safely.
	MaxNativeMessageBytes = 1024 * 1024
	// DefaultDaemonHTTPURL is the loopback daemon endpoint expected by browser clients.
	DefaultDaemonHTTPURL = "http://127.0.0.1:9000"
)

// NativeHostRequest represents a single message sent by the Hydra WebExtension.
type NativeHostRequest struct {
	Action string `json:"action"`
	JobID  string `json:"job_id,omitempty"`
	URL    string `json:"url,omitempty"`
}

// NativeHostResponse represents a structured reply returned to the WebExtension.
type NativeHostResponse struct {
	OK              bool   `json:"ok"`
	Action          string `json:"action,omitempty"`
	Protocol        int    `json:"protocol,omitempty"`
	DaemonURL       string `json:"daemon_url,omitempty"`
	DaemonRunning   bool   `json:"daemon_running"`
	Token           string `json:"token,omitempty"`
	TokenConfigured bool   `json:"token_configured"`
	GUIAvailable    bool   `json:"gui_available"`
	Started         bool   `json:"started,omitempty"`
	Error           string `json:"error,omitempty"`
}

// NativeHostDeps encapsulates side-effecting helpers for clean testing.
type NativeHostDeps struct {
	DaemonURL         string
	LoadToken         func() (string, error)
	EnsureToken       func() (string, error)
	IsDaemonReachable func() bool
	StartDaemon       func() error
	LaunchGUI         func(jobID string, downloadURL string) error
	OpenDownloads     func() error
	FindGUIBinary     func() string
}

// DefaultNativeHostDeps returns production dependencies backed by local Hydra binaries and XDG state.
func DefaultNativeHostDeps() NativeHostDeps {
	return NativeHostDeps{
		DaemonURL:         DefaultDaemonHTTPURL,
		LoadToken:         LoadAPIToken,
		EnsureToken:       EnsureAPIToken,
		IsDaemonReachable: isLocalDaemonHealthy,
		StartDaemon:       startLocalDaemonProcess,
		LaunchGUI:         launchHydraGUIProcess,
		OpenDownloads:     openDefaultDownloadsFolder,
		FindGUIBinary:     func() string { return findSiblingOrPathBinary("hydra-gui") },
	}
}

func findSiblingOrPathBinary(name string) string {
	if exe, err := os.Executable(); err == nil && exe != "" {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return candidate
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func isLocalDaemonHealthy() bool {
	client := &http.Client{Timeout: 600 * time.Millisecond}
	resp, err := client.Get(DefaultDaemonHTTPURL + "/api/health")
	if err == nil {
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}

	conn, dialErr := net.DialTimeout("tcp", "127.0.0.1:9000", 350*time.Millisecond)
	if dialErr != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func startLocalDaemonProcess() error {
	if systemctlPath, err := exec.LookPath("systemctl"); err == nil {
		probe := exec.Command(systemctlPath, "--user", "show-environment")
		if probe.Run() == nil {
			startCmd := exec.Command(systemctlPath, "--user", "start", "hydra.service")
			if startCmd.Run() == nil {
				return nil
			}
		}
	}

	daemonBin := findSiblingOrPathBinary("hydra-daemon")
	if daemonBin == "" {
		return fmt.Errorf("hydra-daemon binary not found in PATH or beside hydra")
	}

	cmd := exec.Command(daemonBin, "--autostart")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

func launchHydraGUIProcess(jobID string, downloadURL string) error {
	guiBin := findSiblingOrPathBinary("hydra-gui")
	if guiBin == "" {
		return fmt.Errorf("hydra-gui binary not found")
	}

	args := []string{"--ensure-daemon"}
	if strings.TrimSpace(jobID) != "" {
		args = append(args, "--select-job", strings.TrimSpace(jobID))
	} else if strings.TrimSpace(downloadURL) != "" {
		args = append(args, "--new-download", "--url", strings.TrimSpace(downloadURL))
	}

	cmd := exec.Command(guiBin, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

func openDefaultDownloadsFolder() error {
	xdgOpen, err := exec.LookPath("xdg-open")
	if err != nil {
		return fmt.Errorf("xdg-open not available")
	}

	cfg := GetConfig()
	target := cfg.DefaultSavePath
	if target == "" {
		target = GetDefaultDownloadsDir()
	}
	resolved, resolveErr := ResolvePath(target + "/")
	if resolveErr != nil {
		resolved = GetDefaultDownloadsDir()
	}

	cmd := exec.Command(xdgOpen, resolved)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}

// ReadNativeMessage reads a length-prefixed JSON frame from a WebExtension browser stream.
func ReadNativeMessage(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}

	msgLen := binary.LittleEndian.Uint32(lenBuf[:])
	if msgLen == 0 {
		return nil, fmt.Errorf("empty native message frame")
	}
	if msgLen > MaxNativeMessageBytes {
		return nil, fmt.Errorf("native message frame exceeds %d bytes (%d)", MaxNativeMessageBytes, msgLen)
	}

	payload := make([]byte, msgLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// WriteNativeMessage encodes and writes a length-prefixed JSON frame to a WebExtension browser stream.
func WriteNativeMessage(w io.Writer, msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal native message: %w", err)
	}
	if len(payload) > MaxNativeMessageBytes {
		return fmt.Errorf("native message reply exceeds %d bytes", MaxNativeMessageBytes)
	}

	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// HandleNativeHostRequest processes a single browser bridge command using the supplied dependencies.
func HandleNativeHostRequest(req NativeHostRequest, deps NativeHostDeps) NativeHostResponse {
	daemonURL := deps.DaemonURL
	if daemonURL == "" {
		daemonURL = DefaultDaemonHTTPURL
	}

	guiAvailable := deps.FindGUIBinary != nil && deps.FindGUIBinary() != ""
	action := strings.ToLower(strings.TrimSpace(req.Action))

	switch action {
	case "ping":
		return NativeHostResponse{
			OK:           true,
			Action:       "ping",
			Protocol:     1,
			DaemonURL:    daemonURL,
			GUIAvailable: guiAvailable,
		}

	case "get_integration_info":
		var token string
		if deps.LoadToken != nil {
			tok, err := deps.LoadToken()
			if err == nil {
				token = strings.TrimSpace(tok)
			}
		}
		running := deps.IsDaemonReachable != nil && deps.IsDaemonReachable()
		return NativeHostResponse{
			OK:              true,
			Action:          "get_integration_info",
			Protocol:        1,
			DaemonURL:       daemonURL,
			DaemonRunning:   running,
			Token:           token,
			TokenConfigured: token != "",
			GUIAvailable:    guiAvailable,
		}

	case "ensure_daemon":
		var token string
		if deps.EnsureToken != nil {
			tok, err := deps.EnsureToken()
			if err != nil {
				return NativeHostResponse{
					OK:           false,
					Action:       "ensure_daemon",
					DaemonURL:    daemonURL,
					GUIAvailable: guiAvailable,
					Error:        err.Error(),
				}
			}
			token = strings.TrimSpace(tok)
		} else if deps.LoadToken != nil {
			tok, _ := deps.LoadToken()
			token = strings.TrimSpace(tok)
		}

		if deps.IsDaemonReachable != nil && deps.IsDaemonReachable() {
			return NativeHostResponse{
				OK:              true,
				Action:          "ensure_daemon",
				DaemonURL:       daemonURL,
				DaemonRunning:   true,
				Token:           token,
				TokenConfigured: token != "",
				GUIAvailable:    guiAvailable,
				Started:         false,
			}
		}

		if deps.StartDaemon == nil {
			return NativeHostResponse{
				OK:              false,
				Action:          "ensure_daemon",
				DaemonURL:       daemonURL,
				Token:           token,
				TokenConfigured: token != "",
				GUIAvailable:    guiAvailable,
				Error:           "daemon launcher is unavailable",
			}
		}

		if err := deps.StartDaemon(); err != nil {
			return NativeHostResponse{
				OK:              false,
				Action:          "ensure_daemon",
				DaemonURL:       daemonURL,
				Token:           token,
				TokenConfigured: token != "",
				GUIAvailable:    guiAvailable,
				Error:           err.Error(),
			}
		}

		running := false
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if deps.IsDaemonReachable != nil && deps.IsDaemonReachable() {
				running = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		resp := NativeHostResponse{
			OK:              running,
			Action:          "ensure_daemon",
			DaemonURL:       daemonURL,
			DaemonRunning:   running,
			Token:           token,
			TokenConfigured: token != "",
			GUIAvailable:    guiAvailable,
			Started:         true,
		}
		if !running {
			resp.Error = "started hydra-daemon, but HTTP endpoint did not become ready in time"
		}
		return resp

	case "open_gui":
		if deps.LaunchGUI == nil {
			return NativeHostResponse{
				OK:     false,
				Action: "open_gui",
				Error:  "GUI launcher unavailable",
			}
		}
		if err := deps.LaunchGUI(req.JobID, req.URL); err != nil {
			return NativeHostResponse{
				OK:     false,
				Action: "open_gui",
				Error:  err.Error(),
			}
		}
		return NativeHostResponse{
			OK:           true,
			Action:       "open_gui",
			GUIAvailable: true,
		}

	case "open_downloads":
		if deps.OpenDownloads == nil {
			return NativeHostResponse{
				OK:     false,
				Action: "open_downloads",
				Error:  "desktop opener unavailable",
			}
		}
		if err := deps.OpenDownloads(); err != nil {
			return NativeHostResponse{
				OK:     false,
				Action: "open_downloads",
				Error:  err.Error(),
			}
		}
		return NativeHostResponse{
			OK:     true,
			Action: "open_downloads",
		}

	default:
		return NativeHostResponse{
			OK:     false,
			Action: action,
			Error:  fmt.Sprintf("unknown native host action: %q", req.Action),
		}
	}
}

// ServeNativeMessaging handles length-framed WebExtension JSON messages until EOF.
func ServeNativeMessaging(r io.Reader, w io.Writer, deps NativeHostDeps) error {
	for {
		raw, err := ReadNativeMessage(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		var req NativeHostRequest
		var resp NativeHostResponse
		if unmarshalErr := json.Unmarshal(raw, &req); unmarshalErr != nil {
			resp = NativeHostResponse{
				OK:    false,
				Error: fmt.Sprintf("invalid JSON request: %v", unmarshalErr),
			}
		} else {
			resp = HandleNativeHostRequest(req, deps)
		}

		if writeErr := WriteNativeMessage(w, resp); writeErr != nil {
			return writeErr
		}
	}
}

