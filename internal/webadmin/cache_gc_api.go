package webadmin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/store"
)

type garbageCollectCacheRequest struct {
	MinimumAgeHours int `json:"minimumAgeHours"`
	Limit           int `json:"limit"`
}

func (a *Admin) cacheStatusAPI(w http.ResponseWriter, r *http.Request) {
	if a.artifacts == nil {
		a.apiError(w, http.StatusServiceUnavailable, "artifact_store_unavailable", "Der Cache ist nicht konfiguriert.")
		return
	}
	usage, quota, err := a.artifacts.Usage(r.Context())
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "cache_status_failed", "Der Cacheverbrauch konnte nicht gelesen werden.")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"usageBytes": usage, "quotaBytes": quota})
}

func (a *Admin) garbageCollectCacheAPI(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		a.apiError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key ist für die Cachebereinigung erforderlich.")
		return
	}
	session, ok := a.cacheAdminMutationSession(w, r)
	if !ok {
		return
	}
	var request garbageCollectCacheRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	if request.MinimumAgeHours < 24 || request.MinimumAgeHours > 8760 || request.Limit < 1 || request.Limit > 1000 {
		a.apiError(w, http.StatusUnprocessableEntity, "cache_gc_policy_invalid", "Mindestalter oder Prüflimit der Cachebereinigung ist ungültig.")
		return
	}
	usage, quota, err := a.artifacts.Usage(r.Context())
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "cache_status_failed", "Der Cacheverbrauch konnte vor der Bereinigung nicht gelesen werden.")
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(request.MinimumAgeHours) * time.Hour)
	result, collectErr := a.artifacts.GarbageCollect(context.WithoutCancel(r.Context()), cutoff, request.Limit, session.User.ID, r.RemoteAddr)
	if collectErr != nil && result.Removed == 0 {
		a.apiError(w, http.StatusInternalServerError, "cache_gc_failed", "Die Cachebereinigung konnte nicht sicher abgeschlossen werden.")
		return
	}
	usage -= result.RemovedBytes
	if usage < 0 {
		usage = 0
	}
	response := map[string]any{
		"examined": result.Examined, "removed": result.Removed, "removedBytes": result.RemovedBytes,
		"usageBytes": usage, "quotaBytes": quota, "minimumAgeHours": request.MinimumAgeHours,
		"partial": collectErr != nil,
	}
	if collectErr != nil {
		response["warning"] = "Die Bereinigung wurde nach bereits entfernten Artefakten sicher beendet. Starte sie erneut, um weitere Kandidaten zu prüfen."
	}
	a.writeJSON(w, http.StatusOK, response)
}

func (a *Admin) cacheAdminMutationSession(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	if a.artifacts == nil {
		a.apiError(w, http.StatusServiceUnavailable, "artifact_store_unavailable", "Der Cache ist nicht konfiguriert.")
		return store.Session{}, false
	}
	session, _, err := a.currentSession(r)
	if err != nil {
		a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
		return session, false
	}
	if !hasRole(session.User, "admin") {
		a.denyCatalogAction(w, r, session, "cache_gc_denied")
		return session, false
	}
	if !constantEqual(session.CSRFToken, r.Header.Get("X-CSRF-Token")) {
		a.apiError(w, http.StatusForbidden, "csrf_invalid", "Ungültiges CSRF-Token.")
		return session, false
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" {
		a.apiError(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type application/json ist erforderlich.")
		return session, false
	}
	return session, true
}
