package webadmin

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/containerguy/lan_installer/internal/store"
)

type enqueueCacheJobRequest struct {
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
}

func (a *Admin) listCacheJobsAPI(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.store.CacheJobs(r.Context(), 500)
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "cache_jobs_read_failed", "Cache-Aufträge konnten nicht gelesen werden.")
		return
	}
	if jobs == nil {
		jobs = []store.CacheJob{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (a *Admin) enqueueCacheJobAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogMutationSession(w, r, true)
	if !ok {
		return
	}
	var request enqueueCacheJobRequest
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	targetType := strings.ReplaceAll(strings.TrimSpace(request.TargetType), "-", "_")
	if targetType != "launcher_version" && targetType != "game_version" || request.TargetID < 1 {
		a.apiError(w, http.StatusUnprocessableEntity, "cache_target_invalid", "Nur eine gültige Launcher- oder Spielversion kann in den Cache geladen werden.")
		return
	}
	job, err := a.store.EnqueueCacheJobWithAudit(r.Context(), targetType, request.TargetID, session.User.ID, &store.AuditEntry{ActorUserID: session.User.ID, Action: "enqueue_cache_job", ObjectType: "cache_job", Details: targetType + ":" + strconv.FormatInt(request.TargetID, 10), RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.cacheJobMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusAccepted, job)
}

func (a *Admin) cancelCacheJobAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogMutationSession(w, r, false)
	if !ok {
		return
	}
	job, err := a.store.RequestCacheJobCancelWithAudit(r.Context(), r.PathValue("id"), &store.AuditEntry{ActorUserID: session.User.ID, Action: "cancel_cache_job", ObjectType: "cache_job", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.cacheJobMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, job)
}

func (a *Admin) retryCacheJobAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogMutationSession(w, r, false)
	if !ok {
		return
	}
	job, err := a.store.RetryCacheJobWithAudit(r.Context(), r.PathValue("id"), &store.AuditEntry{ActorUserID: session.User.ID, Action: "retry_cache_job", ObjectType: "cache_job", RemoteAddr: r.RemoteAddr})
	if err != nil {
		a.cacheJobMutationError(w, err)
		return
	}
	a.writeJSON(w, http.StatusAccepted, job)
}

func (a *Admin) cacheJobMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrCacheJobAlreadyActive):
		a.apiError(w, http.StatusConflict, "cache_job_active", "Für diese Version läuft bereits ein Cache-Auftrag.")
	case errors.Is(err, store.ErrCacheJobNotFound), errors.Is(err, store.ErrCatalogNotFound), errors.Is(err, sql.ErrNoRows):
		a.apiError(w, http.StatusNotFound, "cache_job_not_found", "Cache-Auftrag oder Katalogversion wurde nicht gefunden.")
	case errors.Is(err, store.ErrCacheJobState):
		a.apiError(w, http.StatusConflict, "cache_job_state", "Der Cache-Auftrag wurde bereits abgeschlossen oder zwischenzeitlich geändert.")
	case strings.Contains(strings.ToLower(err.Error()), "active"):
		a.apiError(w, http.StatusUnprocessableEntity, "cache_target_inactive", "Version und Quelle müssen aktiv sein.")
	default:
		a.apiError(w, http.StatusInternalServerError, "cache_job_failed", "Der Cache-Auftrag konnte nicht sicher geändert werden.")
	}
}
