package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

var (
	GlobalCancelMap   map[string]context.CancelFunc
	GlobalCancelMutex *sync.Mutex
)

var hydraAPIToken = os.Getenv("HYDRA_API_TOKEN")

type BatchDownloadPayload struct {
	URLs        []string          `json:"urls"`
	SavePath    string            `json:"save_path"`
	ScheduledAt string            `json:"scheduled_at,omitempty"`
	Headers     map[string]string `json:"headers"`
}

type Server struct {
	Router             *http.ServeMux
	ExecuteDownloadJob func(url string, savePath string, jobID string, headers map[string]string) error
	db                 *DBStore
}

func NewServer(executeJobFunc func(url string, savePath string, jobID string, headers map[string]string) error) *Server {
	dbStore, _ := GetDBStore()

	if strings.TrimSpace(hydraAPIToken) == "" {
		log.Fatal("HYDRA_API_TOKEN must be set")
	}

	s := &Server{
		Router:             http.NewServeMux(),
		ExecuteDownloadJob: executeJobFunc,
		db:                 dbStore,
	}

	withCORS := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Hydra-Token")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}

			next(w, r)
		}
	}

	sameOriginOnly := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" {
				if origin != "http://127.0.0.1:9000" &&
					origin != "http://localhost:9000" {
					writeJSONError(
						w,
						http.StatusForbidden,
						"FORBIDDEN_ORIGIN",
						"Forbidden: Cross-Origin Request Blocked",
					)
					return
				}
			}

			if referer := r.Header.Get("Referer"); referer != "" {
				if !strings.HasPrefix(referer, "http://127.0.0.1:9000") &&
					!strings.HasPrefix(referer, "http://localhost:9000") {
					writeJSONError(
						w,
						http.StatusForbidden,
						"FORBIDDEN_REFERER",
						"Forbidden: Cross-Origin Request Blocked",
					)
					return
				}
			}

			next(w, r)
		}
	}

	s.Router.HandleFunc("/download", withCORS(s.handleDownloadTrigger))
	s.Router.HandleFunc("/api/batch/download", withCORS(s.handleBatchDownloadTrigger))
	s.Router.HandleFunc("/", sameOriginOnly(s.handleRenderDashboard))
	s.Router.HandleFunc("/api/queue", sameOriginOnly(s.handleGetQueueSnippet))
	s.Router.HandleFunc("/api/queue/json", sameOriginOnly(s.handleGetQueueJSON))
	s.Router.HandleFunc("/api/download/pause", sameOriginOnly(s.handlePauseJob))
	s.Router.HandleFunc("/api/download/resume", sameOriginOnly(s.handleResumeJob))
	s.Router.HandleFunc("/api/download/update-url", sameOriginOnly(s.handleUpdateJobURL))
	s.Router.HandleFunc("/api/download/delete", sameOriginOnly(s.handleDeleteJob))
	s.Router.HandleFunc("/api/jobs", s.handleJobsAPI)
	s.Router.HandleFunc("/api/jobs/pause-all", s.handlePauseAllJobs)
	s.Router.HandleFunc("/api/jobs/resume-all", s.handleResumeAllJobs)
	s.Router.HandleFunc("/api/jobs/", s.handleJobAPI)
	s.Router.HandleFunc("/api/settings", sameOriginOnly(s.handleSettings))
	s.Router.HandleFunc("/api/rules", s.handleRulesAPI)
	s.Router.HandleFunc("/api/browse-directory", sameOriginOnly(s.handleBrowseDirectory))
	s.Router.HandleFunc("/api/list-directory", sameOriginOnly(s.handleListDirectory))
	s.Router.HandleFunc("/api/disk-space", sameOriginOnly(s.handleDiskSpace))
	s.Router.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if !requireAPIToken(w, r) {
			return
		}
		s.handleEventsStream(w, r)
	})

	return s
}

func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	GetBroker().ServeHTTP(w, r)
}

func writeJSONError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    code,
		"error":   message,
		"message": message,
	})
}

func requireAPIToken(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Hydra-Token") != hydraAPIToken {
		writeJSONError(
			w,
			http.StatusUnauthorized,
			"UNAUTHORIZED",
			"invalid Hydra API token",
		)
		return false
	}

	return true
}

