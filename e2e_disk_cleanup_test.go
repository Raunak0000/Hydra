package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

func TestEndToEndDiskFullAndCleanup(t *testing.T) {
	env := setupIsolatedSubprocessEnv(t, "e2e-diskfull-token")
	daemon := env.startDaemon(t)

	diskInfo, err := storage.GetDiskSpace(env.downloadDir, 0)
	if err != nil {
		t.Fatalf("GetDiskSpace(%s): %v", env.downloadDir, err)
	}
	oversizedLength := diskInfo.AvailableBytes + 256*1024*1024

	// Case 1: Fresh download exceeding available disk space fails fast and cleans up destination file.
	var oversizedHits atomic.Int32
	oversizedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oversizedHits.Add(1)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatUint(oversizedLength, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer oversizedSrv.Close()

	oversizedSavePath := filepath.Join(env.downloadDir, "too-big.bin")
	start := time.Now()
	oversizedJobID := enqueueJobHTTP(t, daemon, oversizedSrv.URL+"/too-big.bin", oversizedSavePath)

	failedJob := daemon.waitForJob(t, oversizedJobID, 4*time.Second, func(j models.UIJob) bool {
		return j.Status == "FAILED"
	})
	elapsed := time.Since(start)

	if !strings.Contains(strings.ToLower(failedJob.ErrorMessage), "insufficient disk space") {
		t.Fatalf("expected insufficient disk space error message, got %q", failedJob.ErrorMessage)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("expected non-retryable disk-full failure without retry backoff, took %s", elapsed)
	}
	if _, err := os.Stat(oversizedSavePath); !os.IsNotExist(err) {
		t.Fatalf("expected newly reserved destination %s to be cleaned up after disk-full failure, stat err=%v", oversizedSavePath, err)
	}

	code, body := daemon.requestJSON(t, http.MethodDelete, "/api/jobs/"+oversizedJobID, nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE failed job status=%d body=%s", code, string(body))
	}

	// Case 2: Resuming an existing partial download that fails disk-space check must preserve the partial file,
	// while explicit DELETE with keep_file=false removes it (and keep_file=true retains it).
	payload, _ := makeDeterministicPayload(2*1024*1024, 0x4C)
	gateCh := make(chan struct{})
	var reportOversized atomic.Bool

	partialThenFullSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reportOversized.Load() {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.FormatUint(oversizedLength, 10))
			w.WriteHeader(http.StatusOK)
			return
		}
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
				if parsedStart, err := strconv.ParseInt(parts[0], 10, 64); err == nil && parsedStart >= 0 && parsedStart < total {
					start = parsedStart
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
		flusher, _ := w.(http.Flusher)
		chunk := payload[start : end+1]
		if int64(len(chunk)) > 16*1024 {
			_, _ = w.Write(chunk[:16*1024])
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-gateCh:
			case <-r.Context().Done():
			}
			return
		}
		_, _ = w.Write(chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer partialThenFullSrv.Close()

	partialPath := filepath.Join(env.downloadDir, "partial-then-full.bin")
	partialJobID := enqueueJobHTTP(t, daemon, partialThenFullSrv.URL+"/partial-then-full.bin", partialPath)

	daemon.waitForJob(t, partialJobID, 8*time.Second, func(j models.UIJob) bool {
		return j.Status == "DOWNLOADING" && j.Progress > 0
	})

	_ = env.runCLI(t, "pause", partialJobID)
	daemon.waitForJob(t, partialJobID, 5*time.Second, func(j models.UIJob) bool {
		return j.Status == "PAUSED"
	})

	if _, err := os.Stat(partialPath); err != nil {
		t.Fatalf("expected paused partial file %s to exist before resume: %v", partialPath, err)
	}

	reportOversized.Store(true)
	close(gateCh)

	code, body = daemon.requestJSON(t, http.MethodPost, "/api/jobs/"+partialJobID+"/resume", nil)
	if code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("POST /resume status=%d body=%s", code, string(body))
	}

	resumedFailed := daemon.waitForJob(t, partialJobID, 5*time.Second, func(j models.UIJob) bool {
		return j.Status == "FAILED"
	})
	if !strings.Contains(strings.ToLower(resumedFailed.ErrorMessage), "insufficient disk space") {
		t.Fatalf("expected insufficient disk space error on resume, got %q", resumedFailed.ErrorMessage)
	}
	if _, err := os.Stat(partialPath); err != nil {
		t.Fatalf("expected resumed partial file %s to be preserved when disk check fails: %v", partialPath, err)
	}

	// Explicit DELETE without keep_file=true must remove the partial destination from disk.
	code, body = daemon.requestJSON(t, http.MethodDelete, "/api/jobs/"+partialJobID, nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE partial job status=%d body=%s", code, string(body))
	}
	if _, err := os.Stat(partialPath); !os.IsNotExist(err) {
		t.Fatalf("expected partial file %s to be removed on DELETE, stat err=%v", partialPath, err)
	}

	// Verify keep_file=true preserves the file on disk while removing the job from queue.
	keepPath := filepath.Join(env.downloadDir, "keep-me.bin")
	if err := os.WriteFile(keepPath, []byte("partial-content"), 0o600); err != nil {
		t.Fatalf("write keepPath: %v", err)
	}
	reportOversized.Store(false)
	oversizedKeepSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
	}))
	defer oversizedKeepSrv.Close()

	code, body = daemon.requestJSON(t, http.MethodPost, "/api/jobs", map[string]any{
		"url":          oversizedKeepSrv.URL + "/keep-me.bin",
		"save_path":    keepPath,
		"start_paused": true,
	})
	if code != http.StatusAccepted && code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("enqueue paused keep-me status=%d body=%s", code, string(body))
	}
	var keepResp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(body, &keepResp); err != nil {
		t.Fatalf("decode keep-me enqueue: %v", err)
	}
	code, body = daemon.requestJSON(t, http.MethodDelete, "/api/jobs/"+keepResp.JobID+"?keep_file=true", nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE keep_file=true status=%d body=%s", code, string(body))
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("expected file %s to remain with keep_file=true: %v", keepPath, err)
	}

	if err := daemon.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM daemon: %v", err)
	}
	if err := daemon.cmd.Wait(); err != nil {
		t.Fatalf("daemon Wait after SIGTERM: %v", err)
	}
}
