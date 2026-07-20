package webadmin

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/containerguy/lan_installer/internal/store"
)

type clientInventoryView struct {
	ID, Name, WindowsVersion, ClientVersion, Status string
	ScanID                                          string
	EnrolledAt, LastSeenAt, ScannedAt               string
	Installations                                   []clientInstallationView
	HasBulkEligible                                 bool
}

type clientInstallationView struct {
	store.InventoryInstallation
	CompatibleGames                 []store.Game
	SuggestedSlug, SuggestedVersion string
	LauncherName                    string
	SelectedGameID                  int64
	ImportOpen, BulkEligible        bool
	BulkSelected                    bool
	BulkAction                      string
}

type clientsPageData struct {
	User                                          store.User
	CSRFToken, EnrollmentCode, ExpiresAt, Message string
	CatalogTab                                    string
	CanEnroll, CanImport, MessageIsError          bool
	Clients                                       []clientInventoryView
}

func displayTime(value time.Time) string {
	if value.IsZero() {
		return "noch nie"
	}
	return value.Local().Format("02.01.2006 15:04")
}

func (a *Admin) clientsPage(w http.ResponseWriter, r *http.Request) {
	session, _, err := a.currentSession(r)
	if err != nil {
		return
	}
	a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusOK, "", false)
}

func (a *Admin) createEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	session, _, ok := a.validRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin") {
		_ = a.store.Audit(r.Context(), &session.User.ID, "create_enrollment_code_denied", "enrollment_code", "", "", r.RemoteAddr)
		http.Error(w, "Nur Administratoren dürfen Enrollment-Codes erzeugen", http.StatusForbidden)
		return
	}
	code, err := a.store.CreateEnrollmentCode(r.Context(), session.User.ID, 10*time.Minute)
	if err != nil {
		http.Error(w, "Enrollment-Code konnte nicht erzeugt werden", http.StatusInternalServerError)
		return
	}
	_ = a.store.Audit(r.Context(), &session.User.ID, "create_enrollment_code", "enrollment_code", code.ID, "", r.RemoteAddr)
	a.renderClientsPage(w, r, session, code, http.StatusOK, "", false)
}

func (a *Admin) importClientInventoryItem(w http.ResponseWriter, r *http.Request) {
	session, _, ok := a.validRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin") {
		_ = a.store.Audit(r.Context(), &session.User.ID, "import_inventory_item_denied", "device_inventory", "", "", r.RemoteAddr)
		http.Error(w, "Nur Administratoren dürfen Inventarfunde in den Katalog übernehmen", http.StatusForbidden)
		return
	}
	position, err := strconv.Atoi(r.FormValue("position"))
	if err != nil {
		a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusBadRequest, "Der ausgewählte Inventareintrag ist ungültig.", true)
		return
	}
	request := store.InventoryCatalogImport{
		DeviceID: r.FormValue("device_id"), ScanID: r.FormValue("scan_id"), Position: position,
		Slug: r.FormValue("slug"), Name: r.FormValue("name"), Version: r.FormValue("version"),
	}
	action := "import_inventory_item"
	switch r.FormValue("mode") {
	case "create":
	case "version":
		request.CreateVersion = true
		action = "import_inventory_version"
	case "link":
		request.GameID, err = strconv.ParseInt(r.FormValue("game_id"), 10, 64)
		if err != nil || request.GameID < 1 {
			a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusBadRequest, "Bitte wähle einen passenden Katalogeintrag aus.", true)
			return
		}
		action = "link_inventory_item"
	default:
		a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusBadRequest, "Die gewünschte Importaktion ist ungültig.", true)
		return
	}
	result, err := a.store.ImportInventoryItem(r.Context(), request, &store.AuditEntry{ActorUserID: session.User.ID, Action: action, RemoteAddr: r.RemoteAddr})
	if err != nil {
		status := http.StatusBadRequest
		message := "Der Inventarfund konnte nicht übernommen werden. Prüfe Auswahl, Name, Slug und Version."
		if errors.Is(err, store.ErrInventoryCatalogConflict) {
			status = http.StatusConflict
			message = "Dieser Fund wurde inzwischen einem anderen Katalogeintrag zugeordnet."
		} else if errors.Is(err, store.ErrCatalogNotFound) {
			status = http.StatusConflict
			message = "Der Inventarscan oder Katalogeintrag ist nicht mehr aktuell. Lade die Seite neu."
		}
		a.renderClientsPage(w, r, session, store.EnrollmentCode{}, status, message, true)
		return
	}
	state := "linked"
	if result.VersionCreated && !result.Created {
		state = "version-created"
	} else if result.Created && result.VersionCreated {
		state = "created"
	} else if result.Created {
		state = "game-created"
	}
	http.Redirect(w, r, "/admin/clients?import="+state, http.StatusSeeOther)
}

