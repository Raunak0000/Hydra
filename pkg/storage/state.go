package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Raunak0000/Hydra/pkg/models"
)

// SaveJobState atomically saves an active UIJob to its .hydra metadata file.
func SaveJobState(job models.UIJob) error {
	if job.SavePath == "" {
		return fmt.Errorf("job save path cannot be empty")
	}

	statePath := job.SavePath + ".hydra"
	dir := filepath.Dir(statePath)

	tmp, err := os.CreateTemp(dir, ".hydra-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}

	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set state file permissions: %w", err)
	}

	if err := json.NewEncoder(tmp).Encode(job); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode job state: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync job state: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}

	if err := os.Rename(tmpPath, statePath); err != nil {
		return fmt.Errorf("replace job state: %w", err)
	}

	return nil
}

// LoadJobState parses a saved .hydra state file from disk to resume a download
func LoadJobState(targetFilePath string) (models.UIJob, error) {
	statePath := targetFilePath + ".hydra"

	file, err := os.Open(statePath)
	if err != nil {
		return models.UIJob{}, err
	}
	defer file.Close()

	var job models.UIJob
	err = json.NewDecoder(file).Decode(&job)
	return job, err
}

// ClearJobState removes the metadata .hydra tracker file once a task finishes completely
func ClearJobState(targetFilePath string) {
	statePath := targetFilePath + ".hydra"
	_ = os.Remove(statePath)
}
