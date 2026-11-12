package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
	"github.com/Raunak0000/Hydra/pkg/tui"
	tea "github.com/charmbracelet/bubbletea"
)

type e2eEnv struct {
	daemonURL   string
	token       string
	cliBin      string
	downloadDir string
	payloadURL  string
	payloadBytes []byte
	payloadSHA   string
	dbStore     *storage.DBStore
}

func makeDeterministicPayload(size int, seed byte) ([]byte, string) {
	buf := make([]byte, size)
	for i := range buf {
		buf[i] = byte((i*31 + int(seed)) % 251)
	}
	sum := sha256.Sum256(buf)
	return buf, hex.EncodeToString(sum[:])
}

func setupIsolatedDaemonE2E(t *testing.T) e2eEnv {
	t.Helper()

	baseDir := t.TempDir()
	cliBin := filepath.Join(baseDir, "hydra")
	buildCmd := exec.Command("go", "build", "-o", cliBin, "./cmd/hydra-cli")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build hydra-cli binary: %v\n%s", err, string(out))
	}

	configHome := filepath.Join(baseDir, "config")
	dataHome := filepath.Join(baseDir, "data")
	runtimeDir := filepath.Join(baseDir, "runtime")
	homeDir := filepath.Join(baseDir, "home")
	dlDir := filepath.Join(homeDir, "Downloads")
	for _, dir := range []string{configHome, dataHome, runtimeDir, dlDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("HYDRA_API_TOKEN", "")

	token, err := storage.EnsureAPIToken()
	if err != nil {
		t.Fatalf("EnsureAPIToken: %v", err)
	}

	dbStore, err := storage.GetDBStore()
	if err != nil {
		t.Fatalf("GetDBStore: %v", err)
	}

	storage.GlobalCancelMap = make(map[string]context.CancelFunc)
	storage.GlobalCancelMutex = &sync.Mutex{}

	executeDownloadJob := newExecuteDownloadJob(dbStore)
	storage.InitQueueManager(2, executeDownloadJob)
	t.Cleanup(func() {
		if qm := storage.GetQueueManager(); qm != nil {
			qm.Stop()
		}
	})

	sockPath := storage.GetSocketPath()
	ipcListener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sockPath, err)
	}
	t.Cleanup(func() {
		_ = ipcListener.Close()
		_ = os.Remove(sockPath)
	})

	go storage.ServeIPCListener(ipcListener, func(url, savePath, jobID string) {
		if err := executeDownloadJob(url, savePath, jobID, nil); err != nil {
			currentJob, exists := dbStore.GetJob(jobID)
			if !exists || currentJob.Status == "PAUSED" || currentJob.Status == "QUEUED" {
				return
			}
			_ = dbStore.UpdateErrorMessage(jobID, err.Error())
			_ = dbStore.UpdateStatus(jobID, "FAILED")
			storage.GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
		}
	})

	server := storage.NewServer(executeDownloadJob)
	httpSrv := httptest.NewServer(server.Router)
	t.Cleanup(httpSrv.Close)

	payload, payloadSHA := makeDeterministicPayload(128*1024, 7)
	fileServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release-bundle.tar.gz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/gzip")

		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			return
		}

		rangeHdr := r.Header.Get("Range")
		if strings.HasPrefix(rangeHdr, "bytes=") {
			parts := strings.SplitN(strings.TrimPrefix(rangeHdr, "bytes="), "-", 2)
			start, err1 := strconv.ParseInt(parts[0], 10, 64)
			end := int64(len(payload) - 1)
			if len(parts) == 2 && parts[1] != "" {
				parsedEnd, err2 := strconv.ParseInt(parts[1], 10, 64)
				if err2 == nil && parsedEnd < end {
					end = parsedEnd
				}
			}
			if err1 == nil && start >= 0 && start <= end && end < int64(len(payload)) {
				slice := payload[start : end+1]
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
				w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(slice)
				return
			}
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(fileServer.Close)

	return e2eEnv{
		daemonURL:    httpSrv.URL,
		token:        token,
		cliBin:       cliBin,
		downloadDir:  dlDir,
		payloadURL:   fileServer.URL + "/release-bundle.tar.gz",
		payloadBytes: payload,
		payloadSHA:   payloadSHA,
		dbStore:      dbStore,
	}
}

