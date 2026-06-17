package storage

import (
	"sync"

	"github.com/Raunak0000/Hydra/pkg/models"
)

type MemoryStore struct {
	mu   sync.RWMutex
	Jobs map[string]*models.UIJob
}

var (
	GlobalStore *MemoryStore
	once        sync.Once
)

func GetStore() *MemoryStore {
	once.Do(func() {
		GlobalStore = &MemoryStore{
			Jobs: make(map[string]*models.UIJob),
		}
	})
	return GlobalStore
}

func (s *MemoryStore) SetJob(id string, job *models.UIJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Jobs[id] = job
}

func (s *MemoryStore) UpdateProgress(jobID string, progress float64, downloaded string, filename string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job, exists := s.Jobs[jobID]; exists {
		// Mutate local frame copies
		job.Progress = progress
		job.Downloaded = downloaded
		job.Status = status

		if filename != "" && filename != "Calculating..." {
			job.FileName = filename
		}

		// Write the updated flat layout back down into the thread-safe map index registry
		s.Jobs[jobID] = job
	}
}
func (s *MemoryStore) UpdateTotalSize(id string, totalSize string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, exists := s.Jobs[id]; exists {
		job.TotalSize = totalSize
	}
}

func (s *MemoryStore) GetAllJobs() []models.UIJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []models.UIJob
	for _, job := range s.Jobs {
		list = append(list, *job)
	}
	return list
}
