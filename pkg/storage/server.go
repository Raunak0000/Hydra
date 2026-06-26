package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	ExecuteDownloadJob func(url string, savePath string, jobID string)
}

// pkg/storage/server.go -> Update your NewServer mapping block

func NewServer(executeJobFunc func(url string, savePath string, jobID string)) *Server {
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
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

			// If browser is just probing for cross-origin permissions, intercept and approve instantly!
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}
			next(w, r)
		}
	}

	// Bind your routes safely without rigid method prefix constraints
	s.Router.HandleFunc("/download", withCORS(s.handleDownloadTrigger))
	s.Router.HandleFunc("/", s.handleRenderDashboard)
	s.Router.HandleFunc("/api/queue", s.handleGetQueueSnippet)
	s.Router.HandleFunc("/api/download/pause", s.handlePauseJob)
	s.Router.HandleFunc("/api/download/resume", s.handleResumeJob)

	return s
}

// pkg/storage/server.go

func (s *Server) handleDownloadTrigger(w http.ResponseWriter, r *http.Request) {
	// Enable CORS for cross-origin requests (e.g. from bookmarklets or browser pages)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle preflight requests
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		URL      string `json:"url"`
		SavePath string `json:"save_path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil { // cite: 212
		http.Error(w, "Malformed JSON payload body context", http.StatusBadRequest) // cite: 212
		return
	}

	if payload.URL == "" || payload.SavePath == "" { // cite: 213
		http.Error(w, "Missing url or save_path targeting strings", http.StatusUnprocessableEntity) // cite: 213
		return
	}

	// ── SANITIZE AND ANCHOR THE SAVEPATH INPUT ──
	securedPath, err := SanitizeDownloadPath(payload.SavePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	store := GetStore()
	jobID := fmt.Sprintf("job_%d", len(store.GetAllJobs())+1) // Balanced uniform ID alignment

	newJob := models.UIJob{ // cite: 213
		ID:         jobID,            // cite: 213
		FileName:   "Calculating...", // cite: 213
		URL:        payload.URL,      // cite: 213
		SavePath:   securedPath,      // Persist the final absolute path
		Progress:   0.0,              // cite: 213
		Downloaded: "0.00 MB",        // cite: 213
		Status:     "DOWNLOADING",    // cite: 213
	} // cite: 213

	store.SetJob(jobID, &newJob) // cite: 213

	// Pass the verified secure path down to the engine runner
	go s.ExecuteDownloadJob(payload.URL, securedPath, jobID)

	w.Header().Set("Content-Type", "application/json") // cite: 213
	w.WriteHeader(http.StatusAccepted)                 // cite: 213
	json.NewEncoder(w).Encode(map[string]string{       // cite: 213
		"status": "queued", // cite: 213
		"job_id": jobID,    // cite: 213
	}) // cite: 213
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
	if exists && job != nil {
		targetURL = job.URL
		targetSavePath = job.SavePath
	}
	store.mu.RUnlock()

	if !exists || job == nil {
		http.Error(w, "Job profile not found in active cache store", http.StatusNotFound)
		return
	}

	// 2. Mark its state back to DOWNLOADING so the UI updates
	store.UpdateStatus(jobID, "DOWNLOADING")

	// 3. 🚀 RE-LAUNCH THE DOWNLOAD CONCURRENCY WORKERS BACK INTO THE CORE PIPELINE!
	go s.ExecuteDownloadJob(targetURL, targetSavePath, jobID)

	w.WriteHeader(http.StatusOK)
}
