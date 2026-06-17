package storage

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/views"
)

type Server struct {
	Router             *http.ServeMux
	ExecuteDownloadJob func(url string, savePath string, jobID string)
}

func NewServer(executeJobFunc func(url string, savePath string, jobID string)) *Server {
	s := &Server{
		Router:             http.NewServeMux(),
		ExecuteDownloadJob: executeJobFunc,
	}

	s.Router.HandleFunc("POST /download", s.handleDownloadTrigger)
	s.Router.HandleFunc("GET /", s.handleRenderDashboard)
	s.Router.HandleFunc("GET /api/queue", s.handleGetQueueSnippet)

	return s
}

func (s *Server) handleDownloadTrigger(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		URL      string `json:"url"`
		SavePath string `json:"save_path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Malformed JSON payload body context", http.StatusBadRequest)
		return
	}

	if payload.URL == "" || payload.SavePath == "" {
		http.Error(w, "Missing url or save_path targeting strings", http.StatusUnprocessableEntity)
		return
	}

	store := GetStore()
	jobID := fmt.Sprintf("job_%d", len(store.GetAllJobs()))

	// 1. Initialize a clean direct struct instance (NO ampersand & pointer)
	newJob := models.UIJob{
		ID:         jobID,
		FileName:   "Calculating...",
		URL:        payload.URL,
		Progress:   0.0,
		Downloaded: "0.00 MB",
		Status:     "DOWNLOADING",
	}

	// 2. Save the flat value direct into the cache structure map safely
	store.SetJob(jobID, &newJob)

	// 3. Fire off the core multi-threaded execution handler routine background goroutine
	go s.ExecuteDownloadJob(payload.URL, payload.SavePath, jobID)

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

// ── FIXED HTMX QUEUE SNIPPET POLLING ENDPOINT ──
func (s *Server) handleGetQueueSnippet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// 1. Fetch your thread-safe map store
	jobMap := GetStore().GetAllJobs()

	// 2. Convert map[string]models.UIJob directly into a flat slice of []models.UIJob
	var jobSlice []models.UIJob
	for _, job := range jobMap {
		// Since 'job' is already a flat models.UIJob value, we append it directly
		// with no nil checks or dereference pointers needed!
		jobSlice = append(jobSlice, job)
	}

	// 3. Call your QueueRows template function
	err := views.QueueRows(jobSlice).Render(r.Context(), w)
	if err != nil {
		http.Error(w, "Failed to render queue rows component frames: "+err.Error(), http.StatusInternalServerError)
	}
}
