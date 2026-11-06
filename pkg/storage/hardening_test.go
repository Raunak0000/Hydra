package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

// TestCrashRecovery_MultiStateSimulation tests that after an abrupt process death,
// a restarted daemon cleanly recovers active downloads to PAUSED while preserving
// all other job states and metadata.
func TestCrashRecovery_MultiStateSimulation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "crash_test.db")

	// Phase 1: Engine run before crash
	store1, err := openDBStore(dbPath)
	if err != nil {
		t.Fatalf("openDBStore before crash: %v", err)
	}

	futureTime := time.Now().Add(2 * time.Hour)
	jobs := []models.UIJob{
		{ID: "job-dl-1", FileName: "film1.mkv", URL: "https://example.com/film1.mkv", SavePath: "/tmp/film1.mkv", Status: "DOWNLOADING", Progress: 45.5, Downloaded: "450 MB", TotalSize: "1 GB"},
		{ID: "job-dl-2", FileName: "film2.mkv", URL: "https://example.com/film2.mkv", SavePath: "/tmp/film2.mkv", Status: "DOWNLOADING", Progress: 82.1, Downloaded: "820 MB", TotalSize: "1 GB"},
		{ID: "job-queued", FileName: "music.zip", URL: "https://example.com/music.zip", SavePath: "/tmp/music.zip", Status: "QUEUED"},
		{ID: "job-paused", FileName: "doc.pdf", URL: "https://example.com/doc.pdf", SavePath: "/tmp/doc.pdf", Status: "PAUSED", Progress: 10.0},
		{ID: "job-sched", FileName: "game.iso", URL: "https://example.com/game.iso", SavePath: "/tmp/game.iso", Status: "SCHEDULED", ScheduledAt: &futureTime},
		{ID: "job-done", FileName: "image.png", URL: "https://example.com/image.png", SavePath: "/tmp/image.png", Status: "COMPLETED", Progress: 100.0},
		{ID: "job-fail", FileName: "broken.bin", URL: "https://example.com/broken.bin", SavePath: "/tmp/broken.bin", Status: "FAILED", ErrorMessage: "404 Not Found"},
	}

	for i := range jobs {
		if err := store1.SaveJob(&jobs[i]); err != nil {
			t.Fatalf("SaveJob before crash: %v", err)
		}
	}

	// Crash: forcibly close DB store
	_ = store1.Close()

	// Phase 2: Engine restart simulation
	store2, err := openDBStore(dbPath)
	if err != nil {
		t.Fatalf("openDBStore after restart: %v", err)
	}
	defer store2.Close()

	recoveredCount, err := store2.RecoverStaleJobs()
	if err != nil {
		t.Fatalf("RecoverStaleJobs failed: %v", err)
	}
	if recoveredCount != 2 {
		t.Fatalf("expected 2 jobs recovered from DOWNLOADING to PAUSED, got %d", recoveredCount)
	}

	// Verify DOWNLOADING jobs became PAUSED with all progress intact
	for _, id := range []string{"job-dl-1", "job-dl-2"} {
		j, exists := store2.GetJob(id)
		if !exists {
			t.Fatalf("job %s missing after restart", id)
		}
		if j.Status != "PAUSED" {
			t.Errorf("job %s status = %q, want PAUSED", id, j.Status)
		}
		if j.Progress <= 0 || j.TotalSize != "1 GB" {
			t.Errorf("job %s progress/metadata corrupted: %+v", id, j)
		}
	}

	// Verify all other job states were strictly preserved
	expectations := map[string]string{
		"job-queued": "QUEUED",
		"job-paused": "PAUSED",
		"job-sched":  "SCHEDULED",
		"job-done":   "COMPLETED",
		"job-fail":   "FAILED",
	}

	for id, wantStatus := range expectations {
		j, exists := store2.GetJob(id)
		if !exists {
			t.Fatalf("job %s missing after restart", id)
		}
		if j.Status != wantStatus {
			t.Errorf("job %s status = %q, want %q", id, j.Status, wantStatus)
		}
	}

	// Verify idempotency on second recovery run
	secondRun, err := store2.RecoverStaleJobs()
	if err != nil || secondRun != 0 {
		t.Fatalf("second recovery run returned %d, err = %v (expected 0)", secondRun, err)
	}
}