func (a *Admin) importClientInventoryBatch(w http.ResponseWriter, r *http.Request) {
	session, _, ok := a.validRequest(w, r)
	if !ok {
		return
	}
	if !hasRole(session.User, "admin") {
		_ = a.store.Audit(r.Context(), &session.User.ID, "bulk_import_inventory_denied", "device_inventory", "", "", r.RemoteAddr)
		http.Error(w, "Nur Administratoren dürfen Inventarfunde in den Katalog übernehmen", http.StatusForbidden)
		return
	}
	deviceID := strings.TrimSpace(r.FormValue("device_id"))
	scanID := strings.TrimSpace(r.FormValue("scan_id"))
	positionValues := r.Form["position"]
	if deviceID == "" || scanID == "" || len(positionValues) == 0 || len(positionValues) > store.MaxInventoryCatalogBatch {
		a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusBadRequest, "Wähle zwischen 1 und 100 offenen Katalogübernahmen eines Clients aus.", true)
		return
	}
	selected := make(map[int]struct{}, len(positionValues))
	for _, raw := range positionValues {
		position, err := strconv.Atoi(raw)
		if err != nil || position < 0 || position > 9999 {
			a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusBadRequest, "Mindestens eine ausgewählte Installation ist ungültig.", true)
			return
		}
		if _, duplicate := selected[position]; duplicate {
			a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusBadRequest, "Eine Installation wurde mehrfach ausgewählt.", true)
			return
		}
		selected[position] = struct{}{}
	}
	inventories, err := a.store.DeviceInventories(r.Context())
	if err != nil {
		http.Error(w, "Clientinventar konnte nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	games, err := a.store.Games(r.Context())
	if err != nil {
		http.Error(w, "Katalog konnte nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	usedSlugs := make(map[string]struct{}, len(games)+len(selected))
	for _, game := range games {
		usedSlugs[game.Slug] = struct{}{}
	}
	requests := make([]store.InventoryCatalogImport, 0, len(selected))
	foundCurrentScan := false
	for _, inventory := range inventories {
		if inventory.ID != deviceID || inventory.ScanID != scanID {
			continue
		}
		foundCurrentScan = true
		for _, installation := range inventory.Installations {
			if _, wanted := selected[installation.Position]; !wanted {
				continue
			}
			request := store.InventoryCatalogImport{DeviceID: deviceID, ScanID: scanID, Position: installation.Position}
			if installation.CatalogGameID > 0 {
				if installation.CatalogVersionID > 0 || installation.DetectedVersion == nil || strings.TrimSpace(*installation.DetectedVersion) == "" {
					a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusConflict, "Mindestens eine Auswahl ist nicht mehr offen. Es wurde nichts übernommen.", true)
					return
				}
				request.CreateVersion = true
			} else if installation.Launcher == "standalone" {
				for _, game := range games {
					if game.LauncherAdapter == "standalone" && game.ExternalGameID == installation.ExternalGameID {
						request.GameID = game.ID
						break
					}
				}
				if request.GameID == 0 {
					a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusConflict, "Ein manuell registriertes Spiel fehlt im Katalog. Die gesamte Sammelübernahme wurde abgebrochen.", true)
					return
				}
			} else {
				request.Name = installation.DisplayName
				request.Slug = uniqueBulkCatalogSlug(suggestCatalogSlug(installation.DisplayName, installation.Launcher, installation.ExternalGameID), installation.Position, usedSlugs)
				if installation.DetectedVersion != nil {
					request.Version = *installation.DetectedVersion
				}
			}
			requests = append(requests, request)
			delete(selected, installation.Position)
		}
		break
	}
	if !foundCurrentScan || len(selected) != 0 || len(requests) != len(positionValues) {
		a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusConflict, "Der Inventarscan oder mindestens eine Auswahl ist nicht mehr aktuell. Es wurde nichts übernommen.", true)
		return
	}
	_, err = a.store.ImportInventoryItems(r.Context(), requests, &store.AuditEntry{ActorUserID: session.User.ID, Action: "bulk_import_inventory", RemoteAddr: r.RemoteAddr})
	if err != nil {
		status := http.StatusBadRequest
		message := "Die Sammelübernahme wurde abgebrochen. Es wurde nichts gespeichert; prüfe die ausgewählten Einträge."
		if errors.Is(err, store.ErrInventoryCatalogConflict) || errors.Is(err, store.ErrCatalogNotFound) {
			status = http.StatusConflict
			message = "Mindestens eine Auswahl wurde zwischenzeitlich geändert. Die gesamte Sammelübernahme wurde zurückgerollt."
		}
		a.renderClientsPage(w, r, session, store.EnrollmentCode{}, status, message, true)
		return
	}
	a.renderClientsPage(w, r, session, store.EnrollmentCode{}, http.StatusOK, strconv.Itoa(len(requests))+" ausgewählte Katalogübernahmen wurden vollständig gespeichert. Neue Spiele und Versionen bleiben deaktivierte Entwürfe.", false)
}

