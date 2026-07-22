package webadmin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	lanrelease "github.com/containerguy/lan_installer/internal/release"
	"github.com/containerguy/lan_installer/internal/store"
)

type publishEventReleaseRequest struct {
	Envelope json.RawMessage `json:"envelope"`
	Activate bool            `json:"activate"`
}

type generateEventReleaseRequest struct {
	EventID              int64  `json:"eventId"`
	MinimumClientVersion string `json:"minimumClientVersion"`
	ValidUntil           string `json:"validUntil"`
	Activate             bool   `json:"activate"`
}

type publishClientUpdateRequest struct {
	Envelope json.RawMessage `json:"envelope"`
}

type rollbackCandidateRequest struct {
	ValidUntil string `json:"validUntil"`
	Activate   bool   `json:"activate"`
}

func (a *Admin) releaseStatusAPI(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	active, err := a.store.ActiveEventRelease(r.Context())
	var activeEvent any
	if err == nil {
		state := releaseDeliveryState(now, active)
		activeEvent = map[string]any{"eventId": active.EventID, "releaseId": active.ReleaseID, "sequence": active.Sequence, "issuedAt": active.IssuedAt, "validUntil": active.ValidUntil, "minimumClientVersion": active.MinimumVersion, "deliveryState": state, "delivering": state == "active"}
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.apiError(w, http.StatusInternalServerError, "release_status_failed", "Release-Status konnte nicht gelesen werden.")
		return
	}
	update, updateErr := a.store.LatestClientUpdateRelease(r.Context(), "stable")
	var clientUpdate any
	if updateErr == nil {
		clientUpdate = map[string]any{"channel": update.Channel, "sequence": update.Sequence, "version": update.Version, "minimumVersion": update.MinimumVersion}
	} else if !errors.Is(updateErr, sql.ErrNoRows) {
		a.apiError(w, http.StatusInternalServerError, "release_status_failed", "Release-Status konnte nicht gelesen werden.")
		return
	}
	releases, releasesErr := a.store.EventReleases(r.Context())
	if releasesErr != nil {
		a.apiError(w, http.StatusInternalServerError, "release_status_failed", "Release-Status konnte nicht gelesen werden.")
		return
	}
	eventReleases := make([]map[string]any, 0, len(releases))
	for _, release := range releases {
		isSelected := active.EventID == release.EventID && active.Sequence == release.Sequence
		eventReleases = append(eventReleases, map[string]any{"eventId": release.EventID, "releaseId": release.ReleaseID, "sequence": release.Sequence, "issuedAt": release.IssuedAt, "validUntil": release.ValidUntil, "minimumClientVersion": release.MinimumVersion, "selected": isSelected, "deliveryState": releaseDeliveryState(now, release)})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"activeEvent": activeEvent, "eventReleases": eventReleases, "clientUpdate": clientUpdate})
}

func (a *Admin) eventReleasePreflightAPI(w http.ResponseWriter, r *http.Request) {
	if a.releases == nil {
		a.apiError(w, http.StatusServiceUnavailable, "release_service_unavailable", "Release-Publishing ist noch nicht konfiguriert.")
		return
	}
	eventID, err := strconv.ParseInt(r.PathValue("eventID"), 10, 64)
	if err != nil || eventID < 1 {
		a.apiError(w, http.StatusUnprocessableEntity, "release_event_invalid", "Das Event ist ungültig.")
		return
	}
	result, err := a.releases.PreflightEvent(r.Context(), eventID)
	if errors.Is(err, sql.ErrNoRows) {
		a.apiError(w, http.StatusNotFound, "release_event_unknown", "Das Event existiert nicht im Katalog.")
		return
	}
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "release_preflight_failed", "Die Event-Prüfung konnte nicht abgeschlossen werden.")
		return
	}
	a.writeJSON(w, http.StatusOK, result)
}

func releaseDeliveryState(now time.Time, release store.StoredRelease) string {
	if now.Before(release.IssuedAt) {
		return "scheduled"
	}
	if !now.Before(release.ValidUntil) {
		return "expired"
	}
	return "active"
}

func (a *Admin) publishEventReleaseAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.releaseMutationSession(w, r)
	if !ok {
		return
	}
	var request publishEventReleaseRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	if len(request.Envelope) == 0 {
		a.apiError(w, http.StatusUnprocessableEntity, "release_envelope_required", "Ein signiertes Event-Envelope ist erforderlich.")
		return
	}
	metadata, err := a.releases.PublishEvent(r.Context(), request.Envelope, request.Activate, &store.AuditEntry{ActorUserID: session.User.ID, Action: "publish_event_release", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.releaseMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, map[string]any{"eventId": metadata.EventID, "releaseId": metadata.ReleaseID, "sequence": metadata.Sequence, "active": request.Activate})
}

