package models

// ChunkState tracks the exact slice offset boundaries for a single thread
type ChunkState struct {
	Index         int   `json:"index"`
	Start         int64 `json:"start"`
	CurrentOffset int64 `json:"current_offset"`
	End           int64 `json:"end"`
	Completed     bool  `json:"completed"`
}

// UIJob handles real-time download status metrics across backend and frontend layers
type UIJob struct {
	ID               string            `json:"id"`
	FileName         string            `json:"file_name"`
	URL              string            `json:"url"`
	SavePath         string            `json:"save_path"`
	Progress         float64           `json:"progress"`
	TotalSize        string            `json:"total_size"`
	Downloaded       string            `json:"downloaded"`
	Speed            string            `json:"speed"`
	ETA              string            `json:"eta"`
	Status           string            `json:"status"`
	MaxSpeedBytes    int64             `json:"max_speed_bytes,omitempty"` // 0 = unlimited
	ExpectedChecksum string            `json:"expected_checksum,omitempty"`
	ChecksumAlgo     string            `json:"checksum_algo,omitempty"` // md5, sha256, crc32
	ChecksumVerified bool              `json:"checksum_verified,omitempty"`
	Chunks           []ChunkState      `json:"chunks,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
}
