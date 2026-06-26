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
			Status:     job.Status,
		}, true
	}
	return models.UIJob{}, false
}

// pkg/storage/store.go

func (s *MemoryStore) GetAllJobs() []models.UIJob {
	s.mu.RLock()
	defer s.mu.RUnlock() // Clean execution unlock wrapper fallback context

	var list []models.UIJob
	for _, job := range s.Jobs { // cite: 218
		if job == nil {
			continue
		}

		// ── DEEP STRUCT FIELD CLONING INSIDE READ MUTEX SCOPE ──
		list = append(list, models.UIJob{
			ID:         job.ID,
			FileName:   job.FileName,
			URL:        job.URL,
			Progress:   job.Progress,
			TotalSize:  job.TotalSize,
			Downloaded: job.Downloaded,
			Status:     job.Status,
		})
	}

	sort.Slice(list, func(i, j int) bool { // cite: 218
		valI := strings.TrimPrefix(list[i].ID, "job_") // cite: 218
		valJ := strings.TrimPrefix(list[j].ID, "job_") // cite: 218
		numI, _ := strconv.Atoi(valI)                  // cite: 218
		numJ, _ := strconv.Atoi(valJ)                  // cite: 218
		return numI < numJ                             // cite: 218
	}) // cite: 218

	return list // cite: 218
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

	// 4. Truncate filename if it exceeds 120 characters to avoid filesystem ENAMETOOLONG errors
	cleaned = TruncateFilename(cleaned, 120)

	return cleaned, nil
}

func TruncateFilename(path string, maxLen int) string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	if len(base) <= maxLen {
		return path
	}

	ext := filepath.Ext(base)
	if len(ext) > 10 {
		ext = ""
	}

	nameLen := maxLen - len(ext)
	if nameLen <= 0 {
		nameLen = maxLen
		ext = ""
	}

	truncatedBase := base[:nameLen] + ext
	return filepath.Join(dir, truncatedBase)
}
