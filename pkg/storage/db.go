package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

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

		// Base Schema
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
			eta TEXT DEFAULT '--',
			status TEXT NOT NULL,
			max_speed_bytes INTEGER DEFAULT 0,
			expected_checksum TEXT DEFAULT '',
			checksum_algo TEXT DEFAULT '',
			checksum_verified INTEGER DEFAULT 0,
			chunks TEXT,
			headers TEXT,
			batch_id TEXT DEFAULT '',
			scheduled_at DATETIME,
			error_message TEXT DEFAULT '',
			completed_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		`
		if _, err := db.Exec(schema); err != nil {
			initErr = fmt.Errorf("failed to create database tables: %w", err)
			return
		}

		// Auto-migrations for existing databases
		db.Exec(`ALTER TABLE jobs ADD COLUMN eta TEXT DEFAULT '--';`)
		db.Exec(`ALTER TABLE jobs ADD COLUMN max_speed_bytes INTEGER DEFAULT 0;`)
		db.Exec(`ALTER TABLE jobs ADD COLUMN expected_checksum TEXT DEFAULT '';`)
		db.Exec(`ALTER TABLE jobs ADD COLUMN checksum_algo TEXT DEFAULT '';`)
		db.Exec(`ALTER TABLE jobs ADD COLUMN checksum_verified INTEGER DEFAULT 0;`)
		// ── Phase 2 Migrations ──
		db.Exec(`ALTER TABLE jobs ADD COLUMN batch_id TEXT DEFAULT '';`)
		db.Exec(`ALTER TABLE jobs ADD COLUMN scheduled_at DATETIME;`)
		db.Exec(`ALTER TABLE jobs ADD COLUMN error_message TEXT DEFAULT '';`)
		db.Exec(`ALTER TABLE jobs ADD COLUMN completed_at DATETIME;`)

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

	verifiedInt := 0
	if job.ChecksumVerified {
		verifiedInt = 1
	}

	var scheduledAtVal any = nil
	if job.ScheduledAt != nil {
		scheduledAtVal = job.ScheduledAt.Format(time.RFC3339)
	}

	var completedAtVal any = nil
	if job.CompletedAt != nil {
		completedAtVal = job.CompletedAt.Format(time.RFC3339)
	}

	query := `
	INSERT INTO jobs (
		id, file_name, url, save_path, progress, total_size, downloaded, speed, eta, status,
		max_speed_bytes, expected_checksum, checksum_algo, checksum_verified, chunks, headers,
		batch_id, scheduled_at, error_message, completed_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(id) DO UPDATE SET
		file_name = excluded.file_name,
		save_path = excluded.save_path,
		progress = excluded.progress,
		total_size = excluded.total_size,
		downloaded = excluded.downloaded,
		speed = excluded.speed,
		eta = excluded.eta,
		status = excluded.status,
		max_speed_bytes = excluded.max_speed_bytes,
		expected_checksum = excluded.expected_checksum,
		checksum_algo = excluded.checksum_algo,
		checksum_verified = excluded.checksum_verified,
		chunks = excluded.chunks,
		headers = excluded.headers,
		batch_id = excluded.batch_id,
		scheduled_at = excluded.scheduled_at,
		error_message = excluded.error_message,
		completed_at = excluded.completed_at,
		updated_at = CURRENT_TIMESTAMP;
	`
	_, err := s.db.Exec(query,
		job.ID, job.FileName, job.URL, job.SavePath,
		job.Progress, job.TotalSize, job.Downloaded, job.Speed, job.ETA, job.Status,
		job.MaxSpeedBytes, job.ExpectedChecksum, job.ChecksumAlgo, verifiedInt,
		string(chunksJSON), string(headersJSON),
		job.BatchID, scheduledAtVal, job.ErrorMessage, completedAtVal,
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
	    completed_at = CASE WHEN ? = 'COMPLETED' THEN CURRENT_TIMESTAMP ELSE completed_at END,
	    updated_at = CURRENT_TIMESTAMP
	WHERE id = ?;
	`
	_, err := s.db.Exec(query, progress, downloaded, speed, eta, status, status, filename, filename, filename, status, jobID)
	return err
}

