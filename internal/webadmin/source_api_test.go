package webadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/sourceprobe"
	"github.com/containerguy/lan_installer/internal/store"
)

type fakeSourceTester struct {
	calls  int
	result sourceprobe.Result
	err    error
	input  sourceprobe.Input
}

func (f *fakeSourceTester) Test(_ context.Context, input sourceprobe.Input) (sourceprobe.Result, error) {
	f.calls++
	f.input = input
	if f.err != nil {
		return sourceprobe.Result{}, f.err
	}
	return f.result, nil
}

func sourceAPITestAdmin(t *testing.T, vault *secretbox.Box, tester SourceTester) (*store.Store, *Admin, *http.Cookie, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "source-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.BootstrapAdmin(context.Background(), "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	user, err := st.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := st.CreateSession(context.Background(), user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	admin := New(st, false, vault, WithSourceTester(tester))
	return st, admin, &http.Cookie{Name: sessionCookieName, Value: token}, session.CSRFToken
}

func sourceJSONRequest(method, path string, body any, cookie *http.Cookie, csrf string) *http.Request {
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	return request
}

func testAndExtractToken(t *testing.T, admin *Admin, cookie *http.Cookie, csrf string, body map[string]any) string {
	t.Helper()
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources/test", body, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("test source: %d %s", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	token, _ := result["testToken"].(string)
	if token == "" {
		t.Fatal("test token missing")
	}
	return token
}

func TestSourceAPITestThenAtomicCreate(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	tester := &fakeSourceTester{result: sourceprobe.Result{
		TestedAt: time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC),
		Latency:  42 * time.Millisecond, Capabilities: []string{"webdav-propfind"},
		StatusCode: http.StatusMultiStatus,
	}}
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, tester)
	defer st.Close()
	body := map[string]any{
		"sourceId": 0, "revision": 0, "name": "Nextcloud", "kind": "nextcloud_webdav",
		"baseUrl": "https://cloud.example.test/remote.php/dav/files/user", "enabled": true,
		"auth":      map[string]any{"type": "basic", "username": "user", "secretMode": "replace", "password": "secret-app-password"},
		"testToken": "",
	}
	token := testAndExtractToken(t, admin, cookie, csrf, body)
	if tester.input.Password != "secret-app-password" {
		t.Fatal("probe did not receive transient password")
	}
	body["testToken"] = token
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources", body, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret-app-password") {
		t.Fatal("password returned by API")
	}
	sources, err := st.Sources(context.Background())
	if err != nil || len(sources) != 1 || sources[0].Revision != 1 || sources[0].LastTestState != "succeeded" {
		t.Fatalf("sources=%#v err=%v", sources, err)
	}
	config, err := st.WebDAVConfig(context.Background(), sources[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := vault.Open(config.SecretNonce, config.SecretCiphertext, secretbox.WebDAVAAD(sources[0].ID))
	if err != nil || string(plain) != "secret-app-password" {
		t.Fatalf("secret=%q err=%v", plain, err)
	}
}

func TestSourceTokenBindingAndAtomicFailure(t *testing.T) {
	tester := &fakeSourceTester{result: sourceprobe.Result{TestedAt: time.Now().UTC(), Latency: time.Millisecond, Capabilities: []string{"webdav-propfind"}, StatusCode: 207}}
	st, admin, cookie, csrf := sourceAPITestAdmin(t, nil, tester)
	defer st.Close()
	body := map[string]any{
		"sourceId": 0, "revision": 0, "name": "Cloud", "kind": "webdav",
		"baseUrl": "https://cloud.example.test/dav", "enabled": true,
		"auth":      map[string]any{"type": "basic", "username": "user", "secretMode": "replace", "password": "password"},
		"testToken": "",
	}
	token := testAndExtractToken(t, admin, cookie, csrf, body)
	body["testToken"] = token
	body["baseUrl"] = "https://changed.example.test/dav"
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources", body, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "test_token_invalid") {
		t.Fatalf("changed config accepted: %d %s", response.Code, response.Body.String())
	}
	body["baseUrl"] = "https://cloud.example.test/dav"
	request = sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources", body, cookie, csrf)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "source_save_failed") {
		t.Fatalf("unencrypted save accepted: %d %s", response.Code, response.Body.String())
	}
	sources, err := st.Sources(context.Background())
	if err != nil || len(sources) != 0 {
		t.Fatalf("partial source persisted: %#v %v", sources, err)
	}
}

