package webadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/store"
)

type partialGCArtifactStore struct {
	*artifact.Store
	result artifact.GCResult
	err    error
	calls  int
	cancel context.CancelFunc
}

func (s *partialGCArtifactStore) Usage(context.Context) (int64, int64, error) {
	return 1000, 2000, nil
}

func (s *partialGCArtifactStore) GarbageCollect(context.Context, time.Time, int, int64, string) (artifact.GCResult, error) {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	return s.result, s.err
}

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
	if err = st.RenewArtifactGCGrace(t.Context(), digest, time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	request = httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("stored artifact status: %d %s", response.Code, response.Body.String())
	}
	candidates, err := st.ArtifactGCCandidates(t.Context(), time.Now().UTC().Add(-24*time.Hour), 100)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("status verification did not renew GC grace: %#v %v", candidates, err)
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

func TestCacheStatusAndGarbageCollectionAPI(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	artifacts, err := artifact.NewWithQuota(filepath.Join(t.TempDir(), "artifacts"), st, 1024)
	if err != nil {
		t.Fatal(err)
	}
	admin.artifacts = artifacts
	content := []byte("unreferenced cache payload")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if err = st.RegisterArtifact(t.Context(), store.Artifact{Digest: digest, SizeBytes: int64(len(content)), ContentType: "application/octet-stream", CreatedAt: time.Now().UTC().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/admin/api/v1/cache-status", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"usageBytes":26`) || !strings.Contains(response.Body.String(), `"quotaBytes":1024`) {
		t.Fatalf("cache status: %d %s", response.Code, response.Body.String())
	}

	body, _ := json.Marshal(garbageCollectCacheRequest{MinimumAgeHours: 24, Limit: 100})
	request = httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-gc", bytes.NewReader(body))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", "d145fd25-5ebf-45a2-9b03-3277d74b887c")
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"removed":1`) || !strings.Contains(response.Body.String(), `"removedBytes":26`) || !strings.Contains(response.Body.String(), `"usageBytes":0`) {
		t.Fatalf("cache gc: %d %s", response.Code, response.Body.String())
	}
	if _, err = st.Artifact(t.Context(), digest); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("garbage-collected metadata remains: %v", err)
	}
}

func TestCacheGarbageCollectionRequiresAdminCSRFAndPolicy(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	artifacts, err := artifact.New(filepath.Join(t.TempDir(), "artifacts"), st)
	if err != nil {
		t.Fatal(err)
	}
	admin.artifacts = artifacts

	tests := []struct {
		name, key, csrf, contentType, body, wantCode string
		status                                       int
	}{
		{name: "idempotency key", csrf: csrf, contentType: "application/json", body: `{"minimumAgeHours":24,"limit":100}`, status: http.StatusBadRequest, wantCode: "idempotency_key_required"},
		{name: "csrf", key: "475ec1a8-bc3c-4ddd-9294-8c76f872d9a4", contentType: "application/json", body: `{"minimumAgeHours":24,"limit":100}`, status: http.StatusForbidden, wantCode: "csrf_invalid"},
		{name: "content type", key: "7c575f81-0b69-44f3-b09b-103a4bd14ae3", csrf: csrf, contentType: "text/plain", body: `{"minimumAgeHours":24,"limit":100}`, status: http.StatusUnsupportedMediaType, wantCode: "content_type_invalid"},
		{name: "policy", key: "4a11b1fe-85df-4498-9cee-3d6d88a33f64", csrf: csrf, contentType: "application/json", body: `{"minimumAgeHours":-1,"limit":100}`, status: http.StatusUnprocessableEntity, wantCode: "cache_gc_policy_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-gc", strings.NewReader(test.body))
			request.AddCookie(cookie)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("X-CSRF-Token", test.csrf)
			request.Header.Set("Idempotency-Key", test.key)
			response := httptest.NewRecorder()
			admin.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("response: %d %s", response.Code, response.Body.String())
			}
		})
	}

	user, err := st.UserByUsername(t.Context(), "admin")
	if err != nil || st.SetUserRoles(t.Context(), user.ID, "viewer") != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-gc", strings.NewReader(`{"minimumAgeHours":24,"limit":100}`))
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", "02b2d596-8980-43db-96ea-157a9c37ad0a")
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "forbidden") {
		t.Fatalf("viewer garbage collection: %d %s", response.Code, response.Body.String())
	}
}

func TestCacheGarbageCollectionPartialResultIsIdempotentlyReplayedAfterCancellation(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	artifacts, err := artifact.New(filepath.Join(t.TempDir(), "artifacts"), st)
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(t.Context())
	fake := &partialGCArtifactStore{
		Store:  artifacts,
		result: artifact.GCResult{Examined: 2, Removed: 1, RemovedBytes: 100},
		err:    errors.New("remaining candidate failed"),
		cancel: cancel,
	}
	admin.artifacts = fake
	key := "a8269aaa-d51d-4d8f-a771-4ffac82df233"
	body := `{"minimumAgeHours":24,"limit":100}`

	request := httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-gc", strings.NewReader(body)).WithContext(requestContext)
	request.AddCookie(cookie)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"partial":true`) || !strings.Contains(response.Body.String(), `"removed":1`) {
		t.Fatalf("partial gc: %d %s", response.Code, response.Body.String())
	}
	firstBody := response.Body.String()

	replay := httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-gc", strings.NewReader(body))
	replay.AddCookie(cookie)
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("X-CSRF-Token", csrf)
	replay.Header.Set("Idempotency-Key", key)
	replayResponse := httptest.NewRecorder()
	admin.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusOK || replayResponse.Body.String() != firstBody || fake.calls != 1 {
		t.Fatalf("replay: %d %s calls=%d", replayResponse.Code, replayResponse.Body.String(), fake.calls)
	}

	mismatch := httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-gc", strings.NewReader(`{"minimumAgeHours":48,"limit":100}`))
	mismatch.AddCookie(cookie)
	mismatch.Header.Set("Content-Type", "application/json")
	mismatch.Header.Set("X-CSRF-Token", csrf)
	mismatch.Header.Set("Idempotency-Key", key)
	mismatchResponse := httptest.NewRecorder()
	admin.ServeHTTP(mismatchResponse, mismatch)
	if mismatchResponse.Code != http.StatusConflict || !strings.Contains(mismatchResponse.Body.String(), "idempotency_key_reused") || fake.calls != 1 {
		t.Fatalf("mismatched replay: %d %s calls=%d", mismatchResponse.Code, mismatchResponse.Body.String(), fake.calls)
	}
}