// handleJobsAPI exposes the stable API used by the TUI and other local clients.
func (s *Server) handleJobsAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAPIToken(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		var jobs []models.UIJob
		if s.db != nil {
			jobs = s.db.GetAllJobs()
		}
		if jobs == nil {
			jobs = []models.UIJob{}
		}
		_ = json.NewEncoder(w).Encode(jobs)

	case http.MethodPost:
		s.handleDownloadTrigger(w, r)

	default:
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"method not allowed",
		)
	}
}

// handleJobAPI maps REST-style job paths to the existing control handlers.
func (s *Server) handleJobAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAPIToken(w, r) {
		return
	}

	pathValue := strings.Trim(
		strings.TrimPrefix(r.URL.Path, "/api/jobs/"),
		"/",
	)

	if pathValue == "" {
		writeJSONError(
			w,
			http.StatusBadRequest,
			"INVALID_JOB_ID",
			"missing or invalid job id",
		)
		return
	}

	jobID := pathValue

	for _, suffix := range []string{"/pause", "/resume", "/url"} {
		if strings.HasSuffix(pathValue, suffix) {
			jobID = strings.TrimSuffix(pathValue, suffix)
			break
		}
	}

	if jobID == "" || strings.Contains(jobID, "/") {
		writeJSONError(
			w,
			http.StatusBadRequest,
			"INVALID_JOB_ID",
			"missing or invalid job id",
		)
		return
	}

	if _, exists := s.db.GetJob(jobID); !exists {
		writeJSONError(
			w,
			http.StatusNotFound,
			"JOB_NOT_FOUND",
			"job not found",
		)
		return
	}

	r.URL.RawQuery = "id=" + url.QueryEscape(jobID)

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(pathValue, "/pause"):
		s.handlePauseJob(w, r)

	case r.Method == http.MethodPost && strings.HasSuffix(pathValue, "/resume"):
		s.handleResumeJob(w, r)

	case r.Method == http.MethodPost && strings.HasSuffix(pathValue, "/url"):
		s.handleUpdateJobURL(w, r)

	case r.Method == http.MethodDelete:
		s.handleDeleteJob(w, r)

	default:
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"method not allowed",
		)
	}
}

func (s *Server) handlePauseAllJobs(w http.ResponseWriter, r *http.Request) {
	if !requireAPIToken(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	paused := PauseAllJobs(s.db)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"paused": paused,
	})
}

func (s *Server) handleResumeAllJobs(w http.ResponseWriter, r *http.Request) {
	if !requireAPIToken(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}

	resumed := ResumeAllJobs(s.db)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"resumed": resumed,
	})
}

func (s *Server) handleBrowseDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"method not allowed",
		)
		return
	}

	var payload struct {
		CurrentPath string `json:"current_path"`
	}

	_ = json.NewDecoder(r.Body).Decode(&payload)

	defaultPath := payload.CurrentPath

	if defaultPath == "" ||
		defaultPath == "PENDING" ||
		defaultPath == "DEFAULT" {
		defaultPath = GetDefaultDownloadsDir()
	} else {
		resolved, err := ResolvePath(defaultPath)

		if err == nil {
			if stat, err := os.Stat(resolved); err == nil && !stat.IsDir() {
				defaultPath = filepath.Dir(resolved)
			} else if filepath.Ext(resolved) != "" {
				defaultPath = filepath.Dir(resolved)
			}
		}
	}

	selectedDir, err := ChooseFolderDialog(defaultPath)

	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    selectedDir,
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		cfg := GetConfig()

		if err := json.NewEncoder(w).Encode(cfg); err != nil {
			return
		}

	case http.MethodPost:
		var updated Config

		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			writeJSONError(
				w,
				http.StatusBadRequest,
				"INVALID_JSON",
				"malformed JSON config",
			)
			return
		}

		if updated.MaxConcurrentDownloads < 1 {
			writeJSONError(
				w,
				http.StatusBadRequest,
				"INVALID_CONCURRENCY",
				"max concurrent downloads must be at least 1",
			)
			return
		}

		if err := SaveConfig(&updated); err != nil {
			writeJSONError(
				w,
				http.StatusInternalServerError,
				"CONFIG_SAVE_FAILED",
				fmt.Sprintf("failed to save config: %v", err),
			)
			return
		}

		// Apply the new concurrency limit immediately.
		if qm := GetQueueManager(); qm != nil {
			qm.mu.Lock()
			qm.maxConcurrent = updated.MaxConcurrentDownloads
			qm.mu.Unlock()

			qm.ProcessNext()
		}

		w.WriteHeader(http.StatusOK)

		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "Settings updated",
		})

	default:
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"method not allowed",
		)
	}
}