func TestSourceEditReusesEncryptedSecretAndChecksRevision(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	tester := &fakeSourceTester{result: sourceprobe.Result{TestedAt: time.Now().UTC(), Latency: 3 * time.Millisecond, Capabilities: []string{"webdav-propfind"}, StatusCode: 207}}
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, tester)
	defer st.Close()
	created, err := st.SaveSourceAtomic(context.Background(), store.Source{Name: "Cloud", Kind: "webdav", BaseURL: "https://cloud.example.test/dav", Enabled: true}, 0, func(id int64, _ *store.WebDAVConfig) (*store.WebDAVConfig, error) {
		nonce, ciphertext, sealErr := vault.Seal([]byte("stored-password"), secretbox.WebDAVAAD(id))
		return &store.WebDAVConfig{AuthType: "basic", Username: "old-user", SecretNonce: nonce, SecretCiphertext: ciphertext}, sealErr
	})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"sourceId": created.ID, "revision": created.Revision, "name": "Cloud", "kind": "webdav",
		"baseUrl": "https://cloud.example.test/dav", "enabled": true,
		"auth":      map[string]any{"type": "basic", "username": "old-user", "secretMode": "reuse", "password": ""},
		"testToken": "",
	}
	token := testAndExtractToken(t, admin, cookie, csrf, body)
	if tester.input.Password != "stored-password" {
		t.Fatal("stored password was not used transiently for probe")
	}
	body["testToken"] = token
	request := sourceJSONRequest(http.MethodPut, "/admin/api/v1/sources/"+strconv.FormatInt(created.ID, 10), body, cookie, csrf)
	request.Header.Set("If-Match", "\"1\"")
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update: %d %s", response.Code, response.Body.String())
	}
	config, err := st.WebDAVConfig(context.Background(), created.ID)
	if err != nil || config.Username != "old-user" {
		t.Fatalf("config=%#v err=%v", config, err)
	}
	plain, _ := vault.Open(config.SecretNonce, config.SecretCiphertext, secretbox.WebDAVAAD(created.ID))
	if string(plain) != "stored-password" {
		t.Fatal("stored secret changed during reuse")
	}

	request = sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources/"+strconv.FormatInt(created.ID, 10)+"/deactivate", map[string]any{}, cookie, csrf)
	request.Header.Set("If-Match", "\"1\"")
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale revision accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestSourceProbeFailurePersistsNothing(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	tester := &fakeSourceTester{err: &sourceprobe.ProbeError{Code: "target_blocked", Message: "Die Zieladresse ist gesperrt."}}
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, tester)
	defer st.Close()
	body := map[string]any{
		"sourceId": 0, "name": "Blocked", "kind": "webdav", "baseUrl": "https://127.0.0.1/dav", "enabled": true,
		"auth": map[string]any{"type": "none", "secretMode": "none"},
	}
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources/test", body, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "target_blocked") {
		t.Fatalf("probe failure: %d %s", response.Code, response.Body.String())
	}
	sources, err := st.Sources(context.Background())
	if err != nil || len(sources) != 0 {
		t.Fatalf("test persisted data: %#v %v", sources, err)
	}
}

func TestSourceAPIRejectsMissingCSRF(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, _ := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources/test", map[string]any{}, cookie, "")
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", response.Code)
	}
}

