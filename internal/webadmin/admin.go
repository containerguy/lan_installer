package webadmin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/auth"
	lanrelease "github.com/containerguy/lan_installer/internal/release"
	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/sourceprobe"
	"github.com/containerguy/lan_installer/internal/store"
)

const (
	sessionCookieName   = "lanready_session"
	loginCSRFCookieName = "lanready_login_csrf"
)

type Admin struct {
	store         *store.Store
	secureCookies bool
	vault         *secretbox.Box
	mux           *http.ServeMux
	sourceTester  SourceTester
	sourceTests   *sourceTestTokens
	releases      *lanrelease.Service
	artifacts     ArtifactStore
}
type pageData struct {
	Title     string
	User      *store.User
	CSRFToken string
	LoginCSRF string
	Error     string
	Next      string
}

func New(st *store.Store, secureCookies bool, vault *secretbox.Box, options ...Option) *Admin {
	a := &Admin{store: st, secureCookies: secureCookies, vault: vault, mux: http.NewServeMux(), sourceTester: sourceprobe.New(sourceprobe.Policy{}), sourceTests: newSourceTestTokens()}
	for _, option := range options {
		option(a)
	}
	a.mux.HandleFunc("GET /admin/login", a.loginPage)
	a.mux.HandleFunc("GET /admin/assets/management.css", managementCSSAsset)
	a.mux.HandleFunc("GET /admin/assets/sources.js", sourcesJSAsset)
	a.mux.HandleFunc("GET /admin/assets/catalog.js", catalogJSAsset)
	a.mux.HandleFunc("GET /admin/assets/catalog-cache-helpers.js", catalogCacheHelpersJSAsset)
	a.mux.HandleFunc("POST /admin/login", a.login)
	a.mux.HandleFunc("POST /admin/logout", a.requireLogin(a.logout))
	a.mux.HandleFunc("GET /admin/", a.requireLogin(a.dashboard))
	a.mux.HandleFunc("GET /admin/catalog", a.requireLogin(a.catalog))
	a.mux.HandleFunc("GET /admin/events", a.requireLogin(a.catalog))
	a.mux.HandleFunc("GET /admin/sources", a.requireLogin(a.sourcesPage))
	a.mux.HandleFunc("GET /admin/clients", a.requireLogin(a.clientsPage))
	a.mux.HandleFunc("POST /admin/clients/enrollment-code", a.requireLogin(a.createEnrollmentCode))
	a.mux.HandleFunc("POST /admin/clients/catalog-import", a.requireLogin(a.importClientInventoryItem))
	a.mux.HandleFunc("POST /admin/api/v1/enrollment-codes", a.apiRoute(a.idempotentPost("POST /admin/api/v1/enrollment-codes", a.createEnrollmentCodeAPI)))
	a.mux.HandleFunc("DELETE /admin/api/v1/enrollment-codes/{id}", a.apiRoute(a.revokeEnrollmentCodeAPI))
	a.mux.HandleFunc("GET /admin/device", a.requireLogin(a.deviceAuthorizationPage))
	a.mux.HandleFunc("POST /admin/device", a.requireLogin(a.deviceAuthorizationDecision))
	a.mux.HandleFunc("GET /admin/api/v1/sources", a.apiRoute(a.listSourcesAPI))
	a.mux.HandleFunc("GET /admin/api/v1/catalog", a.apiRoute(a.catalogSnapshotAPI))
	a.mux.HandleFunc("POST /admin/api/v1/catalog/{kind}", a.apiRoute(a.idempotentPost("POST /admin/api/v1/catalog/{kind}", a.saveCatalogAPI)))
	a.mux.HandleFunc("PUT /admin/api/v1/catalog/{kind}/{id}", a.apiRoute(a.saveCatalogAPI))
	a.mux.HandleFunc("POST /admin/api/v1/catalog/{kind}/{id}/deactivate", a.apiRoute(a.idempotentPost("POST /admin/api/v1/catalog/{kind}/{id}/deactivate", a.deactivateCatalogAPI)))
	a.mux.HandleFunc("DELETE /admin/api/v1/catalog/{kind}/{id}", a.apiRoute(a.deleteCatalogAPI))
	a.mux.HandleFunc("POST /admin/api/v1/event-games", a.apiRoute(a.idempotentPost("POST /admin/api/v1/event-games", a.saveEventGameAPI)))
	a.mux.HandleFunc("DELETE /admin/api/v1/event-games/{eventID}/{gameVersionID}", a.apiRoute(a.deleteEventGameAPI))
	a.mux.HandleFunc("GET /admin/api/v1/sources/{id}", a.apiRoute(a.getSourceAPI))
	a.mux.HandleFunc("POST /admin/api/v1/sources/test", a.apiRoute(a.testSourceAPI))
	a.mux.HandleFunc("POST /admin/api/v1/sources", a.apiRoute(a.idempotentPost("POST /admin/api/v1/sources", a.createSourceAPI)))
	a.mux.HandleFunc("PUT /admin/api/v1/sources/{id}", a.apiRoute(a.updateSourceAPI))
	a.mux.HandleFunc("POST /admin/api/v1/sources/{id}/deactivate", a.apiRoute(a.idempotentPost("POST /admin/api/v1/sources/{id}/deactivate", a.deactivateSourceAPI)))
	a.mux.HandleFunc("DELETE /admin/api/v1/sources/{id}", a.apiRoute(a.deleteSourceAPI))
	a.mux.HandleFunc("POST /admin/api/v1/releases/events", a.apiRoute(a.idempotentPost("POST /admin/api/v1/releases/events", a.publishEventReleaseAPI)))
	a.mux.HandleFunc("POST /admin/api/v1/releases/events/{eventID}/{sequence}/activate", a.apiRoute(a.idempotentPost("POST /admin/api/v1/releases/events/{eventID}/{sequence}/activate", a.activateEventReleaseAPI)))
	a.mux.HandleFunc("POST /admin/api/v1/releases/events/{eventID}/{sequence}/rollback-candidate", a.apiRoute(a.buildRollbackCandidateAPI))
	a.mux.HandleFunc("POST /admin/api/v1/releases/client-updates", a.apiRoute(a.idempotentPost("POST /admin/api/v1/releases/client-updates", a.publishClientUpdateReleaseAPI)))
	a.mux.HandleFunc("GET /admin/api/v1/artifacts/client-update/{digest}", a.apiRoute(a.clientUpdateArtifactStatusAPI))
	a.mux.HandleFunc("POST /admin/api/v1/artifacts/client-update/{digest}", a.apiRoute(a.uploadClientUpdateArtifactAPI))
	a.mux.HandleFunc("GET /admin/api/v1/cache-jobs", a.apiRoute(a.listCacheJobsAPI))
	a.mux.HandleFunc("POST /admin/api/v1/cache-jobs", a.apiRoute(a.idempotentPost("POST /admin/api/v1/cache-jobs", a.enqueueCacheJobAPI)))
	a.mux.HandleFunc("POST /admin/api/v1/cache-jobs/{id}/cancel", a.apiRoute(a.idempotentPost("POST /admin/api/v1/cache-jobs/{id}/cancel", a.cancelCacheJobAPI)))
	a.mux.HandleFunc("POST /admin/api/v1/cache-jobs/{id}/retry", a.apiRoute(a.idempotentPost("POST /admin/api/v1/cache-jobs/{id}/retry", a.retryCacheJobAPI)))
	a.mux.HandleFunc("GET /admin/api/v1/cache-status", a.apiRoute(a.cacheStatusAPI))
	a.mux.HandleFunc("POST /admin/api/v1/cache-gc", a.apiRoute(a.idempotentPost("POST /admin/api/v1/cache-gc", a.garbageCollectCacheAPI)))
	a.mux.HandleFunc("GET /admin/api/v1/releases/status", a.apiRoute(a.releaseStatusAPI))
	return a
}
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.mux.ServeHTTP(w, r) }