func (s *Server) handleRulesAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAPIToken(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		cfg := GetConfig()
		_ = json.NewEncoder(w).Encode(cfg.Categories)

	case http.MethodPost:
		var req struct {
			Name       string   `json:"name"`
			Path       string   `json:"path"`
			Extensions []string `json:"extensions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "INVALID_JSON", "malformed JSON body")
			return
		}
		if req.Name == "" || req.Path == "" {
			writeJSONError(w, http.StatusBadRequest, "INVALID_INPUT", "name and path are required")
			return
		}
		if err := SetCategoryRule(req.Name, req.Path, req.Extensions); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"message": "rule saved",
		})

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			writeJSONError(w, http.StatusBadRequest, "INVALID_INPUT", "missing name parameter")
			return
		}
		if err := DeleteCategoryRule(name); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "DELETE_FAILED", err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"message": "rule deleted",
		})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *Server) handleDownloadTrigger(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Hydra-Token")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"Method Not Allowed",
		)
		return
	}

	if r.Header.Get("X-Hydra-Token") != hydraAPIToken {
		writeJSONError(
			w,
			http.StatusUnauthorized,
			"INVALID_TOKEN",
			"Unauthorized: Invalid security token",
		)
		return
	}

	var payload struct {
		JobID            string            `json:"job_id"`
		URL              string            `json:"url"`
		SavePath         string            `json:"save_path"`
		Filename         string            `json:"filename"`
		ScheduledAt      string            `json:"scheduled_at"`
		Headers          map[string]string `json:"headers"`
		MaxSpeedBytes    int64             `json:"max_speed_bytes"`
		ExpectedChecksum string            `json:"expected_checksum"`
		ChecksumAlgo     string            `json:"checksum_algo"`
		BatchID          string            `json:"batch_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(
			w,
			http.StatusBadRequest,
			"MALFORMED_JSON",
			"Malformed JSON payload",
		)
		return
	}

	// Explicit filename supplied by the caller.
	if payload.Filename != "" &&
		payload.Filename != "Calculating..." &&
		payload.Filename != "Pending path..." {

		if err := ValidateFilename(payload.Filename); err != nil {
			writeJSONError(
				w,
				http.StatusBadRequest,
				"INVALID_FILENAME",
				err.Error(),
			)
			return
		}
	}

	if payload.SavePath == "" ||
		(payload.JobID == "" && payload.URL == "") {
		writeJSONError(
			w,
			http.StatusUnprocessableEntity,
			"MISSING_FIELDS",
			"Missing url or save_path",
		)
		return
	}

	var parsedScheduledAt *time.Time

	if payload.ScheduledAt != "" {
		if t, err := time.Parse(time.RFC3339, payload.ScheduledAt); err == nil {
			parsedScheduledAt = &t
		} else if t, err := time.Parse("2006-01-02T15:04", payload.ScheduledAt); err == nil {
			parsedScheduledAt = &t
		}
	}

	// Existing job update.
	if payload.JobID != "" {
		job, exists := s.db.GetJob(payload.JobID)

		if !exists {
			writeJSONError(
				w,
				http.StatusNotFound,
				"JOB_NOT_FOUND",
				"Job not found",
			)
			return
		}

		if job.Status == "DOWNLOADING" || job.Status == "COMPLETED" {
			writeJSONError(
				w,
				http.StatusConflict,
				"INVALID_JOB_STATE",
				"Cannot change destination while download is downloading or completed",
			)
			return
		}

		targetSavePath := payload.SavePath

		if strings.HasSuffix(targetSavePath, "/") ||
			strings.HasSuffix(targetSavePath, string(filepath.Separator)) {

			filename := payload.Filename
			if filename == "" || filename == "Calculating..." || filename == "Pending path..." {
				filename = job.FileName
			}

			if filename == "" ||
				filename == "Calculating..." ||
				filename == "Pending path..." {

				parts := strings.Split(job.URL, "/")

				if len(parts) > 0 {
					filename = strings.Split(
						parts[len(parts)-1],
						"?",
					)[0]
				}
			}

			if filename == "" {
				filename = "downloaded_file.bin"
			}

			if err := ValidateFilename(filename); err != nil {
				writeJSONError(
					w,
					http.StatusBadRequest,
					"INVALID_FILENAME",
					err.Error(),
				)
				return
			}

			targetSavePath = filepath.Join(targetSavePath, filename)
		} else if payload.Filename != "" &&
			payload.Filename != "Calculating..." &&
			payload.Filename != "Pending path..." {
			targetSavePath = filepath.Join(filepath.Dir(targetSavePath), payload.Filename)
		}

		if baseName := filepath.Base(targetSavePath); baseName != "" {
			if err := ValidateFilename(baseName); err != nil {
				writeJSONError(
					w,
					http.StatusBadRequest,
					"INVALID_FILENAME",
					err.Error(),
				)
				return
			}
		}

		securedPath, err := ResolvePath(targetSavePath)

		if err != nil {
			writeJSONError(w, http.StatusForbidden, "INVALID_SAVE_PATH", err.Error())
			return
		}

		if len(job.Chunks) > 0 && job.SavePath != "" && job.SavePath != "PENDING" && job.SavePath != "DEFAULT" && securedPath != job.SavePath {
			writeJSONError(
				w,
				http.StatusConflict,
				"PARTIAL_TRANSFER_EXISTS",
				"Cannot move destination after partial chunk data has been written",
			)
			return
		}

		job.SavePath = securedPath
		job.FileName = filepath.Base(securedPath)
		job.ErrorMessage = ""
		if parsedScheduledAt != nil {
			job.ScheduledAt = parsedScheduledAt
		}
		if len(payload.Headers) > 0 {
			job.Headers = payload.Headers
		}
		if payload.MaxSpeedBytes > 0 {
			job.MaxSpeedBytes = payload.MaxSpeedBytes
		}
		if payload.ExpectedChecksum != "" {
			job.ExpectedChecksum = payload.ExpectedChecksum
		}
		if payload.ChecksumAlgo != "" {
			job.ChecksumAlgo = payload.ChecksumAlgo
		}
		if payload.BatchID != "" {
			job.BatchID = payload.BatchID
		}

		if parsedScheduledAt != nil &&
			parsedScheduledAt.After(time.Now()) {

			job.Status = "SCHEDULED"
			job.ScheduledAt = parsedScheduledAt
			_ = s.db.SaveJob(&job)
			NotifyDownloadScheduled(jobDisplayName(job.FileName, job.SavePath), parsedScheduledAt.Format(time.RFC822))

		} else {
			job.Status = "QUEUED"
			_ = s.db.SaveJob(&job)
			NotifyDownloadQueued(jobDisplayName(job.FileName, job.SavePath), job.SavePath)

			if GetQueueManager() != nil {
				GetQueueManager().ProcessNext()
			}
		}

		GetBroker().BroadcastQueueState(s.db.GetAllJobs())

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_ = json.NewEncoder(w).Encode(
			map[string]string{
				"status": job.Status,
				"job_id": payload.JobID,
			},
		)

		return
	}

	var securedPath string
	var status = "QUEUED"
	var filename = "Calculating..."

	if payload.SavePath == "PENDING" {
		securedPath = "PENDING"
		status = "PENDING_PATH"

		if payload.Filename != "" {
			filename = payload.Filename

			if err := ValidateFilename(filename); err != nil {
				writeJSONError(
					w,
					http.StatusBadRequest,
					"INVALID_FILENAME",
					err.Error(),
				)
				return
			}

			categoryDir := ResolveCategoryPath(filename)
			securedPath = filepath.Join(categoryDir, filename)
		}

		NotifyPendingPath(filename)

	} else {
		targetSavePath := payload.SavePath

		if targetSavePath == "DEFAULT" || targetSavePath == "" {
			filename = payload.Filename

			if filename == "" || filename == "Calculating..." {
				parts := strings.Split(payload.URL, "/")

				if len(parts) > 0 {
					filename = strings.Split(
						parts[len(parts)-1],
						"?",
					)[0]
				}
			}

			if filename == "" {
				filename = "downloaded_file.bin"
			}

			filename = filepath.Base(filename)

			if err := ValidateFilename(filename); err != nil {
				writeJSONError(
					w,
					http.StatusBadRequest,
					"INVALID_FILENAME",
					err.Error(),
				)
				return
			}

			targetSavePath = filepath.Join(
				ResolveCategoryPath(filename),
				filename,
			)

		} else if strings.HasSuffix(targetSavePath, "/") ||
			strings.HasSuffix(targetSavePath, string(filepath.Separator)) {

			inferredName := payload.Filename

			if inferredName == "" || inferredName == "Calculating..." {
				parts := strings.Split(payload.URL, "/")

				if len(parts) > 0 {
					inferredName = strings.Split(
						parts[len(parts)-1],
						"?",
					)[0]
				}
			}

			if inferredName == "" {
				inferredName = "downloaded_file.bin"
			}

			if err := ValidateFilename(inferredName); err != nil {
				writeJSONError(
					w,
					http.StatusBadRequest,
					"INVALID_FILENAME",
					err.Error(),
				)
				return
			}

			filename = inferredName
			targetSavePath = filepath.Join(
				targetSavePath,
				inferredName,
			)

		} else {
			filename = filepath.Base(targetSavePath)

			if err := ValidateFilename(filename); err != nil {
				writeJSONError(
					w,
					http.StatusBadRequest,
					"INVALID_FILENAME",
					err.Error(),
				)
				return
			}
		}

		var err error

		securedPath, err = ResolvePath(targetSavePath)

		if err != nil {
			writeJSONError(w, http.StatusForbidden, "INVALID_SAVE_PATH", err.Error())
			return
		}
	}

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

	if status != "PENDING_PATH" {
		if parsedScheduledAt != nil &&
			parsedScheduledAt.After(time.Now()) {
			status = "SCHEDULED"
		} else {
			status = "QUEUED"
		}
	}

	newJob := models.UIJob{
		ID:               jobID,
		FileName:         filename,
		URL:              payload.URL,
		SavePath:         securedPath,
		Progress:         0.0,
		TotalSize:        "Calculating...",
		Downloaded:       "0.00 MB",
		Speed:            "0.00 KB/s",
		ETA:              "--",
		Status:           status,
		ScheduledAt:      parsedScheduledAt,
		Headers:          payload.Headers,
		MaxSpeedBytes:    payload.MaxSpeedBytes,
		ExpectedChecksum: payload.ExpectedChecksum,
		ChecksumAlgo:     payload.ChecksumAlgo,
		BatchID:          payload.BatchID,
	}

	_ = s.db.SaveJob(&newJob)
	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	switch status {
	case "QUEUED":
		NotifyDownloadQueued(jobDisplayName(filename, securedPath), securedPath)
		if GetQueueManager() != nil {
			GetQueueManager().ProcessNext()
		}
	case "SCHEDULED":
		scheduledStr := ""
		if parsedScheduledAt != nil {
			scheduledStr = parsedScheduledAt.Format(time.RFC822)
		}
		NotifyDownloadScheduled(jobDisplayName(filename, securedPath), scheduledStr)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	_ = json.NewEncoder(w).Encode(
		map[string]string{
			"status": status,
			"job_id": jobID,
		},
	)
}

