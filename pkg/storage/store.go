package storage

import (
	"sort"
	"strconv"
	"strings"
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

func (s *MemoryStore) UpdateProgress(jobID string, progress float64, downloaded string, speed string, filename string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job, exists := s.Jobs[jobID]; exists {
		if (job.Status == "COMPLETED" || job.Status == "FAILED") && status == "DOWNLOADING" {
			return
		}

		job.Progress = progress
		job.Downloaded = downloaded
		job.Speed = speed
		job.Status = status

		if filename != "" && filename != "Calculating..." {
			job.FileName = filename
		}

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

func (s *MemoryStore) UpdateStatus(id string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, exists := s.Jobs[id]; exists {
		job.Status = status
	}
}

func (s *MemoryStore) UpdateJobChunks(id string, chunks []models.ChunkState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, exists := s.Jobs[id]; exists {
		job.Chunks = chunks
	}
}

func (s *MemoryStore) GetJob(id string) (models.UIJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if job, exists := s.Jobs[id]; exists && job != nil {
		return models.UIJob{
			ID:         job.ID,
			FileName:   job.FileName,
			URL:        job.URL,
			SavePath:   job.SavePath,
			Progress:   job.Progress,
			TotalSize:  job.TotalSize,
			Downloaded: job.Downloaded,
			Speed:      job.Speed,
			Status:     job.Status,
			Chunks:     job.Chunks,
		}, true
	}
	return models.UIJob{}, false
}

func (s *MemoryStore) GetAllJobs() []models.UIJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []models.UIJob
	for _, job := range s.Jobs {
		if job == nil {
			continue
		}

		list = append(list, models.UIJob{
			ID:         job.ID,
			FileName:   job.FileName,
			URL:        job.URL,
			SavePath:   job.SavePath,
			Progress:   job.Progress,
			TotalSize:  job.TotalSize,
			Downloaded: job.Downloaded,
			Speed:      job.Speed,
			Status:     job.Status,
			Chunks:     job.Chunks,
		})
	}

	sort.Slice(list, func(i, j int) bool {
		valI := strings.TrimPrefix(list[i].ID, "job_")
		valJ := strings.TrimPrefix(list[j].ID, "job_")
		numI, _ := strconv.Atoi(valI)
		numJ, _ := strconv.Atoi(valJ)
		return numI < numJ
	})

	return list
}

func (s *MemoryStore) DeleteJob(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Jobs, id)
}
