package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	fn()

	_ = w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	_ = r.Close()
	return buf.String()
}

func startMockIPCServer(t *testing.T) (string, *[]string, *sync.Mutex) {
	t.Helper()
	runtimeDir := t.TempDir()
	sockPath := filepath.Join(runtimeDir, "hydra.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	var mu sync.Mutex
	var recorded []string

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				cmd := strings.TrimSpace(line)
				mu.Lock()
				recorded = append(recorded, cmd)
				mu.Unlock()

				switch {
				case cmd == "STATUS":
					jobs := []models.UIJob{
						{
							ID:         "job_1",
							FileName:   "fedora.iso",
							Progress:   50.0,
							Downloaded: "50 MB",
							TotalSize:  "100 MB",
							Speed:      "10 MB/s",
							Status:     "DOWNLOADING",
						},
					}
					_ = json.NewEncoder(c).Encode(jobs)
				case cmd == "RULES":
					rules := map[string]storage.CategoryRule{
						"Compressed": {
							Path:       "~/Downloads/Compressed",
							Extensions: []string{"zip", "tar.gz"},
						},
					}
					_ = json.NewEncoder(c).Encode(rules)
				case cmd == "PAUSE_ALL":
					_, _ = c.Write([]byte("SUCCESS|Paused 1 active/queued job(s).\n"))
				case cmd == "RESUME_ALL":
					_, _ = c.Write([]byte("SUCCESS|Queued 1 paused job(s) to resume.\n"))
				case strings.HasPrefix(cmd, "PAUSE|"),
					strings.HasPrefix(cmd, "RESUME|"),
					strings.HasPrefix(cmd, "DELETE|"),
					strings.HasPrefix(cmd, "UPDATE_URL|"),
					strings.HasPrefix(cmd, "SET_RULE|"),
					strings.HasPrefix(cmd, "DELETE_RULE|"):
					_, _ = c.Write([]byte("SUCCESS|ok\n"))
				default:
					_, _ = c.Write([]byte("ERROR|Unknown command\n"))
				}
			}(conn)
		}
	}()

	return runtimeDir, &recorded, &mu
}

func TestGetSocketPathPrefersXDGRuntimeDir(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	want := filepath.Join(runtimeDir, "hydra.sock")
	if got := getSocketPath(); got != want {
		t.Fatalf("getSocketPath() without file = %q, want %q", got, want)
	}

	if err := os.WriteFile(want, nil, 0600); err != nil {
		t.Fatalf("create dummy socket file: %v", err)
	}
	if got := getSocketPath(); got != want {
		t.Fatalf("getSocketPath() with file = %q, want %q", got, want)
	}
}

