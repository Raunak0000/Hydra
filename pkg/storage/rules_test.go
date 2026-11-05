package storage

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCategoryRules(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Initial default resolve
	if path := ResolveCategoryPath("movie.mkv"); path != "~/Videos" {
		t.Errorf("expected ~/Videos, got %s", path)
	}
	if path := ResolveCategoryPath("song.mp3"); path != "~/Music" {
		t.Errorf("expected ~/Music, got %s", path)
	}
	if path := ResolveCategoryPath("unknown.xyz"); path != "~/Downloads" {
		t.Errorf("expected default ~/Downloads, got %s", path)
	}

	// Add custom category rule
	customPath := filepath.Join(tmpDir, "Books")
	if err := SetCategoryRule("Ebooks", customPath, []string{"xyzbook", "mobi", "azw3"}); err != nil {
		t.Fatalf("SetCategoryRule failed: %v", err)
	}

	if path := ResolveCategoryPath("novel.xyzbook"); path != customPath {
		t.Errorf("expected customPath %s, got %s", customPath, path)
	}
	if path := ResolveCategoryPath("kindle.MOBI"); path != customPath {
		t.Errorf("expected customPath %s for uppercase MOBI, got %s", customPath, path)
	}

	// Delete category rule
	if err := DeleteCategoryRule("Ebooks"); err != nil {
		t.Fatalf("DeleteCategoryRule failed: %v", err)
	}

	if path := ResolveCategoryPath("novel.xyzbook"); path != "~/Downloads" {
		t.Errorf("expected fallback ~/Downloads after deletion, got %s", path)
	}
}

func TestRulesAPI(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	originalToken := hydraAPIToken
	hydraAPIToken = "test-token"
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	server := NewServer(nil)

	// 1. GET /api/rules
	req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	req.Header.Set("X-Hydra-Token", "test-token")
	rec := httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/rules returned status %d", rec.Code)
	}

	var rules map[string]CategoryRule
	if err := json.NewDecoder(rec.Body).Decode(&rules); err != nil {
		t.Fatalf("failed to decode rules JSON: %v", err)
	}
	if len(rules) == 0 {
		t.Fatalf("expected default rules, got empty map")
	}

	// 2. POST /api/rules (Add rule)
	addPayload := map[string]any{
		"name":       "Torrents",
		"path":       "/tmp/Torrents",
		"extensions": []string{"torrent"},
	}
	bodyBytes, _ := json.Marshal(addPayload)
	req = httptest.NewRequest(http.MethodPost, "/api/rules", bytes.NewReader(bodyBytes))
	req.Header.Set("X-Hydra-Token", "test-token")
	rec = httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/rules returned %d: %s", rec.Code, rec.Body.String())
	}

	if path := ResolveCategoryPath("linux.torrent"); path != "/tmp/Torrents" {
		t.Errorf("expected /tmp/Torrents, got %s", path)
	}

	// 3. DELETE /api/rules?name=Torrents
	req = httptest.NewRequest(http.MethodDelete, "/api/rules?name=Torrents", nil)
	req.Header.Set("X-Hydra-Token", "test-token")
	rec = httptest.NewRecorder()
	server.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/rules returned %d: %s", rec.Code, rec.Body.String())
	}

	if path := ResolveCategoryPath("linux.torrent"); path != "~/Downloads" {
		t.Errorf("expected ~/Downloads after delete, got %s", path)
	}
}
