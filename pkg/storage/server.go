package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/views"
)

var (
	GlobalCancelMap   map[string]context.CancelFunc
	GlobalCancelMutex *sync.Mutex
)

type BatchDownloadPayload struct {
	URLs        []string          `json:"urls"`
	SavePath    string            `json:"save_path"`
	ScheduledAt string            `json:"scheduled_at,omitempty"`
	Headers     map[string]string `json:"headers"`
}

type Server struct {
	Router             *http.ServeMux
	ExecuteDownloadJob func(url string, savePath string, jobID string, headers map[string]string)
	db                 *DBStore
}

func NewServer(executeJobFunc func(url string, savePath string, jobID string, headers map[string]string)) *Server {
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
	s.Router.HandleFunc("/api/settings", sameOriginOnly(s.handleSettings))
	s.Router.HandleFunc("/api/browse-directory", sameOriginOnly(s.handleBrowseDirectory))
	// Server-Sent Events real-time push endpoint
	s.Router.HandleFunc("/api/events", s.handleEventsStream)

	return s
}

func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	GetBroker().ServeHTTP(w, r)
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
		// If current path points to a file, browse starting from its parent directory
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

		// Dynamically update runtime queue concurrency limit
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

	if r.Header.Get("X-Hydra-Token") != "hydra_secure_token_bf1f753e" {
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

	// Parse optional schedule timestamp
	var parsedScheduledAt *time.Time
	if payload.ScheduledAt != "" {
		if t, err := time.Parse(time.RFC3339, payload.ScheduledAt); err == nil {
			parsedScheduledAt = &t
		} else if t, err := time.Parse("2006-01-02T15:04", payload.ScheduledAt); err == nil {
			parsedScheduledAt = &t
		}
	}

	// 1. User submitted path for a pending job
	if payload.JobID != "" {
		job, exists := s.db.GetJob(payload.JobID)
		if !exists {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}

		targetSavePath := payload.SavePath
		// If path is a folder, retain existing filename
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
		} else if GetQueueManager() != nil && GetQueueManager().ShouldQueue() {
			job.Status = "QUEUED"
			_ = s.db.SaveJob(&job)
		} else {
			job.Status = "DOWNLOADING"
			_ = s.db.SaveJob(&job)
			go s.ExecuteDownloadJob(job.URL, securedPath, payload.JobID, job.Headers)
		}
		GetBroker().BroadcastQueueState(s.db.GetAllJobs())

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": job.Status, "job_id": payload.JobID})
		return
	}

	// 2. New job creation
	var securedPath string
	var status = "DOWNLOADING"
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
		// If destination ends in directory slash, resolve filename automatically
		if strings.HasSuffix(targetSavePath, "/") || strings.HasSuffix(targetSavePath, string(filepath.Separator)) {
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

	// Generate collision-safe unique job ID using monotonic timestamp
	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

	// Determine if job is scheduled, queued, or running immediately
	if status == "DOWNLOADING" {
		if parsedScheduledAt != nil && parsedScheduledAt.After(time.Now()) {
			status = "SCHEDULED"
		} else if GetQueueManager().ShouldQueue() {
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

	if status == "DOWNLOADING" {
		go s.ExecuteDownloadJob(payload.URL, securedPath, jobID, payload.Headers)
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

	if r.Header.Get("X-Hydra-Token") != "hydra_secure_token_bf1f753e" {
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

	// Parse optional batch schedule timestamp
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

	resolvedBase, err := ResolvePath(baseDir)
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

		// Extract suggested filename from URL path
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
			targetDir, _ = ResolvePath(categoryDir)
		}

		finalFilePath := filepath.Join(targetDir, urlFilename)
		jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

		status := "DOWNLOADING"
		if parsedScheduledAt != nil && parsedScheduledAt.After(time.Now()) {
			status = "SCHEDULED"
		} else if GetQueueManager().ShouldQueue() {
			status = "QUEUED"
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

		if status == "DOWNLOADING" {
			go s.ExecuteDownloadJob(trimmedURL, finalFilePath, jobID, payload.Headers)
		}
	}

	GetBroker().BroadcastQueueState(s.db.GetAllJobs())

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	jobs := s.db.GetAllJobs()
	if err := views.Dashboard(jobs, "hydra_secure_token_bf1f753e").Render(r.Context(), w); err != nil {
		http.Error(w, "Failed to compile dashboard: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleGetQueueSnippet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	jobs := s.db.GetAllJobs()
	if err := views.QueueRows(jobs).Render(r.Context(), w); err != nil {
		http.Error(w, "Failed to render queue: "+err.Error(), http.StatusInternalServerError)
	}
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
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
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

	if GetQueueManager() != nil && GetQueueManager().ShouldQueue() {
		_ = s.db.UpdateStatus(jobID, "QUEUED")
	} else {
		_ = s.db.UpdateStatus(jobID, "DOWNLOADING")
		go s.ExecuteDownloadJob(job.URL, job.SavePath, jobID, job.Headers)
	}

	GetBroker().BroadcastQueueState(s.db.GetAllJobs())
	w.WriteHeader(http.StatusOK)
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

	// Free slot for next queued job
	if qm := GetQueueManager(); qm != nil {
		qm.ProcessNext()
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetQueueJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jobs := s.db.GetAllJobs()
	_ = json.NewEncoder(w).Encode(jobs)
}
