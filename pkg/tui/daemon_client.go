package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

// DaemonClient reads Hydra state through the stable HTTP API.
type DaemonClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type CreateDownloadRequest struct {
	URL              string            `json:"url"`
	SavePath         string            `json:"save_path"`
	Filename         string            `json:"filename,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	MaxSpeedBytes    int64             `json:"max_speed_bytes,omitempty"`
	ExpectedChecksum string            `json:"expected_checksum,omitempty"`
	ChecksumAlgo     string            `json:"checksum_algo,omitempty"`
	ScheduledAt      string            `json:"scheduled_at,omitempty"`
	BatchID          string            `json:"batch_id,omitempty"`
}

func (c *DaemonClient) CreateDownload(download CreateDownloadRequest) (string, error) {
	body, err := json.Marshal(download)
	if err != nil {
		return "", fmt.Errorf("encode download request: %w", err)
	}

	req, err := http.NewRequest(
		http.MethodPost,
		c.BaseURL+"/api/jobs",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", fmt.Errorf("create download request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hydra-Token", c.Token)
	req.Header.Set("User-Agent", "Hydra-TUI/0.1")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK ||
		resp.StatusCode >= http.StatusMultipleChoices {

		return "", fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}

	var response struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("decode create response: %w", err)
	}

	if response.JobID == "" {
		return "", fmt.Errorf("daemon did not return a job ID")
	}

	return response.JobID, nil
}

func NewDaemonClient(baseURL, token string) *DaemonClient {
	return &DaemonClient{
		BaseURL: baseURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (c *DaemonClient) GetJobs() ([]models.UIJob, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/api/jobs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Hydra-Token", c.Token)
	req.Header.Set("User-Agent", "Hydra-TUI/0.1")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}

	var jobs []models.UIJob
	if err := json.NewDecoder(resp.Body).Decode(&jobs); err != nil {
		return nil, fmt.Errorf("decode jobs response: %w", err)
	}
	return jobs, nil
}

func (c *DaemonClient) PauseJob(jobID string) error {
	return c.doAction(http.MethodPost, "/api/jobs/"+jobID+"/pause")
}

func (c *DaemonClient) ResumeJob(jobID string) error {
	return c.doAction(http.MethodPost, "/api/jobs/"+jobID+"/resume")
}

func (c *DaemonClient) DeleteJob(jobID string) error {
	return c.doAction(http.MethodDelete, "/api/jobs/"+jobID)
}

func (c *DaemonClient) doAction(method, path string) error {
	req, err := http.NewRequest(method, c.BaseURL+path, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	req.Header.Set("X-Hydra-Token", c.Token)
	req.Header.Set("User-Agent", "Hydra-TUI/0.1")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}
	return nil
}