func (a *Admin) loginPage(w http.ResponseWriter, r *http.Request) {
	next := safeAdminNext(r.URL.Query().Get("next"))
	if _, _, err := a.currentSession(r); err == nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	a.renderLogin(w, http.StatusOK, "", next)
}
func (a *Admin) renderLogin(w http.ResponseWriter, status int, message, next string) {
	token, err := randomToken()
	if err != nil {
		http.Error(w, "Anmeldeseite konnte nicht erstellt werden", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: loginCSRFCookieName, Value: token, Path: "/admin/login", HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 600})
	render(w, status, loginTemplate, pageData{Title: "Anmelden", LoginCSRF: token, Error: message, Next: safeAdminNext(next)})
}
func (a *Admin) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	csrfCookie, err := r.Cookie(loginCSRFCookieName)
	if err != nil || !constantEqual(csrfCookie.Value, r.FormValue("csrf_token")) {
		http.Error(w, "Ungültiges CSRF-Token", http.StatusForbidden)
		return
	}
	u, err := a.store.UserByUsername(r.Context(), r.FormValue("username"))
	if err != nil || !u.Active || !auth.VerifyPassword(u.PasswordHash, r.FormValue("password")) {
		_ = a.store.Audit(r.Context(), nil, "login_failed", "session", "", r.FormValue("username"), r.RemoteAddr)
		time.Sleep(150 * time.Millisecond)
		a.renderLogin(w, http.StatusUnauthorized, "Benutzername oder Passwort ist falsch.", r.FormValue("next"))
		return
	}
	token, _, err := a.store.CreateSession(r.Context(), u.ID, 12*time.Hour)
	if err != nil {
		http.Error(w, "Sitzung konnte nicht erstellt werden", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/admin", HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: int((12 * time.Hour).Seconds())})
	http.SetCookie(w, &http.Cookie{Name: loginCSRFCookieName, Value: "", Path: "/admin/login", HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	_ = a.store.Audit(r.Context(), &u.ID, "login", "session", "", "", r.RemoteAddr)
	http.Redirect(w, r, safeAdminNext(r.FormValue("next")), http.StatusSeeOther)
}
func (a *Admin) logout(w http.ResponseWriter, r *http.Request) {
	s, token, ok := a.validRequest(w, r)
	if !ok {
		return
	}
	_ = a.store.DeleteSession(r.Context(), token)
	_ = a.store.Audit(r.Context(), &s.User.ID, "logout", "session", "", "", r.RemoteAddr)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/admin", HttpOnly: true, Secure: a.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
func (a *Admin) apiRoute(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID, err := randomToken()
		if err != nil {
			http.Error(w, "Request-ID konnte nicht erzeugt werden", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("LANReady-API-Version", "2")
		if _, _, err = a.currentSession(r); err != nil {
			a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
			return
		}
		next(w, r)
	}
}

func (a *Admin) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := a.currentSession(r); err != nil {
			target := "/admin/login"
			if r.Method == http.MethodGet {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func safeAdminNext(value string) string {
	if value == "" {
		return "/admin/"
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/admin/") || strings.Contains(value, "\\") {
		return "/admin/"
	}
	return parsed.RequestURI()
}
func (a *Admin) currentSession(r *http.Request) (store.Session, string, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return store.Session{}, "", err
	}
	s, err := a.store.Session(r.Context(), c.Value)
	return s, c.Value, err
}
func (a *Admin) validRequest(w http.ResponseWriter, r *http.Request) (store.Session, string, bool) {
	s, t, err := a.currentSession(r)
	if err != nil {
		return s, t, false
	}
	if !constantEqual(s.CSRFToken, r.FormValue("csrf_token")) {
		http.Error(w, "Ungültiges CSRF-Token", http.StatusForbidden)
		return s, t, false
	}
	return s, t, true
}
func constantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func render(w http.ResponseWriter, status int, body string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := template.Must(template.New("page").Parse(layoutStart+body+layoutEnd)).Execute(w, data); err != nil {
		log.Printf("render admin page: %v", err)
	}
}

const layoutStart = `<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}} · LANReady</title><link rel="stylesheet" href="/admin/assets/management.css?v=20260716-5"></head><body class="auth-body"><main class="auth-shell"><div class="auth-brand"><span class="brand-mark">L</span><span>LANReady</span></div>`
const layoutEnd = `</main></body></html>`
const loginTemplate = `<section class="card auth-card"><p class="eyebrow">Management</p><h1>Anmelden</h1><p>Verwalte Quellen, Spiele, Launcher, Events und Clients.</p>{{if .Error}}<div class="auth-error" role="alert">{{.Error}}</div>{{end}}<form method="post" action="/admin/login"><input type="hidden" name="csrf_token" value="{{.LoginCSRF}}"><input type="hidden" name="next" value="{{.Next}}"><label class="field"><span>Benutzername</span><input class="input" name="username" autocomplete="username" required autofocus></label><label class="field"><span>Passwort</span><input class="input" type="password" name="password" autocomplete="current-password" required></label><button class="button primary" type="submit">Anmelden</button></form></section>`
