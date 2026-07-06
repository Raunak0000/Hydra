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

// Shared synchronization references injected from main.go
var (
	GlobalCancelMap   map[string]context.CancelFunc
	GlobalCancelMutex *sync.Mutex
)

type Server struct {
	Router             *http.ServeMux
	ExecuteDownloadJob func(url string, savePath string, jobID string, headers map[string]string)
}

// pkg/storage/server.go -> Update your NewServer mapping block

func NewServer(executeJobFunc func(url string, savePath string, jobID string, headers map[string]string)) *Server {
	s := &Server{
		Router:             http.NewServeMux(),
		ExecuteDownloadJob: executeJobFunc,
	}

	// ── BULLETPROOF CORS MIDDLEWARE INTERCEPTOR ──
	withCORS := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Clear access barriers completely for extension runtime scopes
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Hydra-Token")

			// If browser is just probing for cross-origin permissions, intercept and approve instantly!
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}
			next(w, r)
		}
	}

	// ── SAME-ORIGIN SECURITY MIDDLEWARE INTERCEPTOR ──
	sameOriginOnly := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Verify Origin header if present (always present for cross-origin CORS/state-changing requests)
			if origin := r.Header.Get("Origin"); origin != "" {
				if origin != "http://127.0.0.1:9000" && origin != "http://localhost:9000" {
					http.Error(w, "Forbidden: Cross-Origin Request Blocked", http.StatusForbidden)
					return
				}
			}
			// Verify Referer header if present
			if referer := r.Header.Get("Referer"); referer != "" {
				if !strings.HasPrefix(referer, "http://127.0.0.1:9000") && !strings.HasPrefix(referer, "http://localhost:9000") {
					http.Error(w, "Forbidden: Cross-Origin Request Blocked", http.StatusForbidden)
					return
				}
			}
			next(w, r)
		}
	}

	// Bind your routes safely without rigid method prefix constraints
	s.Router.HandleFunc("/download", withCORS(s.handleDownloadTrigger))
	s.Router.HandleFunc("/", sameOriginOnly(s.handleRenderDashboard))
	s.Router.HandleFunc("/api/queue", sameOriginOnly(s.handleGetQueueSnippet))
	s.Router.HandleFunc("/api/queue/json", sameOriginOnly(s.handleGetQueueJSON))
	s.Router.HandleFunc("/api/download/pause", sameOriginOnly(s.handlePauseJob))
	s.Router.HandleFunc("/api/download/resume", sameOriginOnly(s.handleResumeJob))
	s.Router.HandleFunc("/api/download/delete", sameOriginOnly(s.handleDeleteJob))

	return s
}

// pkg/storage/server.go

func (s *Server) handleDownloadTrigger(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for cross-origin requests (e.g. from bookmarklets or browser pages)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Hydra-Token")

	// Handle preflight requests
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("X-Hydra-Token") != "hydra_secure_token_bf1f753e" {
		http.Error(w, "Unauthorized: Invalid or missing security token context", http.StatusUnauthorized)
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
		http.Error(w, "Malformed JSON payload body context", http.StatusBadRequest)
		return
	}

	if payload.URL == "" || payload.SavePath == "" {
		http.Error(w, "Missing url or save_path targeting strings", http.StatusUnprocessableEntity)
		return
	}

	// 1. If JobID is provided, this is the user submitting the chosen save path for a pending job
	if payload.JobID != "" {
		store := GetStore()
		store.mu.Lock()
		job, exists := store.Jobs[payload.JobID]
		if !exists || job == nil {
			store.mu.Unlock()
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}

		securedPath, err := SanitizeDownloadPath(payload.SavePath)
		if err != nil {
			store.mu.Unlock()
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		job.SavePath = securedPath
		job.Status = "DOWNLOADING"
		urlToDownload := job.URL
		headersToUse := job.Headers
		store.mu.Unlock()

		// Start the actual download runner now
		go s.ExecuteDownloadJob(urlToDownload, securedPath, payload.JobID, headersToUse)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "started",
			"job_id": payload.JobID,
		})
		return
	}

	// 2. If SavePath is "PENDING", register the job as PENDING_PATH without starting download
	var securedPath string
	var status string = "DOWNLOADING"
	var filename string = "Calculating..."

	if payload.SavePath == "PENDING" {
		securedPath = "PENDING"
		status = "PENDING_PATH"
		if payload.Filename != "" {
			filename = payload.Filename
		}
	} else {
		var err error
		securedPath, err = SanitizeDownloadPath(payload.SavePath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
	}

	store := GetStore()
	jobID := fmt.Sprintf("job_%d", len(store.GetAllJobs())+1)

	newJob := models.UIJob{
		ID:         jobID,
		FileName:   filename,
		URL:        payload.URL,
		SavePath:   securedPath,
		Progress:   0.0,
		Downloaded: "0.00 MB",
		Speed:      "0.00 KB/s",
		Status:     status,
		Headers:    payload.Headers,
	}

	store.SetJob(jobID, &newJob)

	if status == "DOWNLOADING" {
		go s.ExecuteDownloadJob(payload.URL, securedPath, jobID, payload.Headers)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "queued",
		"job_id": jobID,
	})
}

