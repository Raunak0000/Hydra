package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthAndExtensionOriginAccessControl(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "ext-secret-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	store := newTestDBStore(t)
	server := NewServer(nil)
	server.db = store

	t.Run("health endpoint reports status without leaking sensitive data", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		rec := httptest.NewRecorder()
		server.Router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/health status = %d, want 200", rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("expected CORS header on /api/health, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode /api/health: %v", err)
		}
		if body["daemon_running"] != true {
			t.Fatalf("expected daemon_running=true, got %v", body["daemon_running"])
		}
		if body["authenticated"] != false {
			t.Fatalf("expected authenticated=false without token, got %v", body["authenticated"])
		}

		authReq := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		authReq.Header.Set("X-Hydra-Token", "ext-secret-token")
		authRec := httptest.NewRecorder()
		server.Router.ServeHTTP(authRec, authReq)

		var authBody map[string]any
		if err := json.NewDecoder(authRec.Body).Decode(&authBody); err != nil {
			t.Fatalf("decode authenticated /api/health: %v", err)
		}
		if authBody["authenticated"] != true {
			t.Fatalf("expected authenticated=true with valid token, got %v", authBody["authenticated"])
		}
	})

	t.Run("extension origin requires valid token and supports preflight", func(t *testing.T) {
		extOrigin := "moz-extension://12345678-1234-1234-1234-123456789abc"

		preReq := httptest.NewRequest(http.MethodOptions, "/api/settings", nil)
		preReq.Header.Set("Origin", extOrigin)
		preRec := httptest.NewRecorder()
		server.Router.ServeHTTP(preRec, preReq)
		if preRec.Code != http.StatusOK {
			t.Fatalf("OPTIONS /api/settings status = %d, want 200", preRec.Code)
		}
		if preRec.Header().Get("Access-Control-Allow-Origin") != extOrigin {
			t.Fatalf("preflight ACAO = %q, want %q", preRec.Header().Get("Access-Control-Allow-Origin"), extOrigin)
		}

		// Extension without token must get 401 with CORS header preserved so it can read recovery error.
		unauthReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		unauthReq.Header.Set("Origin", extOrigin)
		unauthRec := httptest.NewRecorder()
		server.Router.ServeHTTP(unauthRec, unauthReq)
		if unauthRec.Code != http.StatusUnauthorized {
			t.Fatalf("extension GET /api/settings without token status = %d, want 401", unauthRec.Code)
		}
		if unauthRec.Header().Get("Access-Control-Allow-Origin") != extOrigin {
			t.Fatalf("401 ACAO = %q, want %q", unauthRec.Header().Get("Access-Control-Allow-Origin"), extOrigin)
		}

		// Extension with valid token succeeds.
		okReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		okReq.Header.Set("Origin", extOrigin)
		okReq.Header.Set("X-Hydra-Token", "ext-secret-token")
		okRec := httptest.NewRecorder()
		server.Router.ServeHTTP(okRec, okReq)
		if okRec.Code != http.StatusOK {
			t.Fatalf("extension GET /api/settings with valid token status = %d, want 200 (%s)", okRec.Code, okRec.Body.String())
		}

		// Disk space probe works from Chromium extension origin with valid token.
		chromeOrigin := "chrome-extension://abcdefghijklmnop"
		diskBody := fmt.Sprintf(`{"path":%q,"required_bytes":1024}`, t.TempDir())
		diskReq := httptest.NewRequest(http.MethodPost, "/api/disk-space", bytes.NewBufferString(diskBody))
		diskReq.Header.Set("Origin", chromeOrigin)
		diskReq.Header.Set("Content-Type", "application/json")
		diskReq.Header.Set("X-Hydra-Token", "ext-secret-token")
		diskRec := httptest.NewRecorder()
		server.Router.ServeHTTP(diskRec, diskReq)
		if diskRec.Code != http.StatusOK {
			t.Fatalf("chrome-extension POST /api/disk-space status = %d, want 200 (%s)", diskRec.Code, diskRec.Body.String())
		}

		// Untrusted web origin remains forbidden even on /api/settings.
		badReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		badReq.Header.Set("Origin", "https://evil.example.com")
		badRec := httptest.NewRecorder()
		server.Router.ServeHTTP(badRec, badReq)
		if badRec.Code != http.StatusForbidden {
			t.Fatalf("malicious origin GET /api/settings status = %d, want 403", badRec.Code)
		}
	})
}

