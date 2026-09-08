package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

var (
	GlobalCancelMap   map[string]context.CancelFunc
	GlobalCancelMutex *sync.Mutex
)

const hydraAPIToken = "hydra_secure_token_bf1f753e"

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
				if origin != "http://127.0.0.1:9000" && origin != "http://localhost:9000" {
					http.Error(w, "Forbidden: Cross-Origin Request Blocked", http.StatusForbidden)
					return
				}
			}
			if referer := r.Header.Get("Referer"); referer != "" {
				if !strings.HasPrefix(referer, "http://127.0.0.1:9000") && !strings.HasPrefix(referer, "http://localhost:9000") {
					http.Error(w, "Forbidden: Cross-Origin Request Blocked", http.StatusForbidden)
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
	s.Router.HandleFunc("/api/download/delete", sameOriginOnly(s.handleDeleteJob))
	s.Router.HandleFunc("/api/jobs", s.handleJobsAPI)
	s.Router.HandleFunc("/api/jobs/", s.handleJobAPI)
	s.Router.HandleFunc("/api/settings", sameOriginOnly(s.handleSettings))
	s.Router.HandleFunc("/api/browse-directory", sameOriginOnly(s.handleBrowseDirectory))
	s.Router.HandleFunc("/api/list-directory", sameOriginOnly(s.handleListDirectory))
	s.Router.HandleFunc("/api/disk-space", sameOriginOnly(s.handleDiskSpace))
	// Server-Sent Events real-time push endpoint
	s.Router.HandleFunc("/api/events", s.handleEventsStream)

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
		writeJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid Hydra API token")
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
		_ = json.NewEncoder(w).Encode(s.db.GetAllJobs())
	case http.MethodPost:
		s.handleDownloadTrigger(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

// handleJobAPI maps REST-style job paths to the existing control handlers.
func (s *Server) handleJobAPI(w http.ResponseWriter, r *http.Request) {
	if !requireAPIToken(w, r) {
		return
	}

	pathValue := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/")
	if pathValue == "" {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JOB_ID", "missing or invalid job id")
		return
	}

	jobID := pathValue
	for _, suffix := range []string{"/pause", "/resume"} {
		if strings.HasSuffix(pathValue, suffix) {
			jobID = strings.TrimSuffix(pathValue, suffix)
			break
		}
	}
	if jobID == "" || strings.Contains(jobID, "/") {
		writeJSONError(w, http.StatusBadRequest, "INVALID_JOB_ID", "missing or invalid job id")
		return
	}

	if _, exists := s.db.GetJob(jobID); !exists {
		writeJSONError(w, http.StatusNotFound, "JOB_NOT_FOUND", "job not found")
		return
	}

	r.URL.RawQuery = "id=" + url.QueryEscape(jobID)
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(pathValue, "/pause"):
		s.handlePauseJob(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(pathValue, "/resume"):
		s.handleResumeJob(w, r)
	case r.Method == http.MethodDelete:
		s.handleDeleteJob(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (s *Server) handleBrowseDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		CurrentPath string `json:"current_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)

	defaultPath := payload.CurrentPath
	if defaultPath == "" || defaultPath == "PENDING" || defaultPath == "DEFAULT" {
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
		_ = json.NewEncoder(w).Encode(cfg)

	case http.MethodPost:
		var updated Config
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			http.Error(w, "Malformed JSON config", http.StatusBadRequest)
			return
		}

		if err := SaveConfig(&updated); err != nil {
			http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
			return
		}

		if qm := GetQueueManager(); qm != nil && updated.MaxConcurrentDownloads > 0 {
			qm.mu.Lock()
			qm.maxConcurrent = updated.MaxConcurrentDownloads
			qm.mu.Unlock()
			qm.ProcessNext()
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "Settings updated"})

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("X-Hydra-Token") != hydraAPIToken {
		http.Error(w, "Unauthorized: Invalid security token", http.StatusUnauthorized)
		return
	}

	var payload struct {
		JobID       string            `json:"job_id"`
		URL         string            `json:"url"`
		SavePath    string            `json:"save_path"`
		Filename    string            `json:"filename"`
		ScheduledAt string            `json:"scheduled_at"`
		Headers     map[string]string `json:"headers"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Malformed JSON payload", http.StatusBadRequest)
		return
	}

	if payload.SavePath == "" || (payload.JobID == "" && payload.URL == "") {
		http.Error(w, "Missing url or save_path", http.StatusUnprocessableEntity)
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

	if payload.JobID != "" {
		job, exists := s.db.GetJob(payload.JobID)
		if !exists {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}

		targetSavePath := payload.SavePath
		if strings.HasSuffix(targetSavePath, "/") || strings.HasSuffix(targetSavePath, string(filepath.Separator)) {
			filename := job.FileName
			if filename == "" || filename == "Calculating..." || filename == "Pending path..." {
				parts := strings.Split(job.URL, "/")
				if len(parts) > 0 {
					filename = strings.Split(parts[len(parts)-1], "?")[0]
				}
			}
			if filename == "" {
				filename = "downloaded_file.bin"
			}
			targetSavePath = filepath.Join(targetSavePath, filename)
		}

		securedPath, err := ResolvePath(targetSavePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		job.SavePath = securedPath
		if parsedScheduledAt != nil && parsedScheduledAt.After(time.Now()) {
			job.Status = "SCHEDULED"
			job.ScheduledAt = parsedScheduledAt
			_ = s.db.SaveJob(&job)
		} else {
			job.Status = "QUEUED"
			_ = s.db.SaveJob(&job)
			if GetQueueManager() != nil {
				GetQueueManager().ProcessNext()
			}
		}
		GetBroker().BroadcastQueueState(s.db.GetAllJobs())

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": job.Status, "job_id": payload.JobID})
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
					filename = strings.Split(parts[len(parts)-1], "?")[0]
				}
			}
			if filename == "" {
				filename = "downloaded_file.bin"
			}
			filename = filepath.Base(filename)
			targetSavePath = filepath.Join(ResolveCategoryPath(filename), filename)
		} else if strings.HasSuffix(targetSavePath, "/") || strings.HasSuffix(targetSavePath, string(filepath.Separator)) {
			inferredName := payload.Filename
			if inferredName == "" || inferredName == "Calculating..." {
				parts := strings.Split(payload.URL, "/")
				if len(parts) > 0 {
					inferredName = strings.Split(parts[len(parts)-1], "?")[0]
				}
			}
			if inferredName == "" {
				inferredName = "downloaded_file.bin"
			}
			filename = inferredName
			targetSavePath = filepath.Join(targetSavePath, inferredName)
		} else {
			filename = filepath.Base(targetSavePath)
		}

		var err error
		securedPath, err = ResolvePath(targetSavePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

	if status != "PENDING_PATH" {
		if parsedScheduledAt != nil && parsedScheduledAt.After(time.Now()) {
			status = "SCHEDULED"
		} else {
			status = "QUEUED"
		}
	}

	newJob := models.UIJob{
		ID:          jobID,
		FileName:    filename,
		URL:         payload.URL,
		SavePath:    securedPath,
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
	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	if status == "QUEUED" && GetQueueManager() != nil {
		GetQueueManager().ProcessNext()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status, "job_id": jobID})
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
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("X-Hydra-Token") != hydraAPIToken {
		http.Error(w, "Unauthorized: Invalid security token", http.StatusUnauthorized)
		return
	}

	var payload BatchDownloadPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Malformed JSON payload", http.StatusBadRequest)
		return
	}

	if len(payload.URLs) == 0 {
		http.Error(w, "No URLs provided in batch request", http.StatusUnprocessableEntity)
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
	if baseDir == "" || baseDir == "PENDING" || baseDir == "DEFAULT" {
		baseDir = GetDefaultDownloadsDir()
	}

	resolvedBase, err := ResolvePath(baseDir + "/")
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid destination directory: %v", err), http.StatusBadRequest)
		return
	}

	batchID := fmt.Sprintf("batch_%d", time.Now().UnixNano())
	dispatchedJobIDs := make([]string, 0, len(payload.URLs))

	for _, rawURL := range payload.URLs {
		trimmedURL := strings.TrimSpace(rawURL)
		if trimmedURL == "" {
			continue
		}

		urlFilename := "downloaded_file.bin"
		parts := strings.Split(trimmedURL, "/")
		if len(parts) > 0 {
			cleanPart := strings.Split(parts[len(parts)-1], "?")[0]
			if cleanPart != "" {
				urlFilename = cleanPart
			}
		}

		categoryDir := ResolveCategoryPath(urlFilename)
		targetDir := resolvedBase
		if payload.SavePath == "DEFAULT" || payload.SavePath == "" {
			targetDir, _ = ResolvePath(categoryDir + "/")
		}

		finalFilePath := filepath.Join(targetDir, urlFilename)
		jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

		status := "QUEUED"
		if parsedScheduledAt != nil && parsedScheduledAt.After(time.Now()) {
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
		dispatchedJobIDs = append(dispatchedJobIDs, jobID)
	}

	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

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
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "running", "app": "Hydra Download Manager"})
}

func (s *Server) handleGetQueueSnippet(w http.ResponseWriter, r *http.Request) {
	s.handleGetQueueJSON(w, r)
}

func (s *Server) handlePauseJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "Missing job id parameter", http.StatusBadRequest)
		return
	}

	if GlobalCancelMutex != nil && GlobalCancelMap != nil {
		GlobalCancelMutex.Lock()
		if cancel, exists := GlobalCancelMap[jobID]; exists {
			cancel()
		}
		GlobalCancelMutex.Unlock()
	}

	_ = s.db.UpdateStatus(jobID, "PAUSED")
	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	if GetQueueManager() != nil {
		GetQueueManager().ProcessNext()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "job paused"})
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "Missing job id parameter", http.StatusBadRequest)
		return
	}

	_, exists := s.db.GetJob(jobID)
	if !exists {
		http.Error(w, "Job profile not found", http.StatusNotFound)
		return
	}

	_ = s.db.UpdateStatus(jobID, "QUEUED")
	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

	if GetQueueManager() != nil {
		GetQueueManager().ProcessNext()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "job resumed"})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "Missing job id parameter", http.StatusBadRequest)
		return
	}

	job, exists := s.db.GetJob(jobID)
	if !exists {
		http.Error(w, "Job profile not found", http.StatusNotFound)
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
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "job deleted"})
}

func (s *Server) handleGetQueueJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jobs := s.db.GetAllJobs()
	_ = json.NewEncoder(w).Encode(jobs)
}

func (s *Server) handleListDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)

	targetPath := payload.Path
	if targetPath == "" || targetPath == "~" || targetPath == "DEFAULT" || targetPath == "PENDING" {
		home, err := os.UserHomeDir()
		if err != nil {
			http.Error(w, "Cannot resolve home directory", http.StatusInternalServerError)
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
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)

	targetPath := payload.Path
	if targetPath == "" {
		targetPath = GetDefaultDownloadsDir()
	}

	resolved, err := ResolvePath(targetPath + "/")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"free_gb": "Unknown"})
		return
	}

	var stat syscall.Statfs_t
	freeGB := "Unknown"
	if syscall.Statfs(resolved, &stat) == nil {
		freeBytes := stat.Bavail * uint64(stat.Bsize)
		freeGB = fmt.Sprintf("%.2f GB", float64(freeBytes)/(1024*1024*1024))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"free_gb": freeGB})
}
