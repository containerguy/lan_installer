package webadmin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/secretbox"
)

func TestClientUpdateArtifactUploadAndStatus(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	artifacts, err := artifact.New(filepath.Join(t.TempDir(), "artifacts"), st)
	if err != nil {
		t.Fatal(err)
	}
	admin.artifacts = artifacts

	content := []byte("MZ signed portable client fixture")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	path := "/admin/api/v1/artifacts/client-update/" + digest + "?size=33"

	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":false`) {
		t.Fatalf("missing artifact status: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(content))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", clientUpdateContentType)
	request.Header.Set("X-CSRF-Token", csrf)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("artifact upload: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("stored artifact status: %d %s", response.Code, response.Body.String())
	}
	if err = artifacts.Verify(t.Context(), digest, int64(len(content)), clientUpdateContentType); err != nil {
		t.Fatalf("stored artifact verification: %v", err)
	}
}

func TestClientUpdateArtifactUploadRejectsUntrustedInput(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	artifacts, err := artifact.New(filepath.Join(t.TempDir(), "artifacts"), st)
	if err != nil {
		t.Fatal(err)
	}
	admin.artifacts = artifacts

	content := []byte("MZ wrong digest")
	path := "/admin/api/v1/artifacts/client-update/" + strings.Repeat("0", 64) + "?size=15"
	tests := []struct {
		name        string
		csrf        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{name: "missing csrf", contentType: clientUpdateContentType, wantStatus: http.StatusForbidden, wantCode: "csrf_invalid"},
		{name: "wrong content type", csrf: csrf, contentType: "application/octet-stream", wantStatus: http.StatusUnsupportedMediaType, wantCode: "content_type_invalid"},
		{name: "wrong digest", csrf: csrf, contentType: clientUpdateContentType, wantStatus: http.StatusUnprocessableEntity, wantCode: "artifact_digest_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(content))
			request.AddCookie(cookie)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("X-CSRF-Token", test.csrf)
			response := httptest.NewRecorder()
			admin.ServeHTTP(response, request)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestClientUpdateArtifactStatusRejectsOversizedArtifact(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, _ := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	artifacts, err := artifact.New(filepath.Join(t.TempDir(), "artifacts"), st)
	if err != nil {
		t.Fatal(err)
	}
	admin.artifacts = artifacts
	path := "/admin/api/v1/artifacts/client-update/" + strings.Repeat("0", 64) + "?size=1073741825"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "artifact_metadata_invalid") {
		t.Fatalf("oversized artifact accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestClientUpdateArtifactUploadHonorsSharedCacheQuota(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	artifacts, err := artifact.NewWithQuota(filepath.Join(t.TempDir(), "artifacts"), st, 4)
	if err != nil {
		t.Fatal(err)
	}
	admin.artifacts = artifacts
	content := []byte("12345")
	sum := sha256.Sum256(content)
	path := "/admin/api/v1/artifacts/client-update/" + hex.EncodeToString(sum[:]) + "?size=5"
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(content))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", clientUpdateContentType)
	request.Header.Set("X-CSRF-Token", csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusInsufficientStorage || !strings.Contains(response.Body.String(), "cache_quota_exceeded") {
		t.Fatalf("quota bypassed: %d %s", response.Code, response.Body.String())
	}
}
