package storage

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestIsNonRetryableDownloadError_DiskFullVariants(t *testing.T) {
	nonRetryable := []error{
		errors.New("insufficient disk space: required 10.00 GB, available 1.00 GB"),
		fmt.Errorf("preallocating destination: %w", syscall.ENOSPC),
		fmt.Errorf("worker 0 write failed at offset 4096: %w", syscall.ENOSPC),
		errors.New("disk quota exceeded"),
	}
	for _, err := range nonRetryable {
		if !isNonRetryableDownloadError(err) {
			t.Fatalf("expected non-retryable for %q", err.Error())
		}
	}

	retryable := []error{
		errors.New("connection reset by peer"),
		errors.New("i/o timeout"),
	}
	for _, err := range retryable {
		if isNonRetryableDownloadError(err) {
			t.Fatalf("expected retryable for %q", err.Error())
		}
	}
}

func TestQueueManager_FailsFastOnENOSPCWithoutRetrying(t *testing.T) {
	t.Setenv("HYDRA_ENABLE_NOTIFICATIONS", "0")
	dbPath := t.TempDir() + "/queue_enospc.db"
	db, err := openDBStore(dbPath)
	if err != nil {
		t.Fatalf("openDBStore: %v", err)
	}
	defer db.Close()

	var attempts atomic.Int32
	qm := NewQueueManager(1, func(url, savePath, jobID string, headers map[string]string) error {
		attempts.Add(1)
		return fmt.Errorf("worker 0 write failed at offset 65536: %w", syscall.ENOSPC)
	})
	qm.db = db
	qm.initialBackoff = 50 * time.Millisecond
	defer qm.Stop()

	job := models.UIJob{
		ID:       "job-enospc-1",
		URL:      "http://127.0.0.1/enospc.bin",
		SavePath: t.TempDir() + "/enospc.bin",
		FileName: "enospc.bin",
		Status:   "QUEUED",
	}
	if err := db.SaveJob(&job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	qm.executeJobWithRetry(job)

	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected ENOSPC to fail fast on attempt 1 without retries, got %d attempts", got)
	}

	gotJob, exists := db.GetJob(job.ID)
	if !exists {
		t.Fatalf("expected job to remain in DB with FAILED status")
	}
	if gotJob.Status != "FAILED" {
		t.Fatalf("expected FAILED status, got %s", gotJob.Status)
	}
	if !strings.Contains(strings.ToLower(gotJob.ErrorMessage), "no space left on device") {
		t.Fatalf("expected ENOSPC message in ErrorMessage, got %q", gotJob.ErrorMessage)
	}
}
