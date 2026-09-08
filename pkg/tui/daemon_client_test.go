package tui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDaemonClientActions(t *testing.T) {
	seen := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Hydra-Token") != "token" {
			t.Errorf("missing API token")
		}
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewDaemonClient(server.URL, "token")
	if err := client.PauseJob("job-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.ResumeJob("job-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteJob("job-1"); err != nil {
		t.Fatal(err)
	}

	want := []string{"POST /api/jobs/job-1/pause", "POST /api/jobs/job-1/resume", "DELETE /api/jobs/job-1"}
	for index := range want {
		if seen[index] != want[index] {
			t.Fatalf("request %d = %q, want %q", index, seen[index], want[index])
		}
	}
}

func TestDaemonClientActionRejectsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if err := NewDaemonClient(server.URL, "token").PauseJob("missing"); err == nil {
		t.Fatal("expected server error")
	}
}
