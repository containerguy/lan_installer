package webadmin

import (
	"html/template"
	"net/http"

	"github.com/containerguy/lan_installer/internal/store"
)

func (a *Admin) sourcesPage(w http.ResponseWriter, r *http.Request) {
	session, _, err := a.currentSession(r)
	if err != nil {
		return
	}
	sources, err := a.store.Sources(r.Context())
	if err != nil {
		http.Error(w, "Quellen konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	data := struct {
		User      store.User
		CSRFToken string
		Sources   []store.Source
		CanAdmin  bool
		CanTest   bool
	}{session.User, session.CSRFToken, sources, hasRole(session.User, "admin"), hasRole(session.User, "admin", "operator")}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err = sourcesTemplate.Execute(w, data); err != nil {
		http.Error(w, "Quellenseite konnte nicht dargestellt werden", http.StatusInternalServerError)
	}
}

var sourcesTemplate = template.Must(template.New("sources").Parse(sourcesPageHTML))

const sourcesPageHTML = `<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="csrf-token" content="{{.CSRFToken}}">
<meta name="can-admin" content="{{.CanAdmin}}">
<meta name="can-test" content="{{.CanTest}}">
<title>Quellen · LANReady</title>
<link rel="stylesheet" href="/admin/assets/management.css?v=20260716-5">
</head>
<body>
<div class="shell">
<aside>
  <div class="brand"><span class="brand-mark">L</span><span>LANReady</span></div>
  <nav class="nav" aria-label="Management">
    <a href="/admin/">Dashboard</a>
    <a href="/admin/sources" aria-current="page">Quellen</a>
    <a href="/admin/catalog">Katalog</a>
    <a href="/admin/events">Events</a>
    <a href="/admin/clients">Clients</a>
  </nav>
  <div class="sidebar-user"><div class="user-identity"><span>{{.User.Username}}</span><small>{{range .User.Roles}}{{.}} {{end}}</small></div><form class="sidebar-logout" method="post" action="/admin/logout"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="button" type="submit">Abmelden</button></form></div>
</aside>
<main>
  <header class="top">
    <div><h1>Externe Quellen</h1><p>HTTPS-, WebDAV- und Nextcloud-Verbindungen sicher verwalten und prüfen.</p></div>
    {{if .CanAdmin}}<button class="button primary" type="button" id="add-source">Quelle hinzufügen</button>{{end}}
  </header>
  <p class="notice" id="page-message" role="status" aria-live="polite" hidden></p>

  <section class="card" aria-labelledby="source-list-title">
    <div class="toolbar">
      <h2 id="source-list-title" class="sr-only">Quellenliste</h2>
      <label class="search"><span class="sr-only">Quellen durchsuchen</span><input class="input" id="search" type="search" placeholder="Quellen durchsuchen"></label>
      <label><span class="sr-only">Typ filtern</span><select class="select compact" id="kind-filter"><option value="">Alle Typen</option><option value="https">HTTPS</option><option value="webdav">WebDAV</option><option value="nextcloud_webdav">Nextcloud WebDAV</option></select></label>
      <label><span class="sr-only">Status filtern</span><select class="select compact" id="enabled-filter"><option value="">Aktiv und deaktiviert</option><option value="true">Aktiv</option><option value="false">Deaktiviert</option></select></label>
      <label><span class="sr-only">Sortieren</span><select class="select compact" id="sort"><option value="name">Name A–Z</option><option value="type">Typ</option><option value="status">Status</option></select></label>
      <span class="subtle" id="result-count">{{len .Sources}} Quelle(n)</span>
    </div>
    {{if .Sources}}
    <div class="table-wrap">
      <table>
        <thead><tr><th>Name</th><th>Typ</th><th>Status</th><th>Zugang</th><th><span class="sr-only">Aktion</span></th></tr></thead>
        <tbody id="source-rows">
        {{range .Sources}}
          <tr data-name="{{.Name}}" data-search="{{.Name}} {{.Kind}} {{.BaseURL}}" data-kind="{{.Kind}}" data-enabled="{{.Enabled}}" data-status="{{if .Enabled}}{{.LastTestState}}{{else}}inactive{{end}}">
            <td data-label="Name"><div class="source-name">{{.Name}}</div><div class="subtle">{{.BaseURL}}</div></td>
            <td data-label="Typ">{{if eq .Kind "nextcloud_webdav"}}Nextcloud WebDAV{{else if eq .Kind "webdav"}}WebDAV{{else}}HTTPS{{end}}</td>
            <td data-label="Status">{{if not .Enabled}}<span class="badge inactive"><span class="dot"></span>Deaktiviert</span>{{else if eq .LastTestState "succeeded"}}<span class="badge ok"><span class="dot"></span>Erreichbar</span>{{else}}<span class="badge warn"><span class="dot"></span>Nicht getestet</span>{{end}}{{if .LastTestedAt}}<div class="subtle">Letzter Test: {{.LastTestedAt}} · {{.LastTestLatencyMS}} ms</div>{{end}}</td>
            <td data-label="Zugang">{{if .AuthConfigured}}verschlüsselt hinterlegt{{else}}keine{{end}}</td>
            <td data-label="Aktion"><button class="button edit-source" type="button" data-id="{{.ID}}" data-name="{{.Name}}">Details</button></td>
          </tr>
        {{end}}
        </tbody>
      </table>
    </div>
    <div class="empty" id="no-results" hidden>Keine Quelle passt zu den gewählten Filtern.</div>
    {{else}}<div class="empty">Noch keine Quelle vorhanden. Lege zuerst eine HTTPS- oder WebDAV-Verbindung an.</div>{{end}}
  </section>

  <section class="card editor" id="editor" hidden aria-labelledby="editor-title" aria-busy="false" tabindex="-1">
    <div class="editor-head">
      <div><h2 id="editor-title">Quelle hinzufügen</h2><p id="editor-help">Vor dem Speichern ist ein erfolgreicher Verbindungstest erforderlich.</p></div>
      <span class="badge" id="editor-mode">Neu</span>
    </div>
    <form id="source-form" novalidate>
      <input type="hidden" id="source-id" value="0">
      <input type="hidden" id="source-revision" value="0">
      <input type="hidden" id="test-token">
      <div class="grid">
        <label class="field"><span>Name</span><input class="input" id="name" maxlength="200" required aria-describedby="name-error"><small class="field-error" id="name-error"></small></label>
        <label class="field"><span>Typ</span><select class="select" id="kind"><option value="https">HTTPS</option><option value="webdav">WebDAV</option><option value="nextcloud_webdav">Nextcloud WebDAV</option></select></label>
        <label class="field wide"><span>HTTPS-Basis-URL</span><input class="input" id="base-url" type="url" maxlength="2048" placeholder="https://cloud.example/remote.php/dav/files/lanready" required aria-describedby="url-error"><small class="field-error" id="url-error"></small></label>
        <label class="field"><span>Authentifizierung</span><select class="select" id="auth-type"><option value="none">Keine</option><option value="basic">Basic / App-Passwort</option></select></label>
        <label class="field"><span>Benutzername</span><input class="input" id="username" autocomplete="username" maxlength="320" aria-describedby="username-error"><small class="field-error" id="username-error"></small></label>
        <label class="field wide"><span>Passwort / App-Passwort</span><input class="input" id="password" type="password" autocomplete="new-password" maxlength="4096" placeholder="Bei Bearbeitung leer lassen, um das Secret zu behalten" aria-describedby="password-error"><small class="field-error" id="password-error"></small></label>
        <label class="check wide"><input id="enabled" type="checkbox" checked> Quelle aktivieren</label>
      </div>
      <p class="notice" id="result" role="status" aria-live="polite">Noch nicht in dieser Sitzung getestet.</p>
      <div class="actions">
        {{if .CanAdmin}}<button class="button danger destructive" id="delete-source" type="button" hidden>Löschen</button>
        <button class="button" id="deactivate-source" type="button" hidden>Deaktivieren</button>{{end}}
        <button class="button" id="retry-source" type="button" hidden>Erneut laden</button>
        <button class="button" id="cancel" type="button">Schließen</button>
        {{if .CanTest}}<button class="button" id="test-source" type="button">Verbindung testen</button>{{end}}
        {{if .CanAdmin}}<button class="button primary" id="save-source" type="submit" disabled>Speichern</button>{{end}}
      </div>
    </form>
  </section>
</main>
</div>
<script src="/admin/assets/sources.js?v=20260716-3" defer></script>
</body>
</html>`