func (a *Admin) generateEventReleaseAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.releaseMutationSession(w, r)
	if !ok {
		return
	}
	var request generateEventReleaseRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	validUntil, err := time.Parse(time.RFC3339, request.ValidUntil)
	if request.EventID < 1 || err != nil || request.MinimumClientVersion == "" {
		a.apiError(w, http.StatusUnprocessableEntity, "release_fields_invalid", "Event, Gültigkeitsende und Mindestclient-Version sind erforderlich.")
		return
	}
	result, err := a.releases.GenerateAndPublishEvent(r.Context(), request.EventID, request.MinimumClientVersion, validUntil, request.Activate, &store.AuditEntry{ActorUserID: session.User.ID, Action: "generate_publish_event_release", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.releaseMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, map[string]any{"eventId": result.EventID, "releaseId": result.ReleaseID, "sequence": result.Sequence, "gameCount": result.GameCount, "launcherCount": result.LauncherCount, "active": request.Activate})
}

func (a *Admin) publishClientUpdateReleaseAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.releaseMutationSession(w, r)
	if !ok {
		return
	}
	var request publishClientUpdateRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	if len(request.Envelope) == 0 {
		a.apiError(w, http.StatusUnprocessableEntity, "release_envelope_required", "Ein signiertes Clientupdate-Envelope ist erforderlich.")
		return
	}
	metadata, err := a.releases.PublishClientUpdate(r.Context(), request.Envelope, &store.AuditEntry{ActorUserID: session.User.ID, Action: "publish_client_update_release", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.releaseMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, map[string]any{"channel": metadata.Channel, "version": metadata.Version, "sequence": metadata.Sequence})
}

func (a *Admin) activateEventReleaseAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.releaseMutationSession(w, r)
	if !ok {
		return
	}
	var empty struct{}
	if !a.decodeCatalogJSON(w, r, &empty) {
		return
	}
	sequence, err := strconv.ParseInt(r.PathValue("sequence"), 10, 64)
	if err != nil || sequence < 1 {
		a.apiError(w, http.StatusUnprocessableEntity, "release_sequence_invalid", "Die Release-Sequenz ist ungültig.")
		return
	}
	eventID := r.PathValue("eventID")
	metadata, err := a.releases.ActivateEvent(r.Context(), eventID, sequence, &store.AuditEntry{ActorUserID: session.User.ID, Action: "activate_event_release", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.releaseMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"eventId": metadata.EventID, "releaseId": metadata.ReleaseID, "sequence": metadata.Sequence, "active": true})
}

func (a *Admin) buildRollbackCandidateAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.releaseMutationSession(w, r)
	if !ok {
		return
	}
	var request rollbackCandidateRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	sequence, err := strconv.ParseInt(r.PathValue("sequence"), 10, 64)
	if err != nil || sequence < 1 {
		a.apiError(w, http.StatusUnprocessableEntity, "release_sequence_invalid", "Die Rollback-Quellsequenz ist ungültig.")
		return
	}
	validUntil, err := time.Parse(time.RFC3339, request.ValidUntil)
	if err != nil {
		a.apiError(w, http.StatusUnprocessableEntity, "rollback_validity_invalid", "Für den Rollback-Kandidaten ist ein gültiges zukünftiges Ende erforderlich.")
		return
	}
	metadata, err := a.releases.GenerateAndPublishRollback(r.Context(), r.PathValue("eventID"), sequence, validUntil, request.Activate, &store.AuditEntry{ActorUserID: session.User.ID, Action: "generate_publish_event_rollback", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.releaseMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, map[string]any{"eventId": metadata.EventID, "sourceSequence": sequence, "sequence": metadata.Sequence, "releaseId": metadata.ReleaseID, "active": request.Activate})
}

