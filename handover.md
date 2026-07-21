# LANReady – Übergabe

Stand: 20.07.2026

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
- Kopierte beziehungsweise manuell installierte Spiele werden nicht frei vom Client erfunden: Ein Admin legt sie zuerst als Spiel des festen Systemtyps **Ohne Launcher** an. Der Benutzer bindet anschließend lokal eine `.exe`; diese Registrierung liegt DPAPI-CurrentUser-geschützt im Geräteprofil und kann nach persönlicher Browserfreigabe wie ein Launcherfund synchronisiert werden.

## MVP-Einordnung

Der ausgerollte Stand ist ein nutzbarer Management-/Client-Teststand, aber nach der verbindlichen Definition in `docs/MVP_ACCEPTANCE.md` noch kein vollständiges MVP: Geräte-Enrollment, persönliche Anmeldung, Launcher-Erkennung und Inventarsynchronisation sind vorhanden; die reale Installations-/Updateorchestrierung und die Windows-11-Ende-zu-Ende-Matrix fehlen noch. Die bisherige Kennzeichnung von UI-CRUD als abgeschlossenem MVP-Slice war falsch.

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

- Gestaltete portable Wails-GUI mit Enrollment, persönlicher Browser-Anmeldung, Erkennung installierter Steam-/EA-App-/Ubisoft-Connect-Spiele, Auswahl und bestätigter Inventarsynchronisation.
- Signierte Ed25519-Manifeste, SHA-256-Dateiprüfung, fortsetzbare Downloads und Statusberichte.
- Der aktuelle portable Testbuild aus Commit `32cbb67` liegt auf dem Windows-PC unter `C:\Users\Eluminare\Downloads\LANReady-Portable-Test-32cbb67\LANReady.exe`; SHA-256: `984a8d100f2ecd84f3600e54d6d6ffdfad4861b19515840ef1f6d148bf675b7f`. Beim Bereitstellen lief keine andere LANReady-Instanz.
- Die Test-EXE ist absichtlich noch nicht Authenticode-signiert; SmartScreen kann deshalb warnen. Der Releasepfad bleibt ohne Zertifikat fail-closed.

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
- produktives Schema v6 wurde zuerst als konsistentes Onlinebackup isoliert mit dem Candidate-Image auf exakt v1–v17 migriert; `quick_check`, `foreign_key_check`, Benutzer-/Quellenzahlen sowie Login, Clients, Sources und Logout waren grün
- `/healthz` und `/admin/login` öffentlich HTTP 200; `/admin/clients` leitet unauthentifiziert mit HTTP 303 auf die geschützte UI weiter
- vollständiger HTTPS-Login über NPM: Clients und Quellen jeweils HTTP 200, zwei Quellen erwartet, Logout HTTP 200
- neue Geräte-API öffentlich erreichbar: falsche GET-Methode auf `/v2/devices/enroll` HTTP 405, syntaktisch ungültiger POST HTTP 422
- nicht persistierender Quellenprobe gegen `https://example.com`: HTTP 200, Zustand `succeeded`
- der Zugriff vom Ubuntu-Host auf die eigene öffentliche Domain kann wegen fehlendem beziehungsweise unzuverlässigem NAT-Hairpin auslaufen; dies ist kein Containerfehler. Die öffentliche Prüfung aus dem Client-Netz und die interne Container-Health-Prüfung sind erfolgreich.
- Datenbank und Secrets jeweils Modus 0600; `/cache` ist produktiv als Bind-Mount von `/mnt/lanready-cache` eingebunden, dahinter liegt der TrueNAS-NFSv4.2-Hard-Mount. Die Quota beträgt 1 TiB.
- rootless Docker, read-only root filesystem, keine veröffentlichten Host-Ports, `cap_drop: ALL`, `no-new-privileges` und ausschließlich das externe NPM-Netz `172.18.0.0/16`. Rootless Docker ist als User-Service aktiviert, `Linger=yes`; NPM und LANReady laufen nachweislich über `/run/user/1000/docker.sock` im selben Daemon.
- `lanready-compose.service` wurde mit Stop, tatsächlichem NFS-Unmount, Remount und Start geprüft. Danach waren Cache-Mount, Proxy-Alias, interner Healthzugriff aus NPM und die öffentliche HTTPS-URL grün. Ein ungefragter kalter Host-Reboot wurde nicht ausgeführt und bleibt ein dokumentierter P2-Betriebstest für das nächste Wartungsfenster.

