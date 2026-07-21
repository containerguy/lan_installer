package webadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/store"
)

func catalogJSONRequest(method, path string, body any, cookie *http.Cookie, csrf string) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.AddCookie(cookie)
	return request
}

func TestEventsPageIncludesAdminReleaseWorkflow(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, _ := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	request := httptest.NewRequest(http.MethodGet, "/admin/events", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("events page: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, required := range []string{"Event veröffentlichen", `id="release-event"`, `id="release-valid-until-input"`, `id="release-minimum-client-input"`, `id="release-preview"`, `id="release-activate"`, "Release erstellen, signieren und veröffentlichen", "Signiertes Clientupdate veröffentlichen", `id="client-update-file"`, `id="client-update-artifact-file"`, `id="upload-client-update-artifact"`, `id="client-update-artifact-status"`, `id="client-update-preview"`, `name="can-publish" content="true"`} {
		if !strings.Contains(body, required) {
			t.Fatalf("events page misses %q", required)
		}
	}
	if strings.Contains(body, "Private-Key-Datei auswählen") {
		t.Fatal("events page asks for a private key")
	}
	if strings.Contains(body, `id="release-file"`) {
		t.Fatal("primary event publishing flow still asks for a JSON envelope")
	}
}

func TestCatalogPageIncludesRoleAwareCacheManagement(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, _ := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	request := httptest.NewRequest(http.MethodGet, "/admin/catalog?tab=game-versions", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	body := response.Body.String()
	for _, fragment := range []string{`id="cache-management"`, `id="cache-usage-progress"`, `id="cache-gc-age"`, `id="run-cache-gc"`, `id="entity-provider"`, "LANReady-Paketquelle", "Referenzierte Katalog- und Release-Artefakte"} {
		if response.Code != http.StatusOK || !strings.Contains(body, fragment) {
			t.Fatalf("admin cache management misses %q: %d", fragment, response.Code)
		}
	}
	user, err := st.UserByUsername(t.Context(), "admin")
	if err != nil || st.SetUserRoles(t.Context(), user.ID, "viewer") != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/catalog?tab=game-versions", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	body = response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, `id="run-cache-gc"`) || !strings.Contains(body, "Nur Administratoren dürfen unreferenzierte Cacheartefakte entfernen") {
		t.Fatalf("viewer cache management roles: %d", response.Code)
	}
}

func TestCatalogAPIAcceptsLauncherManagedGameVersionWithoutHTTPSource(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	ctx := context.Background()
	launchers, err := st.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var steamID, standaloneID int64
	for _, launcher := range launchers {
		switch launcher.Adapter {
		case "steam":
			steamID = launcher.ID
		case "standalone":
			standaloneID = launcher.ID
		}
	}
	steamGameID, err := st.SaveGameAtomic(ctx, store.Game{Slug: "aoe2-de", Name: "Age of Empires II: Definitive Edition", LauncherID: steamID, ExternalGameID: "813780", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"id": 0, "gameId": steamGameID, "version": "101.103", "sourceId": 0, "sourcePath": "", "sha256": "", "sizeBytes": 0, "enabled": true}
	request := catalogJSONRequest(http.MethodPost, "/admin/api/v1/catalog/game-version", body, cookie, csrf)
	request.Header.Set("Idempotency-Key", "019b1234-1234-7123-8123-123456789add")
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("launcher-managed version: %d %s", response.Code, response.Body.String())
	}
	versions, err := st.GameVersions(ctx)
	if err != nil || len(versions) != 1 || versions[0].SourceID != 0 || versions[0].LauncherName != "Steam" {
		t.Fatalf("launcher-managed catalog snapshot: %#v err=%v", versions, err)
	}

	standaloneGameID, err := st.SaveGameAtomic(ctx, store.Game{Slug: "manual-game", Name: "Manual Game", LauncherID: standaloneID, ExternalGameID: "manual-game", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body["gameId"], body["version"] = standaloneGameID, "1"
	request = catalogJSONRequest(http.MethodPost, "/admin/api/v1/catalog/game-version", body, cookie, csrf)
	request.Header.Set("Idempotency-Key", "019b1234-1234-7123-8123-123456789ade")
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("standalone version without package source: %d %s", response.Code, response.Body.String())
	}
}

func TestCacheControlsAreDisabledOnlyDuringGarbageCollection(t *testing.T) {
	source := string(catalogJS)
	mutationStart := strings.Index(source, "async function mutateCacheJob")
	gcStart := strings.Index(source, "async function runCacheGarbageCollection")
	if mutationStart < 0 || gcStart < 0 {
		t.Fatal("cache control functions are missing from catalog asset")
	}
	mutationEnd := strings.Index(source[mutationStart:], "\n  function rowFor")
	gcEnd := strings.Index(source[gcStart:], "\n  function render()")
	if mutationEnd < 0 || gcEnd < 0 {
		t.Fatal("cache control function boundaries are missing from catalog asset")
	}
	mutation := source[mutationStart : mutationStart+mutationEnd]
	if strings.Contains(mutation, `byId("cache-gc-age").disabled`) {
		t.Fatal("cache job mutation changes the garbage collection age control")
	}
	gc := source[gcStart : gcStart+gcEnd]
	if !strings.Contains(gc, `byId("cache-gc-age").disabled = true`) || !strings.Contains(gc, `byId("cache-gc-age").disabled = false`) {
		t.Fatal("garbage collection does not disable and restore its age control")
	}
}

func TestCatalogAPILifecycleRolesAndReferenceErrors(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	ctx := context.Background()
	source, err := st.SaveSourceAtomic(ctx, store.Source{Name: "Downloads", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *store.WebDAVConfig) (*store.WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, _ := st.Launchers(ctx)

	create := map[string]any{"id": 0, "slug": "counter-strike-2", "name": "Counter-Strike 2", "launcherId": launchers[0].ID, "externalGameId": "730", "enabled": true}
	request := catalogJSONRequest(http.MethodPost, "/admin/api/v1/catalog/game", create, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"id"`) {
		t.Fatalf("create game: %d %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err = json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	gameID := int64(created["id"].(float64))

	request = httptest.NewRequest(http.MethodGet, "/admin/api/v1/catalog", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Counter-Strike 2") || !strings.Contains(response.Body.String(), `"launcherVersions":[]`) {
		t.Fatalf("snapshot: %d %s", response.Code, response.Body.String())
	}

	versionID, err := st.SaveGameVersionAtomic(ctx, store.GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "games/cs2.zip", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request = catalogJSONRequest(http.MethodDelete, "/admin/api/v1/catalog/game/"+strconv.FormatInt(gameID, 10), nil, cookie, csrf)
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "catalog_referenced") {
		t.Fatalf("referenced delete: %d %s", response.Code, response.Body.String())
	}
	if err = st.DeleteCatalogAtomic(ctx, "game-version", versionID, 1, nil); err != nil {
		t.Fatal(err)
	}

	request = catalogJSONRequest(http.MethodDelete, "/admin/api/v1/catalog/game/"+strconv.FormatInt(gameID, 10), nil, cookie, csrf)
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("LANReady-API-Version") != "2" {
		t.Fatalf("successful delete contract: %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}

	user, _ := st.UserByUsername(ctx, "admin")
	if err = st.SetUserRoles(ctx, user.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	request = catalogJSONRequest(http.MethodPost, "/admin/api/v1/catalog/game", create, cookie, csrf)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "forbidden") {
		t.Fatalf("viewer mutation: %d %s", response.Code, response.Body.String())
	}
}

func TestCatalogAPIRevisionAndIdempotencyContract(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()
	ctx := context.Background()
	launchers, _ := st.Launchers(ctx)
	body := map[string]any{"id": 0, "revision": 0, "slug": "revision-game", "name": "Revision Game", "launcherId": launchers[0].ID, "enabled": true}
	key := "019b1234-1234-7123-8123-123456789ace"
	var firstBody string
	for attempt := 0; attempt < 2; attempt++ {
		request := catalogJSONRequest(http.MethodPost, "/admin/api/v1/catalog/game", body, cookie, csrf)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		admin.ServeHTTP(response, request)
		if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
			t.Fatalf("idempotent create %d: %d %s", attempt, response.Code, response.Body.String())
		}
		if attempt == 1 && response.Body.String() != firstBody {
			t.Fatalf("idempotent response changed: %s / %s", firstBody, response.Body.String())
		}
		firstBody = response.Body.String()
	}
	games, err := st.Games(ctx)
	if err != nil || len(games) != 1 {
		t.Fatalf("duplicate games: %#v %v", games, err)
	}
	update := map[string]any{"id": games[0].ID, "revision": 1, "slug": "revision-game", "name": "Revision Game 2", "launcherId": launchers[0].ID, "enabled": true}
	request := catalogJSONRequest(http.MethodPut, "/admin/api/v1/catalog/game/"+strconv.FormatInt(games[0].ID, 10), update, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match accepted: %d %s", response.Code, response.Body.String())
	}
	request = catalogJSONRequest(http.MethodPut, "/admin/api/v1/catalog/game/"+strconv.FormatInt(games[0].ID, 10), update, cookie, csrf)
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("revision update failed: %d %s", response.Code, response.Body.String())
	}
	request = catalogJSONRequest(http.MethodPut, "/admin/api/v1/catalog/game/"+strconv.FormatInt(games[0].ID, 10), update, cookie, csrf)
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale revision accepted: %d %s", response.Code, response.Body.String())
	}

	source, err := st.SaveSourceAtomic(ctx, store.Source{Name: "Event Source", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *store.WebDAVConfig) (*store.WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(ctx, store.GameVersion{GameID: games[0].ID, Version: "1", SourceID: source.ID, SourcePath: "games/revision.zip", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.SaveEventAtomic(ctx, store.Event{Slug: "revision-event", Name: "Revision Event", Status: "draft"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assignment := map[string]any{"eventId": eventID, "gameVersionId": versionID, "revision": 0, "required": true}
	assignmentKey := "019b1234-1234-7123-8123-123456789acf"
	for attempt := 0; attempt < 2; attempt++ {
		request = catalogJSONRequest(http.MethodPost, "/admin/api/v1/event-games", assignment, cookie, csrf)
		request.Header.Set("Idempotency-Key", assignmentKey)
		response = httptest.NewRecorder()
		admin.ServeHTTP(response, request)
		if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
			t.Fatalf("event assignment replay %d: %d %s", attempt, response.Code, response.Body.String())
		}
	}
	assignments, err := st.EventGames(ctx)
	if err != nil || len(assignments) != 1 || assignments[0].Revision != 1 {
		t.Fatalf("duplicate assignments: %#v %v", assignments, err)
	}
	request = catalogJSONRequest(http.MethodDelete, "/admin/api/v1/event-games/"+strconv.FormatInt(eventID, 10)+"/"+strconv.FormatInt(versionID, 10), nil, cookie, csrf)
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("LANReady-API-Version") != "2" {
		t.Fatalf("event assignment delete contract: %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestCatalogAPIStrictJSONCSRFAndPageAssets(t *testing.T) {
	vault, _ := secretbox.New(make([]byte, 32))
	st, admin, cookie, csrf := sourceAPITestAdmin(t, vault, &fakeSourceTester{})
	defer st.Close()

	request := catalogJSONRequest(http.MethodPost, "/admin/api/v1/catalog/game", map[string]any{"unknown": true}, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_json") {
		t.Fatalf("unknown JSON: %d %s", response.Code, response.Body.String())
	}

	request = catalogJSONRequest(http.MethodPost, "/admin/api/v1/catalog/game", map[string]any{"id": 0}, cookie, "")
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF accepted: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/api/v1/catalog", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("empty catalog snapshot: %d %s", response.Code, response.Body.String())
	}
	for _, fragment := range []string{`"sources":[]`, `"games":[]`, `"launcherVersions":[]`, `"gameVersions":[]`, `"events":[]`, `"eventGames":[]`} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("catalog collection is not an array: %s in %s", fragment, response.Body.String())
		}
	}

	for _, path := range []string{"/admin/catalog", "/admin/events"} {
		request = httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		response = httptest.NewRecorder()
		admin.ServeHTTP(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "/admin/assets/management.css") || !strings.Contains(body, "/admin/assets/catalog-assignment-helpers.js") || !strings.Contains(body, "/admin/assets/catalog.js") {
			t.Fatalf("catalog page %s: %d %s", path, response.Code, body)
		}
		if strings.Contains(body, "<style") || strings.Contains(body, "<script>") {
			t.Fatalf("inline CSP asset on %s", path)
		}
		for _, fragment := range []string{`id="add-entity" disabled`, `id="retry-catalog"`, `role="tabpanel"`, `aria-busy="true"`, `value="published" disabled`, `aria-readonly="true"`} {
			if !strings.Contains(body, fragment) {
				t.Fatalf("catalog state/accessibility markup missing on %s: %s", path, fragment)
			}
		}
	}

	user, _ := st.UserByUsername(context.Background(), "admin")
	if err := st.SetUserRoles(context.Background(), user.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/admin/catalog", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	viewerBody := response.Body.String()
	for _, fragment := range []string{`id="add-entity"`, `id="save-entity"`, `id="delete-entity"`, `id="deactivate-entity"`, `id="assignment-event"`, `id="assignment-version"`} {
		if strings.Contains(viewerBody, fragment) {
			t.Fatalf("viewer mutation control visible: %s", fragment)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	viewerDashboard := response.Body.String()
	for _, fragment := range []string{"Quellen ansehen", "Spiele ansehen", "Launcher-Versionen ansehen", "Event-Entwürfe ansehen"} {
		if !strings.Contains(viewerDashboard, fragment) {
			t.Fatalf("viewer dashboard action missing: %s", fragment)
		}
	}
	for _, fragment := range []string{"Quelle hinzufügen oder testen", "Spiele verwalten", "Launcher-Version anlegen", "Event-Entwurf bearbeiten"} {
		if strings.Contains(viewerDashboard, fragment) {
			t.Fatalf("viewer dashboard exposes mutation CTA: %s", fragment)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/assets/catalog.js", nil)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("catalog asset headers: %d %#v", response.Code, response.Header())
	}
	catalogAsset := response.Body.String()
	for _, fragment := range []string{"normalizeSnapshot", "payload.fieldErrors", "toAPIDateTime", "selectableParents", "Ungespeicherte Änderungen verwerfen?", "retry-catalog", "revisionHeader", "Idempotency-Key", "SilentArgsVerified", "disabledControls"} {
		if !strings.Contains(catalogAsset, fragment) {
			t.Fatalf("catalog UX hardening missing: %s", fragment)
		}
	}

	if strings.Contains(catalogAsset, "silentArgs:") || strings.Contains(catalogAsset, "silentArgsVerified:") {
		t.Fatal("catalog UI sends server-owned silent argument fields")
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/assets/catalog-assignment-helpers.js", nil)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("catalog assignment helper headers: %d %#v", response.Code, response.Header())
	}
	for _, fragment := range []string{"availableVersions", "retainedID", "GameVersionID"} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("catalog assignment helper missing: %s", fragment)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/admin/assets/management.css", nil)
	response = httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), ".sidebar-user{display:none}") {
		t.Fatalf("mobile session controls are hidden: %d", response.Code)
	}
	for _, fragment := range []string{".user-identity", "scrollbar-width:none"} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("responsive navigation hardening missing: %s", fragment)
		}
	}
}