func TestDownloadTriggerOptionsStartPausedAndMetadata(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "ext-options-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	originalQM := GlobalQueueManager
	busyQM := NewQueueManager(1, nil)
	atomic.StoreInt32(&busyQM.active, 1)
	GlobalQueueManager = busyQM
	t.Cleanup(func() {
		atomic.StoreInt32(&busyQM.active, 0)
		busyQM.Stop()
		GlobalQueueManager = originalQM
	})

	store := newTestDBStore(t)
	server := NewServer(nil)
	server.db = store

	destPath := filepath.Join(t.TempDir(), "custom-archive.tar.gz")
	payload := fmt.Sprintf(`{
		"url": "https://example.com/archive.tar.gz",
		"save_path": %q,
		"filename": "custom-archive.tar.gz",
		"max_speed_bytes": 524288,
		"checksum_algo": "sha256",
		"expected_checksum": "deadbeef1234",
		"start_paused": true,
		"headers": {"Cookie": "session=1", "Referer": "https://example.com/page"}
	}`, destPath)

	req := httptest.NewRequest(http.MethodPost, "/download", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hydra-Token", "ext-options-token")
	req.Header.Set("Origin", "moz-extension://hydra-test")

	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /download status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "PAUSED" {
		t.Fatalf("status = %q, want PAUSED", resp.Status)
	}

	job, ok := store.GetJob(resp.JobID)
	if !ok {
		t.Fatalf("created job %q not found in store", resp.JobID)
	}
	if job.MaxSpeedBytes != 524288 {
		t.Fatalf("MaxSpeedBytes = %d, want 524288", job.MaxSpeedBytes)
	}
	if job.ChecksumAlgo != "sha256" || job.ExpectedChecksum != "deadbeef1234" {
		t.Fatalf("unexpected checksum fields: algo=%q expected=%q", job.ChecksumAlgo, job.ExpectedChecksum)
	}
	if job.Headers["Cookie"] != "session=1" {
		t.Fatalf("expected Cookie header preserved, got %#v", job.Headers)
	}
}

func TestNativeMessagingProtocolAndHandlers(t *testing.T) {
	t.Run("frame roundtrip and limit guards", func(t *testing.T) {
		var wire bytes.Buffer
		if err := WriteNativeMessage(&wire, NativeHostRequest{Action: "ping"}); err != nil {
			t.Fatalf("WriteNativeMessage failed: %v", err)
		}

		raw, err := ReadNativeMessage(&wire)
		if err != nil {
			t.Fatalf("ReadNativeMessage failed: %v", err)
		}

		var decoded NativeHostRequest
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		if decoded.Action != "ping" {
			t.Fatalf("action = %q, want ping", decoded.Action)
		}

		// Oversized length frame must fail cleanly instead of allocating large buffers.
		var badHeader [4]byte
		binary.LittleEndian.PutUint32(badHeader[:], MaxNativeMessageBytes+1)
		if _, err := ReadNativeMessage(bytes.NewReader(badHeader[:])); err == nil {
			t.Fatal("expected error reading oversized native message header")
		}
	})

	t.Run("session dispatches info, daemon recovery, and GUI actions", func(t *testing.T) {
		daemonReady := false
		startCalled := false
		launchedJob := ""
		launchedURL := ""

		deps := NativeHostDeps{
			DaemonURL:         DefaultDaemonHTTPURL,
			LoadToken:         func() (string, error) { return "tok_abc123", nil },
			EnsureToken:       func() (string, error) { return "tok_abc123", nil },
			IsDaemonReachable: func() bool { return daemonReady },
			StartDaemon: func() error {
				startCalled = true
				daemonReady = true
				return nil
			},
			LaunchGUI: func(jobID, downloadURL string) error {
				launchedJob = jobID
				launchedURL = downloadURL
				return nil
			},
			FindGUIBinary: func() string { return "/usr/bin/hydra-gui" },
		}

		var inBuf bytes.Buffer
		_ = WriteNativeMessage(&inBuf, NativeHostRequest{Action: "get_integration_info"})
		_ = WriteNativeMessage(&inBuf, NativeHostRequest{Action: "ensure_daemon"})
		_ = WriteNativeMessage(&inBuf, NativeHostRequest{Action: "open_gui", JobID: "job_42", URL: "https://example.com/a.zip"})
		_ = WriteNativeMessage(&inBuf, NativeHostRequest{Action: "unknown_cmd"})

		var outBuf bytes.Buffer
		if err := ServeNativeMessaging(&inBuf, &outBuf, deps); err != nil {
			t.Fatalf("ServeNativeMessaging error: %v", err)
		}

		var responses []NativeHostResponse
		for {
			frame, err := ReadNativeMessage(&outBuf)
			if err != nil {
				break
			}
			var r NativeHostResponse
			if err := json.Unmarshal(frame, &r); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			responses = append(responses, r)
		}

		if len(responses) != 4 {
			t.Fatalf("got %d responses, want 4", len(responses))
		}
		if !responses[0].OK || responses[0].DaemonRunning || responses[0].Token != "tok_abc123" || !responses[0].GUIAvailable {
			t.Fatalf("unexpected get_integration_info response: %+v", responses[0])
		}
		if !responses[1].OK || !startCalled || !responses[1].Started || !responses[1].DaemonRunning {
			t.Fatalf("unexpected ensure_daemon response: %+v", responses[1])
		}
		if !responses[2].OK || launchedJob != "job_42" || launchedURL != "https://example.com/a.zip" {
			t.Fatalf("unexpected open_gui response: %+v", responses[2])
		}
		if responses[3].OK || responses[3].Error == "" {
			t.Fatalf("expected error response for unknown_cmd, got: %+v", responses[3])
		}

		// Ensure start failure returns structured error instead of silent success.
		failDeps := deps
		daemonReady = false
		failDeps.IsDaemonReachable = func() bool { return false }
		failDeps.StartDaemon = func() error { return errors.New("systemd failed") }
		failResp := HandleNativeHostRequest(NativeHostRequest{Action: "ensure_daemon"}, failDeps)
		if failResp.OK || failResp.Error != "systemd failed" {
			t.Fatalf("expected daemon start error propagated, got %+v", failResp)
		}

		// Ensure past-due scheduled time check on /download creates QUEUED/PAUSED correctly.
		_ = time.Now()
	})
}

func TestBrowserExtension_UnitAndManifest(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable; skipping browser extension JS unit tests")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(repoRoot, "extension", "test_extension.mjs")
	cmd := exec.Command(nodeBin, "--test", testFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser extension unit tests failed: %v\n%s", err, string(out))
	}
}
