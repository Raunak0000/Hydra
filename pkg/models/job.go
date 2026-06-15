package models

// UIJob handles real-time download status metrics across the backend and frontend layers
type UIJob struct {
	ID         string  `json:"id"`
	FileName   string  `json:"file_name"`
	URL        string  `json:"url"`
	Progress   float64 `json:"progress"`
	TotalSize  string  `json:"total_size"`
	Downloaded string  `json:"downloaded"`
	Status     string  `json:"status"`
}