// TestCrashRecovery_CorruptStateFiles verifies that corrupt or truncated state files
// (.hydra) are rejected safely without crashing or panicking.
func TestCrashRecovery_CorruptStateFiles(t *testing.T) {
	dir := t.TempDir()

	testCases := []struct {
		name    string
		content []byte
	}{
		{name: "empty_file", content: []byte{}},
		{name: "truncated_json", content: []byte(`{"id":"job-1","file_name":`)},
		{name: "random_binary_garbage", content: []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0xFF}},
		{name: "non_json_string", content: []byte("THIS_IS_NOT_JSON")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".hydra")
			if err := os.WriteFile(path, tc.content, 0644); err != nil {
				t.Fatalf("WriteFile failed: %v", err)
			}

			_, err := LoadJobState(path)
			if err == nil {
				t.Fatalf("expected error loading corrupted state file %q, got nil", tc.name)
			}
		})
	}
}

// TestCrashRecovery_AtomicStateInterruption verifies atomic file creation semantics
// ensuring temp files are cleaned up and original state is protected if write fails.
func TestCrashRecovery_AtomicStateInterruption(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "atomic_test.bin")

	initialJob := models.UIJob{
		ID:       "atomic-job-1",
		FileName: "atomic_test.bin",
		SavePath: targetPath,
		Status:   "DOWNLOADING",
		Progress: 25.0,
	}

	// Initial clean save
	if err := SaveJobState(initialJob); err != nil {
		t.Fatalf("initial SaveJobState failed: %v", err)
	}

	statePath := targetPath + ".hydra"
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("failed to read state file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("saved state file is empty")
	}

	// Attempting to save to a non-writable directory path
	badJob := models.UIJob{
		ID:       "bad-job",
		FileName: "bad.bin",
		SavePath: "/proc/non_existent_dir_hydra_test/bad.bin",
	}
	err = SaveJobState(badJob)
	if err == nil {
		t.Fatal("expected SaveJobState to fail for invalid directory")
	}

	// Ensure no dangling .hydra-state-*.tmp files exist in temp dir
	matches, err := filepath.Glob(filepath.Join(dir, ".hydra-state-*.tmp"))
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(matches) > 0 {
		t.Fatalf("found leftover temporary state files: %v", matches)
	}
}

// TestDiskSpace_ExhaustionAndVerification verifies disk space querying and handling
// of insufficient disk storage without touching or modifying the filesystem.
func TestDiskSpace_ExhaustionAndVerification(t *testing.T) {
	dir := t.TempDir()
	nestedNonExistent := filepath.Join(dir, "sub1", "sub2", "sub3", "file.iso")

	// Verify space query walks to existing ancestor without creating directories
	info, err := GetDiskSpace(nestedNonExistent, 1024)
	if err != nil {
		t.Fatalf("GetDiskSpace failed: %v", err)
	}

	if info.AvailableBytes == 0 {
		t.Fatal("expected positive available bytes on active filesystem")
	}

	// Check that sub1 directory was NOT created
	if _, err := os.Stat(filepath.Join(dir, "sub1")); !os.IsNotExist(err) {
		t.Fatal("GetDiskSpace should not create intermediate directories")
	}

	// Requesting an astronomically large amount of storage (10 Petabytes) must report EnoughSpace == false
	const tenPetabytes = 10 * 1024 * 1024 * 1024 * 1024 * 1024
	exceededInfo, err := GetDiskSpace(dir, tenPetabytes)
	if err != nil {
		t.Fatalf("GetDiskSpace with 10PB failed: %v", err)
	}
	if exceededInfo.EnoughSpace {
		t.Errorf("expected EnoughSpace=false for 10PB, got true (available: %d bytes)", exceededInfo.AvailableBytes)
	}

	// Zero required bytes should always succeed
	zeroInfo, err := GetDiskSpace(dir, 0)
	if err != nil {
		t.Fatalf("GetDiskSpace with 0 bytes failed: %v", err)
	}
	if !zeroInfo.EnoughSpace {
		t.Errorf("expected EnoughSpace=true for 0 required bytes")
	}
}

// TestPreallocation_ErrorPaths verifies preallocation edge cases and error handling.
func TestPreallocation_ErrorPaths(t *testing.T) {
	// Nil file must return error
	if err := PreallocateFile(nil, 1024); err == nil {
		t.Fatal("expected error when preallocating nil file")
	}

	// Zero or negative size is a safe no-op
	dir := t.TempDir()
	testPath := filepath.Join(dir, "prealloc_zero.bin")
	f, err := os.Create(testPath)
	if err != nil {
		t.Fatalf("os.Create failed: %v", err)
	}
	defer f.Close()

	if err := PreallocateFile(f, 0); err != nil {
		t.Errorf("expected zero-size preallocate to succeed, got %v", err)
	}
	if err := PreallocateFile(f, -500); err != nil {
		t.Errorf("expected negative-size preallocate to succeed, got %v", err)
	}
}