func (a *Admin) releaseMutationSession(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	if a.releases == nil {
		a.apiError(w, http.StatusServiceUnavailable, "release_service_unavailable", "Release-Publishing ist noch nicht konfiguriert.")
		return store.Session{}, false
	}
	session, ok := a.catalogAdminMutationSession(w, r)
	if !ok {
		return session, false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		a.apiError(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type application/json ist erforderlich.")
		return session, false
	}
	return session, true
}

func (a *Admin) releaseMutationError(w http.ResponseWriter, err error) {
	var preflight *lanrelease.EventPreflightError
	var compatibility *lanrelease.ClientCompatibilityError
	var updateCompatibility *lanrelease.CompatibleClientUpdateError
	switch {
	case errors.As(err, &preflight):
		a.apiErrorWithFields(w, http.StatusUnprocessableEntity, "release_preflight_failed", "Das Event ist noch nicht veröffentlichbar. Prüfe die angezeigten Spielversionen.", map[string]any{"issues": preflight.Preflight.Issues})
	case errors.As(err, &compatibility):
		a.apiErrorWithFields(w, http.StatusConflict, "release_clients_incompatible", "Aktivierung blockiert: Aktualisiere zuerst alle aktiven LANReady-Clients auf mindestens "+compatibility.MinimumVersion+".", map[string]any{"devices": compatibility.Devices, "minimumClientVersion": compatibility.MinimumVersion})
	case errors.As(err, &updateCompatibility):
		a.apiErrorWithFields(w, http.StatusConflict, "release_client_update_missing", "Aktivierung blockiert: Es sind Clients unterhalb von "+updateCompatibility.MinimumVersion+" aktiv und im Stable-Kanal liegt kein signiertes Clientupdate. Entweder ein Clientupdate veröffentlichen oder alle betroffenen PCs manuell auf "+updateCompatibility.MinimumVersion+" aktualisieren.", map[string]any{"minimumClientVersion": updateCompatibility.MinimumVersion})
	case errors.Is(err, store.ErrReleaseSequence):
		a.apiError(w, http.StatusConflict, "release_sequence_conflict", "Die Release-Sequenz ist nicht der nächste monotone Wert.")
	case errors.Is(err, store.ErrReleaseEventUnknown):
		a.apiError(w, http.StatusUnprocessableEntity, "release_event_unknown", "Das Event existiert nicht im Katalog.")
	case errors.Is(err, store.ErrReleaseEventArchived):
		a.apiError(w, http.StatusConflict, "release_event_archived", "Ein archiviertes Event kann nicht erneut veröffentlicht werden.")
	case errors.Is(err, store.ErrReleaseArtifact), errors.Is(err, sql.ErrNoRows):
		a.apiError(w, http.StatusUnprocessableEntity, "release_artifact_invalid", "Mindestens ein signiertes Artefakt fehlt oder besitzt abweichende Metadaten.")
	case errors.Is(err, store.ErrReleaseValidity):
		a.apiError(w, http.StatusUnprocessableEntity, "release_validity_invalid", "Das Release ist derzeit nicht gültig und kann deshalb nicht aktiviert werden.")
	case errors.Is(err, store.ErrReleaseVersion):
		a.apiError(w, http.StatusConflict, "release_version_conflict", "Die Clientupdate-Version ist kein monotones Upgrade oder besitzt eine ungültige Mindestversion.")
	case errors.Is(err, store.ErrReleaseNotLatest):
		a.apiError(w, http.StatusConflict, "release_activation_not_latest", "Nur die neueste Event-Sequenz kann aktiviert werden. Ein Rollback muss als neue höhere signierte Sequenz veröffentlicht werden.")
	case errors.Is(err, lanrelease.ErrEventHasNoGames):
		a.apiError(w, http.StatusUnprocessableEntity, "release_event_empty", "Dem Event ist noch keine Spielversion zugeordnet.")
	case errors.Is(err, lanrelease.ErrEventCatalogInvalid):
		a.apiError(w, http.StatusUnprocessableEntity, "release_catalog_invalid", "Mindestens ein zugeordneter Eintrag ist deaktiviert, unvollständig oder besitzt keine externe Spiel-ID.")
	case errors.Is(err, lanrelease.ErrEventPackageUnsupported):
		a.apiError(w, http.StatusUnprocessableEntity, "release_package_unsupported", "Paketbasierte und launcherlose Spiele werden erst nach dem Windows-Installations-Slice veröffentlichbar.")
	case errors.Is(err, lanrelease.ErrEventActionsUnsupported):
		a.apiError(w, http.StatusUnprocessableEntity, "release_actions_unsupported", "Dieses Release enthält Paketaktionen, die der aktuelle Windows-Client noch nicht sicher ausführen kann.")
	case errors.Is(err, lanrelease.ErrEventSignerUnavailable):
		a.apiError(w, http.StatusServiceUnavailable, "release_signer_unavailable", "Der geschützte Signer ist derzeit nicht verfügbar.")
	default:
		a.apiError(w, http.StatusUnprocessableEntity, "release_invalid", "Das Release ist ungültig, nicht vertrauenswürdig oder bereits veröffentlicht.")
	}
}
