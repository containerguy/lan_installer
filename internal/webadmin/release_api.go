package webadmin

import (
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/containerguy/lan_installer/internal/store"
)

type publishEventReleaseRequest struct {
	Envelope json.RawMessage `json:"envelope"`
	Activate bool            `json:"activate"`
}

type publishClientUpdateRequest struct {
	Envelope json.RawMessage `json:"envelope"`
}

type rollbackCandidateRequest struct {
	ValidUntil string `json:"validUntil"`
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
	_, ok := a.releaseMutationSession(w, r)
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
	payload, metadata, err := a.releases.BuildRollbackCandidate(r.Context(), r.PathValue("eventID"), sequence, validUntil)
	if err != nil {
		a.releaseMutationError(w, err)
		return
	}
	var value any
	if err = json.Unmarshal(payload, &value); err != nil {
		a.apiError(w, http.StatusInternalServerError, "rollback_candidate_failed", "Rollback-Kandidat konnte nicht erstellt werden.")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"eventId": metadata.EventID, "sourceSequence": sequence, "sequence": metadata.Sequence, "releaseId": metadata.ReleaseID, "payload": value})
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
	switch {
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
	default:
		a.apiError(w, http.StatusUnprocessableEntity, "release_invalid", "Das Release ist ungültig, nicht vertrauenswürdig oder bereits veröffentlicht.")
	}
}
