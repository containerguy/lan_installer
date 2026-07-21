package webadmin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/containerguy/lan_installer/internal/store"
)

type catalogRequest struct {
	ID             int64  `json:"id"`
	Revision       int64  `json:"revision"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Adapter        string `json:"adapter"`
	Enabled        bool   `json:"enabled"`
	LauncherID     int64  `json:"launcherId"`
	GameID         int64  `json:"gameId"`
	SourceID       int64  `json:"sourceId"`
	ExternalGameID string `json:"externalGameId"`
	Version        string `json:"version"`
	SourcePath     string `json:"sourcePath"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"sizeBytes"`
	StartsAt       string `json:"startsAt"`
	EndsAt         string `json:"endsAt"`
	Status         string `json:"status"`
}

type eventGameRequest struct {
	EventID       int64 `json:"eventId"`
	GameVersionID int64 `json:"gameVersionId"`
	Revision      int64 `json:"revision"`
	Required      bool  `json:"required"`
}

func (a *Admin) catalogSnapshotAPI(w http.ResponseWriter, r *http.Request) {
	session, _, err := a.currentSession(r)
	if err != nil {
		a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
		return
	}
	launchers, err := a.store.Launchers(r.Context())
	if err != nil {
		a.catalogReadError(w)
		return
	}
	sources, err := a.store.Sources(r.Context())
	if err != nil {
		a.catalogReadError(w)
		return
	}
	games, err := a.store.Games(r.Context())
	if err != nil {
		a.catalogReadError(w)
		return
	}
	launcherVersions, err := a.store.LauncherVersions(r.Context())
	if err != nil {
		a.catalogReadError(w)
		return
	}
	gameVersions, err := a.store.GameVersions(r.Context())
	if err != nil {
		a.catalogReadError(w)
		return
	}
	events, err := a.store.Events(r.Context())
	if err != nil {
		a.catalogReadError(w)
		return
	}
	eventGames, err := a.store.EventGames(r.Context())
	if err != nil {
		a.catalogReadError(w)
		return
	}
	cacheJobs, err := a.store.CacheJobs(r.Context(), 500)
	if err != nil {
		a.catalogReadError(w)
		return
	}
	if launchers == nil {
		launchers = []store.Launcher{}
	}
	if sources == nil {
		sources = []store.Source{}
	}
	if games == nil {
		games = []store.Game{}
	}
	if launcherVersions == nil {
		launcherVersions = []store.LauncherVersion{}
	}
	if gameVersions == nil {
		gameVersions = []store.GameVersion{}
	}
	if events == nil {
		events = []store.Event{}
	}
	if eventGames == nil {
		eventGames = []store.EventGame{}
	}
	if cacheJobs == nil {
		cacheJobs = []store.CacheJob{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"launchers": launchers, "sources": sources, "games": games,
		"launcherVersions": launcherVersions, "gameVersions": gameVersions,
		"events": events, "eventGames": eventGames, "cacheJobs": cacheJobs,
		"permissions": map[string]bool{
			"canEdit":    hasRole(session.User, "admin", "operator"),
			"canDelete":  hasRole(session.User, "admin"),
			"canPublish": hasRole(session.User, "admin"),
		},
	})
}

func (a *Admin) catalogReadError(w http.ResponseWriter) {
	a.apiError(w, http.StatusInternalServerError, "catalog_read_failed", "Katalogdaten konnten nicht gelesen werden.")
}

func (a *Admin) catalogIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	revision, valid := parseIfMatch(r.Header.Get("If-Match"))
	if !valid {
		a.apiError(w, http.StatusPreconditionRequired, "if_match_required", "Aktuelle Revision fehlt.")
		return 0, false
	}
	return revision, true
}

func (a *Admin) saveCatalogAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogMutationSession(w, r, true)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if kind != "launcher" && kind != "game" && kind != "launcher-version" && kind != "game-version" && kind != "event" {
		a.apiError(w, http.StatusNotFound, "catalog_type_unknown", "Katalogtyp wurde nicht gefunden.")
		return
	}
	var request catalogRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	pathID := int64(0)
	if value := r.PathValue("id"); value != "" {
		var err error
		pathID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || pathID < 1 {
			a.apiError(w, http.StatusNotFound, "catalog_not_found", "Eintrag wurde nicht gefunden.")
			return
		}
	}
	if r.Method == http.MethodPost && (request.ID != 0 || request.Revision != 0) || r.Method == http.MethodPut && request.ID != pathID {
		a.apiError(w, http.StatusBadRequest, "catalog_id_mismatch", "Die Eintrags-ID passt nicht zur Anfrage.")
		return
	}
	if r.Method == http.MethodPut {
		revision, valid := a.catalogIfMatch(w, r)
		if !valid {
			return
		}
		if request.Revision != 0 && request.Revision != revision {
			a.apiError(w, http.StatusBadRequest, "catalog_revision_mismatch", "Die Revision passt nicht zur Anfrage.")
			return
		}
		request.Revision = revision
	}
	request.ID = pathID
	action := "create_" + kind
	status := http.StatusCreated
	if pathID > 0 {
		action = "update_" + kind
		status = http.StatusOK
	}
	audit := &store.AuditEntry{ActorUserID: session.User.ID, Action: action, RemoteAddr: r.RemoteAddr}
	var (
		id  int64
		err error
	)
	switch kind {
	case "launcher":
		id, err = a.store.SaveLauncherAtomic(r.Context(), store.Launcher{ID: request.ID, Revision: request.Revision, Slug: request.Slug, Name: request.Name, Adapter: request.Adapter, Enabled: request.Enabled}, audit)
	case "game":
		id, err = a.store.SaveGameAtomic(r.Context(), store.Game{ID: request.ID, Revision: request.Revision, Slug: request.Slug, Name: request.Name, LauncherID: request.LauncherID, ExternalGameID: request.ExternalGameID, Enabled: request.Enabled}, audit)
	case "launcher-version":
		id, err = a.store.SaveLauncherVersionAtomic(r.Context(), store.LauncherVersion{ID: request.ID, Revision: request.Revision, LauncherID: request.LauncherID, Version: request.Version, SourceID: request.SourceID, SourcePath: request.SourcePath, SHA256: request.SHA256, SizeBytes: request.SizeBytes, Enabled: request.Enabled}, audit)
	case "game-version":
		id, err = a.store.SaveGameVersionAtomic(r.Context(), store.GameVersion{ID: request.ID, Revision: request.Revision, GameID: request.GameID, Version: request.Version, SourceID: request.SourceID, SourcePath: request.SourcePath, SHA256: request.SHA256, SizeBytes: request.SizeBytes, Enabled: request.Enabled}, audit)
	case "event":
		id, err = a.store.SaveEventAtomic(r.Context(), store.Event{ID: request.ID, Revision: request.Revision, Slug: request.Slug, Name: request.Name, StartsAt: request.StartsAt, EndsAt: request.EndsAt, Status: request.Status}, audit)
	}
	if err != nil {
		a.catalogMutationError(w, err)
		return
	}
	savedRevision := int64(1)
	if request.Revision > 0 {
		savedRevision = request.Revision + 1
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", savedRevision))
	a.writeJSON(w, status, map[string]any{"id": id, "revision": savedRevision, "message": "Änderungen wurden gespeichert."})
}

func (a *Admin) deactivateCatalogAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogMutationSession(w, r, false)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		a.apiError(w, http.StatusNotFound, "catalog_not_found", "Eintrag wurde nicht gefunden.")
		return
	}
	revision, valid := a.catalogIfMatch(w, r)
	if !valid {
		return
	}
	err = a.store.SetCatalogEnabledAtomic(r.Context(), kind, id, revision, false, &store.AuditEntry{ActorUserID: session.User.ID, Action: "deactivate_" + kind, RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.catalogMutationError(w, err)
		return
	}
	newRevision := revision + 1
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", newRevision))
	a.writeJSON(w, http.StatusOK, map[string]any{"id": id, "revision": newRevision, "enabled": false, "message": "Eintrag wurde deaktiviert."})
}

func (a *Admin) deleteCatalogAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogAdminMutationSession(w, r)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		a.apiError(w, http.StatusNotFound, "catalog_not_found", "Eintrag wurde nicht gefunden.")
		return
	}
	revision, valid := a.catalogIfMatch(w, r)
	if !valid {
		return
	}
	err = a.store.DeleteCatalogAtomic(r.Context(), kind, id, revision, &store.AuditEntry{ActorUserID: session.User.ID, Action: "delete_" + kind, RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.catalogMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) saveEventGameAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogMutationSession(w, r, true)
	if !ok {
		return
	}
	var request eventGameRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	if request.Revision != 0 {
		a.apiError(w, http.StatusBadRequest, "event_game_revision_invalid", "Beim Anlegen darf keine Revision angegeben werden.")
		return
	}
	err := a.store.SaveEventGameAtomic(r.Context(), store.EventGame{EventID: request.EventID, GameVersionID: request.GameVersionID, Required: request.Required}, &store.AuditEntry{ActorUserID: session.User.ID, Action: "save_event_game", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.catalogMutationError(w, err)
		return
	}
	w.Header().Set("ETag", `"1"`)
	a.writeJSON(w, http.StatusCreated, map[string]any{"eventId": request.EventID, "gameVersionId": request.GameVersionID, "revision": 1, "message": "Spielversion wurde dem Event zugeordnet."})
}

func (a *Admin) deleteEventGameAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogMutationSession(w, r, false)
	if !ok {
		return
	}
	eventID, eventErr := strconv.ParseInt(r.PathValue("eventID"), 10, 64)
	gameVersionID, versionErr := strconv.ParseInt(r.PathValue("gameVersionID"), 10, 64)
	if eventErr != nil || versionErr != nil {
		a.apiError(w, http.StatusNotFound, "event_game_not_found", "Zuordnung wurde nicht gefunden.")
		return
	}
	revision, valid := a.catalogIfMatch(w, r)
	if !valid {
		return
	}
	err := a.store.DeleteEventGameAtomic(r.Context(), eventID, gameVersionID, revision, &store.AuditEntry{ActorUserID: session.User.ID, Action: "delete_event_game", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.catalogMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) catalogMutationSession(w http.ResponseWriter, r *http.Request, requireJSON bool) (store.Session, bool) {
	session, _, err := a.currentSession(r)
	if err != nil {
		a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
		return session, false
	}
	if !hasRole(session.User, "admin", "operator") {
		a.denyCatalogAction(w, r, session, "catalog_mutation_denied")
		return session, false
	}
	if !constantEqual(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		a.apiError(w, http.StatusForbidden, "csrf_invalid", "Ungültiges CSRF-Token.")
		return session, false
	}
	if requireJSON {
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
		if mediaType != "application/json" {
			a.apiError(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type application/json ist erforderlich.")
			return session, false
		}
	}
	return session, true
}

func (a *Admin) catalogAdminMutationSession(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	session, _, err := a.currentSession(r)
	if err != nil {
		a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
		return session, false
	}
	if !hasRole(session.User, "admin") {
		a.denyCatalogAction(w, r, session, "catalog_delete_denied")
		return session, false
	}
	if !constantEqual(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		a.apiError(w, http.StatusForbidden, "csrf_invalid", "Ungültiges CSRF-Token.")
		return session, false
	}
	return session, true
}

func (a *Admin) denyCatalogAction(w http.ResponseWriter, r *http.Request, session store.Session, action string) {
	_ = a.store.Audit(r.Context(), &session.User.ID, action, "catalog", "", "", r.RemoteAddr)
	a.apiError(w, http.StatusForbidden, "forbidden", "Für diese Aktion fehlt die Berechtigung.")
}

func (a *Admin) decodeCatalogJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		a.apiError(w, http.StatusBadRequest, "invalid_json", "Die Anfrage enthält ungültige Felder oder Werte.")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		a.apiError(w, http.StatusBadRequest, "invalid_json", "Nach dem JSON-Objekt sind keine weiteren Daten erlaubt.")
		return false
	}
	return true
}

func (a *Admin) catalogMutationError(w http.ResponseWriter, err error) {
	var referenced *store.CatalogReferencedError
	switch {
	case errors.Is(err, store.ErrRevisionConflict):
		a.apiError(w, http.StatusPreconditionFailed, "revision_conflict", "Der Eintrag wurde zwischenzeitlich geändert.")
	case errors.As(err, &referenced):
		a.apiErrorWithFields(w, http.StatusConflict, "catalog_referenced", "Der Eintrag wird noch verwendet. Entferne zuerst die abhängigen Einträge.", map[string]any{"references": referenced.References})
	case errors.Is(err, store.ErrPublishedEventLocked):
		a.apiError(w, http.StatusConflict, "event_immutable", "Archivierte Events sind unveränderlich.")
	case errors.Is(err, store.ErrEventGameConflict):
		a.apiError(w, http.StatusConflict, "event_game_conflict", "Dieses Event enthält bereits eine andere Version desselben Spiels. Entferne zuerst die bisherige Version.")
	case errors.Is(err, store.ErrCatalogNotFound), errors.Is(err, sql.ErrNoRows):
		a.apiError(w, http.StatusNotFound, "catalog_not_found", "Eintrag wurde nicht gefunden.")
	case strings.Contains(strings.ToLower(err.Error()), "unique constraint"):
		a.apiError(w, http.StatusConflict, "catalog_conflict", "Slug oder Version ist bereits vorhanden.")
	default:
		a.apiError(w, http.StatusUnprocessableEntity, "catalog_invalid", catalogValidationMessage(err))
	}
}

func catalogValidationMessage(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "slug"):
		return "Der Slug darf nur Kleinbuchstaben, Zahlen und einzelne Bindestriche enthalten."
	case strings.Contains(message, "sha256"):
		return "SHA-256 muss leer sein oder genau 64 Hex-Zeichen enthalten."
	case strings.Contains(message, "path"):
		return "Der Quellpfad muss relativ sein und darf das Quellverzeichnis nicht verlassen."
	case strings.Contains(message, "adapter"):
		return "Der Launcher-Adapter wird nicht unterstützt."
	case strings.Contains(message, "date"):
		return "Die Event-Zeitangabe ist ungültig."
	case strings.Contains(message, "disabled game version"):
		return "Eine deaktivierte Spielversion kann nicht zugeordnet werden."
	case strings.Contains(message, "disabled game"):
		return "Ein deaktiviertes Spiel kann nicht neu zugeordnet werden."
	case strings.Contains(message, "disabled launcher"):
		return "Ein deaktivierter Launcher kann nicht neu zugeordnet werden."
	case strings.Contains(message, "disabled source"):
		return "Eine deaktivierte Quelle kann nicht neu zugeordnet werden."
	default:
		return "Bitte prüfe die markierten Pflichtfelder und Werte."
	}
}
