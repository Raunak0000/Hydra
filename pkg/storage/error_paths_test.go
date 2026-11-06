package storage

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIStructuredErrorResponses(t *testing.T) {
	originalToken := hydraAPIToken
	hydraAPIToken = "secret-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	server := NewServer(nil)

	tests := []struct {
		name         string
		method       string
		path         string
		headers      map[string]string
		body         string
		wantStatus   int
		wantCode     string
	}{
		{
			name:       "unauthorized download request",
			method:     http.MethodPost,
			path:       "/download",
			headers:    map[string]string{"X-Hydra-Token": "bad-token"},
			body:       `{"url":"https://example.com/file.bin","save_path":"/tmp/file.bin"}`,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "INVALID_TOKEN",
		},
		{
			name:       "malformed JSON download request",
			method:     http.MethodPost,
			path:       "/download",
			headers:    map[string]string{"X-Hydra-Token": "secret-token"},
			body:       `{not-valid-json`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "MALFORMED_JSON",
		},
		{
			name:       "missing save_path and url",
			method:     http.MethodPost,
			path:       "/download",
			headers:    map[string]string{"X-Hydra-Token": "secret-token"},
			body:       `{}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "MISSING_FIELDS",
		},
		{
			name:       "invalid filename traversal",
			method:     http.MethodPost,
			path:       "/download",
			headers:    map[string]string{"X-Hydra-Token": "secret-token"},
			body:       `{"url":"https://example.com/file.bin","save_path":"/tmp","filename":"../../etc/passwd"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_FILENAME",
		},
		{
			name:       "empty batch request",
			method:     http.MethodPost,
			path:       "/api/batch/download",
			headers:    map[string]string{"X-Hydra-Token": "secret-token"},
			body:       `{"urls":[]}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "EMPTY_BATCH",
		},
		{
			name:       "pause missing job id",
			method:     http.MethodGet,
			path:       "/api/download/pause",
			headers:    map[string]string{"Origin": "http://127.0.0.1:9000"},
			wantStatus: http.StatusBadRequest,
			wantCode:   "MISSING_JOB_ID",
		},
		{
			name:       "resume non-existent job",
			method:     http.MethodGet,
			path:       "/api/download/resume?id=non_existent_job",
			headers:    map[string]string{"Origin": "http://127.0.0.1:9000"},
			wantStatus: http.StatusNotFound,
			wantCode:   "JOB_NOT_FOUND",
		},
		{
			name:       "update url missing job id",
			method:     http.MethodPost,
			path:       "/api/download/update-url",
			headers:    map[string]string{"Origin": "http://127.0.0.1:9000"},
			wantStatus: http.StatusBadRequest,
			wantCode:   "MISSING_JOB_ID",
		},
		{
			name:       "delete non-existent job",
			method:     http.MethodGet,
			path:       "/api/download/delete?id=ghost_job_999",
			headers:    map[string]string{"Origin": "http://127.0.0.1:9000"},
			wantStatus: http.StatusNotFound,
			wantCode:   "JOB_NOT_FOUND",
		},
		{
			name:       "cross-origin forbidden origin",
			method:     http.MethodGet,
			path:       "/api/queue",
			headers:    map[string]string{"Origin": "http://malicious-site.com"},
			wantStatus: http.StatusForbidden,
			wantCode:   "FORBIDDEN_ORIGIN",
		},
		{
			name:       "method not allowed on browse directory",
			method:     http.MethodGet,
			path:       "/api/browse-directory",
			headers:    map[string]string{"Origin": "http://127.0.0.1:9000"},
			wantStatus: http.StatusMethodNotAllowed,
			wantCode:   "METHOD_NOT_ALLOWED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyReader *bytes.Reader
			if tt.body != "" {
				bodyReader = bytes.NewReader([]byte(tt.body))
			} else {
				bodyReader = bytes.NewReader(nil)
			}

			req := httptest.NewRequest(tt.method, tt.path, bodyReader)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			rec := httptest.NewRecorder()
			server.Router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d (body: %s)", tt.wantStatus, rec.Code, rec.Body.String())
			}

			var errResp struct {
				Code    string `json:"code"`
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
				t.Fatalf("failed to parse JSON error response: %v", err)
			}

			if errResp.Code != tt.wantCode {
				t.Errorf("expected error code %q, got %q", tt.wantCode, errResp.Code)
			}
			if errResp.Error == "" || errResp.Message == "" {
				t.Errorf("expected explicit, non-empty error message, got error=%q message=%q", errResp.Error, errResp.Message)
			}
		})
	}
}

