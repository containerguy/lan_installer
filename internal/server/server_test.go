package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthenticationAndManifest(t *testing.T) {
	root := t.TempDir()
	eventDir := filepath.Join(root, "events", "demo")
	if err := os.MkdirAll(eventDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventDir, "envelope.json"), []byte(`{"manifest":{},"signature":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := New(root, "client-secret", "admin-secret")
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRequest(http.MethodGet, "/v1/events/demo/manifest", nil)
	unauthorizedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorizedResult, unauthorized)
	if unauthorizedResult.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status %d", unauthorizedResult.Code)
	}

	authorized := httptest.NewRequest(http.MethodGet, "/v1/events/demo/manifest", nil)
	authorized.Header.Set("Authorization", "Bearer client-secret")
	authorizedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(authorizedResult, authorized)
	if authorizedResult.Code != http.StatusOK || !strings.Contains(authorizedResult.Body.String(), "signature") {
		t.Fatalf("unexpected response %d: %s", authorizedResult.Code, authorizedResult.Body.String())
	}
}