func (s *Server) handleBatchDownloadTrigger(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Hydra-Token")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"Method Not Allowed",
		)
		return
	}

	if r.Header.Get("X-Hydra-Token") != hydraAPIToken {
		writeJSONError(
			w,
			http.StatusUnauthorized,
			"INVALID_TOKEN",
			"Unauthorized: Invalid security token",
		)
		return
	}

	var payload BatchDownloadPayload

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(
			w,
			http.StatusBadRequest,
			"MALFORMED_JSON",
			"Malformed JSON payload",
		)
		return
	}

	if len(payload.URLs) == 0 {
		writeJSONError(
			w,
			http.StatusUnprocessableEntity,
			"EMPTY_BATCH",
			"No URLs provided in batch request",
		)
		return
	}

	var parsedScheduledAt *time.Time

	if payload.ScheduledAt != "" {
		if t, err := time.Parse(time.RFC3339, payload.ScheduledAt); err == nil {
			parsedScheduledAt = &t
		} else if t, err := time.Parse("2006-01-02T15:04", payload.ScheduledAt); err == nil {
			parsedScheduledAt = &t
		}
	}

	baseDir := payload.SavePath

	if baseDir == "" ||
		baseDir == "PENDING" ||
		baseDir == "DEFAULT" {
		baseDir = GetDefaultDownloadsDir()
	}

	resolvedBase, err := ResolvePath(baseDir + "/")

	if err != nil {
		writeJSONError(
			w,
			http.StatusBadRequest,
			"INVALID_DESTINATION_DIR",
			fmt.Sprintf("Invalid destination directory: %v", err),
		)
		return
	}

	batchID := fmt.Sprintf("batch_%d", time.Now().UnixNano())
	dispatchedJobIDs := make([]string, 0, len(payload.URLs))
	var firstFileName, firstFilePath string

	for _, rawURL := range payload.URLs {
		trimmedURL := strings.TrimSpace(rawURL)

		if trimmedURL == "" {
			continue
		}

		urlFilename := "downloaded_file.bin"
		parts := strings.Split(trimmedURL, "/")

		if len(parts) > 0 {
			cleanPart := strings.Split(
				parts[len(parts)-1],
				"?",
			)[0]

			if cleanPart != "" {
				urlFilename = cleanPart
			}
		}

		if err := ValidateFilename(urlFilename); err != nil {
			continue
		}

		categoryDir := ResolveCategoryPath(urlFilename)
		targetDir := resolvedBase

		if payload.SavePath == "DEFAULT" ||
			payload.SavePath == "" {
			targetDir, _ = ResolvePath(categoryDir + "/")
		}

		finalFilePath := filepath.Join(
			targetDir,
			urlFilename,
		)

		jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

		status := "QUEUED"

		if parsedScheduledAt != nil &&
			parsedScheduledAt.After(time.Now()) {
			status = "SCHEDULED"
		}

		newJob := models.UIJob{
			ID:          jobID,
			BatchID:     batchID,
			FileName:    urlFilename,
			URL:         trimmedURL,
			SavePath:    finalFilePath,
			Progress:    0.0,
			TotalSize:   "Calculating...",
			Downloaded:  "0.00 MB",
			Speed:       "0.00 KB/s",
			ETA:         "--",
			Status:      status,
			ScheduledAt: parsedScheduledAt,
			Headers:     payload.Headers,
		}

		_ = s.db.SaveJob(&newJob)
		if len(dispatchedJobIDs) == 0 {
			firstFileName = urlFilename
			firstFilePath = finalFilePath
		}
		dispatchedJobIDs = append(dispatchedJobIDs, jobID)
	}

	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	if len(dispatchedJobIDs) == 1 {
		NotifyDownloadQueued(firstFileName, firstFilePath)
	} else if len(dispatchedJobIDs) > 1 {
		NotifyBatchQueued(len(dispatchedJobIDs), resolvedBase)
	}

	if GetQueueManager() != nil {
		GetQueueManager().ProcessNext()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":          "accepted",
		"batch_id":        batchID,
		"total_queued":    len(dispatchedJobIDs),
		"dispatched_jobs": dispatchedJobIDs,
	})
}

