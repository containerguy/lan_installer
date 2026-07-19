# LANReady – Übergabe

Stand: 18.07.2026

## Aktuelles Ziel

LANReady wird als selbst gehostete Plattform für LAN-Partys aufgebaut: Management-Web-UI auf Ubuntu, signierte Eventmanifeste, zentraler On-Demand-Cache und ein portabler beziehungsweise installierbarer Windows-Client im Benutzerkontext.

## Verifizierte Produktentscheidungen

- Öffentliche URL: `https://game-manager.familie-keller.info` über Nginx Proxy Manager.
- Initiale Launcher: Steam, EA App und Ubisoft Connect.
- Launcher-Zugangsdaten werden niemals gespeichert; Anmeldung bleibt im Launcher.
- Externe Quellen starten mit HTTPS, WebDAV und Nextcloud WebDAV; SMB/NFS folgen später als externe Mounts. WebDAV-Konfiguration liegt in einer dedizierten SQLite-Tabelle; Passwörter sind mit einem externen Docker-Secret verschlüsselt.
- Der Ubuntu-Server darf Inhalte bedarfsgesteuert cachen und im LAN verteilen.
- Benutzer dürfen Installationen/Updates ablehnen, erhalten dann aber Warnungen.
- Installierter Windows-Agent läuft pro Benutzer beim Anmelden, nicht als Systemdienst.
- Rollenmodell: admin, operator, viewer; spätere Entra-ID-Anbindung über externe Identitäten.
- Signaturschlüssel später in separater, maximal gehärteter Signer-Grenze; aktuell offline.
- Der Windows-Client erkennt nach persönlicher Browser-Anmeldung lokal installierte Spiele von Steam, EA App und Ubisoft Connect und synchronisiert Launcher, externe Spiel-ID, erkannte Version und Installationspfad in das Inventar des enrollten Geräts. Der zentrale Katalog wird nie automatisch verändert; eine Übernahme erfolgt ausdrücklich durch einen Admin.

## MVP-Einordnung

Der ausgerollte Stand ist ein technisches Fundament und Admin-Gerüst, aber nach der verbindlichen Definition in `docs/MVP_ACCEPTANCE.md` noch kein MVP. Die bisherige Kennzeichnung von UI-CRUD als abgeschlossener UI-Slice war falsch.

## Slice 0 – Gate bestanden und freigegeben

- MVP-Vertrag, Rollenmatrix, Domänenmodell und bytegenauer API-v2-Vertrag erstellt.
- Maschinenprüfbare Schemas für EventRelease, Clientupdate, Bootstrap/426 und Enrollment sowie deterministische Ed25519-Golden-Vektoren ergänzt.
- Strikte Request-Kanonisierung, Envelope-Prüfung und semantische Publish-Validatoren mit Negativtests implementiert.
- Klickbare Management- und Windows-Abläufe unter `docs/prototypes/lanready-mvp-flows.html`; 13 Headless-Szenarien sind reproduzierbar grün.
- Unabhängige UX- und Engineering-Reviews: keine offenen P0/P1.
- Der Benutzer hat die klickbaren Abläufe ausdrücklich freigegeben.

## Slice 1 – Quellenverwaltung produktiv

- Gestaltete, responsive Quellen-UI unter `/admin/sources` mit Liste, Suche, Filtern, Sortierung, Erstellen, Bearbeiten, Deaktivieren und referenzsicherem Löschen.
- HTTPS-, WebDAV- und Nextcloud-WebDAV-Tests mit festen Zeit-, Größen- und Redirect-Grenzen sowie SSRF-Schutz. Private Ziele sind standardmäßig gesperrt und nur über eine explizite Host-/CIDR-Allowlist erreichbar.
- Quelle, dedizierte WebDAV-Konfiguration, verschlüsseltes Secret und Audit-Eintrag werden atomar in SQLite gespeichert. Bestehende Secrets werden bei Bearbeitung nie an Browser oder API zurückgegeben.
- Rollen wirken in UI und API: Admin verwaltet Quellen, Operator darf nur unveränderte gespeicherte Verbindungen erneut testen, Viewer nur lesen.
- Persistente Idempotenz für Erstellen und Deaktivieren, optimistische Revisionen, CSRF-, Content-Type- und Request-ID-Prüfung.
- Unit-, Integrations-, Negativ-, Race- und Browserprüfungen bestanden. UX- und Engineering-Review endeten jeweils mit null offenen P0/P1.
- Produktionsdeployment `slice1-20260716`; öffentlicher Login, Quellenansicht und ein nicht persistierender Test gegen `https://example.com` erfolgreich.
- Dokumentierte P2-Restpunkte: enges Crashfenster zwischen Mutation und Abschluss des Idempotenzdatensatzes, serverseitig vollständig gerenderte Quellenliste statt API-Paginierung sowie noch nicht dauerhaft eingecheckte Browserregressionen für verzögerte Testantworten.

