package storage

import (
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

type QueueManager struct {
	maxConcurrent  int
	mu             sync.Mutex
	triggerFunc    func(url string, savePath string, jobID string, headers map[string]string) error
	db             *DBStore
	initialBackoff time.Duration
	active         int32
	inFlight       map[string]bool
	slotFreed      chan struct{}
	broadcast      chan models.UIJob
	clients        map[chan models.UIJob]bool
	stopChan       chan struct{}
}

var (
	GlobalQueueManager *QueueManager
	queueOnce          sync.Once
)

// NewQueueManager initializes an isolated QueueManager instance.
func NewQueueManager(maxConcurrent int, trigger func(string, string, string, map[string]string) error) *QueueManager {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &QueueManager{
		maxConcurrent: maxConcurrent,
		triggerFunc:   trigger,
		inFlight:      make(map[string]bool),
		slotFreed:     make(chan struct{}, 1),
		broadcast:     make(chan models.UIJob, 256),
		clients:       make(map[chan models.UIJob]bool),
		stopChan:      make(chan struct{}),
	}
}

func InitQueueManager(maxConcurrent int, trigger func(string, string, string, map[string]string) error) *QueueManager {
	queueOnce.Do(func() {
		GlobalQueueManager = NewQueueManager(maxConcurrent, trigger)
		go GlobalQueueManager.StartBroadcaster()
		go GlobalQueueManager.startScheduler()
	})
	return GlobalQueueManager
}

func GetQueueManager() *QueueManager {
	if GlobalQueueManager == nil {
		GlobalQueueManager = NewQueueManager(2, nil)
	}
	return GlobalQueueManager
}

// Stop terminates background goroutines for the QueueManager.
func (qm *QueueManager) Stop() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	select {
	case <-qm.stopChan:
	default:
		close(qm.stopChan)
	}
}

func (qm *QueueManager) claimSlotForDirectRun(jobID string) bool {
	for {
		qm.mu.Lock()
		select {
		case <-qm.stopChan:
			qm.mu.Unlock()
			return false
		default:
		}
		if qm.inFlight[jobID] {
			qm.mu.Unlock()
			return false
		}
		if int(atomic.LoadInt32(&qm.active)) < qm.maxConcurrent {
			qm.inFlight[jobID] = true
			atomic.AddInt32(&qm.active, 1)
			qm.mu.Unlock()
			return true
		}
		qm.mu.Unlock()

		select {
		case <-qm.slotFreed:
		case <-qm.stopChan:
			return false
		}
	}
}

// SSE Client Registration
func (qm *QueueManager) AddClient(ch chan models.UIJob) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.clients[ch] = true
}

func (qm *QueueManager) RemoveClient(ch chan models.UIJob) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	if _, ok := qm.clients[ch]; ok {
		delete(qm.clients, ch)
		close(ch)
	}
}

// SSE Broadcaster routine
func (qm *QueueManager) StartBroadcaster() {
	for {
		select {
		case job := <-qm.broadcast:
			qm.mu.Lock()
			for clientChan := range qm.clients {
				select {
				case clientChan <- job:
				default:
					// Non-blocking drop if client stream buffer is full
				}
			}
			qm.mu.Unlock()
		case <-qm.stopChan:
			return
		}
	}
}

func (qm *QueueManager) store() (*DBStore, error) {
	if qm.db != nil {
		return qm.db, nil
	}
	return GetDBStore()
}

func (qm *QueueManager) promoteScheduledJobs() {
	dbStore, err := qm.store()
	if err != nil {
		return
	}

	dueJobs := dbStore.GetPendingScheduledJobs()
	if len(dueJobs) == 0 {
		qm.ProcessNext()
		return
	}

	for _, job := range dueJobs {
		_ = dbStore.UpdateStatus(job.ID, "QUEUED")
		NotifyDownloadQueued(jobDisplayName(job.FileName, job.SavePath), job.SavePath)
	}

	GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
	qm.ProcessNext()
}

func (qm *QueueManager) startScheduler() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			qm.promoteScheduledJobs()
		case <-qm.stopChan:
			return
		}
	}
}

func (qm *QueueManager) ActiveCount() int {
	return int(atomic.LoadInt32(&qm.active))
}

func (qm *QueueManager) ShouldQueue() bool {
	qm.mu.Lock()
	maxConcurrent := qm.maxConcurrent
	qm.mu.Unlock()
	return qm.ActiveCount() >= maxConcurrent
}

func (qm *QueueManager) ProcessNext() {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	select {
	case <-qm.stopChan:
		return
	default:
	}

	if qm.triggerFunc == nil {
		return
	}

	dbStore, err := qm.store()
	if err != nil {
		return
	}

	if int(atomic.LoadInt32(&qm.active)) >= qm.maxConcurrent {
		return
	}

	jobs := dbStore.GetAllJobs()
	for i := range jobs {
		if int(atomic.LoadInt32(&qm.active)) >= qm.maxConcurrent {
			break
		}
		if jobs[i].Status != "QUEUED" || qm.inFlight[jobs[i].ID] {
			continue
		}
		qm.inFlight[jobs[i].ID] = true
		atomic.AddInt32(&qm.active, 1)
		go qm.runClaimedJob(jobs[i])
	}
}

func jobDisplayName(filename, savePath string) string {
	name := strings.TrimSpace(filename)
	if name == "" || name == "Calculating..." || name == "Pending path..." {
		if savePath != "" && savePath != "PENDING" && savePath != "DEFAULT" {
			name = filepath.Base(savePath)
		}
	}
	if name == "" {
		return "Download"
	}
	return name
}

