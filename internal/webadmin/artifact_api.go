package webadmin

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/store"
)

const (
	clientUpdateContentType     = "application/vnd.microsoft.portable-executable"
	maxClientUpdateArtifactSize = int64(1 << 30)
)

var artifactDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *Admin) clientUpdateArtifactStatusAPI(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.artifactAdminSession(w, r, false); !ok {
		return
	}
	digest, size, ok := a.clientUpdateArtifactIdentity(w, r)
	if !ok {
		return
	}
	err := a.artifacts.Verify(r.Context(), digest, size, clientUpdateContentType)
	if err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"ready": false, "digest": digest, "size": size})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ready": true, "digest": digest, "size": size})
}

func (a *Admin) uploadClientUpdateArtifactAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.artifactAdminSession(w, r, true)
	if !ok {
		return
	}
	digest, size, ok := a.clientUpdateArtifactIdentity(w, r)
	if !ok {
		return
	}
	if r.ContentLength != size {
		a.apiError(w, http.StatusUnprocessableEntity, "artifact_size_invalid", "Die hochgeladene EXE besitzt nicht die im signierten Envelope deklarierte Größe.")
		return
	}
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Minute)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		a.apiError(w, http.StatusServiceUnavailable, "artifact_upload_deadline_failed", "Der sichere Upload konnte nicht vorbereitet werden.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, size+1)
	stored, err := a.artifacts.Ingest(r.Context(), digest, size, clientUpdateContentType, r.Body)
	if err != nil {
		switch {
		case errors.Is(err, artifact.ErrDigestMismatch):
			a.apiError(w, http.StatusUnprocessableEntity, "artifact_digest_mismatch", "Die EXE stimmt nicht mit dem signierten SHA-256-Digest überein.")
		case errors.Is(err, artifact.ErrSizeMismatch), errors.Is(err, artifact.ErrInvalidMetadata):
			a.apiError(w, http.StatusUnprocessableEntity, "artifact_size_invalid", "Die EXE besitzt nicht die signierte Größe oder gültige Metadaten.")
		case errors.Is(err, artifact.ErrQuotaExceeded):
			a.apiError(w, http.StatusInsufficientStorage, "cache_quota_exceeded", "Die konfigurierte Cachegrenze reicht für diese EXE nicht aus.")
		default:
			a.apiError(w, http.StatusInternalServerError, "artifact_upload_failed", "Das Clientupdate-Artefakt konnte nicht sicher gespeichert werden.")
		}
		return
	}
	_ = a.store.Audit(r.Context(), &session.User.ID, "upload_client_update_artifact", "artifact", digest, "", r.RemoteAddr)
	a.writeJSON(w, http.StatusCreated, map[string]any{"ready": true, "digest": stored.Digest, "size": stored.SizeBytes})
}

func (a *Admin) artifactAdminSession(w http.ResponseWriter, r *http.Request, mutation bool) (store.Session, bool) {
	if a.artifacts == nil {
		a.apiError(w, http.StatusServiceUnavailable, "artifact_store_unavailable", "Der Artefaktspeicher ist nicht konfiguriert.")
		return store.Session{}, false
	}
	session, _, err := a.currentSession(r)
	if err != nil {
		a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
		return session, false
	}
	if !hasRole(session.User, "admin") {
		a.denyCatalogAction(w, r, session, "artifact_upload_denied")
		return session, false
	}
	if mutation {
		if !constantEqual(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
			a.apiError(w, http.StatusForbidden, "csrf_invalid", "Ungültiges CSRF-Token.")
			return session, false
		}
		mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
		if mediaType != clientUpdateContentType {
			a.apiError(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type der Client-EXE ist ungültig.")
			return session, false
		}
	}
	return session, true
}

func (a *Admin) clientUpdateArtifactIdentity(w http.ResponseWriter, r *http.Request) (string, int64, bool) {
	digest := r.PathValue("digest")
	size, err := strconv.ParseInt(r.URL.Query().Get("size"), 10, 64)
	if !artifactDigestPattern.MatchString(digest) || err != nil || size < 1 || size > maxClientUpdateArtifactSize {
		a.apiError(w, http.StatusUnprocessableEntity, "artifact_metadata_invalid", "Digest oder Größe des Clientupdate-Artefakts ist ungültig.")
		return "", 0, false
	}
	return digest, size, true
}