Rollback-Artefakte für Slice 1:

- Backup: `/home/ubuntu/lanready/backups/slice1-20260716T1230`
- Vorheriges Image: `lanready-server:pre-slice1-20260716`

Rollback-Artefakte für Deployment `dc44e6c-20260719T091313Z`:

- Backup: `/home/ubuntu/lanready/backups/pre-dc44e6c-20260719T091313Z`
- Vorheriges Image: `lanready-server:rollback-dc44e6c-20260719T091313Z`
- Geprüftes Rollback-Skript: `/home/ubuntu/lanready-deploy/dc44e6c-20260719T091313Z/upload/lanready_rollback.sh`
- Finales Onlinebackup ist bytegenau identisch zum vorab isoliert migrierten Snapshot; SHA-256: `a7cf7e04a3c563b81a880cdea0183cac1d45a5a2a87d03544dea11b1712aa210`

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
sudo systemctl restart lanready-compose.service
```

Ein manueller Compose-Aufruf muss immer beide Dateien verwenden: `docker compose -f compose.yaml -f compose.npm.yaml ...`. Andernfalls fehlt nach einem Recreate das externe Proxy-Netz und Nginx Proxy Manager liefert 502.

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

SSO, SMB/NFS als direkte Inhaltsquellen, P2P und differenzielles Chunking bleiben optionale Post-MVP-Slices. NFS wird bereits als administrativ gemountetes Cache-Backend eingesetzt.

## Produktiv ausgerollter Fortschritt 2026-07-19

- Geräte-API v2 ist lokal implementiert: einmaliges Enrollment, Ed25519-signierte Requests, persistente Nonce-/Rate-Limit-Prüfung, Browser-Einmalcode und gerätegebundene kurzlebige Benutzertokens.
- `/admin/clients` zeigt reale Geräte und das aktuelle authentisierte Inventar. Admins können Funde explizit zuordnen oder deaktivierte Spiel-/Versionsentwürfe erzeugen; Viewer/Operator bleiben read-only.
- SQLite-Schema v17 enthält dauerhafte `inventory_catalog_mappings` mit Request-Fingerprint, separate `inventory_catalog_version_mappings` je erkanntem Build, persistente Cache-Aufträge mit erhaltener Historie nach dem Löschen einer Quelle, normalisierte Release-Artefaktverweise, unveränderliche SHA-256-Artefaktmetadaten, monotone Event- und Clientupdate-Releases sowie den niemals sinkenden Clientversions-Höchststand je Gerät. Release-Aktivierung ist eine eigene ausdrückliche Transaktion; fehlende oder abweichende Artefakte blockieren die Veröffentlichung atomar.
- Der portable Windows-Client kann ein vollständig mit DPAPI CurrentUser integritätsgeschütztes Geräteprofil enrollen, per Browser anmelden, Steam-/EA-App-/Ubisoft-Connect-Installationen erkennen, eine Vorschau anzeigen und das bestätigte Inventar synchronisieren. Windows-x64 kompiliert erfolgreich.
- Eine gestaltete Wails-2.13-Windows-GUI bindet Enrollment, lokale Suche, Einzelauswahl, Browserfreigabe und eine zweite ausdrückliche Synchronisationsbestätigung an diesen Kern. Ein portables ZIP und ein NSIS-per-user-Setup mit eigenem Icon und WebView2-Evergreen-Prüfung werden lokal reproduzierbar gebaut; die `0.1.0`-Artefakte sind noch nicht Authenticode-signiert.
- Der CAS schreibt Downloads zunächst unter `/cache/.tmp`, prüft Größe und SHA-256, synchronisiert die Datei, setzt Modus 0400 und verschiebt sie atomar nach `/cache/sha256/<präfix>/<digest>`. Vor Wiederverwendung und authentisierter HEAD-/GET-Ausgabe mit genau einem Range wird die vollständige Datei erneut verifiziert. Serielle persistente HTTPS-/WebDAV-Aufträge besitzen SSRF-, DNS-, Redirect- und Credential-Policy, harte 1-TiB-Quota, Fortschritt, Abbruch, Retry und Neustart-Requeue. Ein unterbrochener Auftrag beginnt nach Neustart derzeit wieder bei Byte 0; HTTP-Resume des Workers ist noch nicht implementiert. Verbrauch und Quota sind in der Katalog-UI sichtbar; Admins können unreferenzierte Artefakte mit mindestens 24 Stunden Gnadenfrist manuell und idempotent bereinigen. Referenzen, laufende Jobs und eine bei verifizierter Wiederverwendung erneuerte Gnadenfrist werden unmittelbar vor dem Entfernen erneut geprüft. Langsame NFS-Operationen halten keine SQLite-Verbindung; partielle Läufe bleiben als solche abrufbar. Ein Scheduler und die allgemeine Bereinigung alter Ingest-Temporärdateien fehlen noch, und die Quota zählt registrierte Artefaktmetadaten statt beliebiger Orphan-/Temp-Dateien.
- Der EA-App-Adapter liest seit Commit `f40e314` reale installierte Spiele aus EA-Games- und Uninstall-Registrydaten, verwendet ausschließlich eine eindeutige Content-ID und Spielversion aus dem größenbegrenzten `__Installer/installerdata.xml` und unterstützt UTF-8 sowie UTF-16LE/BE. Hilfsprogramme, Steam-Pfade, Netzwerk-/Device-Pfade und instabile ID-Fallbacks werden nicht inventarisiert; Fremdwerte können durch Feldgrenzen nicht den gesamten Scan blockieren. Vollständige Go-Tests, Windows-Cross-Build, unabhängiges P1-Re-Review und ein echter Windows-Scan sind grün. Auf dem Test-PC wurden sieben eindeutig identifizierbare EA-Spiele mit ID, Version und Pfad erkannt. Battlefield Bad Company 2 bleibt mit Warnung ausgelassen, weil das Manifest zwei gleich plausible IDs (`bfbc2_dd`, `bfbc2_le`) enthält; eine Edition zu erraten wäre nicht scanstabil.
- Der lokale Release-Service validiert die verbindlichen JSON-Schemata, doppelte JSON-Felder, Ed25519-Key-ID/Signatur, Release-Semantik, Gültigkeitsfenster und CAS-Referenzen. Geräte erhalten exakt aktivierte Event-Envelopes und stabile Clientupdate-Envelopes authentifiziert; Bootstrap und die schreibenden Geräteendpunkte sperren bei unterschrittener Mindestversion. Laufzeitversion und Höchststand werden getrennt gespeichert, sodass ein Downgrade nicht durch einen älteren Scan legitimiert werden kann.
- `lanready-release` erzeugt Schlüssel ohne Überschreiben, akzeptiert Private Keys nur als reguläre 0600-Datei und signiert/verifiziert Event- oder Update-Payloads gegen dieselben Schemata. Der spätere Browser-Slice ergänzt einen getrennten Online-Event-Key im netzwerklosen Signer; der Webserver mountet weiterhin keinen Private Key. Die Events-UI bietet Releasehistorie, spätere Aktivierung, signierten Rollback und weiterhin Vorschau/Veröffentlichung separat offline signierter Clientupdates. Die zum Update-Envelope gehörende Authenticode-signierte portable EXE kann direkt über die Admin-UI in den CAS gestreamt werden; ein 1-GiB-Limit, exakte Content-Length, MIME-, Größen- und SHA-256-Prüfung sowie die erneute Release-Service-Prüfung sperren unvollständige Veröffentlichungen. Für Nginx Proxy Manager sind `client_max_body_size 1g` und `proxy_request_buffering off` dokumentiert.
- Der Windows-Client besitzt lokal einen Self-Update-Ablauf: authentifizierter Resume-Download mit sichtbarem Fortschritt/Abbruch, eingebetteter Ed25519-Keyring, strikte Envelope-/SemVer-/Sequenzprüfung, signierter Artefakttyp und Updater-Protokollstand, Größen-/SHA-256-Prüfung, gepinnter Authenticode-Zertifikatsfingerprint, Apply-Handshake, Cross-Process-Lock, atomarer Austausch und automatische Wiederherstellung, falls die neue GUI keinen Frontend-Ready-Health-Marker schreibt. Die geschützte Profilsequenz wird erst nach erkanntem Marker durch den Updater festgeschrieben. Release-Key und erwarteter Publisher werden beim Build in die GUI eingebettet; Private Keys sind aus Git und Docker-Buildkontext ausgeschlossen.
- `scripts/build-windows-release.ps1` ist der fail-closed Releasepfad: übereinstimmende Binär-/Windows-Dateiversion, SHA-256-Authenticode für Client/Uninstaller/Installer, RFC-3161-Zeitstempel, Windows-Policyprüfung und Zertifikatsfingerprintvergleich. Ohne Zertifikat/Fingerprint verweigert `make windows-package` die Ausgabe.
- Die installierte Variante registriert lokal einen `LIMITED` per-user Task-Scheduler-Start. Der versteckte Agent prüft Updates alle 30 Minuten, zeigt Windows-Benachrichtigungen, öffnet bei zweitem Start die vorhandene GUI und bleibt beim Fensterschließen aktiv; ein ausdrücklicher Einstellungsbutton beendet ihn für die Sitzung. Portable Builds registrieren nichts.
- Lokale Gates nach dem Slice: vollständige Go-Tests, `go vet`, Race Detector für Store/Webadmin/Device-API/Deviceclient und Windows-x64-Build grün. Echtes Chrome-QA der Clientseite bei 1440×1000 und 390×844 einschließlich geöffnetem Importformular ohne horizontalen Seitenüberlauf.
- Noch nicht als MVP abgenommen: reale Windows-11-Tests der drei Launcheradapter und des Task-Scheduler-/Toast-Ablaufs, Installations-/Updateorchestrierung für Launcher/Spiele sowie ein Ende-zu-Ende-Self-Update mit öffentlich vertrauenswürdig Authenticode-signiertem und zeitgestempeltem Release. Das produktive Serverbinary läuft auf `32cbb67`; `LANREADY_VERSION` wurde passend aktualisiert. Vollständige Tests, Vet, relevante Race-Detector-Läufe und unabhängige P0/P1-Reviews sind grün. Nach dem Deployment wurden laufende Version, Container-Health, read-only Root-Filesystem, NPM-Netz, NFS-Mount, SQLite-Konsistenz und der öffentliche HTTPS-Healthcheck geprüft. Die destruktive GC-Mutation wurde produktiv bewusst nicht gegen reale Daten ausgelöst; API, Idempotenz, Partial-Result, Rollen und Race-Semantik sind durch Integrationstests abgedeckt. `LANREADY_AUTHENTICODE_PUBLISHER_SHA256` bleibt bis zu einem echten Codesigning-Zertifikat absichtlich leer, sodass Clientupdates fail-closed bleiben.
- Der NFS-Cachepfad `192.168.200.200:/mnt/S01/Shares/GameManager` ist produktiv per `/etc/fstab` als NFSv4.2-Hard-Mount unter `/mnt/lanready-cache` eingebunden. Der Container bindet ihn auf `/cache`; eine exakte `.lanready-cache-volume`-ID lässt den Server bei fehlendem oder falschem Volume vor jedem Cachezugriff fail-closed abbrechen. TrueNAS begrenzt Share 1 auf Host `192.168.220.39`, mappt User und Gruppe vollständig auf den gesperrten Account `lanready-nfs` (UID/GID 3001) und verwendet `SYS`. Das POSIX1e-Dataset mit `aclmode=DISCARD` gibt UID 3001 RWX und UID 3000 nur Read/Traverse. Reale Tests auf dem Export belegen Create/Write/Fsync/Read/Range/atomaren Rename/Neustartverifikation/GC sowie die Abwehr statischer Symlink-Ausbrüche; die administrativen Rechte der Mountwurzel bleiben unverändert. Der produktive Start wurde zusätzlich mit falscher Volume-ID und ohne echten Mount fail-closed geprüft. `lanready-compose.service` erzwingt Mount-Reihenfolge und Recreate mit dem NPM-Override. Source- und Env-Backups vor der Umstellung liegen unter `/home/ubuntu/lanready/backups/`.
- Der Windows-Client wertet nun das vom Geräte-Bootstrap gemeldete aktive Event aus: kanonischer authentisierter Releaseabruf ohne Redirects, eingebettete JSON-Schemata, Ed25519-Keyring, Semantik, Event-ID, Gültigkeitsfenster, Mindestclientversion und monotone Sequenz werden fail-closed geprüft. Die gestaltete Hauptansicht zeigt Event, Fortschrittsring, erforderliche/optionale Launcher und Spiele, lokale Versionsabweichungen sowie klar getrennte Warn-/Sicherheitszustände. Launcher-Versionen bleiben konservativ als „Version ungeprüft“ markiert, solange nur ein zugehöriger Spielefund vorliegt.
- Event-Watermarks werden vollständig im DPAPI-CurrentUser-Profil behalten, vor einem atomaren dauerhaften Dateiaustausch geflusht und durch normale Clientlogik niemals eviziert. Absichtliches Löschen oder Zurückspielen des gesamten Benutzerprofils bleibt als lokale Anti-Rollback-Grenze dokumentiert und benötigt für weitergehenden Schutz einen späteren serverseitigen gerätebezogenen Höchststand. Eine gemeinsame 2-MiB-Grenze wird bereits vor dem Replace geprüft, sodass ein übergroßer Stand das lesbare Profil nicht beschädigt.
- Spieleerkennung läuft nicht mehr unter dem globalen App-Mutex. Status- und manuelle Erkennung besitzen feste Zeitlimits; ein festhängender Registry-/UNC-Lauf wird als einzelner Hintergrundlauf dedupliziert und blockiert weder UI noch Folgeaktionen unbegrenzt. Regressionstests decken Timeout/Mutex-Freigabe, Event 128→129 ohne Watermark-Verlust, Rollbackablehnung und Profilgrößenfehler ab.
- Das unabhängige Event-Readiness-Review endete nach P1-/P2-Nacharbeit mit PASS und null offenen P0/P1/P2. Vollständige Go- und Node-Tests, Go Vet sowie Windows-x64-Cross-Build für CLI und Wails-GUI sind grün. Der Race-Detector war nach dem Discovery-Refactor grün; der letzte reine Profilgrößen-Test änderte keine Parallelitätslogik. Auf der Docker-VM wurde wegen 99 Prozent Root-Belegung ausschließlich ungenutzter Build-Cache und dangling Images bereinigt; Produktionscontainer, Volumes und benannte Images blieben unangetastet.

## Produktiv ausgerollt – Spiele ohne Launcher (20.07.2026)

- SQLite v18 ergänzt `standalone` in Inventar- und Zuordnungstabellen, migriert vorhandene Items/Katalog-/Versionszuordnungen verlustfrei und legt den unveränderlichen Systemtyp **Ohne Launcher** an. Ein Konflikt mit einem bereits benutzten Slug/Adapter bricht die Migration ab, statt fremde Katalogdaten zu überschreiben.
- Die Management-UI kann Spiele diesem Typ zuordnen; der Server erzwingt deren Slug als stabile externe Spiel-ID. Der Systemtyp kann nicht dupliziert, umbenannt, deaktiviert oder gelöscht werden und erscheint nicht als Ziel für Launcher-Versionen.
- Die signierte Geräte-API `GET /v2/device/standalone-games` liefert nur aktive kataloggebundene Einträge. Der Windows-Client wählt das Spiel aus dieser Liste und die Haupt-EXE über den nativen Windows-Dateidialog. UNC-Pfade, gemappte Netzlaufwerke, Windows-Gerätenamen, Alternate Data Streams und freie unbekannte Einträge werden vor Datei-I/O abgewiesen.
- Katalog-ID, externe ID, Anzeigename und EXE-Pfad liegen im bereits DPAPI-CurrentUser-geschützten Profil. Die Discovery mischt erreichbare Registrierungen mit Steam/EA/Ubisoft-Funden; fehlende Dateien erzeugen eine Warnung. Nur vorhandene Windows-VERSIONINFO-Daten gelten als erkannte Version, sonst bleibt der Fund ausdrücklich unverifiziert.
- Persönlich bestätigte Inventarsynchronisation und signierte Event-Bereitschaft akzeptieren `standalone`. Ein Standalone-Spiel benötigt keinen Launcher-Release; exakte Versionsbereitschaft bleibt erforderlich und der Event-Target-Root ist auf `user_games` begrenzt.
- Lokale Gates: vollständige Go- und Node-Tests, Go Vet, Race Detector für Store/Device-API/Deviceclient/Protocol/Windowsapp, Windows-amd64-GUI-Cross-Build und Chrome-Layoutprüfung bei 1440×900 sind grün. Zwei zuvor fest bis 20.07.2026 gültige Release-Testfixtures wurden zeitstabil gemacht. Das unabhängige P1-Re-Review endete nach Pfad-/Identitäts-/Protokollhärtung mit PASS und null offenen P0/P1/P2.
- Produktionsdeployment `32cbb67`: Container healthy, Binaryversion passend, Schema v18 vorhanden, `quick_check` und `foreign_key_check` ohne Befund. Admin-Login, Katalogseite, Katalog-API, fester `standalone`-Launcher und Logout wurden authentifiziert Ende-zu-Ende durch Nginx Proxy Manager/TLS geprüft.
- Vollständiger Rollback-Snapshot einschließlich SQLite-WAL/SHM: `/home/ubuntu/lanready/backups/rollback-standalone-32cbb67`; vorheriges Image: `lanready-server:rollback-pre-32cbb67`. Das alte Image `90e8b64` startete isoliert und healthy gegen eine Kopie der produktiven v18-Datenbank. Der ältere Pfad `/home/ubuntu/lanready/backups/pre-standalone-32cbb67-main-only-incomplete` enthält nur eine wegen damaligem WAL-Betrieb unvollständige Hauptdatei und darf allein nicht zur Wiederherstellung benutzt werden.

## Produktiv ausgerollt – Bulkübernahme, Bezugsplattform und stabile portable Identität (20.07.2026)

- Deployment `ad9d9b5`: Unter **Clients** können Administratoren bis zu 100 offene Inventarfunde eines aktuellen Scans auswählen und atomar übernehmen. Ein Fehler rollt die gesamte Auswahl zurück; ein persistenter Idempotenzschlüssel schützt Wiederholungen nach unklaren Netzantworten. Manuell registrierte Standalone-Spiele werden ausschließlich mit ihrem vorhandenen Katalogspiel verknüpft und können keine zweite Identität erzeugen.
- Der Katalog trennt nun **Bezugsplattform** und optionales **LANReady-Paket**. Spielversionen von Steam, EA App und Ubisoft Connect dürfen ohne HTTPS-/WebDAV-Paketquelle geführt und Events zugeordnet werden. Standalone-Versionen benötigen weiterhin zwingend eine Paketquelle. Das reale Anstoßen von Launcher-Installationen/-Updates bleibt ein eigener noch offener Adapter-Slice.
- Die portable GUI verwendet unabhängig vom EXE-Ordner das DPAPI-CurrentUser-Profil unter `%APPDATA%\LANReady\device.json`; bei defektem `%APPDATA%` wird ein absoluter Pfad unter `%USERPROFILE%\AppData\Roaming` verwendet. Alte relative Profile werden begrenzt in aktuellem Verzeichnis sowie Downloads/Desktop/Documents gesucht und migriert. Mehrere inhaltlich abweichende Profile, Symlinks und relative Zielpfade brechen fail-closed ab, damit Serverbindung und Anti-Rollback-Watermarks nicht geraten oder zurückgesetzt werden.
- Der generische „Profil zurücksetzen“-Knopf nach einem beliebigen Statusfehler wurde entfernt. Eine vorübergehende Server-/Profilstörung löscht den Geräteschlüssel nicht mehr; Trennen bleibt eine ausdrückliche Aktion in den Einstellungen.
- Vollständige Go- und Node-Tests, Linux-/Windows-Cross-Build, `gofmt`, `go vet`, Race Detector sowie unabhängiges Review endeten mit null offenen P0/P1/P2. Produktion: Container healthy, Binaryversion `ad9d9b5`, SQLite `quick_check=ok`, keine Foreign-Key-Verletzungen, Schema v18, Härtung/NPM-Netz/Cache-ID und öffentliches HTTPS geprüft; Admin-Login, Clients, Katalog und neues Clients-JavaScript authentifiziert HTTP 200.
- Rollback: konsistentes SQLite-Onlinebackup und Konfiguration unter `/home/ubuntu/lanready/backups/pre-ad9d9b5-20260720`; vorheriges Image `lanready-server:rollback-pre-ad9d9b5`.
- Portable Test-GUI: `C:\Users\Eluminare\Downloads\LANReady-Portable-Test-ad9d9b5\LANReady.exe`, Laufzeitversion `0.1.0-test.ad9d9b5`, SHA-256 `8b87ce4ccc8b8dcec3e59f5682853895b2e2654bfacb514b87e66b0c32b2a166`. Die Datei ist weiterhin bewusst nicht Authenticode-signiert.

## Betriebs- und Benutzerdokumentation (20.07.2026)

- `docs/README.md` ist der zentrale Einstieg; separate Handbücher beschreiben Installation/Upgrade, Web-UI/Windows-Client sowie Backup/Restore/Rollback. Die README verweist verbindlich darauf und kennzeichnet noch fehlende Launcher-Installationsorchestrierung, Benutzer-/Rollen-UI, SSO und produktive Codesignatur ausdrücklich.
- Die Installation verlangt eine HTTPS-Origin und dokumentiert Nginx Proxy Manager, rootful/rootless Docker, systemd/NFS-Bootreihenfolge, lokalen und externen Cache sowie portable und per-user installierte Windows-Anwendung. NFS-Sentinel und Schreibprobe laufen nach einer unmittelbaren Mountpoint-Assertion als exakt der effektive Compose-Benutzer.
- Backup/Restore sichert SQLite konsistent, löst alle vier Secret-Quelldateien aus der effektiven Compose-Konfiguration auf, archiviert das alte Serverimage per `docker image save` und behandelt externen CAS, Konfiguration und Datenbank als gemeinsamen Stand. Restore stoppt fail-closed, arbeitet mit einem vollständig geprüften Staging-Datenbaum, erhält alte Daten/Secrets/Konfiguration, mischt keine Verzeichnisstände und startet erst nach Image-, Mount-, Sentinel- und Schreibprüfung.
- `scripts/lanready_secrets.py` sichert und restauriert auch außerhalb von `./secrets` konfigurierte reguläre Dateien fail-closed und atomar. `scripts/lanready_compose_paths.py` ermittelt kanonisch die realen `/data`-/`/cache`-Bind-Quellen und unterscheidet Cache innerhalb von `data` von einem externen Mount. `scripts/verify_docs.py` prüft erforderliche Handbücher, lokale Links, Markdown-Anker und Repository-Grenzen.
- `make test` enthält nun `docs-check`. Die Python-Tests decken fehlende Links/Anker, Pfadausbruch, abweichende externe Cachepfade, Cache innerhalb von `data`, vier externe Secretpfade, geänderte Restoreziele und den Fehlerfall eines fehlenden Backup-Secrets ab.
- Das unabhängige P1-Betriebsreview endete nach drei Nacharbeitsrunden mit PASS und null offenen P0/P1/P2. Es fand unter anderem zuvor gefährliche HTTP-, NFS-, Secret-, Restore- und Image-Rollback-Beispiele; diese sind im integrierten Stand geschlossen.

## Produktiv ausgerollt – Event-Spielversionsauswahl (20.07.2026)

- Commit `c3ef4be` behebt zwei gekoppelte UI-Fehler: Event- und Versionsselect wurden bei jedem Render mit Auswahl-ID 0 neu aufgebaut, wodurch beispielsweise Counter-Strike 2 auf den ersten Eintrag Age of Empires zurücksprang. Außerdem wurden bereits dem gewählten Event zugeordnete Versionen nicht aus dem Dropdown gefiltert.
- Die Auswahl bleibt nun über Render, vollständiges Load und Cache-Poll erhalten. Zugeordnete Versionen verschwinden ausschließlich aus der Auswahlliste des jeweiligen Events und bleiben für andere Events verfügbar. Eventwechsel filtert sofort neu; bei leerer Auswahl sind Select und Zuordnen-Button deaktiviert. Auch nach Zuordnung der letzten verfügbaren Version aktiviert das `finally` den Button nicht irrtümlich wieder.
- `catalog_assignment_helpers.js` kapselt Auswahlretention, eventbezogene Filterung und Submitentscheidung. Drei Node-Regressionstests decken CS2 statt erstem Eintrag, eventbezogene Sichtbarkeit und die letzte verfügbare Version ab. Asset-Route, Embed, CSP-freundliche Ladefolge, Dockerfile, Makefile und Go-HTTP-Assettest wurden ergänzt.
- Lokale Node-/Dokumentations-/Python-Gates und unabhängiges P1-Re-Review sind grün, null offene P0/P1/P2. Ein isolierter Docker-Build aus dem sauberen `git archive` führte auf der Ubuntu-VM sämtliche Node- und Go-Tests sowie Linux-/Windows-Builds erfolgreich aus.
- Vollständiger Vorab-Rollbackstand: `/home/ubuntu/lanready/backups/pre-c3ef4be-20260720T193841Z`, einschließlich SQLite/Daten, Secrets, Konfiguration, altem Image `lanready-server:rollback-pre-c3ef4be`, vollständigem 3,9-GB-CAS und Prüfsummen.
- Produktion läuft auf Binary/Image `c3ef4be`: Container healthy, Schema v18, SQLite `quick_check=ok`, null Foreign-Key-Verletzungen, read-only Root-Filesystem, `cap_drop: ALL`, NPM-Netz, NFS-Mount/Sentinel und öffentliche HTTPS-Healthantwort geprüft. Neue Helper-/Katalogassets sind öffentlich erreichbar; der authentifizierte Events-Bereich liefert HTTP 200. Die getrackte Produktions-Quellkopie entspricht dem Candidate, während Laufzeitdaten und Secrets beim Sync ausgeschlossen blieben.
- Der darauf folgende Browser-Signierslice erzeugt Event-Payloads aus Katalogzuordnungen, signiert sie mit einem getrennten Online-Event-Key über einen netzwerklosen Unix-Socket-Signer und veröffentlicht sie atomar; Aktivierung ist eine bewusste, standardmäßig abgewählte Option. Providerverwaltete Steam-/EA-/Ubisoft-Versionen verwenden keine Fake-Artefakte und verlangen Client `>=0.2.0`. Standalone- und paketbasierte Zuordnungen werden bis zum Windows-Installations-Slice pro Spiel blockiert. Im realen Event betrifft das FlatOut 2 und WC3 TFT. Beide zeigen zusätzlich mit unterschiedlichen Dateinamen auf denselben Digest und Blob; nicht automatisch korrigieren.
- Aktueller uncommitted Nacharbeitsstand: Paket-/Standalone-Aktionen sind zusätzlich zentral in Publish und Activate gesperrt; Aktivierung ab `0.2.0` verlangt ein gültiges Stable-Clientupdate und passende Laufzeitversionen aller aktiven PCs. Update- und Event-Keyrings sind zweckgebunden. Der alte Offline-Public-Key bleibt nur als Legacy-Event-Verifikationskey erhalten; Rollbacks werden mit dem Online-Event-Key und Mindestclient `0.2.0` neu signiert. Build, Serverstart und Backup lehnen identische Online-/Offline-Keys ab. Lokale Doku-/Python-/JS-/Compose-Gates sind grün; Go-/Container-/Windows-Candidate, Commit/Push und Deployment fehlen wegen des Codex-Ausführungslimits. Produktion bleibt `c3ef4be`.