func waitForJobStatus(t *testing.T, store *storage.DBStore, jobID, wantStatus string, timeout time.Duration) models.UIJob {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if job, ok := store.GetJob(jobID); ok {
			if job.Status == wantStatus {
				return job
			}
			if job.Status == "FAILED" && wantStatus != "FAILED" {
				t.Fatalf("job %s unexpectedly FAILED: %s", jobID, job.ErrorMessage)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	job, _ := store.GetJob(jobID)
	t.Fatalf("timed out waiting for job %s to reach %q (last status=%q, err=%q)", jobID, wantStatus, job.Status, job.ErrorMessage)
	return models.UIJob{}
}

func runExtensionE2EStep(t *testing.T, env e2eEnv, extSavePath string) string {
	t.Helper()

	// 1. Native Messaging host handshake via ServeNativeMessaging against live daemon
	var inBuf bytes.Buffer
	if err := storage.WriteNativeMessage(&inBuf, storage.NativeHostRequest{Action: "get_integration_info"}); err != nil {
		t.Fatalf("WriteNativeMessage: %v", err)
	}
	var outBuf bytes.Buffer
	deps := storage.NativeHostDeps{
		DaemonURL: env.daemonURL,
		LoadToken: storage.LoadAPIToken,
		IsDaemonReachable: func() bool {
			resp, err := http.Get(env.daemonURL + "/api/health")
			if err != nil {
				return false
			}
			_ = resp.Body.Close()
			return resp.StatusCode == http.StatusOK
		},
		FindGUIBinary: func() string { return "hydra-gui" },
	}
	if err := storage.ServeNativeMessaging(&inBuf, &outBuf, deps); err != nil {
		t.Fatalf("ServeNativeMessaging: %v", err)
	}
	frame, err := storage.ReadNativeMessage(&outBuf)
	if err != nil {
		t.Fatalf("ReadNativeMessage: %v", err)
	}
	var nativeResp storage.NativeHostResponse
	if err := json.Unmarshal(frame, &nativeResp); err != nil {
		t.Fatalf("unmarshal native response: %v", err)
	}
	if !nativeResp.OK || !nativeResp.DaemonRunning || nativeResp.Token != env.token {
		t.Fatalf("unexpected native host info: %+v", nativeResp)
	}

	// 2. Firefox WebExtension JS (extension/shared.js) -> live daemon HTTP API with moz-extension Origin
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node required for Firefox extension E2E verification: %v", err)
	}

	script := fmt.Sprintf(`
import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";

const shared = await import(pathToFileURL(%q).href);
const daemonURL = %q;
const token = %q;
const payloadURL = %q;
const savePath = %q;
const expectedSHA = %q;
const extOrigin = "moz-extension://12345678-1234-1234-1234-123456789abc";

const match = shared.evaluateResponseForInterception({
	url: payloadURL,
	responseHeaders: [
		{ name: "Content-Type", value: "application/gzip" },
		{ name: "Content-Disposition", value: "attachment; filename=\"release-bundle.tar.gz\"" },
		{ name: "Content-Length", value: "131072" },
	],
	settings: {
		daemonURL,
		excludedHosts: [],
		autoCapture: true,
	},
});
assert.equal(match.intercept, true, match.reason || "expected intercept");

const diskPre = await fetch(daemonURL + "/api/disk-space", {
	method: "POST",
	headers: {
		"Origin": extOrigin,
		"Content-Type": "application/json",
		"X-Hydra-Token": token,
	},
	body: JSON.stringify({ path: savePath, required_bytes: 131072 }),
});
assert.equal(diskPre.status, 200);
assert.equal(diskPre.headers.get("access-control-allow-origin"), extOrigin);
const diskJson = await diskPre.json();
assert.equal(diskJson.enough_space, true);

const body = shared.buildDownloadPayload(
	{
		url: payloadURL,
		filename: match.filename,
		capturedHeaders: { "User-Agent": "Firefox-E2E/1.0" },
	},
	{
		filename: "release-bundle.tar.gz",
		destinationDir: %q,
		startMode: "paused",
		checksumAlgo: "sha256",
		expectedChecksum: expectedSHA,
	}
);

const res = await fetch(daemonURL + "/download", {
	method: "POST",
	headers: {
		"Origin": extOrigin,
		"Content-Type": "application/json",
		"X-Hydra-Token": token,
	},
	body: JSON.stringify(body),
});
assert.equal(res.status, 202);
const created = await res.json();
assert.equal(created.status, "PAUSED");
assert.ok(created.job_id);
console.log(created.job_id);
`, filepath.Join("extension", "shared.js"), env.daemonURL, env.token, env.payloadURL, extSavePath, env.payloadSHA, env.downloadDir)

	cmd := exec.Command(nodeBin, "--input-type=module", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Firefox extension E2E failed: %v\n%s", err, string(out))
	}

	jobID := strings.TrimSpace(string(out))
	if jobID == "" {
		t.Fatal("Firefox extension step returned empty job_id")
	}
	return jobID
}

func runCLIE2EStep(t *testing.T, env e2eEnv, jobID string) {
	t.Helper()

	runCLI := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(env.cliBin, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hydra %v failed: %v\n%s", args, err, string(out))
		}
		return string(out)
	}

	// Verify CLI sees the PAUSED job submitted by the Firefox extension
	statusOut := runCLI("status")
	if !strings.Contains(statusOut, jobID) || !strings.Contains(statusOut, "PAUSED") {
		t.Fatalf("hydra status did not list extension job %s as PAUSED:\n%s", jobID, statusOut)
	}

	// Verify CLI rules management over UNIX IPC
	runCLI("set-rule", "E2EArchives", env.downloadDir, "tar.gz")
	rulesOut := runCLI("rules")
	if !strings.Contains(rulesOut, "E2EArchives") {
		t.Fatalf("hydra rules did not list E2EArchives:\n%s", rulesOut)
	}
	runCLI("delete-rule", "E2EArchives")

	// Resume the Firefox extension job via CLI -> daemon finishes downloading + verifies SHA-256
	resumeOut := runCLI("resume", jobID)
	if !strings.Contains(resumeOut, "resumed") {
		t.Fatalf("unexpected hydra resume output:\n%s", resumeOut)
	}
}

