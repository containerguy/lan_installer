package webadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/sourceprobe"
	"github.com/containerguy/lan_installer/internal/store"
)

type sourceAuthResponse struct {
	Type        string `json:"type"`
	Username    string `json:"username"`
	SecretState string `json:"secretState"`
}

type sourceResponse struct {
	ID       int64              `json:"id"`
	Revision int64              `json:"revision"`
	Name     string             `json:"name"`
	Kind     string             `json:"kind"`
	BaseURL  string             `json:"baseUrl"`
	Enabled  bool               `json:"enabled"`
	Auth     sourceAuthResponse `json:"auth"`
	LastTest map[string]any     `json:"lastTest"`
}

func (a *Admin) listSourcesAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, err := a.currentSession(r); err != nil {
		return
	}
	options := store.SourceListOptions{Query: r.URL.Query().Get("query"), Kind: r.URL.Query().Get("kind"), Limit: 50}
	if raw := r.URL.Query().Get("enabled"); raw != "" {
		enabled, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			a.apiError(w, http.StatusBadRequest, "source_filter_invalid", "Aktivfilter ist ungültig.")
			return
		}
		options.Enabled = &enabled
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		cursor, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || cursor < 0 {
			a.apiError(w, http.StatusBadRequest, "source_cursor_invalid", "Seitencursor ist ungültig.")
			return
		}
		options.Cursor = cursor
	}
	values, nextCursor, err := a.store.ListSources(r.Context(), options)
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "source_read_failed", "Quellen konnten nicht gelesen werden.")
		return
	}
	responses := make([]sourceResponse, 0, len(values))
	for _, value := range values {
		responses = append(responses, a.sourceResponse(r, value))
	}
	var cursor any
	if nextCursor > 0 {
		cursor = strconv.FormatInt(nextCursor, 10)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": responses, "nextCursor": cursor})
}

func (a *Admin) getSourceAPI(w http.ResponseWriter, r *http.Request) {
	if _, _, err := a.currentSession(r); err != nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.apiError(w, http.StatusNotFound, "source_not_found", "Quelle wurde nicht gefunden.")
		return
	}
	value, err := a.store.Source(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		a.apiError(w, http.StatusNotFound, "source_not_found", "Quelle wurde nicht gefunden.")
		return
	}
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "source_read_failed", "Quelle konnte nicht gelesen werden.")
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", value.Revision))
	a.writeJSON(w, http.StatusOK, a.sourceResponse(r, value))
}

func (a *Admin) sourceResponse(r *http.Request, value store.Source) sourceResponse {
	auth := sourceAuthResponse{Type: "none", SecretState: "missing"}
	if config, err := a.store.WebDAVConfig(r.Context(), value.ID); err == nil {
		auth.Type = config.AuthType
		auth.Username = config.Username
		if len(config.SecretCiphertext) > 0 {
			auth.SecretState = "stored"
		}
	}
	last := map[string]any{"state": value.LastTestState}
	if value.LastTestedAt != "" {
		last["testedAt"] = value.LastTestedAt
		last["latencyMs"] = value.LastTestLatencyMS
	}
	return sourceResponse{
		ID: value.ID, Revision: value.Revision, Name: value.Name, Kind: value.Kind,
		BaseURL: value.BaseURL, Enabled: value.Enabled, Auth: auth, LastTest: last,
	}
}

func (a *Admin) testSourceAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.validJSONRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin", "operator") {
		a.denySourceAction(w, r, session, "test_source", "Keine Berechtigung zum Testen von Quellen.")
		return
	}
	request, ok := a.decodeSourceRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin") {
		canonical, canonicalErr := a.canonicalOperatorTestRequest(r.Context(), request)
		if canonicalErr != nil {
			a.denySourceAction(w, r, session, "test_source_modified", canonicalErr.Error())
			return
		}
		request = canonical
	}
	fingerprint, password, err := a.sourceFingerprint(r.Context(), request)
	if err != nil {
		a.apiError(w, http.StatusUnprocessableEntity, "invalid_source", err.Error())
		return
	}
	result, err := a.sourceTester.Test(r.Context(), sourceprobe.Input{
		Kind: request.Kind, BaseURL: request.BaseURL,
		Username: request.Auth.Username, Password: password,
	})
	if err != nil {
		status := http.StatusUnprocessableEntity
		code, message := "source_test_failed", "Verbindungstest fehlgeschlagen."
		var probeErr *sourceprobe.ProbeError
		if errors.As(err, &probeErr) {
			code, message = probeErr.Code, probeErr.Message
			if code == "timeout" {
				status = http.StatusGatewayTimeout
			}
		}
		a.apiError(w, status, code, message)
		return
	}
	token, err := a.sourceTests.issue(sourceTestRecord{
		UserID: session.User.ID, Fingerprint: fingerprint, Result: result,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "test_token_failed", "Testergebnis konnte nicht bestätigt werden.")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"state": "succeeded", "testedAt": result.TestedAt.Format(time.RFC3339),
		"latencyMs": result.Latency.Milliseconds(), "capabilities": result.Capabilities,
		"testToken": token, "expiresIn": 300,
	})
}

