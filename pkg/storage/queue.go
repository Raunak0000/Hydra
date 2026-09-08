package storage

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

type QueueManager struct {
	maxConcurrent int
	mu            sync.Mutex
	triggerFunc   func(url string, savePath string, jobID string, headers map[string]string) error
	sem           chan struct{}
	broadcast     chan models.UIJob
	clients       map[chan models.UIJob]bool
	stopChan      chan struct{}
}

var (
	GlobalQueueManager *QueueManager
	queueOnce          sync.Once
)

func InitQueueManager(maxConcurrent int, trigger func(string, string, string, map[string]string) error) *QueueManager {
	queueOnce.Do(func() {
		GlobalQueueManager = &QueueManager{
			maxConcurrent: maxConcurrent,
			triggerFunc:   trigger,
			sem:           make(chan struct{}, maxConcurrent),
			broadcast:     make(chan models.UIJob, 256),
			clients:       make(map[chan models.UIJob]bool),
			stopChan:      make(chan struct{}),
		}
		go GlobalQueueManager.StartBroadcaster()
		go GlobalQueueManager.startScheduler()
	})
	return GlobalQueueManager
}

func GetQueueManager() *QueueManager {
	if GlobalQueueManager == nil {
		GlobalQueueManager = &QueueManager{
			maxConcurrent: 2,
			sem:           make(chan struct{}, 2),
			broadcast:     make(chan models.UIJob, 256),
			clients:       make(map[chan models.UIJob]bool),
			stopChan:      make(chan struct{}),
		}
	}
	return GlobalQueueManager
}

// Semaphore worker acquisition
func (qm *QueueManager) acquireWorker(ctx context.Context) error {
	select {
	case qm.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (qm *QueueManager) releaseWorker() {
	<-qm.sem
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

func (qm *QueueManager) startScheduler() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dbStore, err := GetDBStore()
			if err != nil {
				continue
			}

			dueJobs := dbStore.GetPendingScheduledJobs()
			if len(dueJobs) == 0 {
				qm.ProcessNext()
				continue
			}

			for _, job := range dueJobs {
				_ = dbStore.UpdateStatus(job.ID, "QUEUED")
			}

			GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
			qm.ProcessNext()
		case <-qm.stopChan:
			return
		}
	}
}

func (qm *QueueManager) ActiveCount() int {
	return len(qm.sem)
}

func (qm *QueueManager) ShouldQueue() bool {
	return qm.ActiveCount() >= qm.maxConcurrent
}

func (qm *QueueManager) ProcessNext() {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	dbStore, err := GetDBStore()
	if err != nil {
		return
	}

	if qm.ActiveCount() >= qm.maxConcurrent {
		return
	}

	var nextQueued *models.UIJob
	jobs := dbStore.GetAllJobs()
	for i := range jobs {
		if jobs[i].Status == "QUEUED" {
			nextQueued = &jobs[i]
			break
		}
	}

	if nextQueued != nil && qm.triggerFunc != nil {
		go qm.processJobWithRetry(*nextQueued)
	}
}

// Exponential Backoff Retry Pipeline
func (qm *QueueManager) processJobWithRetry(job models.UIJob) {
	ctx := context.Background()
	if err := qm.acquireWorker(ctx); err != nil {
		return
	}
	defer qm.releaseWorker()

	dbStore, err := GetDBStore()
	if err != nil {
		return
	}

	maxRetries := 3
	backoff := 2 * time.Second

	var downloadErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		job.Status = "DOWNLOADING"
		_ = dbStore.UpdateStatus(job.ID, "DOWNLOADING")
		GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
		qm.broadcast <- job

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
			qm.broadcast <- job
			qm.ProcessNext()
			return
		}

		log.Printf("[QueueWarning] Job %s failed attempt %d/%d: %v. Retrying in %v...", job.ID, attempt, maxRetries, downloadErr, backoff)
		time.Sleep(backoff)
		backoff *= 2
	}

	// Permanent failure handling after max retries
	job.Status = "FAILED"
	job.ErrorMessage = downloadErr.Error()
	now := time.Now()
	job.CompletedAt = &now
	_ = dbStore.UpdateStatus(job.ID, "FAILED")
	GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
	qm.broadcast <- job

	qm.ProcessNext()
}
