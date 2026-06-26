package storage

import (
	"encoding/json"
	"os"

	"github.com/Raunak0000/Hydra/pkg/models"
)

// SaveJobState serializes an active UIJob's state layout directly into a .hydra metadata file
func SaveJobState(job models.UIJob) error {
	// ── FIX: Capitalized SavePath to match models.UIJob definition perfectly ──
	statePath := job.SavePath + ".hydra"

	file, err := os.OpenFile(statePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()

	return json.NewEncoder(file).Encode(job)
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
