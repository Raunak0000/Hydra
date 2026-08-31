package storage

import (
	"sync"
	"time"

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

func InitQueueManager(maxConcurrent int, trigger func(string, string, string, map[string]string)) *QueueManager {
	queueOnce.Do(func() {
		GlobalQueueManager = &QueueManager{
			maxConcurrent: maxConcurrent,
			triggerFunc:   trigger,
		}
		go GlobalQueueManager.startScheduler()
	})
	return GlobalQueueManager
}

func GetQueueManager() *QueueManager {
	if GlobalQueueManager == nil {
		GlobalQueueManager = &QueueManager{
			maxConcurrent: 2,
		}
	}
	return GlobalQueueManager
}

func (qm *QueueManager) startScheduler() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		dbStore, err := GetDBStore()
		if err != nil {
			continue
		}

		dueJobs := dbStore.GetPendingScheduledJobs()
		if len(dueJobs) == 0 {
			continue
		}

		for _, job := range dueJobs {
			_ = dbStore.UpdateStatus(job.ID, "QUEUED")
		}

		GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
		qm.ProcessNext()
	}
}

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

func (qm *QueueManager) ShouldQueue() bool {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	return qm.ActiveCount() >= qm.maxConcurrent
}

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

	if activeCount < qm.maxConcurrent && nextQueued != nil && qm.triggerFunc != nil {
		_ = dbStore.UpdateStatus(nextQueued.ID, "DOWNLOADING")
		GetBroker().BroadcastQueueState(dbStore.GetAllJobs())
		go qm.triggerFunc(nextQueued.URL, nextQueued.SavePath, nextQueued.ID, nextQueued.Headers)
	}
}