func (s *DBStore) UpdateStatus(jobID string, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
	UPDATE jobs 
	SET status = ?,
	    completed_at = CASE WHEN ? = 'COMPLETED' THEN CURRENT_TIMESTAMP ELSE completed_at END,
	    updated_at = CURRENT_TIMESTAMP 
	WHERE id = ?;`
	_, err := s.db.Exec(query, status, status, jobID)
	return err
}

func (s *DBStore) UpdateErrorMessage(jobID string, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE jobs SET error_message = ?, status = 'FAILED', updated_at = CURRENT_TIMESTAMP WHERE id = ?;`, errMsg, jobID)
	return err
}

func (s *DBStore) UpdateChecksumVerified(jobID string, verified bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	val := 0
	if verified {
		val = 1
	}
	_, err := s.db.Exec(`UPDATE jobs SET checksum_verified = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`, val, jobID)
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

	query := `
	SELECT id, file_name, url, save_path, progress, total_size, downloaded, speed, eta, status,
	       max_speed_bytes, expected_checksum, checksum_algo, checksum_verified, chunks, headers,
	       batch_id, scheduled_at, error_message, completed_at, created_at
	FROM jobs WHERE id = ?;`
	row := s.db.QueryRow(query, jobID)

	job, err := scanJobRow(row.Scan)
	if err != nil {
		return models.UIJob{}, false
	}
	return job, true
}

func (s *DBStore) GetAllJobs() []models.UIJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, file_name, url, save_path, progress, total_size, downloaded, speed, eta, status,
	       max_speed_bytes, expected_checksum, checksum_algo, checksum_verified, chunks, headers,
	       batch_id, scheduled_at, error_message, completed_at, created_at
	FROM jobs ORDER BY created_at ASC;`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var list []models.UIJob
	for rows.Next() {
		if job, err := scanJobRow(rows.Scan); err == nil {
			list = append(list, job)
		}
	}
	return list
}

func (s *DBStore) GetPendingScheduledJobs() []models.UIJob {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
	SELECT id, file_name, url, save_path, progress, total_size, downloaded, speed, eta, status,
	       max_speed_bytes, expected_checksum, checksum_algo, checksum_verified, chunks, headers,
	       batch_id, scheduled_at, error_message, completed_at, created_at
	FROM jobs 
	WHERE status = 'SCHEDULED' AND scheduled_at <= CURRENT_TIMESTAMP;`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var list []models.UIJob
	for rows.Next() {
		if job, err := scanJobRow(rows.Scan); err == nil {
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

func scanJobRow(scanFn func(dest ...any) error) (models.UIJob, error) {
	var job models.UIJob
	var chunksStr, headersStr, etaStr, expCheckStr, algoStr, batchIdStr, errMsgStr sql.NullString
	var maxSpeed, verifiedInt sql.NullInt64
	var scheduledAt, completedAt, createdAt sql.NullTime

	err := scanFn(
		&job.ID, &job.FileName, &job.URL, &job.SavePath, &job.Progress, &job.TotalSize, &job.Downloaded, &job.Speed, &etaStr, &job.Status,
		&maxSpeed, &expCheckStr, &algoStr, &verifiedInt, &chunksStr, &headersStr,
		&batchIdStr, &scheduledAt, &errMsgStr, &completedAt, &createdAt,
	)
	if err != nil {
		return models.UIJob{}, err
	}

	if etaStr.Valid {
		job.ETA = etaStr.String
	}
	if maxSpeed.Valid {
		job.MaxSpeedBytes = maxSpeed.Int64
	}
	if expCheckStr.Valid {
		job.ExpectedChecksum = expCheckStr.String
	}
	if algoStr.Valid {
		job.ChecksumAlgo = algoStr.String
	}
	if verifiedInt.Valid {
		job.ChecksumVerified = verifiedInt.Int64 == 1
	}
	if batchIdStr.Valid {
		job.BatchID = batchIdStr.String
	}
	if errMsgStr.Valid {
		job.ErrorMessage = errMsgStr.String
	}
	if scheduledAt.Valid {
		job.ScheduledAt = &scheduledAt.Time
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	if createdAt.Valid {
		job.CreatedAt = createdAt.Time
	}
	if chunksStr.Valid && chunksStr.String != "" {
		_ = json.Unmarshal([]byte(chunksStr.String), &job.Chunks)
	}
	if headersStr.Valid && headersStr.String != "" {
		_ = json.Unmarshal([]byte(headersStr.String), &job.Headers)
	}

	return job, nil
}
