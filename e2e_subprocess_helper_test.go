package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

type testDaemonEnv struct {
	homeDir     string
	configDir   string
	dataDir     string
	runtimeDir  string
	downloadDir string
	apiToken    string
	daemonBin   string
	cliBin      string
}

type runningDaemon struct {
	cmd      *exec.Cmd
	httpAddr string
	baseURL  string
	sockPath string
	apiToken string
	logs     *bytes.Buffer
	logsMu   *sync.Mutex
}

func (d *runningDaemon) waitHealthy(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(d.baseURL + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if conn, err := net.DialTimeout("unix", d.sockPath, 200*time.Millisecond); err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	d.logsMu.Lock()
	out := d.logs.String()
	d.logsMu.Unlock()
	t.Fatalf("daemon at %s / %s did not become healthy within %s\nlogs:\n%s", d.baseURL, d.sockPath, timeout, out)
}

func (d *runningDaemon) requestJSON(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s body: %v", path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, d.baseURL+path, reader)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	req.Header.Set("X-Hydra-Token", d.apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	return resp.StatusCode, respBytes
}

func (d *runningDaemon) listJobs(t *testing.T) []models.UIJob {
	t.Helper()
	code, raw := d.requestJSON(t, http.MethodGet, "/api/jobs", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/jobs status=%d body=%s", code, string(raw))
	}
	var jobs []models.UIJob
	if err := json.Unmarshal(raw, &jobs); err != nil {
		t.Fatalf("decode jobs: %v (body=%s)", err, string(raw))
	}
	return jobs
}

func (d *runningDaemon) waitForJob(t *testing.T, jobID string, timeout time.Duration, pred func(models.UIJob) bool) models.UIJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last models.UIJob
	var found bool
	for time.Now().Before(deadline) {
		for _, job := range d.listJobs(t) {
			if job.ID == jobID {
				last = job
				found = true
				if pred(job) {
					return job
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	d.logsMu.Lock()
	out := d.logs.String()
	d.logsMu.Unlock()
	t.Fatalf("job %s did not match predicate within %s (found=%v last=%+v)\nlogs:\n%s", jobID, timeout, found, last, out)
	return models.UIJob{}
}

type syncBuffer struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (s syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func setupIsolatedSubprocessEnv(t *testing.T, token string) testDaemonEnv {
	t.Helper()
	base := t.TempDir()
	binDir := filepath.Join(base, "bin")
	daemonBin := filepath.Join(binDir, "hydra-daemon")
	cliBin := filepath.Join(binDir, "hydra-cli")

	buildDaemon := exec.Command("go", "build", "-o", daemonBin, ".")
	if out, err := buildDaemon.CombinedOutput(); err != nil {
		t.Fatalf("go build daemon: %v\n%s", err, string(out))
	}
	buildCLI := exec.Command("go", "build", "-o", cliBin, "./cmd/hydra-cli")
	if out, err := buildCLI.CombinedOutput(); err != nil {
		t.Fatalf("go build cli: %v\n%s", err, string(out))
	}

	env := testDaemonEnv{
		homeDir:     filepath.Join(base, "home"),
		configDir:   filepath.Join(base, "config"),
		dataDir:     filepath.Join(base, "data"),
		runtimeDir:  filepath.Join(base, "runtime"),
		downloadDir: filepath.Join(base, "downloads"),
		apiToken:    token,
		daemonBin:   daemonBin,
		cliBin:      cliBin,
	}
	for _, dir := range []string{env.homeDir, env.configDir, env.dataDir, env.runtimeDir, env.downloadDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return env
}

func (e testDaemonEnv) subprocessEnv(httpAddr string) []string {
	filtered := make([]string, 0, len(os.Environ())+8)
	skipPrefixes := []string{
		"HOME=",
		"XDG_CONFIG_HOME=",
		"XDG_DATA_HOME=",
		"XDG_RUNTIME_DIR=",
		"HYDRA_API_TOKEN=",
		"HYDRA_HTTP_ADDR=",
		"HYDRA_ENABLE_NOTIFICATIONS=",
	}
	for _, kv := range os.Environ() {
		skip := false
		for _, prefix := range skipPrefixes {
			if strings.HasPrefix(kv, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, kv)
		}
	}
	return append(
		filtered,
		"HOME="+e.homeDir,
		"XDG_CONFIG_HOME="+e.configDir,
		"XDG_DATA_HOME="+e.dataDir,
		"XDG_RUNTIME_DIR="+e.runtimeDir,
		"HYDRA_API_TOKEN="+e.apiToken,
		"HYDRA_HTTP_ADDR="+httpAddr,
		"HYDRA_ENABLE_NOTIFICATIONS=0",
	)
}

func reserveEphemeralLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1:0: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func (e testDaemonEnv) startDaemon(t *testing.T) *runningDaemon {
	t.Helper()
	httpAddr := reserveEphemeralLoopbackAddr(t)
	cmd := exec.Command(e.daemonBin)
	cmd.Env = e.subprocessEnv(httpAddr)
	var mu sync.Mutex
	var buf bytes.Buffer
	sb := syncBuffer{mu: &mu, buf: &buf}
	cmd.Stdout = sb
	cmd.Stderr = sb

	if err := cmd.Start(); err != nil {
		t.Fatalf("start hydra-daemon: %v", err)
	}

	d := &runningDaemon{
		cmd:      cmd,
		httpAddr: httpAddr,
		baseURL:  "http://" + httpAddr,
		sockPath: filepath.Join(e.runtimeDir, "hydra.sock"),
		apiToken: e.apiToken,
		logs:     &buf,
		logsMu:   &mu,
	}
	t.Cleanup(func() {
		if d.cmd.Process != nil && d.cmd.ProcessState == nil {
			_ = d.cmd.Process.Kill()
			_ = d.cmd.Wait()
		}
	})
	d.waitHealthy(t, 8*time.Second)
	return d
}

func (e testDaemonEnv) runCLI(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(e.cliBin, args...)
	cmd.Env = e.subprocessEnv("")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hydra-cli %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func enqueueJobHTTP(t *testing.T, d *runningDaemon, fileURL, savePath string) string {
	t.Helper()
	code, raw := d.requestJSON(t, http.MethodPost, "/api/jobs", map[string]any{
		"url":       fileURL,
		"save_path": savePath,
	})
	if code != http.StatusAccepted && code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("enqueue %s status=%d body=%s", fileURL, code, string(raw))
	}
	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode enqueue response: %v (body=%s)", err, string(raw))
	}
	if resp.JobID == "" {
		t.Fatalf("empty job_id in enqueue response: %s", string(raw))
	}
	return resp.JobID
}

// serveGatedRangeFile serves a Range-capable payload that streams initialBytes cleanly
// and then waits on gateCh before sending remaining bytes while gated.Load() is true.
func serveGatedRangeFile(
	t *testing.T,
	payload []byte,
	initialBytes int64,
	gated *atomic.Bool,
	gateCh <-chan struct{},
	nonZeroRangeSeen *atomic.Bool,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total := int64(len(payload))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")

		start := int64(0)
		end := total - 1
		isPartial := false
		if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
			spec := strings.TrimPrefix(rangeHdr, "bytes=")
			parts := strings.SplitN(spec, "-", 2)
			if len(parts) == 2 && parts[0] != "" {
				parsedStart, err := strconv.ParseInt(parts[0], 10, 64)
				if err == nil && parsedStart >= 0 && parsedStart < total {
					start = parsedStart
					if start > 0 && nonZeroRangeSeen != nil {
						nonZeroRangeSeen.Store(true)
					}
					if parts[1] != "" {
						if parsedEnd, err := strconv.ParseInt(parts[1], 10, 64); err == nil && parsedEnd < total {
							end = parsedEnd
						}
					}
					isPartial = true
				}
			}
		}

		length := end - start + 1
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if isPartial {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		if r.Method == http.MethodHead {
			return
		}

		flusher, hasFlusher := w.(http.Flusher)
		chunk := payload[start : end+1]
		if gated.Load() && initialBytes > 0 && int64(len(chunk)) > initialBytes {
			if _, err := w.Write(chunk[:initialBytes]); err != nil {
				return
			}
			if hasFlusher {
				flusher.Flush()
			}
			select {
			case <-gateCh:
			case <-r.Context().Done():
				return
			}
			chunk = chunk[initialBytes:]
		}

		const blockSize = 32 * 1024
		for len(chunk) > 0 {
			n := len(chunk)
			if n > blockSize {
				n = blockSize
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			chunk = chunk[n:]
		}
		if hasFlusher {
			flusher.Flush()
		}
	}))
}

