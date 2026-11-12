package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestEndToEndInterruptedDownloadAndDaemonRestart(t *testing.T) {
	env := setupIsolatedSubprocessEnv(t, "e2e-restart-token")

	// Phase 1: Graceful SIGTERM mid-download -> restart -> resume via CLI -> verify SHA-256 + Range resume.
	payloadA, _ := makeDeterministicPayload(2*1024*1024, 0x31)
	expectedHashA := sha256.Sum256(payloadA)

	var gatedA atomic.Bool
	gatedA.Store(true)
	gateChA := make(chan struct{})
	var nonZeroRangeA atomic.Bool

	srvA := serveGatedRangeFile(t, payloadA, 16*1024, &gatedA, gateChA, &nonZeroRangeA)
	defer srvA.Close()

	daemon1 := env.startDaemon(t)
	savePathA := filepath.Join(env.downloadDir, "phase1-graceful.bin")
	jobAID := enqueueJobHTTP(t, daemon1, srvA.URL+"/phase1-graceful.bin", savePathA)

	daemon1.waitForJob(t, jobAID, 8*time.Second, func(j models.UIJob) bool {
		return j.Status == "DOWNLOADING" && j.Progress > 0
	})

	if err := daemon1.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM daemon1: %v", err)
	}
	if err := daemon1.cmd.Wait(); err != nil {
		t.Fatalf("daemon1 Wait after SIGTERM: %v", err)
	}
	if _, err := os.Stat(daemon1.sockPath); !os.IsNotExist(err) {
		t.Fatalf("expected IPC socket %s to be removed on graceful shutdown", daemon1.sockPath)
	}

	// Open the stream gate now so resumed workers can finish downloading.
	gatedA.Store(false)
	close(gateChA)

	daemon2 := env.startDaemon(t)
	recoveredA := daemon2.waitForJob(t, jobAID, 5*time.Second, func(j models.UIJob) bool {
		return j.Status == "PAUSED"
	})
	if recoveredA.Progress <= 0 {
		t.Fatalf("expected recovered job A to retain downloaded progress, got %+v", recoveredA)
	}

	out := env.runCLI(t, "resume", jobAID)
	if !strings.Contains(strings.ToLower(out), "resum") {
		t.Fatalf("unexpected CLI resume output for %s: %q", jobAID, out)
	}

	daemon2.waitForJob(t, jobAID, 10*time.Second, func(j models.UIJob) bool {
		return j.Status == "COMPLETED"
	})

	gotBytesA, err := os.ReadFile(savePathA)
	if err != nil {
		t.Fatalf("read phase1 file: %v", err)
	}
	if sha256.Sum256(gotBytesA) != expectedHashA {
		t.Fatalf("phase1 SHA-256 mismatch after daemon restart resume (got len=%d want=%d)", len(gotBytesA), len(payloadA))
	}
	if !nonZeroRangeA.Load() {
		t.Fatalf("expected resumed phase1 download to request non-zero byte Range")
	}

	// Phase 2: Ungraceful SIGKILL crash mid-download -> restart RecoverStaleJobs -> resume via CLI -> verify SHA-256.
	payloadB, _ := makeDeterministicPayload(2*1024*1024, 0x6B)
	expectedHashB := sha256.Sum256(payloadB)

	var gatedB atomic.Bool
	gatedB.Store(true)
	gateChB := make(chan struct{})
	srvB := serveGatedRangeFile(t, payloadB, 16*1024, &gatedB, gateChB, nil)
	defer srvB.Close()

	savePathB := filepath.Join(env.downloadDir, "phase2-crash.bin")
	jobBID := enqueueJobHTTP(t, daemon2, srvB.URL+"/phase2-crash.bin", savePathB)

	daemon2.waitForJob(t, jobBID, 8*time.Second, func(j models.UIJob) bool {
		return j.Status == "DOWNLOADING" && j.Progress > 0
	})
	// Allow the 2-second chunk checkpoint ticker to persist offsets before SIGKILL.
	time.Sleep(2200 * time.Millisecond)

	if err := daemon2.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL daemon2: %v", err)
	}
	_ = daemon2.cmd.Wait()

	gatedB.Store(false)
	close(gateChB)

	// Starting daemon3 with the stale hydra.sock still present must recover cleanly.
	daemon3 := env.startDaemon(t)
	recoveredB := daemon3.waitForJob(t, jobBID, 5*time.Second, func(j models.UIJob) bool {
		return j.Status == "PAUSED"
	})
	if recoveredB.Progress <= 0 {
		t.Fatalf("expected crash-recovered job B to retain checkpointed progress, got %+v", recoveredB)
	}

	_ = env.runCLI(t, "resume", jobBID)
	daemon3.waitForJob(t, jobBID, 10*time.Second, func(j models.UIJob) bool {
		return j.Status == "COMPLETED"
	})

	gotBytesB, err := os.ReadFile(savePathB)
	if err != nil {
		t.Fatalf("read phase2 file: %v", err)
	}
	if sha256.Sum256(gotBytesB) != expectedHashB {
		t.Fatalf("phase2 SHA-256 mismatch after crash recovery resume (got len=%d want=%d)", len(gotBytesB), len(payloadB))
	}

	if err := daemon3.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM daemon3: %v", err)
	}
	if err := daemon3.cmd.Wait(); err != nil {
		t.Fatalf("daemon3 Wait after SIGTERM: %v", err)
	}
}

