package webadmin

import (
	"html/template"
	"log"
	"net/http"

	"github.com/containerguy/lan_installer/internal/store"
)

type dashboardData struct {
	User                store.User
	CSRFToken           string
	CanAdminSources     bool
	CanTestSources      bool
	CanEditCatalog      bool
	SourceCount         int
	HealthySourceCount  int
	GameCount           int
	VersionCount        int
	MissingDigestCount  int
	DraftEventCount     int
	ActiveLauncherCount int
	ClientCount         int
}

func (a *Admin) dashboard(w http.ResponseWriter, r *http.Request) {
	session, _, err := a.currentSession(r)
	if err != nil {
		return
	}
	sources, err := a.store.Sources(r.Context())
	if err != nil {
		http.Error(w, "Dashboard konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	games, err := a.store.Games(r.Context())
	if err != nil {
		http.Error(w, "Dashboard konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	launchers, err := a.store.Launchers(r.Context())
	if err != nil {
		http.Error(w, "Dashboard konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	launcherVersions, err := a.store.LauncherVersions(r.Context())
	if err != nil {
		http.Error(w, "Dashboard konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	gameVersions, err := a.store.GameVersions(r.Context())
	if err != nil {
		http.Error(w, "Dashboard konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	events, err := a.store.Events(r.Context())
	if err != nil {
		http.Error(w, "Dashboard konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	clients, err := a.store.DeviceInventories(r.Context())
	if err != nil {
		http.Error(w, "Dashboard konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	data := dashboardData{
		User: session.User, CSRFToken: session.CSRFToken,
		CanAdminSources: hasRole(session.User, "admin"),
		CanTestSources:  hasRole(session.User, "admin", "operator"),
		CanEditCatalog:  hasRole(session.User, "admin", "operator"),
		SourceCount:     len(sources), GameCount: len(games), VersionCount: len(launcherVersions) + len(gameVersions), ClientCount: len(clients),
	}
	for _, source := range sources {
		if source.Enabled && source.LastTestState == "succeeded" {
			data.HealthySourceCount++
		}
	}
	for _, launcher := range launchers {
		if launcher.Enabled {
			data.ActiveLauncherCount++
		}
	}
	for _, version := range launcherVersions {
		if version.Enabled && version.SHA256 == "" {
			data.MissingDigestCount++
		}
	}
	for _, version := range gameVersions {
		if version.Enabled && version.SHA256 == "" {
			data.MissingDigestCount++
		}
	}
	for _, event := range events {
		if event.Status == "draft" {
			data.DraftEventCount++
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := dashboardTemplate.Execute(w, data); err != nil {
		log.Printf("render dashboard: %v", err)
	}
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(dashboardPageHTML))

const dashboardPageHTML = `<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Dashboard · LANReady</title>
<link rel="stylesheet" href="/admin/assets/management.css?v=20260716-5">
</head>
<body>
<div class="shell">
<aside>
  <div class="brand"><span class="brand-mark">L</span><span>LANReady</span></div>
  <nav class="nav" aria-label="Management">
    <a href="/admin/" aria-current="page">Dashboard</a>
    <a href="/admin/sources">Quellen</a>
    <a href="/admin/catalog">Katalog</a>
    <a href="/admin/events">Events</a>
    <a href="/admin/clients">Clients</a>
  </nav>
  <div class="sidebar-user"><div class="user-identity"><span>{{.User.Username}}</span><small>{{range $i,$r := .User.Roles}}{{if $i}}, {{end}}{{$r}}{{end}}</small></div>
    <form class="sidebar-logout" method="post" action="/admin/logout"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="button" type="submit">Abmelden</button></form>
  </div>
</aside>
<main>
  <header class="top">
    <div><p class="eyebrow">Management</p><h1>Guten Überblick.</h1><p>Quellen, Katalog und Event-Entwürfe auf einen Blick.</p></div>
  </header>

  <section class="dashboard-grid" aria-label="Systemübersicht">
    <article class="card stat-card"><span class="stat-label">Quellen</span><strong class="stat-value">{{.HealthySourceCount}} / {{.SourceCount}}</strong><span class="stat-detail">aktiv und erfolgreich geprüft</span></article>
    <article class="card stat-card"><span class="stat-label">Spiele</span><strong class="stat-value">{{.GameCount}}</strong><span class="stat-detail">im Katalog</span></article>
    <article class="card stat-card"><span class="stat-label">Versionen</span><strong class="stat-value">{{.VersionCount}}</strong><span class="stat-detail">{{.MissingDigestCount}} ohne SHA-256</span></article>
    <article class="card stat-card"><span class="stat-label">Event-Entwürfe</span><strong class="stat-value">{{.DraftEventCount}}</strong><span class="stat-detail">noch nicht veröffentlicht</span></article>
  </section>

  <div class="dashboard-layout">
    <section class="card dashboard-card" aria-labelledby="readiness-title">
      <h2 id="readiness-title">Bereit für den nächsten Schritt?</h2>
      <p>Diese Punkte bestimmen, welche Inhalte später veröffentlicht werden können.</p>
      <div class="task-list">
        <div class="task-row"><span class="task-icon">Q</span><div><strong>Quellen erreichbar</strong><div class="subtle">{{.HealthySourceCount}} von {{.SourceCount}} Quellen sind aktiv und erfolgreich getestet.</div></div><a class="button" href="/admin/sources">{{if .CanTestSources}}Prüfen{{else}}Ansehen{{end}}</a></div>
        <div class="task-row"><span class="task-icon">L</span><div><strong>Launcher aktiv</strong><div class="subtle">{{.ActiveLauncherCount}} Launcher stehen für Spiele bereit.</div></div><a class="button" href="/admin/catalog?tab=launchers">Öffnen</a></div>
        <div class="task-row"><span class="task-icon">#</span><div><strong>Prüfsummen vollständig</strong><div class="subtle">{{.MissingDigestCount}} aktive Versionen benötigen noch eine SHA-256-Prüfsumme.</div></div><a class="button" href="/admin/catalog?tab=game-versions">Versionen</a></div>
        <div class="task-row"><span class="task-icon">C</span><div><strong>Clients</strong><div class="subtle">{{.ClientCount}} Gerät(e) sind registriert; Inventar und letzter Kontakt sind in der Clientansicht sichtbar.</div></div><a class="button" href="/admin/clients">Öffnen</a></div>
      </div>
    </section>
    <section class="card dashboard-card" aria-labelledby="quick-title">
      <h2 id="quick-title">Direkt starten</h2>
      <p>Häufig verwendete Verwaltungsabläufe.</p>
      <div class="quick-links">
        <a class="quick-link" href="/admin/sources"><span>{{if .CanAdminSources}}Quelle hinzufügen oder testen{{else if .CanTestSources}}Quellen testen{{else}}Quellen ansehen{{end}}</span><span aria-hidden="true">→</span></a>
        <a class="quick-link" href="/admin/catalog?tab=games"><span>{{if .CanEditCatalog}}Spiele verwalten{{else}}Spiele ansehen{{end}}</span><span aria-hidden="true">→</span></a>
        <a class="quick-link" href="/admin/catalog?tab=launcher-versions"><span>{{if .CanEditCatalog}}Launcher-Version anlegen{{else}}Launcher-Versionen ansehen{{end}}</span><span aria-hidden="true">→</span></a>
        <a class="quick-link" href="/admin/events"><span>{{if .CanEditCatalog}}Event-Entwurf bearbeiten{{else}}Event-Entwürfe ansehen{{end}}</span><span aria-hidden="true">→</span></a>
      </div>
    </section>
  </div>
</main>
</div>
</body>
</html>`