## Implementiert und ausgerollt

### Managementserver

- Go 1.26.5, Alpine 3.24.1, nginx stable 1.30.4.
- Gehärteter Container: read-only root filesystem, tmpfs, alle Capabilities entfernt, no-new-privileges, kein veröffentlichter Host-Port im NPM-Betrieb.
- SQLite-Datenbank mit Dateirechten 0600; WAL/SHM ebenfalls 0600.
- Lokaler Bootstrap-Admin aus Docker Secret; Argon2id-Passworthash.
- Serverseitige, gehashte Sitzungstokens; Secure/HttpOnly/SameSite-Strict-Cookies.
- CSRF-Schutz einschließlich Login und Audit-Log.
- Rollen und Schema für spätere Entra-OIDC-Identitäten.
- Dedizierte `webdav_source_config`-Tabelle mit XChaCha20-Poly1305-verschlüsselten Basic-/App-Passwörtern.
- Nutzbarer Quellenablauf sowie technisches Web-CRUD-Gerüst für Launcher, Launcher-Versionen, Spiele, Spielversionen, Events und Event-Zuordnungen.
- Steam, EA App und Ubisoft Connect sind vorbefüllt.
- Bestehende Manifest-, Content- und Report-API bleibt funktionsfähig.

### Windows-MVP

- Portabler Go-Client, signierte Ed25519-Manifeste, SHA-256-Dateiprüfung, fortsetzbare Downloads und Statusberichte.
- Windows-x64- und Linux-Builds sind erfolgreich.
- Portable ZIP liegt lokal unter `dist/LANReady-Windows.zip`; es muss nach Clientänderungen neu gebaut werden.

## Verifikation

Lokal erfolgreich:

- `go test ./...`
- `go vet ./...`
- leere `gofmt -l`-Ausgabe
- Linux-Server- und Windows-x64-Client-Build
- `docker compose config -q`

Auf `ubuntu@192.168.220.39` erfolgreich:

- vollständiger Docker-Multi-Stage-Build einschließlich Tests
- Container `lanready-server-1` healthy
- `/healthz` und `/admin/login` öffentlich HTTP 200; `/` leitet mit HTTP 303 auf die geschützte UI weiter
- vollständiger HTTPS-Login: POST 303, `/admin/sources` HTTP 200 und erwartete Quellenverwaltung
- nicht persistierender Quellenprobe gegen `https://example.com`: HTTP 200, Zustand `succeeded`
- der Zugriff vom Ubuntu-Host auf die eigene öffentliche Domain kann wegen fehlendem beziehungsweise unzuverlässigem NAT-Hairpin auslaufen; dies ist kein Containerfehler. Die öffentliche Prüfung aus dem Client-Netz und die interne Container-Health-Prüfung sind erfolgreich.
- Datenbank und Secret jeweils Modus 0600

Rollback-Artefakte für Slice 1:

- Backup: `/home/ubuntu/lanready/backups/slice1-20260716T1230`
- Vorheriges Image: `lanready-server:pre-slice1-20260716`

## Betrieb

Projektpfad auf Ubuntu: `/home/ubuntu/lanready`

Admin-Benutzer: `admin`

Das einmalig generierte Passwort liegt ausschließlich auf dem Host:

```bash
ssh ubuntu@192.168.220.39 'cat /home/ubuntu/lanready/secrets/web-admin-password.txt'
```

Deployment:

```bash
cd /home/ubuntu/lanready
docker compose -f compose.yaml -f compose.npm.yaml up -d --build server
```

## Verbindliche nächste Slices