// PauseAllJobs cancels all active downloads and marks active or queued jobs PAUSED.
func PauseAllJobs(dbStore *DBStore) int {
	if dbStore == nil {
		return 0
	}

	jobs := dbStore.GetAllJobs()
	pausedIDs := make([]string, 0)
	for _, job := range jobs {
		if job.Status == "DOWNLOADING" || job.Status == "QUEUED" {
			_ = dbStore.UpdateStatus(job.ID, "PAUSED")
			pausedIDs = append(pausedIDs, job.ID)
		}
	}

	if GlobalCancelMutex != nil && GlobalCancelMap != nil {
		GlobalCancelMutex.Lock()
		for _, id := range pausedIDs {
			if cancel, exists := GlobalCancelMap[id]; exists {
				cancel()
			}
		}
		GlobalCancelMutex.Unlock()
	}

	if len(pausedIDs) > 0 {
		GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
	}
	return len(pausedIDs)
}

// ResumeAllJobs marks all paused jobs QUEUED and nudges the QueueManager.
func ResumeAllJobs(dbStore *DBStore) int {
	if dbStore == nil {
		return 0
	}

	jobs := dbStore.GetAllJobs()
	resumed := 0
	for _, job := range jobs {
		if job.Status == "PAUSED" {
			_ = dbStore.UpdateStatus(job.ID, "QUEUED")
			resumed++
		}
	}

	if resumed > 0 {
		GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
		if qm := GetQueueManager(); qm != nil {
			qm.ProcessNext()
		}
	}
	return resumed
}

func (qm *QueueManager) emitBroadcast(job models.UIJob) {
	select {
	case qm.broadcast <- job:
	default:
	}
}

func isNonRetryableDownloadError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "http 401") ||
		strings.Contains(lower, "http 403") ||
		strings.Contains(lower, "http 404") ||
		strings.Contains(lower, "http 410") ||
		strings.Contains(lower, "id busy") ||
		strings.Contains(lower, "generate a new") ||
		strings.Contains(lower, "token expired") ||
		strings.Contains(lower, "checksum mismatch") ||
		strings.Contains(lower, "insufficient disk space") ||
		strings.Contains(lower, "html webpage") ||
		strings.Contains(lower, "returned html")
}

// Exponential Backoff Retry Pipeline
func (qm *QueueManager) processJobWithRetry(job models.UIJob) {
	if !qm.claimSlotForDirectRun(job.ID) {
		return
	}
	qm.runClaimedJob(job)
}

func (qm *QueueManager) runClaimedJob(job models.UIJob) {
	defer func() {
		qm.mu.Lock()
		delete(qm.inFlight, job.ID)
		qm.mu.Unlock()
		atomic.AddInt32(&qm.active, -1)
		select {
		case qm.slotFreed <- struct{}{}:
		default:
		}
		qm.ProcessNext()
	}()

	qm.executeJobWithRetry(job)
}

func (qm *QueueManager) executeJobWithRetry(job models.UIJob) {
	dbStore, err := qm.store()
	if err != nil {
		return
	}

	maxRetries := 3
	backoff := qm.initialBackoff
	if backoff <= 0 {
		backoff = 2 * time.Second
	}

	var downloadErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if currentJob, exists := dbStore.GetJob(job.ID); exists {
			if currentJob.Status == "PAUSED" || currentJob.Status == "COMPLETED" {
				return
			}
			job = currentJob
		} else {
			return
		}

		job.Status = "DOWNLOADING"
		_ = dbStore.UpdateStatus(job.ID, "DOWNLOADING")
		GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
		qm.emitBroadcast(job)

		if qm.triggerFunc != nil {
			downloadErr = qm.triggerFunc(job.URL, job.SavePath, job.ID, job.Headers)
		} else {
			downloadErr = fmt.Errorf("triggerFunc not configured")
		}

		if downloadErr == nil {
			job.Status = "COMPLETED"
			now := time.Now()
			job.CompletedAt = &now

			_ = dbStore.UpdateStatus(job.ID, "COMPLETED")
			GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
			qm.emitBroadcast(job)
			return
		}

		// Pause or deletion cancels the context cleanly rather than failing.
		currentJob, exists := dbStore.GetJob(job.ID)
		if !exists {
			log.Printf("[Queue] Job %s deleted; stopping retry loop.", job.ID)
			return
		}
		if currentJob.Status == "PAUSED" {
			log.Printf("[Queue] Job %s paused; stopping retry loop.", job.ID)
			return
		}

		log.Printf(
			"[QueueWarning] Job %s failed attempt %d/%d: %v",
			job.ID,
			attempt,
			maxRetries,
			downloadErr,
		)

		if isNonRetryableDownloadError(downloadErr) {
			break
		}

		if attempt < maxRetries {
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-qm.stopChan:
				timer.Stop()
				return
			}
			backoff *= 2
		}
	}

	if _, exists := dbStore.GetJob(job.ID); !exists {
		return
	}

	// Permanent failure handling after retries exhausted or non-retryable failure
	job.Status = "FAILED"
	job.ErrorMessage = downloadErr.Error()
	now := time.Now()
	job.CompletedAt = &now
	_ = dbStore.UpdateErrorMessage(job.ID, downloadErr.Error())
	_ = dbStore.UpdateStatus(job.ID, "FAILED")
	GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
	qm.emitBroadcast(job)

	displayName := jobDisplayName(job.FileName, job.SavePath)
	if strings.Contains(strings.ToLower(downloadErr.Error()), "checksum mismatch") {
		NotifyChecksumFailed(displayName)
	} else {
		NotifyDownloadFailed(displayName, downloadErr.Error())
	}
}