func TestCLICommandHandlersAgainstUnixSocket(t *testing.T) {
	runtimeDir, recorded, mu := startMockIPCServer(t)
	sockPath := filepath.Join(runtimeDir, "hydra.sock")

	dial := func(t *testing.T) net.Conn {
		t.Helper()
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("dial %s: %v", sockPath, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	statusOut := captureStdout(t, func() {
		executeStatusRequest(dial(t))
	})
	if !strings.Contains(statusOut, "fedora.iso") || !strings.Contains(statusOut, "DOWNLOADING") {
		t.Fatalf("unexpected status output:\n%s", statusOut)
	}

	for _, action := range []string{"pause", "resume", "delete"} {
		out := captureStdout(t, func() {
			executeActionRequest(dial(t), action, "job_1")
		})
		if !strings.Contains(out, "successfully "+action+"d") {
			t.Fatalf("unexpected %s output:\n%s", action, out)
		}
	}

	pauseAllOut := captureStdout(t, func() {
		executeBulkActionRequest(dial(t), "PAUSE_ALL")
	})
	if !strings.Contains(pauseAllOut, "Paused 1 active/queued job(s)") {
		t.Fatalf("unexpected pause-all output:\n%s", pauseAllOut)
	}

	resumeAllOut := captureStdout(t, func() {
		executeBulkActionRequest(dial(t), "RESUME_ALL")
	})
	if !strings.Contains(resumeAllOut, "Queued 1 paused job(s) to resume") {
		t.Fatalf("unexpected resume-all output:\n%s", resumeAllOut)
	}

	updateURLOut := captureStdout(t, func() {
		executeUpdateURLRequest(dial(t), "job_1", "http://127.0.0.1:9001/new.iso")
	})
	if !strings.Contains(updateURLOut, "URL for job job_1 updated successfully") {
		t.Fatalf("unexpected update-url output:\n%s", updateURLOut)
	}

	rulesOut := captureStdout(t, func() {
		executeRulesRequest(dial(t))
	})
	if !strings.Contains(rulesOut, "Compressed") || !strings.Contains(rulesOut, "tar.gz") {
		t.Fatalf("unexpected rules output:\n%s", rulesOut)
	}

	setRuleOut := captureStdout(t, func() {
		executeSetRuleRequest(dial(t), "Videos", "~/Videos", "mp4,mkv")
	})
	if !strings.Contains(setRuleOut, "Category rule 'Videos' successfully configured") {
		t.Fatalf("unexpected set-rule output:\n%s", setRuleOut)
	}

	delRuleOut := captureStdout(t, func() {
		executeDeleteRuleRequest(dial(t), "Videos")
	})
	if !strings.Contains(delRuleOut, "Category rule 'Videos' successfully deleted") {
		t.Fatalf("unexpected delete-rule output:\n%s", delRuleOut)
	}

	mu.Lock()
	gotCount := len(*recorded)
	gotCommands := append([]string(nil), (*recorded)...)
	mu.Unlock()
	if gotCount != 10 {
		t.Fatalf("recorded %d IPC commands (%v), want 10", gotCount, gotCommands)
	}
}

func TestCLIBinarySubcommandsAndNativeHost(t *testing.T) {
	binPath := filepath.Join(t.TempDir(), "hydra")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build hydra-cli binary: %v\n%s", err, string(out))
	}

	runtimeDir, recorded, mu := startMockIPCServer(t)
	cfgDir := t.TempDir()
	dataDir := t.TempDir()
	env := append(os.Environ(),
		"XDG_RUNTIME_DIR="+runtimeDir,
		"XDG_CONFIG_HOME="+cfgDir,
		"XDG_DATA_HOME="+dataDir,
	)

	// No-arg usage output exits cleanly
	usageCmd := exec.Command(binPath)
	usageCmd.Env = env
	usageOut, err := usageCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hydra usage failed: %v\n%s", err, string(usageOut))
	}
	if !strings.Contains(string(usageOut), "hydra status") {
		t.Fatalf("expected usage text, got:\n%s", string(usageOut))
	}

	// Live socket subcommand roundtrip
	statusCmd := exec.Command(binPath, "status")
	statusCmd.Env = env
	statusOut, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hydra status failed: %v\n%s", err, string(statusOut))
	}
	if !strings.Contains(string(statusOut), "fedora.iso") {
		t.Fatalf("expected job_1 in hydra status output:\n%s", string(statusOut))
	}

	mu.Lock()
	commands := append([]string(nil), (*recorded)...)
	mu.Unlock()
	if len(commands) != 1 || commands[0] != "STATUS" {
		t.Fatalf("expected STATUS command from binary, got %v", commands)
	}

	// Native-host stdio framing through hydra native-host
	if err := os.MkdirAll(filepath.Join(cfgDir, "hydra"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "hydra", "api.token"), []byte("cli-native-tok\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var stdinBuf bytes.Buffer
	if err := storage.WriteNativeMessage(&stdinBuf, storage.NativeHostRequest{Action: "get_integration_info"}); err != nil {
		t.Fatalf("encode native request: %v", err)
	}
	nativeCmd := exec.Command(binPath, "native-host")
	nativeCmd.Env = env
	nativeCmd.Stdin = &stdinBuf
	nativeOut, err := nativeCmd.Output()
	if err != nil {
		t.Fatalf("hydra native-host failed: %v", err)
	}
	rawFrame, err := storage.ReadNativeMessage(bytes.NewReader(nativeOut))
	if err != nil {
		t.Fatalf("decode native-host response: %v", err)
	}
	var nativeResp storage.NativeHostResponse
	if err := json.Unmarshal(rawFrame, &nativeResp); err != nil {
		t.Fatalf("unmarshal native-host response: %v", err)
	}
	if !nativeResp.OK || nativeResp.Token != "cli-native-tok" {
		t.Fatalf("unexpected native-host response: %+v", nativeResp)
	}
}

