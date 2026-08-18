package storage

import (
	"sync"

	"github.com/Raunak0000/Hydra/pkg/models"
)

type QueueManager struct {
	maxConcurrent int
	mu            sync.Mutex
	triggerFunc   func(url string, savePath string, jobID string, headers map[string]string)
}

var (
	GlobalQueueManager *QueueManager
	queueOnce          sync.Once
)

// InitQueueManager initializes the concurrency coordinator
func InitQueueManager(maxConcurrent int, trigger func(string, string, string, map[string]string)) *QueueManager {
	queueOnce.Do(func() {
		GlobalQueueManager = &QueueManager{
			maxConcurrent: maxConcurrent,
			triggerFunc:   trigger,
		}
	})
	return GlobalQueueManager
}

// GetQueueManager returns the global queue coordinator singleton
func GetQueueManager() *QueueManager {
	if GlobalQueueManager == nil {
		GlobalQueueManager = &QueueManager{
			maxConcurrent: 2,
		}
	}
	return GlobalQueueManager
}

// ActiveCount returns the number of tasks currently in DOWNLOADING state
func (qm *QueueManager) ActiveCount() int {
	dbStore, err := GetDBStore()
	if err != nil {
		return 0
	}
	jobs := dbStore.GetAllJobs()
	active := 0
	for _, job := range jobs {
		if job.Status == "DOWNLOADING" {
			active++
		}
	}
	return active
}

// ShouldQueue returns true if active downloads meet or exceed max concurrency
func (qm *QueueManager) ShouldQueue() bool {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	return qm.ActiveCount() >= qm.maxConcurrent
}

// ProcessNext searches for the oldest QUEUED job and promotes it if slots are available
func (qm *QueueManager) ProcessNext() {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	dbStore, err := GetDBStore()
	if err != nil {
		return
	}

	activeCount := 0
	var nextQueued *models.UIJob

	jobs := dbStore.GetAllJobs()
	for i := range jobs {
		if jobs[i].Status == "DOWNLOADING" {
			activeCount++
		}
		if jobs[i].Status == "QUEUED" && nextQueued == nil {
			nextQueued = &jobs[i]
		}
	}

	// Promote next in line if slot is available
	if activeCount < qm.maxConcurrent && nextQueued != nil && qm.triggerFunc != nil {
		_ = dbStore.UpdateStatus(nextQueued.ID, "DOWNLOADING")
		GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
		go qm.triggerFunc(nextQueued.URL, nextQueued.SavePath, nextQueued.ID, nextQueued.Headers)
	}
}
