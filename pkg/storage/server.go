package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/views"
)

type DownloadRequest struct {
	URL      string `json:"url"`
	SavePath string `json:"save_path"`
}

func StartHTTPServer(downloadTrigger func(string, string, string)) {
	store := GetStore()

	http.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req DownloadRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		jobID := fmt.Sprintf("job_%d", len(store.GetAllJobs())+1)
		fileName := req.SavePath
		if lastIdx := len(req.SavePath) - 1; lastIdx >= 0 {
			for i := lastIdx; i >= 0; i-- {
				if req.SavePath[i] == '/' {
					fileName = req.SavePath[i+1:]
					break
				}
			}
		}

		store.SetJob(jobID, &models.UIJob{
			ID:         jobID,
			FileName:   fileName,
			URL:        req.URL,
			Progress:   0.0,
			TotalSize:  "Calculating...",
			Downloaded: "0 B",
			Status:     "DOWNLOADING",
		})

		go downloadTrigger(req.URL, req.SavePath, jobID)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "SUCCESS",
			"message": "Job handed off to multi-threaded pipeline.",
		})
	})

	http.HandleFunc("/api/queue", func(w http.ResponseWriter, r *http.Request) {
		jobs := store.GetAllJobs()
		w.Header().Set("Content-Type", "text/html")
		views.QueueRows(jobs).Render(context.Background(), w)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		jobs := store.GetAllJobs()
		w.Header().Set("Content-Type", "text/html")
		views.Dashboard(jobs).Render(context.Background(), w)
	})

	fmt.Println("[⚙] Hydra UI Dashboard Server running on http://localhost:9000")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		fmt.Printf("[X] Server failed to start: %v\n", err)
	}
}
