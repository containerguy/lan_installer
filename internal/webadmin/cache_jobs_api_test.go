package webadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/store"
)

func TestCacheJobsAPIEnqueueCancelRetryAndList(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	ctx := context.Background()
	source, err := st.SaveSourceAtomic(ctx, store.Source{Name: "Cache API", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *store.WebDAVConfig) (*store.WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, _ := st.Launchers(ctx)
	gameID, err := st.SaveGameAtomic(ctx, store.Game{Slug: "cache-api", Name: "Cache API", LauncherID: launchers[0].ID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(ctx, store.GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "game.zip", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{"targetType": "game-version", "targetId": versionID}
	key := "019b1234-1234-7123-8123-123456789add"
	var job store.CacheJob
	var firstResponse string
	for attempt := 0; attempt < 2; attempt++ {
		request := catalogJSONRequest(http.MethodPost, "/admin/api/v1/cache-jobs", body, cookie, csrf)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		admin.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("enqueue %d: %d %s", attempt, response.Code, response.Body.String())
		}
		if attempt == 0 {
			firstResponse = response.Body.String()
			if err = json.Unmarshal(response.Body.Bytes(), &job); err != nil || job.ID == "" || job.Status != "queued" {
				t.Fatalf("job response: %#v %v", job, err)
			}
		} else if response.Body.String() != firstResponse {
			t.Fatalf("idempotent response changed: %s / %s", firstResponse, response.Body.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/admin/api/v1/cache-jobs", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), job.ID) || !strings.Contains(response.Body.String(), `"jobs"`) {
		t.Fatalf("list jobs: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-jobs/"+job.ID+"/cancel", nil)
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", csrf)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"Status":"cancelled"`) {
		t.Fatalf("cancel job: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/admin/api/v1/cache-jobs/"+job.ID+"/retry", nil)
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", csrf)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"Status":"queued"`) {
		t.Fatalf("retry job: %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/api/v1/catalog", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"cacheJobs"`) || !strings.Contains(response.Body.String(), job.ID) {
		t.Fatalf("catalog cache status: %d %s", response.Code, response.Body.String())
	}
}

func TestCacheJobsAPIRolesAndCSRF(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	user, err := st.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	request := catalogJSONRequest(http.MethodPost, "/admin/api/v1/cache-jobs", map[string]any{"targetType": "game-version", "targetId": 1}, cookie, "")
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "csrf_invalid") {
		t.Fatalf("missing csrf: %d %s", response.Code, response.Body.String())
	}
	if err = st.SetUserRoles(context.Background(), user.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	request = catalogJSONRequest(http.MethodPost, "/admin/api/v1/cache-jobs", map[string]any{"targetType": "game-version", "targetId": 1}, cookie, csrf)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "forbidden") {
		t.Fatalf("viewer enqueue: %d %s", response.Code, response.Body.String())
	}
}
