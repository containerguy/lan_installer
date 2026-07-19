package webadmin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/store"
)

func TestUnknownDeviceAuthorizationDecisionFailsClosed(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/clients.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	token, session, err := st.CreateSession(ctx, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if err = st.CreateDevice(ctx, store.Device{ID: "device", Name: "PC", PublicKey: key, WindowsVersion: "11", ClientVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	authz, err := st.CreateDeviceAuthorization(ctx, "device", "https://manager.example", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	vault, _ := secretbox.New(make([]byte, 32))
	admin := New(st, false, vault)
	form := url.Values{"csrf_token": {session.CSRFToken}, "code": {authz.UserCode}, "decision": {"tampered"}}
	request := httptest.NewRequest(http.MethodPost, "/admin/device", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("tampered decision: %d %s", response.Code, response.Body.String())
	}
	if _, err = st.PendingDeviceAuthorization(ctx, authz.UserCode); err != nil {
		t.Fatalf("authorization was changed: %v", err)
	}
}

func TestEnrollmentCodeAPIRequiresAdminAndSupportsRevoke(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/enrollment-api.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	token, session, err := st.CreateSession(ctx, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	vault, _ := secretbox.New(make([]byte, 32))
	admin := New(st, false, vault)
	request := httptest.NewRequest(http.MethodPost, "/admin/api/v1/enrollment-codes", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", session.CSRFToken)
	request.Header.Set("Idempotency-Key", "11111111-1111-4111-8111-111111111111")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	var created struct{ ID, Code string }
	if err = json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ID == "" || created.Code == "" {
		t.Fatalf("created: %#v %v", created, err)
	}
	revoke := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/enrollment-codes/"+created.ID, nil)
	revoke.Header.Set("X-CSRF-Token", session.CSRFToken)
	revoke.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	revokeResponse := httptest.NewRecorder()
	admin.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", revokeResponse.Code, revokeResponse.Body.String())
	}
	if _, err = st.EnrollDevice(ctx, created.Code, make([]byte, 32), "PC", "11", "1.0.0"); err != store.ErrEnrollmentCodeInvalid {
		t.Fatalf("revoked code accepted: %v", err)
	}
	if err = st.SetUserRoles(ctx, 1, "viewer"); err != nil {
		t.Fatal(err)
	}
	viewerToken, viewerSession, err := st.CreateSession(ctx, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	denied := httptest.NewRequest(http.MethodPost, "/admin/api/v1/enrollment-codes", strings.NewReader(`{}`))
	denied.Header.Set("Content-Type", "application/json")
	denied.Header.Set("X-CSRF-Token", viewerSession.CSRFToken)
	denied.Header.Set("Idempotency-Key", "22222222-2222-4222-8222-222222222222")
	denied.AddCookie(&http.Cookie{Name: sessionCookieName, Value: viewerToken})
	deniedResponse := httptest.NewRecorder()
	admin.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer create: %d %s", deniedResponse.Code, deniedResponse.Body.String())
	}
}

func TestInventoryCatalogImportRequiresAdminAndCreatesDraft(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/inventory-import.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateDevice(ctx, store.Device{ID: "device-import", Name: "Gaming PC", PublicKey: key, WindowsVersion: "11", ClientVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	authorization, err := st.CreateDeviceAuthorization(ctx, "device-import", "https://manager.example", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ApproveDeviceAuthorization(ctx, authorization.UserCode, 1); err != nil {
		t.Fatal(err)
	}
	userToken, err := st.PollDeviceAuthorization(ctx, "device-import", authorization.AuthorizationID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	version := "build-42"
	if err = st.SaveDeviceInventory(ctx, "device-import", userToken, store.InventoryScan{ID: "scan-import", ClientVersion: "1.0.0", ScannedAt: time.Now(), Installations: []store.InventoryInstallation{{Launcher: "steam", ExternalGameID: "730", DisplayName: "Counter-Strike 2", DetectedVersion: &version, VersionSource: "steam-buildid", InstallPath: `C:\\Games\\CS2`}, {Launcher: "ea_app", ExternalGameID: "EA-1", DisplayName: "EA Game", VersionSource: "unknown", InstallPath: `C:\\Games\\EA`}}}); err != nil {
		t.Fatal(err)
	}
	token, session, err := st.CreateSession(ctx, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	vault, _ := secretbox.New(make([]byte, 32))
	admin := New(st, false, vault)
	form := url.Values{"csrf_token": {session.CSRFToken}, "device_id": {"device-import"}, "scan_id": {"scan-import"}, "position": {"0"}, "mode": {"create"}, "name": {"Counter-Strike 2"}, "slug": {"counter-strike-2"}, "version": {version}}
	request := httptest.NewRequest(http.MethodPost, "/admin/clients/catalog-import", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/clients?import=created" {
		t.Fatalf("create draft: %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	games, err := st.Games(ctx)
	if err != nil || len(games) != 1 || games[0].Enabled {
		t.Fatalf("unexpected game draft: %#v err=%v", games, err)
	}
	versions, err := st.GameVersions(ctx)
	if err != nil || len(versions) != 1 || versions[0].Enabled || versions[0].SourceID != 0 {
		t.Fatalf("unexpected version draft: %#v err=%v", versions, err)
	}
	source, err := st.SaveSourceAtomic(ctx, store.Source{Name: "Packages", Kind: "https", BaseURL: "https://downloads.example.test", Enabled: true}, 0, func(int64, *store.WebDAVConfig) (*store.WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	completion, _ := json.Marshal(map[string]any{"id": versions[0].ID, "revision": versions[0].Revision, "gameId": versions[0].GameID, "version": versions[0].Version, "sourceId": source.ID, "sourcePath": "games/cs2.zip", "enabled": false, "sizeBytes": 0})
	completeRequest := httptest.NewRequest(http.MethodPut, "/admin/api/v1/catalog/game-version/"+strconv.FormatInt(versions[0].ID, 10), strings.NewReader(string(completion)))
	completeRequest.Header.Set("Content-Type", "application/json")
	completeRequest.Header.Set("X-CSRF-Token", session.CSRFToken)
	completeRequest.Header.Set("If-Match", `"1"`)
	completeRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	completeResponse := httptest.NewRecorder()
	admin.ServeHTTP(completeResponse, completeRequest)
	if completeResponse.Code != http.StatusOK {
		t.Fatalf("complete draft: %d %s", completeResponse.Code, completeResponse.Body.String())
	}
	versions, err = st.GameVersions(ctx)
	if err != nil || versions[0].SourceID != source.ID || versions[0].SourcePath != "games/cs2.zip" || versions[0].Revision != 2 {
		t.Fatalf("draft was not completed through catalog API: %#v err=%v", versions, err)
	}

	if err = st.SetUserRoles(ctx, 1, "viewer"); err != nil {
		t.Fatal(err)
	}
	viewerToken, viewerSession, err := st.CreateSession(ctx, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	deniedForm := url.Values{"csrf_token": {viewerSession.CSRFToken}, "device_id": {"device-import"}, "scan_id": {"scan-import"}, "position": {"1"}, "mode": {"create"}, "name": {"EA Game"}, "slug": {"ea-game"}}
	denied := httptest.NewRequest(http.MethodPost, "/admin/clients/catalog-import", strings.NewReader(deniedForm.Encode()))
	denied.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	denied.AddCookie(&http.Cookie{Name: sessionCookieName, Value: viewerToken})
	deniedResponse := httptest.NewRecorder()
	admin.ServeHTTP(deniedResponse, denied)
	if deniedResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer import: %d %s", deniedResponse.Code, deniedResponse.Body.String())
	}
}
