package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicIndex(t *testing.T) {
	server, err := New(t.TempDir(), "client-secret", "admin-secret")
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	result := httptest.NewRecorder()
	server.Handler().ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "LANReady Management Server") {
		t.Fatalf("unexpected response %d: %s", result.Code, result.Body.String())
	}

	notFound := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	notFoundResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(notFoundResult, notFound)
	if notFoundResult.Code != http.StatusNotFound {
		t.Fatalf("unknown path returned %d", notFoundResult.Code)
	}
}

func TestPublicIndexRedirectsToAdminUI(t *testing.T) {
	admin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server, err := NewWithAdmin(t.TempDir(), "client-secret", "admin-secret", admin)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	result := httptest.NewRecorder()
	server.Handler().ServeHTTP(result, request)
	if result.Code != http.StatusSeeOther || result.Header().Get("Location") != "/admin/" {
		t.Fatalf("unexpected redirect %d to %q", result.Code, result.Header().Get("Location"))
	}
}