func runTUIE2EStep(t *testing.T, env e2eEnv, extJobID string, tuiSavePath string) string {
	t.Helper()

	client := tui.NewDaemonClient(env.daemonURL, env.token)
	tuiJobID, err := client.CreateDownload(tui.CreateDownloadRequest{
		URL:              env.payloadURL,
		SavePath:         tuiSavePath,
		Filename:         "tui-bundle.tar.gz",
		ExpectedChecksum: env.payloadSHA,
		ChecksumAlgo:     "sha256",
	})
	if err != nil {
		t.Fatalf("TUI DaemonClient.CreateDownload failed: %v", err)
	}

	waitForJobStatus(t, env.dbStore, tuiJobID, "COMPLETED", 10*time.Second)

	// Drive tui.Model lifecycle headlessly: trigger refresh ('r') -> fetchJobs -> render rows & navigate
	var m tea.Model = tui.NewModel(client)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m, refreshCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if refreshCmd == nil {
		t.Fatal("tui.Model refresh key did not return fetch cmd")
	}
	updated, _ := m.Update(refreshCmd())
	view := updated.View()
	if !strings.Contains(view, "release-bundle.tar.gz") || !strings.Contains(view, "tui-bundle.tar.gz") {
		t.Fatalf("TUI view did not render both extension and TUI jobs:\n%s", view)
	}

	// Exercise TUI keyboard navigation on live jobs
	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	_ = updated.View()

	if _, err := client.GetJobs(); err != nil {
		t.Fatalf("TUI DaemonClient.GetJobs after Init failed: %v", err)
	}
	_ = extJobID
	return tuiJobID
}