func uniqueBulkCatalogSlug(base string, position int, used map[string]struct{}) string {
	if _, exists := used[base]; !exists {
		used[base] = struct{}{}
		return base
	}
	suffix := "-" + strconv.Itoa(position)
	stem := strings.TrimRight(base, "-")
	if len(stem)+len(suffix) > 100 {
		stem = strings.TrimRight(stem[:100-len(suffix)], "-")
	}
	candidate := stem + suffix
	for sequence := 2; ; sequence++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		numberedSuffix := suffix + "-" + strconv.Itoa(sequence)
		numberedStem := strings.TrimRight(base, "-")
		if len(numberedStem)+len(numberedSuffix) > 100 {
			numberedStem = strings.TrimRight(numberedStem[:100-len(numberedSuffix)], "-")
		}
		candidate = numberedStem + numberedSuffix
	}
}

func (a *Admin) createEnrollmentCodeAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogAdminMutationSession(w, r)
	if !ok {
		return
	}
	var request struct{}
	if !a.decodeCatalogJSON(w, r, &request) {
		return
	}
	code, err := a.store.CreateEnrollmentCode(r.Context(), session.User.ID, 10*time.Minute)
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "enrollment_code_create_failed", "Enrollment-Code konnte nicht erzeugt werden.")
		return
	}
	_ = a.store.Audit(r.Context(), &session.User.ID, "create_enrollment_code", "enrollment_code", code.ID, "", r.RemoteAddr)
	a.writeJSON(w, http.StatusCreated, map[string]any{"id": code.ID, "code": code.Code, "expiresAt": code.ExpiresAt.Format(time.RFC3339), "maxUses": code.MaxUses})
}

