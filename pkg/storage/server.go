package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/views"
)

var (
	GlobalCancelMap   map[string]context.CancelFunc
	GlobalCancelMutex *sync.Mutex
)

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
	s.Router.HandleFunc("/", sameOriginOnly(s.handleRenderDashboard))
	s.Router.HandleFunc("/api/queue", sameOriginOnly(s.handleGetQueueSnippet))
	s.Router.HandleFunc("/api/queue/json", sameOriginOnly(s.handleGetQueueJSON))
	s.Router.HandleFunc("/api/download/pause", sameOriginOnly(s.handlePauseJob))
	s.Router.HandleFunc("/api/download/resume", sameOriginOnly(s.handleResumeJob))
	s.Router.HandleFunc("/api/download/delete", sameOriginOnly(s.handleDeleteJob))
	// Server-Sent Events real-time push endpoint
	s.Router.HandleFunc("/api/events", s.handleEventsStream)

	return s
}

func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	GetBroker().ServeHTTP(w, r)
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
		JobID    string            `json:"job_id"`
		URL      string            `json:"url"`
		SavePath string            `json:"save_path"`
		Filename string            `json:"filename"`
		Headers  map[string]string `json:"headers"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Malformed JSON payload", http.StatusBadRequest)
		return
	}

	if payload.URL == "" || payload.SavePath == "" {
		http.Error(w, "Missing url or save_path", http.StatusUnprocessableEntity)
		return
	}

	// 1. User submitted path for a pending job
	if payload.JobID != "" {
		job, exists := s.db.GetJob(payload.JobID)
		if !exists {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}

		securedPath, err := ResolvePath(payload.SavePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		job.SavePath = securedPath
		job.Status = "DOWNLOADING"
		_ = s.db.SaveJob(&job)

		go s.ExecuteDownloadJob(job.URL, securedPath, payload.JobID, job.Headers)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "started", "job_id": payload.JobID})
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
		}
	} else {
		var err error
		securedPath, err = ResolvePath(payload.SavePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	// Inside handleDownloadTrigger:
	jobID := fmt.Sprintf("job_%d", len(s.db.GetAllJobs())+1)

	// Determine if job should run immediately or queue
	if status == "DOWNLOADING" && GetQueueManager().ShouldQueue() {
		status = "QUEUED"
	}

	newJob := models.UIJob{
		ID:         jobID,
		FileName:   filename,
		URL:        payload.URL,
		SavePath:   securedPath,
		Progress:   0.0,
		TotalSize:  "Calculating...",
		Downloaded: "0.00 MB",
		Speed:      "0.00 KB/s",
		ETA:        "--",
		Status:     status,
		Headers:    payload.Headers,
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

func (s *Server) handleRenderDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	jobs := s.db.GetAllJobs()
	if err := views.Dashboard(jobs).Render(r.Context(), w); err != nil {
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

	// 🚀 Check if a queued task can now run
	GetQueueManager().ProcessNext()
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

	if GetQueueManager().ShouldQueue() {
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

	// 🚀 Free slot for next queued job
	GetQueueManager().ProcessNext()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetQueueJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jobs := s.db.GetAllJobs()
	_ = json.NewEncoder(w).Encode(jobs)
}
