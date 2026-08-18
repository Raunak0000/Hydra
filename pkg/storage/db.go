// pkg/storage/db.go

package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/models"
	_ "modernc.org/sqlite"
)

type DBStore struct {
	db *sql.DB
	mu sync.RWMutex
}

var (
	globalDBStore *DBStore
	dbOnce        sync.Once
)

func GetDBStore() (*DBStore, error) {
	var initErr error
	dbOnce.Do(func() {
		dataDir, err := GetDataDir()
		if err != nil {
			initErr = err
			return
		}

		dbPath := filepath.Join(dataDir, "hydra.db")
		db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
		if err != nil {
			initErr = fmt.Errorf("failed to open sqlite database: %w", err)
			return
		}

		// Enforce WAL mode for non-blocking concurrent reads/writes
		if _, err := db.Exec(`
			PRAGMA journal_mode = WAL;
			PRAGMA synchronous = NORMAL;
			PRAGMA foreign_keys = ON;
		`); err != nil {
			initErr = fmt.Errorf("failed to configure sqlite pragma: %w", err)
			return
		}

		// Initialize Schema
		schema := `
		CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			file_name TEXT NOT NULL,
			url TEXT NOT NULL,
			save_path TEXT NOT NULL,
			progress REAL DEFAULT 0.0,
			total_size TEXT DEFAULT 'Calculating...',
			downloaded TEXT DEFAULT '0.00 MB',
			speed TEXT DEFAULT '0.00 KB/s',
			status TEXT NOT NULL,
			chunks TEXT,
			headers TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		`
		if _, err := db.Exec(schema); err != nil {
			initErr = fmt.Errorf("failed to create database tables: %w", err)
			return
		}

		globalDBStore = &DBStore{db: db}
	})

	return globalDBStore, initErr
}

func (s *DBStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *DBStore) SaveJob(job *models.UIJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chunksJSON, _ := json.Marshal(job.Chunks)
	headersJSON, _ := json.Marshal(job.Headers)

	query := `
	INSERT INTO jobs (id, file_name, url, save_path, progress, total_size, downloaded, speed, status, chunks, headers, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(id) DO UPDATE SET
		file_name = excluded.file_name,
		save_path = excluded.save_path,
		progress = excluded.progress,
		total_size = excluded.total_size,
		downloaded = excluded.downloaded,
		speed = excluded.speed,
		status = excluded.status,
		chunks = excluded.chunks,
		headers = excluded.headers,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := s.db.Exec(query,
		job.ID, job.FileName, job.URL, job.SavePath,
		job.Progress, job.TotalSize, job.Downloaded, job.Speed,
		job.Status, string(chunksJSON), string(headersJSON),
	)
	return err
}

func (s *DBStore) UpdateTotalSize(jobID string, totalSize string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE jobs SET total_size = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`, totalSize, jobID)
	return err
}

func (s *DBStore) UpdateProgress(jobID string, progress float64, downloaded string, speed string, eta string, filename string, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	UPDATE jobs 
	SET progress = ?, downloaded = ?, speed = ?, eta = ?, status = CASE WHEN status IN ('COMPLETED', 'FAILED') AND ? = 'DOWNLOADING' THEN status ELSE ? END,
	    file_name = CASE WHEN ? != '' AND ? != 'Calculating...' THEN ? ELSE file_name END,
	    updated_at = CURRENT_TIMESTAMP
	WHERE id = ?;
	`
	_, err := s.db.Exec(query, progress, downloaded, speed, eta, status, status, filename, filename, filename, jobID)
	return err
}

func (s *DBStore) UpdateStatus(jobID string, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE jobs SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`, status, jobID)
	return err
}

func (s *DBStore) UpdateJobChunks(jobID string, chunks []models.ChunkState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	chunksJSON, _ := json.Marshal(chunks)
	_, err := s.db.Exec(`UPDATE jobs SET chunks = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`, string(chunksJSON), jobID)
	return err
}

func (s *DBStore) GetJob(jobID string) (models.UIJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT id, file_name, url, save_path, progress, total_size, downloaded, speed, status, chunks, headers FROM jobs WHERE id = ?;`
	row := s.db.QueryRow(query, jobID)

	var job models.UIJob
	var chunksStr, headersStr sql.NullString

	err := row.Scan(&job.ID, &job.FileName, &job.URL, &job.SavePath, &job.Progress, &job.TotalSize, &job.Downloaded, &job.Speed, &job.Status, &chunksStr, &headersStr)
	if err != nil {
		return models.UIJob{}, false
	}

	if chunksStr.Valid && chunksStr.String != "" {
		_ = json.Unmarshal([]byte(chunksStr.String), &job.Chunks)
	}
	if headersStr.Valid && headersStr.String != "" {
		_ = json.Unmarshal([]byte(headersStr.String), &job.Headers)
	}

	return job, true
}

func (s *DBStore) GetAllJobs() []models.UIJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `SELECT id, file_name, url, save_path, progress, total_size, downloaded, speed, status, chunks, headers FROM jobs ORDER BY created_at ASC;`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var list []models.UIJob
	for rows.Next() {
		var job models.UIJob
		var chunksStr, headersStr sql.NullString
		if err := rows.Scan(&job.ID, &job.FileName, &job.URL, &job.SavePath, &job.Progress, &job.TotalSize, &job.Downloaded, &job.Speed, &job.Status, &chunksStr, &headersStr); err == nil {
			if chunksStr.Valid && chunksStr.String != "" {
				_ = json.Unmarshal([]byte(chunksStr.String), &job.Chunks)
			}
			if headersStr.Valid && headersStr.String != "" {
				_ = json.Unmarshal([]byte(headersStr.String), &job.Headers)
			}
			list = append(list, job)
		}
	}
	return list
}

func (s *DBStore) DeleteJob(jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?;`, jobID)
	return err
}
