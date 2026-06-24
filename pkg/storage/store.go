package storage

import (
	"fmt"
	"path/filepath"
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

func (s *MemoryStore) UpdateProgress(jobID string, progress float64, downloaded string, filename string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job, exists := s.Jobs[jobID]; exists {
		// Prevent overwriting final status with "DOWNLOADING"
		if (job.Status == "COMPLETED" || job.Status == "FAILED") && status == "DOWNLOADING" {
			return
		}

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

func (s *MemoryStore) UpdateStatus(id string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, exists := s.Jobs[id]; exists {
		job.Status = status
	}
}

func (s *MemoryStore) GetAllJobs() []models.UIJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []models.UIJob
	for _, job := range s.Jobs {
		list = append(list, *job)
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

func SanitizeDownloadPath(unsafePath string) (string, error) {
	// 1. Resolve relative shortcuts lexically (e.g., handling ".." tokens)
	cleaned := filepath.Clean(unsafePath)

	// 2. Define the absolute secure jail root anchor
	secureRoot := "/home/raunak/Downloads/"

	// 3. Enforce prefix checking to prevent escaping the target jail folder
	if !strings.HasPrefix(cleaned, secureRoot) {
		return "", fmt.Errorf("security violation: directory traversal attempt blocked")
	}

	return cleaned, nil
}
