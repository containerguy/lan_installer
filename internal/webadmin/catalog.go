package webadmin

import (
	"html/template"
	"log"
	"net/http"

	"github.com/containerguy/lan_installer/internal/store"
)

type catalogPageData struct {
	User                           store.User
	CSRFToken                      string
	InitialTab                     string
	CanEdit, CanDelete, CanPublish bool
}

func (a *Admin) catalog(w http.ResponseWriter, r *http.Request) {
	session, _, err := a.currentSession(r)
	if err != nil {
		return
	}
	tab := r.URL.Query().Get("tab")
	if r.URL.Path == "/admin/events" {
		tab = "events"
	}
	switch tab {
	case "games", "launchers", "launcher-versions", "game-versions", "events":
	default:
		tab = "games"
	}
	data := catalogPageData{
		User: session.User, CSRFToken: session.CSRFToken, InitialTab: tab,
		CanEdit: hasRole(session.User, "admin", "operator"), CanDelete: hasRole(session.User, "admin"),
		CanPublish: hasRole(session.User, "admin"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := catalogTemplate.Execute(w, data); err != nil {
		log.Printf("render catalog: %v", err)
	}
}

func hasRole(user store.User, roles ...string) bool {
	for _, have := range user.Roles {
		for _, want := range roles {
			if have == want {
				return true
			}
		}
	}
	return false
}

var catalogTemplate = template.Must(template.New("catalog").Parse(catalogPageHTML))

const catalogPageHTML = `<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="csrf-token" content="{{.CSRFToken}}">
<meta name="initial-tab" content="{{.InitialTab}}">
<meta name="can-edit" content="{{.CanEdit}}">
<meta name="can-delete" content="{{.CanDelete}}">
<meta name="can-publish" content="{{.CanPublish}}">
<title>Katalog · LANReady</title>
<link rel="stylesheet" href="/admin/assets/management.css?v=20260718-4">
</head>
<body>
<div class="shell">
<aside>
  <div class="brand"><span class="brand-mark">L</span><span>LANReady</span></div>
  <nav class="nav" aria-label="Management">
    <a href="/admin/">Dashboard</a>
    <a href="/admin/sources">Quellen</a>
    <a href="/admin/catalog" aria-current="{{if ne .InitialTab "events"}}page{{end}}">Katalog</a>
    <a href="/admin/events" aria-current="{{if eq .InitialTab "events"}}page{{end}}">Events</a>
    <a href="/admin/clients">Clients</a>
  </nav>
  <div class="sidebar-user"><div class="user-identity"><span>{{.User.Username}}</span><small>{{range .User.Roles}}{{.}} {{end}}</small></div><form class="sidebar-logout" method="post" action="/admin/logout"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="button" type="submit">Abmelden</button></form></div>
</aside>
<main>
  <header class="top">
    <div><p class="eyebrow">Management</p><h1 id="page-title">Katalog</h1><p id="page-subtitle">Spiele, Launcher und bereitgestellte Versionen verwalten.</p></div>
    {{if .CanEdit}}<button class="button primary" type="button" id="add-entity" disabled>Eintrag hinzufügen</button>{{end}}
  </header>
  <div class="page-feedback">
    <p class="notice page-notice" id="page-message" role="status" aria-live="polite" tabindex="-1" hidden></p>
    <button class="button" id="retry-catalog" type="button" hidden>Erneut laden</button>
  </div>

  <div class="tabs" role="tablist" aria-label="Katalogbereiche">
    <button class="tab" id="tab-games" type="button" role="tab" aria-controls="catalog-panel" data-tab="games">Spiele</button>
    <button class="tab" id="tab-launchers" type="button" role="tab" aria-controls="catalog-panel" data-tab="launchers">Launcher</button>
    <button class="tab" id="tab-launcher-versions" type="button" role="tab" aria-controls="catalog-panel" data-tab="launcher-versions">Launcher-Versionen</button>
    <button class="tab" id="tab-game-versions" type="button" role="tab" aria-controls="catalog-panel" data-tab="game-versions">Spiel-Versionen</button>
    <button class="tab" id="tab-events" type="button" role="tab" aria-controls="catalog-panel" data-tab="events">Events</button>
  </div>

  <section class="card" id="catalog-panel" role="tabpanel" aria-labelledby="tab-games" aria-busy="true">
    <div class="toolbar">
      <h2 id="list-title" class="sr-only">Einträge</h2>
      <label class="search"><span class="sr-only">Einträge durchsuchen</span><input class="input" id="catalog-search" type="search" placeholder="Durchsuchen"></label>
      <label><span class="sr-only">Status filtern</span><select class="select compact" id="catalog-status"><option value="">Alle Status</option><option value="active">Aktiv</option><option value="inactive">Deaktiviert</option></select></label>
      <label><span class="sr-only">Sortierung</span><select class="select compact" id="catalog-sort"><option value="name">Name A–Z</option><option value="status">Status</option></select></label>
      <span class="subtle result-count" id="catalog-count">Wird geladen …</span>
    </div>
    <div class="loading-state" id="catalog-loading" role="status" aria-live="polite"><span class="spinner" aria-hidden="true"></span><span>Katalog wird geladen …</span></div>
    <div class="table-wrap" id="catalog-table-wrap" hidden>
      <table id="catalog-table">
        <thead><tr id="catalog-head"></tr></thead>
        <tbody id="catalog-rows"></tbody>
      </table>
    </div>
    <div class="empty" id="catalog-empty" hidden><strong id="catalog-empty-title">Noch keine Einträge</strong><span id="catalog-empty-text">Lege den ersten Eintrag an.</span></div>
  </section>

  <section class="card assignment cache-management" id="cache-management" hidden aria-labelledby="cache-management-title" aria-busy="false">
    <div class="section-heading"><div><p class="eyebrow">Speicher</p><h2 id="cache-management-title">Cache verwalten</h2><p>Zeigt den registrierten CAS-Verbrauch. Referenzierte Katalog- und Release-Artefakte werden bei der Bereinigung niemals entfernt.</p></div></div>
    <div class="cache-overview">
      <div><strong id="cache-usage">Cacheverbrauch wird geladen …</strong><p class="subtle" id="cache-usage-detail">Temporäre oder verwaiste Dateien außerhalb der Metadaten sind in diesem Wert nicht enthalten.</p></div>
      <progress id="cache-usage-progress" max="100" value="0" aria-label="Cachebelegung in Prozent">0 %</progress>
    </div>
    {{if .CanDelete}}<div class="inline-form cache-gc-controls">
      <label class="field"><span>Nur unreferenzierte Artefakte älter als</span><select class="select" id="cache-gc-age"><option value="168" selected>7 Tage</option><option value="720">30 Tage</option><option value="24">24 Stunden</option></select></label>
      <button class="button danger" id="run-cache-gc" type="button">Unreferenzierten Cache bereinigen</button>
    </div>{{else}}<p class="notice">Nur Administratoren dürfen unreferenzierte Cacheartefakte entfernen.</p>{{end}}
    <p class="notice" id="cache-gc-result" role="status" aria-live="polite"></p>
  </section>

  <section class="card editor" id="catalog-editor" hidden aria-labelledby="editor-title" aria-busy="false" tabindex="-1">
    <div class="editor-head">
      <div><h2 id="editor-title">Eintrag hinzufügen</h2><p id="editor-help">Pflichtfelder sind gekennzeichnet.</p></div>
      <span class="badge" id="editor-mode">Neu</span>
    </div>
    <form id="catalog-form" novalidate>
      <input type="hidden" id="entity-id" value="0">
      <div class="grid">
        <label class="field" data-kinds="launcher game event"><span>Name <span class="required-mark" aria-hidden="true">*</span></span><input class="input" id="entity-name" maxlength="200" autocomplete="off" aria-required="true" aria-describedby="entity-name-error"><small class="field-error" id="entity-name-error"></small></label>
        <label class="field" data-kinds="launcher game event"><span>Slug <span class="required-mark" aria-hidden="true">*</span></span><input class="input" id="entity-slug" maxlength="100" pattern="[a-z0-9]+(?:-[a-z0-9]+)*" autocomplete="off" aria-required="true" aria-describedby="entity-slug-error"><small class="field-error" id="entity-slug-error"></small></label>

        <label class="field" data-kinds="launcher"><span>Adapter</span><select class="select" id="entity-adapter"><option value="steam">Steam</option><option value="ea_app">EA App</option><option value="ubisoft_connect">Ubisoft Connect</option></select></label>

        <label class="field" data-kinds="game"><span>Launcher <span class="required-mark" aria-hidden="true">*</span></span><select class="select" id="entity-launcher" aria-required="true" aria-describedby="entity-launcher-error"></select><small class="field-error" id="entity-launcher-error"></small></label>
        <label class="field" data-kinds="game"><span>Externe Spiel-ID</span><input class="input" id="entity-external-id" maxlength="256" autocomplete="off"><small class="field-hint">Zum Beispiel Steam App-ID 730.</small></label>

        <label class="field" data-kinds="launcher-version"><span>Launcher <span class="required-mark" aria-hidden="true">*</span></span><select class="select" id="version-launcher" aria-required="true" aria-describedby="version-launcher-error"></select><small class="field-error" id="version-launcher-error"></small></label>
        <label class="field" data-kinds="game-version"><span>Spiel <span class="required-mark" aria-hidden="true">*</span></span><select class="select" id="version-game" aria-required="true" aria-describedby="version-game-error"></select><small class="field-error" id="version-game-error"></small></label>
        <label class="field" data-kinds="launcher-version game-version"><span>Version <span class="required-mark" aria-hidden="true">*</span></span><input class="input" id="entity-version" maxlength="256" autocomplete="off" aria-required="true" aria-describedby="entity-version-error"><small class="field-error" id="entity-version-error"></small></label>
        <label class="field" data-kinds="launcher-version game-version"><span>Quelle <span class="required-mark" aria-hidden="true">*</span></span><select class="select" id="entity-source" aria-required="true" aria-describedby="entity-source-error"></select><small class="field-error" id="entity-source-error"></small></label>
        <label class="field wide" data-kinds="launcher-version game-version"><span>Relativer Quellpfad <span class="required-mark" aria-hidden="true">*</span></span><input class="input" id="entity-path" maxlength="1024" placeholder="spiele/cs2/package.zip" autocomplete="off" aria-required="true" aria-describedby="entity-path-error"><small class="field-error" id="entity-path-error"></small></label>
        <label class="field wide" data-kinds="launcher-version game-version"><span>SHA-256</span><input class="input mono" id="entity-sha" maxlength="64" pattern="[0-9a-fA-F]{64}" autocomplete="off"><small class="field-hint">Darf bis zum Import leer bleiben; ein Event kann so noch nicht veröffentlicht werden.</small><small class="field-error" id="entity-sha-error"></small></label>
        <label class="field" data-kinds="launcher-version game-version"><span>Größe in Bytes</span><input class="input" id="entity-size" type="number" min="0" step="1" value="0"><small class="field-error" id="entity-size-error"></small></label>
        <label class="field" data-kinds="launcher-version"><span>Silent-Argumente</span><input class="input mono" id="entity-silent-args" readonly aria-readonly="true" placeholder="Noch nicht verifiziert"><small class="field-hint">Nur Anzeige. LANReady übernimmt Argumente ausschließlich aus einer späteren Windows-Verifikation.</small></label>

        <label class="field" data-kinds="event"><span>Beginn</span><input class="input" id="event-start" type="datetime-local"></label>
        <label class="field" data-kinds="event"><span>Ende</span><input class="input" id="event-end" type="datetime-local"><small class="field-error" id="event-end-error"></small></label>
        <label class="field" data-kinds="event"><span>Status</span><select class="select" id="event-status"><option value="draft">Entwurf</option><option value="published" disabled>Veröffentlicht</option><option value="archived">Archiviert</option></select><small class="field-hint">Veröffentlichen erfolgt unten über ein geprüftes, signiertes Release.</small></label>

        <label class="check wide" data-kinds="launcher game launcher-version game-version"><input id="entity-enabled" type="checkbox" checked> Eintrag aktivieren</label>
      </div>
      <p class="notice" id="editor-message" role="status" aria-live="polite"></p>
      <div class="actions">
        {{if .CanDelete}}<button class="button danger destructive" id="delete-entity" type="button" hidden>Löschen</button>{{end}}
        {{if .CanEdit}}<button class="button destructive" id="deactivate-entity" type="button" hidden>Deaktivieren</button>{{end}}
        <button class="button" id="cancel-editor" type="button">Schließen</button>
        {{if .CanEdit}}<button class="button primary" id="save-entity" type="submit">Speichern</button>{{end}}
      </div>
    </form>
  </section>

  <section class="card assignment" id="event-assignment" hidden aria-labelledby="assignment-title">
    <div class="section-heading"><div><h2 id="assignment-title">Spielversionen im Event</h2><p>Nur aktive Versionen können einem Entwurf zugeordnet werden.</p></div></div>
    {{if .CanEdit}}<form id="assignment-form" class="inline-form">
      <label class="field"><span>Event</span><select class="select" id="assignment-event"></select></label>
      <label class="field"><span>Spielversion</span><select class="select" id="assignment-version"></select></label>
      <label class="check"><input id="assignment-required" type="checkbox" checked> erforderlich</label>
      <button class="button primary" type="submit">Zuordnen</button>
    </form>{{end}}
    <div id="assignment-list" class="assignment-list"></div>
  </section>

  <section class="card assignment" id="event-release" hidden aria-labelledby="release-title" aria-busy="false">
    <div class="section-heading"><div><p class="eyebrow">Vertrauensgrenze</p><h2 id="release-title">Signiertes Event-Release veröffentlichen</h2><p>Der private Signaturschlüssel wird ausschließlich im netzwerklosen Signer verwendet. Der Managementserver erhält nur das fertige Envelope und prüft Signatur, Schema, Sequenz und jedes Artefakt erneut.</p></div></div>
    <p class="notice" id="release-current" role="status">Aktiver Clientstand wird geladen …</p>
    <div class="release-history" id="release-history" aria-live="polite"></div>
    {{if .CanPublish}}<form id="release-form" class="release-form" novalidate>
      <label class="field wide"><span>Signiertes Envelope <span class="required-mark" aria-hidden="true">*</span></span><input class="input" id="release-file" type="file" accept="application/json,.json" required aria-describedby="release-file-hint release-error"><small class="field-hint" id="release-file-hint">Wähle ausschließlich eine mit <code>lanready-release sign-event</code> erzeugte JSON-Datei. Niemals einen Private Key hochladen.</small></label>
      <div class="release-preview" id="release-preview" hidden aria-live="polite">
        <strong id="release-preview-title">Release-Vorschau</strong>
        <dl><div><dt>Event</dt><dd id="release-event-id">—</dd></div><div><dt>Release-ID</dt><dd id="release-id" class="mono">—</dd></div><div><dt>Sequenz</dt><dd id="release-sequence">—</dd></div><div><dt>Ausgestellt</dt><dd id="release-issued">—</dd></div><div><dt>Gültig bis</dt><dd id="release-valid-until">—</dd></div><div><dt>Mindestclient</dt><dd id="release-minimum-client">—</dd></div><div><dt>Launcher</dt><dd id="release-launchers">—</dd></div><div><dt>Spiele</dt><dd id="release-games">—</dd></div><div><dt>Artefakte</dt><dd id="release-artifacts">—</dd></div><div><dt>Key-ID</dt><dd id="release-key-id" class="mono">—</dd></div></dl>
        <p class="subtle">Diese Vorschau ist noch keine Vertrauensentscheidung. Die verbindliche Prüfung erfolgt serverseitig vor der atomaren Veröffentlichung.</p>
      </div>
      <label class="check"><input id="release-activate" type="checkbox" aria-describedby="release-activation-impact"> Nach erfolgreicher Veröffentlichung als aktives Event an Clients ausliefern</label>
      <p class="notice" id="release-activation-impact">Ohne Aktivierung wird die Sequenz gespeichert, aber nicht an Clients ausgeliefert.</p>
      <label class="field wide"><span>Rollback-Kandidat gültig bis</span><input class="input" id="rollback-valid-until" type="datetime-local"><small class="field-hint">Wird nur für „Rollback vorbereiten“ verwendet. Du entscheidest das neue Gültigkeitsende ausdrücklich; danach muss der Kandidat offline signiert und wieder hochgeladen werden.</small></label>
      <p class="field-hint">Rollback-Schutz: Frühere Inhalte dürfen nie direkt reaktiviert werden. LANReady erstellt daraus eine neue höhere Sequenz; erst deine neue Offline-Signatur macht sie veröffentlichbar.</p>
      <p class="notice" id="release-error" role="alert" aria-live="assertive"></p>
      <p class="notice" id="release-progress" role="status" aria-live="polite"></p>
      <div class="actions"><button class="button primary" id="publish-release" type="submit" disabled>Prüfen und veröffentlichen</button></div>
    </form>{{else}}<p class="notice">Nur Benutzer mit der Rolle Admin dürfen signierte Releases veröffentlichen.</p>{{end}}
  </section>
  <section class="card assignment" id="client-update-release" hidden aria-labelledby="client-update-title" aria-busy="false">
    <div class="section-heading"><div><p class="eyebrow">Windows-Update</p><h2 id="client-update-title">Signiertes Clientupdate veröffentlichen</h2><p>Der Stable-Kanal ist monoton. Clients prüfen Envelope, Sequenz, Version, Digest und Größe vor einer späteren atomaren Installation.</p></div></div>
    <p class="notice" id="client-update-current" role="status">Clientupdate-Stand wird geladen …</p>
    {{if .CanPublish}}<form id="client-update-form" class="release-form" novalidate>
      <label class="field wide"><span>Signiertes Update-Envelope <span class="required-mark" aria-hidden="true">*</span></span><input class="input" id="client-update-file" type="file" accept="application/json,.json" required><small class="field-hint">Wähle eine mit <code>lanready-release sign-update</code> erzeugte JSON-Datei. Der Private Key bleibt offline.</small></label>
      <label class="field wide"><span>Fertig Authenticode-signierte portable EXE</span><input class="input" id="client-update-artifact-file" type="file" accept="application/vnd.microsoft.portable-executable,.exe"><small class="field-hint">Der Server streamt die Datei direkt in den CAS und akzeptiert sie nur bei exakt passender signierter Größe und SHA-256. Bereits vorhandene identische Artefakte werden automatisch erkannt.</small></label>
      <div class="actions"><button class="button secondary" id="upload-client-update-artifact" type="button" disabled>EXE sicher in den Cache laden</button></div><p class="notice" id="client-update-artifact-status" role="status" aria-live="polite"></p>
      <div class="release-preview" id="client-update-preview" hidden aria-live="polite"><strong>Update-Vorschau</strong><dl><div><dt>Kanal</dt><dd id="client-update-channel">—</dd></div><div><dt>Version</dt><dd id="client-update-version">—</dd></div><div><dt>Sequenz</dt><dd id="client-update-sequence">—</dd></div><div><dt>Mindeststand</dt><dd id="client-update-minimum">—</dd></div><div><dt>Artefakt / Protokoll</dt><dd id="client-update-kind">—</dd></div><div><dt>Größe</dt><dd id="client-update-size">—</dd></div><div><dt>SHA-256</dt><dd id="client-update-digest" class="mono">—</dd></div><div><dt>Authenticode-Zertifikat</dt><dd id="client-update-publisher" class="mono">—</dd></div><div><dt>Veröffentlicht</dt><dd id="client-update-published">—</dd></div><div><dt>Key-ID</dt><dd id="client-update-key-id" class="mono">—</dd></div></dl><p class="subtle">Der Server akzeptiert nur den konfigurierten Authenticode-Zertifikatsfingerprint, <code>portable_exe</code>, Updater-Protokoll 1, gültige Ed25519-Signatur und ein passendes CAS-Artefakt.</p></div>
      <p class="notice" id="client-update-error" role="alert" aria-live="assertive"></p><p class="notice" id="client-update-progress" role="status" aria-live="polite"></p>
      <div class="actions"><button class="button primary" id="publish-client-update" type="submit" disabled>Clientupdate prüfen und veröffentlichen</button></div>
    </form>{{else}}<p class="notice">Nur Benutzer mit der Rolle Admin dürfen signierte Clientupdates veröffentlichen.</p>{{end}}
  </section>
</main>
</div>
<script src="/admin/assets/catalog-cache-helpers.js?v=20260718-1" defer></script>
<script src="/admin/assets/catalog.js?v=20260720-1" defer></script>
</body>
</html>`