func (s *Server) handleRenderDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "running",
		"app":    "Hydra Download Manager",
	})
}

func (s *Server) handleGetQueueSnippet(w http.ResponseWriter, r *http.Request) {
	s.handleGetQueueJSON(w, r)
}

func (s *Server) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")

	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "MISSING_JOB_ID", "Missing job id parameter")
		return
	}

	if s.db != nil {
		_ = s.db.UpdateStatus(jobID, "PAUSED")
		GetBroker().BroadcastQueueState(s.db.GetAllJobs())
	}

	if GlobalCancelMutex != nil && GlobalCancelMap != nil {
		GlobalCancelMutex.Lock()

		if cancel, exists := GlobalCancelMap[jobID]; exists {
			cancel()
		}

		GlobalCancelMutex.Unlock()
	}

	if GetQueueManager() != nil {
		GetQueueManager().ProcessNext()
	}

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(
		map[string]string{
			"status":  "ok",
			"message": "job paused",
		},
	)
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")

	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "MISSING_JOB_ID", "Missing job id parameter")
		return
	}

	if s.db == nil {
		writeJSONError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job profile not found")
		return
	}

	_, exists := s.db.GetJob(jobID)

	if !exists {
		writeJSONError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job profile not found")
		return
	}

	_ = s.db.UpdateStatus(jobID, "QUEUED")
	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	if GetQueueManager() != nil {
		GetQueueManager().ProcessNext()
	}

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(
		map[string]string{
			"status":  "ok",
			"message": "job resumed",
		},
	)
}

