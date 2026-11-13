package main

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Raunak0000/Hydra/pkg/storage"
	"github.com/Raunak0000/Hydra/pkg/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	daemonURL := flag.String("daemon-url", "http://localhost:9000", "Hydra daemon URL")
	defaultToken := os.Getenv("HYDRA_API_TOKEN")
	daemonToken := flag.String("token", defaultToken, "Hydra daemon API token")
	ensureDaemon := flag.Bool("ensure-daemon", false, "Start local hydra-daemon automatically if not running")
	flag.Parse()

	if *ensureDaemon {
		ensureLocalDaemon(*daemonURL)
	}

	if *daemonToken == "" {
		if token, err := storage.LoadAPIToken(); err == nil && token != "" {
			*daemonToken = token
		}
	}

	if *daemonToken == "" {
		fmt.Fprintln(os.Stderr, "Error: set HYDRA_API_TOKEN, pass --token, or start hydra-daemon to generate an API token")
		os.Exit(1)
	}

	client := tui.NewDaemonClient(*daemonURL, *daemonToken)

	program := tea.NewProgram(
		tui.NewModel(client),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hydra-tui: %v\n", err)
		os.Exit(1)
	}
}

func daemonAddressFromURL(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "", false
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port), true
}

func isTCPReachable(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func findDaemonBinary() (string, error) {
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "hydra-daemon")
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return sibling, nil
		}
	}
	return exec.LookPath("hydra-daemon")
}

func ensureLocalDaemon(daemonURL string) {
	addr, ok := daemonAddressFromURL(daemonURL)
	if !ok {
		return
	}

	if isTCPReachable(addr) {
		if tok, err := storage.LoadAPIToken(); err == nil && tok != "" {
			return
		}
	}

	startedViaSystemd := false
	if _, err := exec.LookPath("systemctl"); err == nil {
		if exec.Command("systemctl", "--user", "show-environment").Run() == nil {
			if exec.Command("systemctl", "--user", "restart", "hydra.service").Run() == nil {
				startedViaSystemd = true
			}
		}
	}

	if !startedViaSystemd {
		if binPath, err := findDaemonBinary(); err == nil {
			devNull, openErr := os.OpenFile(os.DevNull, os.O_RDWR, 0)
			if openErr == nil {
				cmd := exec.Command(binPath, "--autostart")
				cmd.Stdin = devNull
				cmd.Stdout = devNull
				cmd.Stderr = devNull
				cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				_ = cmd.Start()
				_ = devNull.Close()
			}
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if isTCPReachable(addr) {
			if tok, err := storage.LoadAPIToken(); err == nil && tok != "" {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