func (a *Admin) createSourceAPI(w http.ResponseWriter, r *http.Request) {
	a.saveSourceAPI(w, r, 0)
}

func (a *Admin) updateSourceAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.apiError(w, http.StatusNotFound, "source_not_found", "Quelle wurde nicht gefunden.")
		return
	}
	a.saveSourceAPI(w, r, id)
}

func (a *Admin) saveSourceAPI(w http.ResponseWriter, r *http.Request, pathID int64) {
	session, ok := a.validJSONRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin") {
		a.denySourceAction(w, r, session, "save_source", "Nur Administratoren dürfen Quellen speichern.")
		return
	}
	request, ok := a.decodeSourceRequest(w, r)
	if !ok {
		return
	}
	if pathID == 0 && request.SourceID != 0 {
		a.apiError(w, http.StatusBadRequest, "source_id_invalid", "Beim Anlegen darf keine bestehende Source-ID angegeben werden.")
		return
	}
	if pathID > 0 {
		request.SourceID = pathID
		revision, valid := parseIfMatch(r.Header.Get("If-Match"))
		if !valid {
			a.apiError(w, http.StatusPreconditionRequired, "if_match_required", "Aktuelle Revision fehlt.")
			return
		}
		request.Revision = revision
	}
	fingerprint, _, err := a.sourceFingerprint(r.Context(), request)
	if err != nil {
		a.apiError(w, http.StatusUnprocessableEntity, "invalid_source", err.Error())
		return
	}
	record, valid := a.sourceTests.take(request.TestToken, session.User.ID, fingerprint)
	if !valid {
		a.apiError(w, http.StatusUnprocessableEntity, "test_token_invalid", "Der Verbindungstest ist abgelaufen oder passt nicht mehr zu den Eingaben.")
		return
	}
	value := store.Source{
		ID: request.SourceID, Name: request.Name, Kind: request.Kind, BaseURL: request.BaseURL,
		Enabled: request.Enabled, LastTestState: "succeeded",
		LastTestedAt:      record.Result.TestedAt.Format(time.RFC3339),
		LastTestLatencyMS: record.Result.Latency.Milliseconds(),
	}
	saved, err := a.store.SaveSourceAtomicWithAudit(r.Context(), value, request.Revision, a.sourceConfigBuilder(request), &store.AuditEntry{ActorUserID: session.User.ID, Action: "save_source", ObjectType: "source", Details: "atomic source+credential save", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.sourceTests.restore(request.TestToken, record)
		if errors.Is(err, store.ErrRevisionConflict) {
			a.apiError(w, http.StatusPreconditionFailed, "revision_conflict", "Die Quelle wurde zwischenzeitlich geändert.")
			return
		}
		a.apiError(w, http.StatusUnprocessableEntity, "source_save_failed", "Quelle und Zugangsdaten wurden nicht gespeichert.")
		return
	}
	status := http.StatusOK
	if pathID == 0 {
		status = http.StatusCreated
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", saved.Revision))
	a.writeJSON(w, status, a.sourceResponse(r, saved))
}

func (a *Admin) deactivateSourceAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.validJSONRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin") {
		a.denySourceAction(w, r, session, "deactivate_source", "Nur Administratoren dürfen Quellen deaktivieren.")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.apiError(w, http.StatusNotFound, "source_not_found", "Quelle wurde nicht gefunden.")
		return
	}
	revision, valid := parseIfMatch(r.Header.Get("If-Match"))
	if !valid {
		a.apiError(w, http.StatusPreconditionRequired, "if_match_required", "Aktuelle Revision fehlt.")
		return
	}
	newRevision, err := a.store.DeactivateSourceWithAudit(r.Context(), id, revision, &store.AuditEntry{ActorUserID: session.User.ID, Action: "deactivate_source", ObjectType: "source", ObjectID: strconv.FormatInt(id, 10), RemoteAddr: r.RemoteAddr})
	if errors.Is(err, store.ErrRevisionConflict) {
		a.apiError(w, http.StatusPreconditionFailed, "revision_conflict", "Die Quelle wurde zwischenzeitlich geändert.")
		return
	}
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "source_deactivate_failed", "Quelle konnte nicht deaktiviert werden.")
		return
	}
	w.Header().Set("ETag", fmt.Sprintf("\"%d\"", newRevision))
	a.writeJSON(w, http.StatusOK, map[string]any{"id": id, "revision": newRevision, "enabled": false})
}

