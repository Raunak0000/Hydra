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

// pkg/storage/server.go

func (s *Server) handleDownloadTrigger(w http.ResponseWriter, r *http.Request) {
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