// ── FIXED VIEW RENDERING LOOP ──
func (s *Server) handleRenderDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Fetch all tracked active download components from memory cache store
	jobs := GetStore().GetAllJobs()

	// Render your main view template wrapper frame component straight to the connection writer stream
	err := views.Dashboard(jobs).Render(r.Context(), w)
	if err != nil {
		http.Error(w, "Failed to compile template UI elements: "+err.Error(), http.StatusInternalServerError)
	}
}

// ── pkg/storage/server.go ──
// Replace your existing handleGetQueueSnippet function at the bottom with this clean version:

func (s *Server) handleGetQueueSnippet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// 1. Fetch your thread-safe slice of flat jobs directly from the store helper
	jobSlice := GetStore().GetAllJobs()

	// 2. Call your QueueRows template function directly with the clean slice
	err := views.QueueRows(jobSlice).Render(r.Context(), w)
	if err != nil {
		http.Error(w, "Failed to render queue rows component frames: "+err.Error(), http.StatusInternalServerError)
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
			cancel() // TRIGGER THE GENTLE CONTEXT CANCEL THREAD INTERRUPT
		}
		GlobalCancelMutex.Unlock()
	}

	store := GetStore()
	store.UpdateStatus(jobID, "PAUSED")
	w.WriteHeader(http.StatusOK)
}

// pkg/storage/server.go -> Update your resume handler at the bottom

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "Missing job id parameter", http.StatusBadRequest)
		return
	}

	store := GetStore()

	// 1. Thread-safely extract the existing job details from memory cache
	store.mu.RLock()
	job, exists := store.Jobs[jobID]
	var targetURL, targetSavePath string
	var targetHeaders map[string]string
	if exists && job != nil {
		targetURL = job.URL
		targetSavePath = job.SavePath
		targetHeaders = job.Headers
	}
	store.mu.RUnlock()

	if !exists || job == nil {
		http.Error(w, "Job profile not found in active cache store", http.StatusNotFound)
		return
	}

	// 2. Mark its state back to DOWNLOADING so the UI updates
	store.UpdateStatus(jobID, "DOWNLOADING")

	// 3. 🚀 RE-LAUNCH THE DOWNLOAD CONCURRENCY WORKERS BACK INTO THE CORE PIPELINE!
	go s.ExecuteDownloadJob(targetURL, targetSavePath, jobID, targetHeaders)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "Missing job id parameter", http.StatusBadRequest)
		return
	}

	store := GetStore()

	// 1. Thread-safely extract target details inside a read lock to identify cleanup files
	store.mu.RLock()
	job, exists := store.Jobs[jobID]
	var targetSavePath string
	if exists && job != nil {
		targetSavePath = job.SavePath
	}
	store.mu.RUnlock()

	if !exists || job == nil {
		http.Error(w, "Job profile not found in active cache store", http.StatusNotFound)
		return
	}

	// 2. Trigger context cancellation to stop active worker network loops instantly
	if GlobalCancelMutex != nil && GlobalCancelMap != nil {
		GlobalCancelMutex.Lock()
		if cancel, active := GlobalCancelMap[jobID]; active {
			cancel()
		}
		GlobalCancelMutex.Unlock()
	}

	// 3. Clean out all file payloads and temporary tracking artifacts
	_ = os.Remove(targetSavePath)
	ClearJobState(targetSavePath)

	// 4. Evict the job tracking data profile index entirely
	store.DeleteJob(jobID)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetQueueJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	jobs := GetStore().GetAllJobs()
	_ = json.NewEncoder(w).Encode(jobs)
}