0. MVP-Vertrag, Rollenmatrix, Domänen-/API-Modell sowie benutzerfreigegebene klickbare Management- und Windows-Flows.
1. Quellen vertikal: nutzbare UI, atomare Credential-Speicherung, Verbindungstest, Timeout/Limit/Redirect- und SSRF-Policy.
2. Inhalte/Versionen vertikal: SHA-256-CAS, atomarer Commit, Quota, Parallelität, Jobs, Retry/Cancel und Garbage Collection samt Status-UI.
3. Events vertikal: Abhängigkeitsvalidierung, isolierter Signer, immutable monotone Releases, Publish-/Rollback-UI und Client-Anti-Rollback-Vertrag.
4. Windows-GUI-Kern: Geräte-Enrollment, persönliche Browser-Einmalcode-Anmeldung, lokale Spielerkennung, bestätigte Inventarsynchronisation, Auswahl, Fortschritt, Resume/Cancel, Ablehnung mit Warnung und generische Installations-/Elevationsgrenze.
5. Steam-Adapter mit realem Windows-11-Ende-zu-Ende-Test.
6. EA-App-Adapter mit denselben Gates.
7. Ubisoft-Connect-Adapter mit denselben Gates.
8. Per-user Agent und signiertes Self-Update nach dem bereits definierten Updatevertrag.
9. MVP-Härtung und Release: Security, Backup/Restore, Rollback, Windows-Testmatrix und finale UX-Abnahme.

SSO, SMB/NFS, P2P und differenzielles Chunking bleiben optionale Post-MVP-Slices.

## Lokaler Fortschritt 2026-07-18 (noch nicht ausgerollt)

