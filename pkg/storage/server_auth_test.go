package storage

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIAuthentication(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "test-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	server := NewServer(nil)

	tests := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{"missing token", "", http.StatusUnauthorized},
		{"invalid token", "wrong-token", http.StatusUnauthorized},
		{"valid token", "test-token", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
			if tt.token != "" {
				req.Header.Set("X-Hydra-Token", tt.token)
			}

			rec := httptest.NewRecorder()
			server.Router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d; body: %s",
					rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}
