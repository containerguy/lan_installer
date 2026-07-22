package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func downloadServer(t *testing.T, withClient bool) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	if withClient {
		if err := os.MkdirAll(filepath.Join(dir, "download"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "download", "LANReady.exe"), []byte("MZ fake client"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := NewWithHandlers(dir, "client-token", "admin-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestClientDownloadServesTheBinary(t *testing.T) {
	s, _ := downloadServer(t, true)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/download/lanready.exe", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	if body := recorder.Body.String(); body != "MZ fake client" {
		t.Fatalf("unexpected body %q", body)
	}
	if cd := recorder.Header().Get("Content-Disposition"); !strings.Contains(cd, "LANReady.exe") {
		t.Fatalf("missing download filename: %q", cd)
	}
}

// The endpoint must never turn into a general file server: the served name is
// fixed, so no request path can reach another file.
func TestClientDownloadRefusesOtherPaths(t *testing.T) {
	s, dir := downloadServer(t, true)
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "download", "other.txt"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/download/secret.txt",
		"/download/other.txt",
		"/download/../secret.txt",
		"/download/%2e%2e/secret.txt",
		"/download/..%2Fsecret.txt",
		"/download/lanready.exe/../../secret.txt",
	} {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code == http.StatusOK && strings.Contains(recorder.Body.String(), "top secret") {
			t.Fatalf("%s leaked another file", path)
		}
	}
}

func TestClientDownloadReportsMissingBinary(t *testing.T) {
	s, _ := downloadServer(t, false)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/download/lanready.exe", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without a build, got %d", recorder.Code)
	}
	info := httptest.NewRecorder()
	s.Handler().ServeHTTP(info, httptest.NewRequest(http.MethodGet, "/download/info", nil))
	if !strings.Contains(info.Body.String(), `"available":false`) {
		t.Fatalf("info should report unavailable: %s", info.Body.String())
	}
}

// The page must publish the digest so a download can be verified independently
// of the transport.
func TestDownloadPageShowsDigest(t *testing.T) {
	s, _ := downloadServer(t, true)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/download", nil))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	digest, err := fileDigest(s.clientDownloadPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, digest) {
		t.Fatal("page does not publish the SHA-256 digest")
	}
	if !strings.Contains(body, "SmartScreen") {
		t.Fatal("page should warn about the unsigned binary")
	}
}