func (s *Server) handleUpdateJobURL(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")

	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "MISSING_JOB_ID", "Missing job id parameter")
		return
	}

	if s.db == nil {
		writeJSONError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job profile not found")
		return
	}

	_, exists := s.db.GetJob(jobID)
	if !exists {
		writeJSONError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job profile not found")
		return
	}

	var newURL string
	if r.Header.Get("Content-Type") == "application/json" {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			newURL = req.URL
		}
	} else {
		newURL = r.URL.Query().Get("url")
		if newURL == "" {
			newURL = r.FormValue("url")
		}
	}

	if newURL == "" {
		writeJSONError(w, http.StatusBadRequest, "MISSING_URL", "Missing url parameter")
		return
	}

	if err := s.db.UpdateJobURL(jobID, newURL); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "UPDATE_URL_FAILED", fmt.Sprintf("Failed to update URL: %v", err))
		return
	}

	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(
		map[string]string{
			"status":  "ok",
			"message": "job url updated",
		},
	)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")

	if jobID == "" {
		writeJSONError(w, http.StatusBadRequest, "MISSING_JOB_ID", "Missing job id parameter")
		return
	}

	if s.db == nil {
		writeJSONError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job profile not found")
		return
	}

	job, exists := s.db.GetJob(jobID)

	if !exists {
		writeJSONError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Job profile not found")
		return
	}

	if GlobalCancelMutex != nil && GlobalCancelMap != nil {
		GlobalCancelMutex.Lock()

		if cancel, active := GlobalCancelMap[jobID]; active {
			cancel()
		}

		GlobalCancelMutex.Unlock()
	}

	_ = os.Remove(job.SavePath)
	ClearJobState(job.SavePath)
	_ = s.db.DeleteJob(jobID)

	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	if qm := GetQueueManager(); qm != nil {
		qm.ProcessNext()
	}

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(
		map[string]string{
			"status":  "ok",
			"message": "job deleted",
		},
	)
}

