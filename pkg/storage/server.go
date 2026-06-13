package storage

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type DownloadRequest struct {
	URL      string `json:"url"`
	SavePath string `json:"save_path"`
}

func StartHTTPServer(downloadTrigger func(string, string)) {
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

		go downloadTrigger(req.URL, req.SavePath)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "SUCCESS",
			"message": "Job handed off to multi-threaded pipeline.",
		})
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprint(w, "Hydra IDM Engine Gateway is Running")
	})

	fmt.Println("[⚙] Hydra Local REST Server listening on http://localhost:9000")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		fmt.Printf("[X] Server failed to start: %v\n", err)
	}
}