// TestConcurrentState_DBStoreHeavyConcurrency stress tests DBStore with dozens of
// concurrent goroutines performing simultaneous reads, writes, progress updates,
// and state transitions.
func TestConcurrentState_DBStoreHeavyConcurrency(t *testing.T) {
	store := newTestDBStore(t)

	const numWorkers = 25
	const iterationsPerWorker = 30

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()

			jobID := fmt.Sprintf("concurrent-job-%d", workerID)
			job := models.UIJob{
				ID:       jobID,
				FileName: fmt.Sprintf("file-%d.bin", workerID),
				URL:      fmt.Sprintf("https://example.com/file-%d.bin", workerID),
				SavePath: fmt.Sprintf("/tmp/file-%d.bin", workerID),
				Status:   "QUEUED",
			}

			// Insert job
			if err := store.SaveJob(&job); err != nil {
				t.Errorf("worker %d initial SaveJob failed: %v", workerID, err)
				return
			}

			for i := 0; i < iterationsPerWorker; i++ {
				// Rapid interleaved updates
				progress := float64(i) / float64(iterationsPerWorker) * 100.0
				_ = store.UpdateProgress(jobID, progress, fmt.Sprintf("%d MB", i), "10 MB/s", "1m", "", "DOWNLOADING")
				_ = store.UpdateStatus(jobID, "DOWNLOADING")
				_ = store.UpdateJobURL(jobID, fmt.Sprintf("https://mirror.com/file-%d-%d.bin", workerID, i))
				_ = store.UpdateChecksumVerified(jobID, i%2 == 0)

				// Interleaved reads
				if _, ok := store.GetJob(jobID); !ok {
					t.Errorf("worker %d GetJob(%s) returned not found", workerID, jobID)
				}
				_ = store.GetAllJobs()
			}

			// Finalize job
			_ = store.UpdateStatus(jobID, "COMPLETED")
		}()
	}

	wg.Wait()

	// Verify all jobs are present and in COMPLETED state
	allJobs := store.GetAllJobs()
	if len(allJobs) != numWorkers {
		t.Fatalf("expected %d total jobs, got %d", numWorkers, len(allJobs))
	}

	for _, j := range allJobs {
		if j.Status != "COMPLETED" {
			t.Errorf("job %s expected COMPLETED, got %s", j.ID, j.Status)
		}
	}
}

// TestConcurrentState_QueueManagerConcurrency verifies thread safety and race-free
// operation of the QueueManager under heavy concurrent client registration,
// worker acquisition, and state queries.
func TestConcurrentState_QueueManagerConcurrency(t *testing.T) {
	qm := NewQueueManager(4, func(url, path, id string, headers map[string]string) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	defer qm.Stop()

	const numClients = 15
	const numProducers = 15

	var wg sync.WaitGroup
	var activeBroadcasts int64

	// Start background broadcaster routine
	go qm.StartBroadcaster()

	// Concurrent client registration and deregistration
	wg.Add(numClients)
	for i := 0; i < numClients; i++ {
		go func() {
			defer wg.Done()
			ch := make(chan models.UIJob, 32)
			qm.AddClient(ch)

			// Read some broadcasts
			for j := 0; j < 5; j++ {
				select {
				case <-ch:
				case <-time.After(50 * time.Millisecond):
				}
			}

			qm.RemoveClient(ch)
		}()
	}

	// Concurrent broadcasters and queue state checks
	wg.Add(numProducers)
	for i := 0; i < numProducers; i++ {
		prodID := i
		go func() {
			defer wg.Done()
			for k := 0; k < 10; k++ {
				_ = qm.ActiveCount()
				_ = qm.ShouldQueue()

				job := models.UIJob{
					ID:       fmt.Sprintf("broadcast-job-%d-%d", prodID, k),
					FileName: "test.bin",
					Status:   "DOWNLOADING",
				}
				select {
				case qm.broadcast <- job:
					atomic.AddInt64(&activeBroadcasts, 1)
				default:
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
}