func (s *Server) handleGetQueueJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	jobs := s.db.GetAllJobs()

	_ = json.NewEncoder(w).Encode(jobs)
}

func (s *Server) handleListDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"Method Not Allowed",
		)
		return
	}

	var payload struct {
		Path string `json:"path"`
	}

	_ = json.NewDecoder(r.Body).Decode(&payload)

	targetPath := payload.Path

	if targetPath == "" ||
		targetPath == "~" ||
		targetPath == "DEFAULT" ||
		targetPath == "PENDING" {

		home, err := os.UserHomeDir()

		if err != nil {
			writeJSONError(
				w,
				http.StatusInternalServerError,
				"RESOLVE_HOME_FAILED",
				"Cannot resolve home directory",
			)
			return
		}

		targetPath = home
	}

	resolved, err := ResolvePath(targetPath)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})

		return
	}

	if stat, statErr := os.Stat(resolved); statErr == nil && !stat.IsDir() {
		resolved = filepath.Dir(resolved)
	}

	entries, err := os.ReadDir(resolved)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Cannot read directory: %v", err),
		})

		return
	}

	type DirEntry struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size,omitempty"`
	}

	dirs := make([]DirEntry, 0)
	files := make([]DirEntry, 0)

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		fullPath := filepath.Join(resolved, entry.Name())

		de := DirEntry{
			Name:  entry.Name(),
			Path:  fullPath,
			IsDir: entry.IsDir(),
		}

		if !entry.IsDir() {
			if info, err := entry.Info(); err == nil {
				de.Size = info.Size()
			}
		}

		if entry.IsDir() {
			dirs = append(dirs, de)
		} else {
			files = append(files, de)
		}
	}

	parentPath := filepath.Dir(resolved)

	if parentPath == resolved {
		parentPath = ""
	}

	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"current":     resolved,
		"parent":      parentPath,
		"directories": dirs,
		"files":       files,
	})
}

func (s *Server) handleDiskSpace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(
			w,
			http.StatusMethodNotAllowed,
			"METHOD_NOT_ALLOWED",
			"method not allowed",
		)
		return
	}

	var payload struct {
		Path          string `json:"path"`
		RequiredBytes int64  `json:"required_bytes"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(
			w,
			http.StatusBadRequest,
			"INVALID_JSON",
			"malformed JSON payload",
		)
		return
	}

	targetPath := payload.Path

	if targetPath == "" {
		targetPath = GetDefaultDownloadsDir()
	}

	info, err := GetDiskSpace(
		targetPath,
		payload.RequiredBytes,
	)

	if err != nil {
		writeJSONError(
			w,
			http.StatusBadRequest,
			"DISK_SPACE_CHECK_FAILED",
			err.Error(),
		)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if payload.RequiredBytes > 0 && !info.EnoughSpace {
		w.WriteHeader(http.StatusInsufficientStorage)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	_ = json.NewEncoder(w).Encode(info)
}