func TestSourceAPIRequestIDUnauthorizedAndStrictJSON(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()

	request := httptest.NewRequest(http.MethodGet, "/admin/api/v1/sources", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("request ID missing: %d %#v", response.Code, response.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/api/v1/sources", nil)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("API auth must be JSON 401, got %d", response.Code)
	}
	var failure map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure["requestId"] == "" || failure["requestId"] != response.Header().Get("X-Request-ID") {
		t.Fatalf("request ID mismatch: %#v", failure)
	}

	request = httptest.NewRequest(http.MethodPost, "/admin/api/v1/sources/test", strings.NewReader(`{"name":"x"} {"extra":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_json") {
		t.Fatalf("trailing JSON accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestSourcePageUsesExternalNoCacheAssets(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, _ := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()

	request := httptest.NewRequest(http.MethodGet, "/admin/sources", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "/admin/assets/management.css") || !strings.Contains(body, "/admin/assets/sources.js") {
		t.Fatalf("external assets missing: %d", response.Code)
	}
	if !strings.Contains(body, `id="retry-source"`) {
		t.Fatal("source load retry control missing")
	}
	if strings.Contains(body, "<style") || strings.Contains(body, "<script>") {
		t.Fatal("inline source assets violate CSP")
	}
	for _, path := range []string{"/admin/assets/management.css", "/admin/assets/sources.js"} {
		assetRequest := httptest.NewRequest(http.MethodGet, path, nil)
		assetResponse := httptest.NewRecorder()
		admin.ServeHTTP(assetResponse, assetRequest)
		if assetResponse.Code != http.StatusOK || assetResponse.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("asset %s headers: %d %#v", path, assetResponse.Code, assetResponse.Header())
		}
	}
	assetRequest := httptest.NewRequest(http.MethodGet, "/admin/assets/sources.js", nil)
	assetResponse := httptest.NewRecorder()
	admin.ServeHTTP(assetResponse, assetRequest)
	for _, fragment := range []string{"Ungespeicherte Änderungen verwerfen?", "beforeunload", "setMutationBusy", "retry-source", "Netzwerkfehler. Verbindung prüfen"} {
		if !strings.Contains(assetResponse.Body.String(), fragment) {
			t.Fatalf("source UX hardening missing: %s", fragment)
		}
	}
}

func TestOperatorCannotExfiltrateStoredSourceSecret(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	tester := &fakeSourceTester{result: sourceprobe.Result{TestedAt: time.Now(), Latency: time.Millisecond, StatusCode: http.StatusMultiStatus}}
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, tester)
	defer st.Close()
	created, err := st.SaveSourceAtomic(context.Background(), store.Source{Name: "Cloud", Kind: "webdav", BaseURL: "https://cloud.example.test/dav", Enabled: true}, 0, func(id int64, _ *store.WebDAVConfig) (*store.WebDAVConfig, error) {
		nonce, ciphertext, sealErr := vault.Seal([]byte("stored-secret"), secretbox.WebDAVAAD(id))
		return &store.WebDAVConfig{AuthType: "basic", Username: "source-user", SecretNonce: nonce, SecretCiphertext: ciphertext}, sealErr
	})
	if err != nil {
		t.Fatal(err)
	}
	user, _ := st.UserByUsername(context.Background(), "admin")
	if err = st.SetUserRoles(context.Background(), user.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	malicious := map[string]any{
		"sourceId": created.ID, "name": "Cloud", "kind": "webdav", "baseUrl": "https://attacker.example.test/collect", "enabled": true,
		"auth": map[string]any{"type": "basic", "username": "source-user", "secretMode": "reuse", "password": ""},
	}
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources/test", malicious, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || tester.calls != 0 {
		t.Fatalf("operator exfiltration reached probe: status=%d calls=%d body=%s", response.Code, tester.calls, response.Body.String())
	}

	exact := map[string]any{
		"sourceId": created.ID, "name": "Cloud", "kind": "webdav", "baseUrl": "https://cloud.example.test/dav", "enabled": true,
		"auth": map[string]any{"type": "basic", "username": "source-user", "secretMode": "reuse", "password": ""},
	}
	request = sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources/test", exact, cookie, csrf)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || tester.calls != 1 || tester.input.BaseURL != "https://cloud.example.test/dav" || tester.input.Password != "stored-secret" {
		t.Fatalf("safe operator retest failed: status=%d calls=%d input=%#v body=%s", response.Code, tester.calls, tester.input, response.Body.String())
	}
}

func TestSourceCreateIdempotencyReplaysParallelResult(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	tester := &fakeSourceTester{result: sourceprobe.Result{TestedAt: time.Now(), Latency: time.Millisecond, StatusCode: http.StatusOK}}
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, tester)
	defer st.Close()
	body := map[string]any{
		"sourceId": 0, "name": "Idempotent HTTPS", "kind": "https", "baseUrl": "https://example.test/content", "enabled": true,
		"auth": map[string]any{"type": "none", "secretMode": "none"},
	}
	body["testToken"] = testAndExtractToken(t, admin, cookie, csrf, body)
	key := "019b1234-1234-7123-8123-123456789abc"
	responses := make([]*httptest.ResponseRecorder, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range responses {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources", body, cookie, csrf)
			request.Header.Set("Idempotency-Key", key)
			responses[index] = httptest.NewRecorder()
			<-start
			admin.ServeHTTP(responses[index], request)
		}(index)
	}
	close(start)
	group.Wait()
	if responses[0].Code != http.StatusCreated || responses[1].Code != http.StatusCreated || responses[0].Body.String() != responses[1].Body.String() {
		t.Fatalf("idempotent create mismatch: %d %s / %d %s", responses[0].Code, responses[0].Body.String(), responses[1].Code, responses[1].Body.String())
	}
	values, err := st.Sources(context.Background())
	if err != nil || len(values) != 1 {
		t.Fatalf("duplicate sources: %#v %v", values, err)
	}

	changed := map[string]any{
		"sourceId": 0, "name": "Different", "kind": "https", "baseUrl": "https://example.test/content", "enabled": true,
		"auth": map[string]any{"type": "none", "secretMode": "none"}, "testToken": body["testToken"],
	}
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources", changed, cookie, csrf)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "idempotency_key_reused") {
		t.Fatalf("idempotency key mismatch accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestSourceDeactivateIdempotencyReplays(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	created, err := st.SaveSourceAtomic(context.Background(), store.Source{Name: "Deactivate", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *store.WebDAVConfig) (*store.WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	key := "019b1234-1234-7123-8123-123456789abd"
	var previous string
	for index := 0; index < 2; index++ {
		request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/sources/"+strconv.FormatInt(created.ID, 10)+"/deactivate", map[string]any{}, cookie, csrf)
		request.Header.Set("If-Match", `"1"`)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		admin.ServeHTTP(response, request)
		if response.Code != http.StatusOK || index == 1 && response.Body.String() != previous {
			t.Fatalf("deactivate replay %d: %d %s", index, response.Code, response.Body.String())
		}
		previous = response.Body.String()
	}
}

func TestSourceListFiltersPaginationAndRoleUI(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, _ := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	for _, value := range []store.Source{
		{Name: "A HTTPS", Kind: "https", BaseURL: "https://a.example.test", Enabled: true},
		{Name: "B WebDAV", Kind: "webdav", BaseURL: "https://b.example.test/dav", Enabled: false},
		{Name: "C WebDAV", Kind: "webdav", BaseURL: "https://c.example.test/dav", Enabled: true},
	} {
		if _, err := st.SaveSourceAtomic(context.Background(), value, 0, func(int64, *store.WebDAVConfig) (*store.WebDAVConfig, error) {
			return &store.WebDAVConfig{AuthType: "none"}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/api/v1/sources?query=webdav&kind=webdav&enabled=true", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "C WebDAV") || strings.Contains(response.Body.String(), "B WebDAV") {
		t.Fatalf("filters failed: %d %s", response.Code, response.Body.String())
	}

	user, _ := st.UserByUsername(context.Background(), "admin")
	if err := st.SetUserRoles(context.Background(), user.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/sources", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	body := response.Body.String()
	if strings.Contains(body, "id=\"add-source\"") || strings.Contains(body, "id=\"save-source\"") || strings.Contains(body, "id=\"delete-source\"") || !strings.Contains(body, "id=\"test-source\"") {
		t.Fatalf("operator controls incorrect: %s", body)
	}
	if err := st.SetUserRoles(context.Background(), user.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/sources", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	body = response.Body.String()
	if strings.Contains(body, "id=\"add-source\"") || strings.Contains(body, "id=\"save-source\"") || strings.Contains(body, "id=\"test-source\"") {
		t.Fatalf("viewer mutation controls visible: %s", body)
	}
}
