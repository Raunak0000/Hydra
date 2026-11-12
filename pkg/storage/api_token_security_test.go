package storage

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityReview_APITokenHasNoStaticDefaultsAndProtectsRoutes(t *testing.T) {
	originalToken := hydraAPIToken
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	// Provision two separate fresh config homes and confirm distinct random 64-hex-char tokens.
	cfgA := t.TempDir()
	cfgB := t.TempDir()
	t.Setenv("HYDRA_API_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", cfgA)
	tokA, err := EnsureAPIToken()
	if err != nil {
		t.Fatalf("EnsureAPIToken cfgA: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", cfgB)
	tokB, err := EnsureAPIToken()
	if err != nil {
		t.Fatalf("EnsureAPIToken cfgB: %v", err)
	}

	if len(tokA) != 64 || len(tokB) != 64 || tokA == tokB {
		t.Fatalf("expected unique 64-hex-char random tokens, got lenA=%d lenB=%d equal=%v", len(tokA), len(tokB), tokA == tokB)
	}

	hydraAPIToken = tokA
	store := newTestDBStore(t)
	server := NewServer(nil)
	server.db = store

	protectedEndpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/jobs", ""},
		{http.MethodPost, "/download", `{"url":"https://example.com/a.zip","save_path":"/tmp/a.zip"}`},
		{http.MethodPost, "/api/batch/download", `{"urls":["https://example.com/a.zip"],"save_path":"DEFAULT"}`},
		{http.MethodGet, "/api/rules", ""},
		{http.MethodPost, "/api/rules", `{"name":"Test","path":"~/Downloads","extensions":["zip"]}`},
		{http.MethodPost, "/api/settings", `{"max_concurrent_downloads":2}`},
		{http.MethodGet, "/api/events", ""},
	}

	for _, ep := range protectedEndpoints {
		for _, candidateToken := range []string{"", "dev-token", "hydra", "secret", tokB} {
			req := httptest.NewRequest(ep.method, ep.path, strings.NewReader(ep.body))
			if candidateToken != "" {
				req.Header.Set("X-Hydra-Token", candidateToken)
			}
			rec := httptest.NewRecorder()
			server.Router.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with token=%q returned %d, want 401", ep.method, ep.path, candidateToken, rec.Code)
			}
		}
	}
}

