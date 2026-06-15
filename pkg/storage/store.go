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
			// FIX THIS LINE (ensure there's only one 'make'):
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

func (s *MemoryStore) UpdateProgress(id string, progress float64, downloaded string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, exists := s.Jobs[id]; exists {
		job.Progress = progress
		job.Downloaded = downloaded
		if progress >= 100.0 {
			job.Status = "COMPLETED"
		}
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