func (a *Admin) deleteSourceAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.validJSONRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin") {
		a.denySourceAction(w, r, session, "delete_source", "Nur Administratoren dürfen Quellen löschen.")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.apiError(w, http.StatusNotFound, "source_not_found", "Quelle wurde nicht gefunden.")
		return
	}
	revision, valid := parseIfMatch(r.Header.Get("If-Match"))
	if !valid {
		a.apiError(w, http.StatusPreconditionRequired, "if_match_required", "Aktuelle Revision fehlt.")
		return
	}
	err = a.store.DeleteSourceWithAudit(r.Context(), id, revision, &store.AuditEntry{ActorUserID: session.User.ID, Action: "delete_source", ObjectType: "source", ObjectID: strconv.FormatInt(id, 10), RemoteAddr: r.RemoteAddr})
	var referenced *store.SourceReferencedError
	if errors.As(err, &referenced) {
		a.apiErrorWithFields(w, http.StatusConflict, "source_referenced",
			"Quelle wird noch von Versionen oder aktiven Cache-Aufträgen verwendet.", map[string]any{
				"launcherVersions": referenced.LauncherVersions,
				"gameVersions":     referenced.GameVersions,
				"cacheJobs":        referenced.CacheJobs,
			})
		return
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		a.apiError(w, http.StatusPreconditionFailed, "revision_conflict", "Die Quelle wurde zwischenzeitlich geändert.")
		return
	}
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "source_delete_failed", "Quelle konnte nicht gelöscht werden.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) decodeSourceRequest(w http.ResponseWriter, r *http.Request) (sourceRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request sourceRequest
	if err := decoder.Decode(&request); err != nil {
		a.apiError(w, http.StatusBadRequest, "invalid_json", "Anfrage ist ungültig.")
		return request, false
	}
	request = normalizeSourceRequest(request)
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		a.apiError(w, http.StatusBadRequest, "invalid_json", "Anfrage enthält unerwartete Daten.")
		return request, false
	}
	return request, true
}

func (a *Admin) validJSONRequest(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		a.apiError(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type muss application/json sein.")
		return store.Session{}, false
	}
	session, _, err := a.currentSession(r)
	if err != nil {
		a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
		return session, false
	}
	if !constantEqual(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		a.apiError(w, http.StatusForbidden, "csrf_invalid", "Ungültiges CSRF-Token.")
		return session, false
	}
	return session, true
}

func parseIfMatch(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, false
	}
	revision, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	return revision, err == nil && revision > 0
}

func (a *Admin) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("LANReady-API-Version", "2")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *Admin) apiError(w http.ResponseWriter, status int, code, message string) {
	a.apiErrorWithFields(w, status, code, message, map[string]any{})
}

func (a *Admin) apiErrorWithFields(w http.ResponseWriter, status int, code, message string, fields map[string]any) {
	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID, _ = randomToken()
		w.Header().Set("X-Request-ID", requestID)
	}
	a.writeJSON(w, status, map[string]any{
		"code": code, "message": message, "fieldErrors": fields, "requestId": requestID,
	})
}

func (a *Admin) denySourceAction(w http.ResponseWriter, r *http.Request, session store.Session, action, message string) {
	_ = a.store.Audit(r.Context(), &session.User.ID, action+"_denied", "source", r.PathValue("id"), "request_id="+w.Header().Get("X-Request-ID"), r.RemoteAddr)
	a.apiError(w, http.StatusForbidden, "forbidden", message)
}

func (a *Admin) canonicalOperatorTestRequest(ctx context.Context, request sourceRequest) (sourceRequest, error) {
	request = normalizeSourceRequest(request)
	if request.SourceID < 1 {
		return sourceRequest{}, errors.New("Operatoren dürfen nur eine unveränderte bestehende Quelle erneut testen.")
	}
	persisted, err := a.store.Source(ctx, request.SourceID)
	if err != nil {
		return sourceRequest{}, errors.New("Die bestehende Quelle konnte nicht geladen werden.")
	}
	canonical := sourceRequest{
		SourceID: persisted.ID, Revision: persisted.Revision, Name: persisted.Name,
		Kind: persisted.Kind, BaseURL: persisted.BaseURL, Enabled: persisted.Enabled,
		Auth: sourceAuthRequest{Type: "none", SecretMode: "none"},
	}
	config, configErr := a.store.WebDAVConfig(ctx, persisted.ID)
	if configErr == nil {
		canonical.Auth.Type = config.AuthType
		canonical.Auth.Username = config.Username
		if config.AuthType == "basic" {
			canonical.Auth.SecretMode = "reuse"
		}
	} else if !errors.Is(configErr, sql.ErrNoRows) {
		return sourceRequest{}, errors.New("Die Zugangskonfiguration konnte nicht geladen werden.")
	}
	if request.SourceID != canonical.SourceID || request.Name != canonical.Name || request.Kind != canonical.Kind ||
		request.BaseURL != canonical.BaseURL || request.Enabled != canonical.Enabled || request.Auth.Type != canonical.Auth.Type ||
		request.Auth.Username != canonical.Auth.Username || request.Auth.SecretMode != canonical.Auth.SecretMode || request.Auth.Password != "" {
		return sourceRequest{}, errors.New("Operatoren dürfen beim Verbindungstest keine Quellen- oder Zugangsdaten verändern.")
	}
	return canonical, nil
}