func (a *Admin) revokeEnrollmentCodeAPI(w http.ResponseWriter, r *http.Request) {
	session, ok := a.catalogAdminMutationSession(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		a.apiError(w, http.StatusNotFound, "enrollment_code_not_found", "Enrollment-Code wurde nicht gefunden.")
		return
	}
	revoked, err := a.store.RevokeEnrollmentCode(r.Context(), id)
	if err != nil {
		a.apiError(w, http.StatusInternalServerError, "enrollment_code_revoke_failed", "Enrollment-Code konnte nicht widerrufen werden.")
		return
	}
	if !revoked {
		a.apiError(w, http.StatusNotFound, "enrollment_code_not_found", "Enrollment-Code wurde nicht gefunden, ist abgelaufen oder bereits verwendet.")
		return
	}
	_ = a.store.Audit(r.Context(), &session.User.ID, "revoke_enrollment_code", "enrollment_code", id, "", r.RemoteAddr)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) renderClientsPage(w http.ResponseWriter, r *http.Request, session store.Session, enrollment store.EnrollmentCode, status int, message string, messageIsError bool) {
	inventories, err := a.store.DeviceInventories(r.Context())
	if err != nil {
		http.Error(w, "Clients konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	launchers, err := a.store.Launchers(r.Context())
	if err != nil {
		http.Error(w, "Launcher konnten nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	games, err := a.store.Games(r.Context())
	if err != nil {
		http.Error(w, "Katalog konnte nicht gelesen werden", http.StatusInternalServerError)
		return
	}
	adapterByLauncherID := make(map[int64]string, len(launchers))
	for _, launcher := range launchers {
		adapterByLauncherID[launcher.ID] = launcher.Adapter
	}
	canImport := hasRole(session.User, "admin")
	data := clientsPageData{User: session.User, CSRFToken: session.CSRFToken, CanEnroll: canImport, CanImport: canImport, EnrollmentCode: enrollment.Code, Message: message, MessageIsError: messageIsError, Clients: make([]clientInventoryView, 0, len(inventories))}
	if data.Message == "" {
		switch r.URL.Query().Get("import") {
		case "created":
			data.Message = "Spiel und erkannte Version wurden als deaktivierte Katalogentwürfe angelegt. Die Bezugsplattform ist hinterlegt; ein eigenes LANReady-Paket ist optional."
			data.CatalogTab = "game-versions"
		case "linked":
			data.Message = "Inventarfund wurde dem Katalogeintrag zugeordnet."
		case "version-created":
			data.Message = "Die neu erkannte Version wurde als deaktivierter Entwurf angelegt. Für Launcher-Spiele ist ein eigenes LANReady-Paket optional."
			data.CatalogTab = "game-versions"
		case "game-created":
			data.Message = "Das Spiel wurde als deaktivierter Katalogentwurf angelegt. Eine Version wurde nicht angelegt; ergänze sie später im Katalog."
			data.CatalogTab = "games"
		case "bulk":
			count, parseErr := strconv.Atoi(r.URL.Query().Get("count"))
			if parseErr == nil && count > 0 && count <= store.MaxInventoryCatalogBatch {
				data.Message = strconv.Itoa(count) + " ausgewählte Katalogübernahmen wurden vollständig gespeichert. Neue Spiele und Versionen bleiben deaktivierte Entwürfe."
				data.CatalogTab = "game-versions"
			}
		}
	}
	if !enrollment.ExpiresAt.IsZero() {
		data.ExpiresAt = displayTime(enrollment.ExpiresAt)
	}
	for _, inventory := range inventories {
		view := clientInventoryView{ID: inventory.ID, Name: inventory.Name, WindowsVersion: inventory.WindowsVersion, ClientVersion: inventory.ClientVersion, Status: inventory.Status, ScanID: inventory.ScanID, EnrolledAt: displayTime(inventory.EnrolledAt), LastSeenAt: displayTime(inventory.LastSeenAt), ScannedAt: displayTime(inventory.ScannedAt), Installations: make([]clientInstallationView, 0, len(inventory.Installations))}
		for _, installation := range inventory.Installations {
			item := clientInstallationView{InventoryInstallation: installation, SuggestedSlug: suggestCatalogSlug(installation.DisplayName, installation.Launcher, installation.ExternalGameID)}
			switch installation.Launcher {
			case "steam":
				item.LauncherName = "Steam"
			case "ea_app":
				item.LauncherName = "EA App"
			case "ubisoft_connect":
				item.LauncherName = "Ubisoft Connect"
			}
			if installation.DetectedVersion != nil {
				item.SuggestedVersion = *installation.DetectedVersion
			}
			if installation.CatalogGameID == 0 {
				item.BulkEligible = canImport
				if installation.Launcher == "standalone" {
					item.BulkAction = "Vorhandenes Spiel zuordnen"
				} else {
					item.BulkAction = "Neuer Spielentwurf"
				}
			} else if installation.CatalogVersionID == 0 && installation.DetectedVersion != nil && strings.TrimSpace(*installation.DetectedVersion) != "" {
				item.BulkEligible = canImport
				item.BulkAction = "Neuer Versionsentwurf"
			}
			if messageIsError && inventory.ID == r.FormValue("device_id") && inventory.ScanID == r.FormValue("scan_id") {
				for _, selectedPosition := range r.Form["position"] {
					if strconv.Itoa(installation.Position) == selectedPosition {
						item.BulkSelected = true
						break
					}
				}
			}
			for _, game := range games {
				if adapterByLauncherID[game.LauncherID] == installation.Launcher && (game.ExternalGameID == "" || game.ExternalGameID == installation.ExternalGameID) {
					item.CompatibleGames = append(item.CompatibleGames, game)
				}
			}
			if messageIsError && inventory.ID == r.FormValue("device_id") && inventory.ScanID == r.FormValue("scan_id") && strconv.Itoa(installation.Position) == r.FormValue("position") {
				item.ImportOpen = true
				if value := r.FormValue("name"); value != "" {
					item.DisplayName = value
				}
				if value := r.FormValue("slug"); value != "" {
					item.SuggestedSlug = value
				}
				if _, exists := r.Form["version"]; exists {
					item.SuggestedVersion = r.FormValue("version")
				}
				item.SelectedGameID, _ = strconv.ParseInt(r.FormValue("game_id"), 10, 64)
			}
			view.Installations = append(view.Installations, item)
			view.HasBulkEligible = view.HasBulkEligible || item.BulkEligible
		}
		data.Clients = append(data.Clients, view)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err = clientsTemplate.Execute(w, data); err != nil {
		http.Error(w, "Clientseite konnte nicht dargestellt werden", http.StatusInternalServerError)
	}
}

func suggestCatalogSlug(name, launcher, externalID string) string {
	value := strings.ToLower(strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(strings.TrimSpace(name)))
	var slug strings.Builder
	separator := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			if separator && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(character)
			separator = false
		} else if unicode.IsSpace(character) || unicode.IsPunct(character) || unicode.IsSymbol(character) {
			separator = true
		}
	}
	result := strings.Trim(slug.String(), "-")
	if result == "" {
		result = launcher + "-" + externalID
		result = strings.Map(func(character rune) rune {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				return character
			}
			return '-'
		}, strings.ToLower(result))
		result = strings.Trim(result, "-")
	}
	if len(result) > 100 {
		result = strings.TrimRight(result[:100], "-")
	}
	return result
}

type deviceAuthorizationData struct {
	User                        store.User
	CSRFToken, Code, DeviceName string
	ExpiresAt, Message          string
	Ready, Success, Denied      bool
}

func (a *Admin) deviceAuthorizationPage(w http.ResponseWriter, r *http.Request) {
	session, _, err := a.currentSession(r)
	if err != nil {
		return
	}
	data := deviceAuthorizationData{User: session.User, CSRFToken: session.CSRFToken, Code: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code")))}
	if data.Code != "" {
		pending, readErr := a.store.PendingDeviceAuthorization(r.Context(), data.Code)
		if readErr != nil {
			data.Message = "Der Code ist ungültig, abgelaufen oder wurde bereits verwendet."
		} else {
			data.Ready = true
			data.DeviceName = pending.DeviceName
			data.ExpiresAt = displayTime(pending.ExpiresAt)
		}
	}
	a.renderDeviceAuthorization(w, data)
}

func (a *Admin) deviceAuthorizationDecision(w http.ResponseWriter, r *http.Request) {
	session, _, ok := a.validRequest(w, r)
	if !ok {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
	pending, err := a.store.PendingDeviceAuthorization(r.Context(), code)
	if err != nil {
		a.renderDeviceAuthorization(w, deviceAuthorizationData{User: session.User, CSRFToken: session.CSRFToken, Code: code, Message: "Der Code ist ungültig, abgelaufen oder wurde bereits verwendet."})
		return
	}
	data := deviceAuthorizationData{User: session.User, CSRFToken: session.CSRFToken, Code: code, DeviceName: pending.DeviceName}
	switch r.FormValue("decision") {
	case "deny":
		err = a.store.DenyDeviceAuthorization(r.Context(), code, session.User.ID)
		data.Denied = err == nil
		data.Message = "Die Anmeldung wurde abgelehnt. Der Windows-Client erhält keinen Zugriff auf das Inventar."
		if err == nil {
			_ = a.store.Audit(r.Context(), &session.User.ID, "deny_device_authorization", "device", pending.DeviceID, "", r.RemoteAddr)
		}
	case "approve":
		err = a.store.ApproveDeviceAuthorization(r.Context(), code, session.User.ID)
		data.Success = err == nil
		data.Message = "Anmeldung bestätigt. Du kannst zum Windows-Client zurückkehren."
		if err == nil {
			_ = a.store.Audit(r.Context(), &session.User.ID, "approve_device_authorization", "device", pending.DeviceID, "", r.RemoteAddr)
		}
	default:
		http.Error(w, "Ungültige Entscheidung", http.StatusBadRequest)
		return
	}
	if err != nil {
		if errors.Is(err, store.ErrAuthorizationExpired) {
			data.Message = "Der Code ist inzwischen abgelaufen. Starte die Anmeldung im Windows-Client erneut."
		} else {
			data.Message = "Die Entscheidung konnte nicht gespeichert werden."
		}
	}
	a.renderDeviceAuthorization(w, data)
}

func (a *Admin) renderDeviceAuthorization(w http.ResponseWriter, data deviceAuthorizationData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := deviceAuthorizationTemplate.Execute(w, data); err != nil {
		http.Error(w, "Anmeldung konnte nicht dargestellt werden", http.StatusInternalServerError)
	}
}

var clientsTemplate = template.Must(template.New("clients").Parse(clientsPageHTML))
var deviceAuthorizationTemplate = template.Must(template.New("device-authorization").Parse(deviceAuthorizationPageHTML))

const clientsPageHTML = `<!doctype html>
<html lang="de">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Clients · LANReady</title>
<link rel="stylesheet" href="/admin/assets/management.css?v=20260720-2">
<script src="/admin/assets/clients.js?v=20260720-1" defer></script>
</head>
<body><div class="shell"><aside>
  <div class="brand"><span class="brand-mark">L</span><span>LANReady</span></div>
  <nav class="nav" aria-label="Management"><a href="/admin/">Dashboard</a><a href="/admin/sources">Quellen</a><a href="/admin/catalog">Katalog</a><a href="/admin/events">Events</a><a href="/admin/clients" aria-current="page">Clients</a></nav>
  <div class="sidebar-user"><div class="user-identity"><span>{{.User.Username}}</span><small>{{range .User.Roles}}{{.}} {{end}}</small></div><form class="sidebar-logout" method="post" action="/admin/logout"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="button" type="submit">Abmelden</button></form></div>
</aside><main>
  <header class="top"><div><p class="eyebrow">Geräte und Inventar</p><h1>Clients</h1><p>Registrierte Windows-PCs, letzter Kontakt und erkannte Installationen.</p></div>{{if .CanEnroll}}<form method="post" action="/admin/clients/enrollment-code"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="button primary" type="submit">Enrollment-Code erzeugen</button></form>{{end}}</header>
  {{if .Message}}<p id="clients-message" class="page-notice {{if .MessageIsError}}error{{else}}success{{end}}" role="{{if .MessageIsError}}alert{{else}}status{{end}}" tabindex="-1">{{.Message}}{{if .CatalogTab}} <a href="/admin/catalog?tab={{.CatalogTab}}">Im Katalog öffnen</a>{{end}}</p>{{end}}
  {{if .EnrollmentCode}}<section class="card enrollment-code"><p class="eyebrow">Einmaliger Enrollment-Code</p><h2>{{.EnrollmentCode}}</h2><p>Gültig bis {{.ExpiresAt}}. Der Klartext wird nur jetzt angezeigt.</p></section>{{end}}
  {{if .Clients}}<div class="clients-grid">{{range $clientIndex, $client := .Clients}}
    <article class="card dashboard-card client-card">
      <div class="editor-head"><div><h2>{{.Name}}</h2><p class="subtle mono">{{.ID}}</p></div>{{if eq .Status "active"}}<span class="badge ok"><span class="dot"></span>Aktiv</span>{{else}}<span class="badge inactive">Gesperrt</span>{{end}}</div>
      <dl class="client-meta"><dt>Windows</dt><dd>{{.WindowsVersion}}</dd><dt>Client-Version</dt><dd>{{.ClientVersion}}</dd><dt>Registriert</dt><dd>{{.EnrolledAt}}</dd><dt>Letzter Kontakt</dt><dd>{{.LastSeenAt}}</dd><dt>Inventarscan</dt><dd>{{.ScannedAt}}</dd></dl>
      {{if .Installations}}{{if and $.CanImport .HasBulkEligible}}<form id="bulk-import-{{$clientIndex}}" method="post" action="/admin/clients/catalog-import-bulk" class="bulk-import-form"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="device_id" value="{{$client.ID}}"><input type="hidden" name="scan_id" value="{{$client.ScanID}}"><div class="bulk-import-toolbar"><label class="check"><input type="checkbox" class="bulk-select-all"> Bis zu 100 offene Übernahmen auswählen</label><span class="subtle bulk-selection-count" aria-live="polite">0 ausgewählt</span><button class="button primary bulk-import-submit" type="submit" disabled>Ausgewählte übernehmen</button></div><p class="subtle bulk-import-help">Launcher-Spiele werden als deaktivierte Entwürfe mit ihrer Bezugsplattform angelegt; ein eigenes LANReady-Paket bleibt optional. Manuell registrierte Spiele werden ausschließlich ihrem bereits vorhandenen Katalogeintrag zugeordnet.</p></form>{{end}}<div class="table-wrap"><table class="inventory-table"><thead><tr>{{if $.CanImport}}<th class="inventory-select-column"><span class="sr-only">Auswahl</span></th>{{end}}<th>Spiel</th><th>Launcher / ID</th><th>Version</th><th>Installationspfad</th><th>Katalog</th></tr></thead><tbody>{{range .Installations}}{{$item := .}}
        <tr>{{if $.CanImport}}<td class="inventory-select-cell" data-label="Auswahl">{{if .BulkEligible}}<input class="bulk-item-select" type="checkbox" name="position" value="{{.Position}}" form="bulk-import-{{$clientIndex}}" aria-label="{{.DisplayName}} für Sammelübernahme auswählen" {{if .BulkSelected}}checked{{end}}><small>{{.BulkAction}}</small>{{else}}<span class="subtle">—</span>{{end}}</td>{{end}}<td data-label="Spiel"><strong>{{.DisplayName}}</strong></td><td data-label="Launcher / ID"><span class="launcher-label">{{.LauncherName}}</span><div class="subtle mono">{{.ExternalGameID}}</div></td><td data-label="Version">{{if .DetectedVersion}}{{.DetectedVersion}}{{else}}unbekannt{{end}}<div class="subtle">{{.VersionSource}}</div></td><td data-label="Pfad"><span class="path-value">{{.InstallPath}}</span></td><td data-label="Katalog">
          {{if .CatalogGameID}}<span class="badge ok">Zugeordnet</span><div class="subtle">{{.CatalogGameName}}{{if .CatalogVersionID}} · Version übernommen{{else if .DetectedVersion}} · neue Version offen{{end}}</div>{{if and $.CanImport .DetectedVersion (not .CatalogVersionID)}}<form method="post" action="/admin/clients/catalog-import" class="version-import-form"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="device_id" value="{{$client.ID}}"><input type="hidden" name="scan_id" value="{{$client.ScanID}}"><input type="hidden" name="position" value="{{.Position}}"><input type="hidden" name="mode" value="version"><button class="button" type="submit">Version als Entwurf</button></form>{{end}}
          {{else if $.CanImport}}<details class="inventory-import" {{if .ImportOpen}}open{{end}}><summary>Übernehmen <span class="sr-only">: {{.DisplayName}}</span></summary><div class="import-panels">
            {{if .CompatibleGames}}<form method="post" action="/admin/clients/catalog-import" class="import-form"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="device_id" value="{{$client.ID}}"><input type="hidden" name="scan_id" value="{{$client.ScanID}}"><input type="hidden" name="position" value="{{.Position}}"><input type="hidden" name="mode" value="link"><input type="hidden" name="version" value="{{.SuggestedVersion}}"><label class="field"><span>Vorhandenem Spiel zuordnen</span><select class="select" name="game_id" required>{{range .CompatibleGames}}<option value="{{.ID}}" {{if eq .ID $item.SelectedGameID}}selected{{end}}>{{.Name}}{{if not .Enabled}} (Entwurf){{end}}</option>{{end}}</select></label><button class="button" type="submit">Zuordnen</button></form>{{end}}
            {{if ne .Launcher "standalone"}}<form method="post" action="/admin/clients/catalog-import" class="import-form"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="device_id" value="{{$client.ID}}"><input type="hidden" name="scan_id" value="{{$client.ScanID}}"><input type="hidden" name="position" value="{{.Position}}"><input type="hidden" name="mode" value="create"><label class="field"><span>Name</span><input class="input" name="name" value="{{.DisplayName}}" maxlength="200" required></label><label class="field"><span>Slug</span><input class="input mono" name="slug" value="{{.SuggestedSlug}}" maxlength="100" pattern="[a-z0-9]+(?:-[a-z0-9]+)*" required></label><label class="field"><span>Katalogversion</span><input class="input" name="version" value="{{.SuggestedVersion}}" maxlength="256" placeholder="unbekannt"><small class="field-hint">Kann lesbarer benannt werden; die erkannte Version bleibt separat zugeordnet.</small></label><p class="subtle">{{if .SuggestedVersion}}Legt deaktivierte Spiel- und Versionsentwürfe an. Die Bezugsplattform ist bereits bekannt; ein eigenes LANReady-Paket kann optional ergänzt werden.{{else}}Legt nur einen deaktivierten Spielentwurf an, weil keine Version erkannt wurde.{{end}}</p><button class="button primary" type="submit">{{if .SuggestedVersion}}Spiel und Version als Entwurf{{else}}Spiel als Entwurf{{end}}</button></form>{{end}}
          </div></details>{{else}}<span class="badge inactive">Nicht zugeordnet</span><div class="subtle">Nur Administratoren können Inventarfunde übernehmen.</div>{{end}}
        </td></tr>{{end}}</tbody></table></div>{{else}}<div class="empty">Noch kein Inventar synchronisiert.</div>{{end}}
    </article>{{end}}</div>
  {{else}}<section class="card empty"><h2>Noch keine Clients registriert</h2><p>Sobald ein Windows-Client enrollt ist, erscheint er hier mit Status und Spieleinventar.</p></section>{{end}}
</main></div><script>{{if .MessageIsError}}document.getElementById("clients-message")?.focus();{{end}}document.querySelectorAll(".import-form,.version-import-form").forEach(function(form){form.addEventListener("submit",function(){form.setAttribute("aria-busy","true");form.querySelectorAll("button[type=submit]").forEach(function(button){button.disabled=true;button.textContent="Wird gespeichert …"})})});document.querySelectorAll(".bulk-import-form").forEach(function(form){var items=Array.from(document.querySelectorAll('input[form="'+form.id+'"].bulk-item-select'));var all=form.querySelector(".bulk-select-all");var count=form.querySelector(".bulk-selection-count");var submit=form.querySelector(".bulk-import-submit");function update(){var selected=items.filter(function(item){return item.checked}).length;count.textContent=selected+" von maximal 100 ausgewählt";submit.disabled=selected===0||selected>100;all.checked=items.length<=100&&selected>0&&selected===items.length;all.indeterminate=selected>0&&!all.checked}items.forEach(function(item){item.addEventListener("change",function(){if(item.checked&&items.filter(function(candidate){return candidate.checked}).length>100){item.checked=false;window.alert("Pro Sammelübernahme können höchstens 100 Einträge ausgewählt werden.")}update()})});all.addEventListener("change",function(){items.forEach(function(item,index){item.checked=all.checked&&index<100});update()});form.addEventListener("submit",function(event){var selected=items.filter(function(item){return item.checked}).length;if(selected===0||selected>100){event.preventDefault();return}if(!window.confirm(selected+" ausgewählte Übernahmen speichern? Die Aktion wird vollständig zurückgerollt, falls ein Eintrag nicht mehr aktuell ist.")){event.preventDefault();return}form.setAttribute("aria-busy","true");submit.disabled=true;submit.textContent="Übernahme läuft …"});update()});</script></body></html>`

const deviceAuthorizationPageHTML = `<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Windows-Client anmelden · LANReady</title><link rel="stylesheet" href="/admin/assets/management.css?v=20260720-2"></head><body class="auth-body"><main class="auth-shell"><div class="auth-brand"><span class="brand-mark">L</span><span>LANReady</span></div><section class="card auth-card"><p class="eyebrow">Windows-Client</p><h1>Client anmelden</h1>{{if .Message}}<div class="notice" role="status">{{.Message}}</div>{{end}}{{if .Success}}<p>Dieses Browserfenster kann geschlossen werden.</p>{{else if .Denied}}<p>Dieses Browserfenster kann geschlossen werden.</p>{{else if .Ready}}<p><strong>{{.DeviceName}}</strong> möchte das lokale Spieleinventar mit deinem Benutzerkonto synchronisieren.</p><p class="subtle">Der Auftrag läuft am {{.ExpiresAt}} ab. Dein Passwort wird nicht an den Windows-Client weitergegeben.</p><form method="post" action="/admin/device"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><input type="hidden" name="code" value="{{.Code}}"><div class="actions"><button class="button primary" type="submit" name="decision" value="approve">Anmeldung bestätigen</button><button class="button" type="submit" name="decision" value="deny">Ablehnen</button></div></form>{{else}}<p>Gib den Code ein, den der Windows-Client anzeigt.</p><form method="get" action="/admin/device"><label class="field"><span>Einmalcode</span><input class="input" name="code" value="{{.Code}}" autocomplete="one-time-code" maxlength="9" required autofocus></label><button class="button primary" type="submit">Code prüfen</button></form>{{end}}</section></main></body></html>`