func runDesktopAndTrayE2EStep(t *testing.T, env e2eEnv, guiSavePath string) {
	t.Helper()

	pyBin, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 required for Desktop UI / Tray E2E verification: %v", err)
	}

	script := fmt.Sprintf(`
import importlib.machinery
import importlib.util
import sys
import threading
import time
from pathlib import Path

repo = Path.cwd()
sys.path.insert(0, str(repo / "packaging" / "gui"))

from hydra_gui.client import HydraGUIClient, SSEWatcher, resolve_token

loader = importlib.machinery.SourceFileLoader("hydra_tray", str(repo / "packaging" / "tray" / "hydra-tray"))
spec = importlib.util.spec_from_loader("hydra_tray", loader)
tray = importlib.util.module_from_spec(spec)
loader.exec_module(tray)

daemon_url = %q
expected_token = %q
payload_url = %q
gui_save_path = %q
expected_sha = %q

# 1. Both Desktop GUI and Tray resolve the daemon-generated api.token automatically
assert resolve_token() == expected_token, "GUI resolve_token mismatch"
assert tray.resolve_token() == expected_token, "Tray resolve_token mismatch"

gui = HydraGUIClient(daemon_url)
tray_client = tray.HydraClient(daemon_url)

# 2. Both see the jobs already completed via Extension+CLI and TUI
ok, gui_jobs, err = gui.get_jobs()
assert ok, f"gui.get_jobs failed: {err}"
assert len(gui_jobs) >= 2, f"expected >=2 jobs in GUI, got {len(gui_jobs)}"

ok_settings, settings, err_settings = gui.get_settings_with_status()
assert ok_settings and isinstance(settings, dict), f"gui.get_settings_with_status failed: {err_settings}"

tray_online, tray_jobs = tray_client.get_jobs()
assert tray_online and len(tray_jobs) >= 2, f"tray get_jobs failed: online={tray_online}"
summary = tray.summarize_jobs(tray_jobs, online=True)
assert summary["completed"] >= 2, f"unexpected tray summary: {summary}"

# 3. SSEWatcher receives live queue snapshots when GUI creates a paused job
sse_events = []
sse_ready = threading.Event()

def on_sse(jobs):
    sse_events.append(jobs)
    if any(j.get("file_name") == "gui-bundle.tar.gz" for j in jobs):
        sse_ready.set()

watcher = SSEWatcher(gui, on_sse)
watcher.start()
try:
    ok_create, gui_job_id, create_err = gui.create_download(
        payload_url,
        gui_save_path,
        filename="gui-bundle.tar.gz",
        expected_checksum=expected_sha,
        checksum_algo="sha256",
        start_paused=True,
    )
    assert ok_create and gui_job_id, f"gui.create_download failed: {create_err}"
    assert sse_ready.wait(timeout=5.0), f"SSEWatcher did not receive gui-bundle.tar.gz (events={len(sse_events)})"
finally:
    watcher.stop()

# 4. Tray resumes the GUI-created job via /api/jobs/<id>/resume and GUI observes completion
assert tray_client.resume_job(gui_job_id), "tray resume_job failed"

deadline = time.monotonic() + 10.0
completed = False
while time.monotonic() < deadline:
    ok, jobs, _ = gui.get_jobs()
    if ok:
        match = [j for j in jobs if j.get("id") == gui_job_id]
        if match and match[0].get("status") == "COMPLETED":
            assert match[0].get("checksum_verified") is True, f"expected checksum_verified=True: {match[0]}"
            completed = True
            break
    time.sleep(0.05)

assert completed, f"GUI-created job {gui_job_id} did not complete after tray resume"

# 5. Tray bulk actions and GUI cleanup work cleanly
assert tray_client.pause_all()
assert tray_client.resume_all()
ok_del, del_err = gui.delete_job(gui_job_id, keep_file=True)
assert ok_del, f"gui.delete_job failed: {del_err}"
`, env.daemonURL, env.token, env.payloadURL, guiSavePath, env.payloadSHA)

	cmd := exec.Command(pyBin, "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Desktop GUI + Tray E2E failed: %v\n%s", err, string(out))
	}
}

func TestEndToEndDaemonCLITUIDesktopAndExtensionTogether(t *testing.T) {
	env := setupIsolatedDaemonE2E(t)

	extSavePath := filepath.Join(env.downloadDir, "release-bundle.tar.gz")
	tuiSavePath := filepath.Join(env.downloadDir, "tui-bundle.tar.gz")
	guiSavePath := filepath.Join(env.downloadDir, "gui-bundle.tar.gz")

	// 1. Firefox extension intercepts payload, validates disk space, and submits paused download
	extJobID := runExtensionE2EStep(t, env, extSavePath)

	// 2. CLI inspects live queue, manages routing rules, and resumes the Firefox extension job
	runCLIE2EStep(t, env, extJobID)

	// 3. Daemon completes download, checks SHA-256, and writes exact payload bytes to disk
	completedExtJob := waitForJobStatus(t, env.dbStore, extJobID, "COMPLETED", 10*time.Second)
	if !completedExtJob.ChecksumVerified {
		t.Fatalf("expected extension job %s ChecksumVerified=true", extJobID)
	}
	gotExtBytes, err := os.ReadFile(extSavePath)
	if err != nil {
		t.Fatalf("read extension downloaded file %s: %v", extSavePath, err)
	}
	if !bytes.Equal(gotExtBytes, env.payloadBytes) {
		t.Fatalf("extension downloaded bytes mismatch: got %d bytes, want %d", len(gotExtBytes), len(env.payloadBytes))
	}

	// 4. TUI client + BubbleTea model interacts with the same live daemon and adds a second download
	_ = runTUIE2EStep(t, env, extJobID, tuiSavePath)
	gotTUIBytes, err := os.ReadFile(tuiSavePath)
	if err != nil {
		t.Fatalf("read TUI downloaded file %s: %v", tuiSavePath, err)
	}
	if !bytes.Equal(gotTUIBytes, env.payloadBytes) {
		t.Fatalf("TUI downloaded bytes mismatch: got %d bytes, want %d", len(gotTUIBytes), len(env.payloadBytes))
	}

	// 5. Desktop GUI client (including live SSEWatcher) & Tray client share the same daemon
	runDesktopAndTrayE2EStep(t, env, guiSavePath)
	gotGUIBytes, err := os.ReadFile(guiSavePath)
	if err != nil {
		t.Fatalf("read GUI downloaded file %s: %v", guiSavePath, err)
	}
	if !bytes.Equal(gotGUIBytes, env.payloadBytes) {
		t.Fatalf("GUI downloaded bytes mismatch: got %d bytes, want %d", len(gotGUIBytes), len(env.payloadBytes))
	}
}