- Geräte-API v2 ist lokal implementiert: einmaliges Enrollment, Ed25519-signierte Requests, persistente Nonce-/Rate-Limit-Prüfung, Browser-Einmalcode und gerätegebundene kurzlebige Benutzertokens.
- `/admin/clients` zeigt reale Geräte und das aktuelle authentisierte Inventar. Admins können Funde explizit zuordnen oder deaktivierte Spiel-/Versionsentwürfe erzeugen; Viewer/Operator bleiben read-only.
- SQLite-Schema v17 enthält dauerhafte `inventory_catalog_mappings` mit Request-Fingerprint, separate `inventory_catalog_version_mappings` je erkanntem Build, persistente Cache-Aufträge mit erhaltener Historie nach dem Löschen einer Quelle, normalisierte Release-Artefaktverweise, unveränderliche SHA-256-Artefaktmetadaten, monotone Event- und Clientupdate-Releases sowie den niemals sinkenden Clientversions-Höchststand je Gerät. Release-Aktivierung ist eine eigene ausdrückliche Transaktion; fehlende oder abweichende Artefakte blockieren die Veröffentlichung atomar.
- Der portable Windows-Client kann ein vollständig mit DPAPI CurrentUser integritätsgeschütztes Geräteprofil enrollen, per Browser anmelden, Steam-/EA-App-/Ubisoft-Connect-Installationen erkennen, eine Vorschau anzeigen und das bestätigte Inventar synchronisieren. Windows-x64 kompiliert erfolgreich.
- Eine gestaltete Wails-2.13-Windows-GUI bindet Enrollment, lokale Suche, Einzelauswahl, Browserfreigabe und eine zweite ausdrückliche Synchronisationsbestätigung an diesen Kern. Ein portables ZIP und ein NSIS-per-user-Setup mit eigenem Icon und WebView2-Evergreen-Prüfung werden lokal reproduzierbar gebaut; die `0.1.0`-Artefakte sind noch nicht Authenticode-signiert.
- Der lokale CAS schreibt deklarierte oder beim Import ermittelte Artefakte erst nach Größen- und SHA-256-Prüfung atomar fest, verifiziert gespeicherte Dateien vor der Ausgabe erneut und liefert sie ausschließlich nach Geräteauthentisierung über HEAD/GET mit striktem Single-Range-Resume aus. Serielle persistente HTTPS/WebDAV-Cache-Aufträge mit SSRF-/Redirect-/Credential-Policy, harter Quota, Fortschritt, Abbruch, Retry, Neustartwiederaufnahme und Managementstatus sind lokal umgesetzt. Der Container verwendet `/cache`; `LANREADY_CACHE_HOST_PATH` zeigt bis zum späteren NAS-Mount auf `./data/cache`, die gemessene Root-Disk begründet eine vorläufige 10-GiB-Quota. Garbage Collection und ein realer Nextcloud/NAS-Ende-zu-Ende-Test fehlen noch.
- Der lokale Release-Service validiert die verbindlichen JSON-Schemata, doppelte JSON-Felder, Ed25519-Key-ID/Signatur, Release-Semantik, Gültigkeitsfenster und CAS-Referenzen. Geräte erhalten exakt aktivierte Event-Envelopes und stabile Clientupdate-Envelopes authentifiziert; Bootstrap und die schreibenden Geräteendpunkte sperren bei unterschrittener Mindestversion. Laufzeitversion und Höchststand werden getrennt gespeichert, sodass ein Downgrade nicht durch einen älteren Scan legitimiert werden kann.
- `lanready-release` erzeugt Schlüssel ohne Überschreiben, akzeptiert Private Keys nur als reguläre 0600-Datei und signiert/verifiziert Event- oder Update-Payloads gegen dieselben Schemata. Das gehärtete Signer-Image besitzt kein Netzwerk; der Webserver mountet ausschließlich den Public Key. Die Events-UI bietet vollständige Releasehistorie, spätere Aktivierung, expliziten Rollback-Kandidaten, Offline-Signierhinweise sowie Vorschau und Veröffentlichung signierter Clientupdates. Die zum Envelope gehörende Authenticode-signierte portable EXE kann dabei direkt über die Admin-UI in den CAS gestreamt werden; ein 1-GiB-Limit, exakte Content-Length, MIME-, Größen- und SHA-256-Prüfung sowie die erneute Release-Service-Prüfung sperren unvollständige Veröffentlichungen. Für Nginx Proxy Manager sind `client_max_body_size 1g` und `proxy_request_buffering off` dokumentiert. Ein automatisch aus Katalog und CAS gebauter unsignierter Event-Kandidat fehlt noch.
- Der Windows-Client besitzt lokal einen Self-Update-Ablauf: authentifizierter Resume-Download mit sichtbarem Fortschritt/Abbruch, eingebetteter Ed25519-Keyring, strikte Envelope-/SemVer-/Sequenzprüfung, signierter Artefakttyp und Updater-Protokollstand, Größen-/SHA-256-Prüfung, gepinnter Authenticode-Zertifikatsfingerprint, Apply-Handshake, Cross-Process-Lock, atomarer Austausch und automatische Wiederherstellung, falls die neue GUI keinen Frontend-Ready-Health-Marker schreibt. Die geschützte Profilsequenz wird erst nach erkanntem Marker durch den Updater festgeschrieben. Release-Key und erwarteter Publisher werden beim Build in die GUI eingebettet; Private Keys sind aus Git und Docker-Buildkontext ausgeschlossen.
- `scripts/build-windows-release.ps1` ist der fail-closed Releasepfad: übereinstimmende Binär-/Windows-Dateiversion, SHA-256-Authenticode für Client/Uninstaller/Installer, RFC-3161-Zeitstempel, Windows-Policyprüfung und Zertifikatsfingerprintvergleich. Ohne Zertifikat/Fingerprint verweigert `make windows-package` die Ausgabe.
- Die installierte Variante registriert lokal einen `LIMITED` per-user Task-Scheduler-Start. Der versteckte Agent prüft Updates alle 30 Minuten, zeigt Windows-Benachrichtigungen, öffnet bei zweitem Start die vorhandene GUI und bleibt beim Fensterschließen aktiv; ein ausdrücklicher Einstellungsbutton beendet ihn für die Sitzung. Portable Builds registrieren nichts.
- Lokale Gates nach dem Slice: vollständige Go-Tests, `go vet`, Race Detector für Store/Webadmin/Device-API/Deviceclient und Windows-x64-Build grün. Echtes Chrome-QA der Clientseite bei 1440×1000 und 390×844 einschließlich geöffnetem Importformular ohne horizontalen Seitenüberlauf.
- Noch nicht als MVP abgenommen: reale Windows-11-Tests der drei Launcheradapter und des Task-Scheduler-/Toast-Ablaufs, Installations-/Updateorchestrierung für Launcher/Spiele sowie ein Ende-zu-Ende-Self-Update mit öffentlich vertrauenswürdig Authenticode-signiertem und zeitgestempeltem Release. Der Produktionsserver enthält diese lokalen Änderungen noch nicht.
