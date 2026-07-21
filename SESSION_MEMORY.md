# LANReady – Session Memory

Stand: 20.07.2026 · Branch `agent/lanready-mvp`

Diese Datei ist der kompakte, secretsfreie Einstieg für eine neue Codex-Session. Danach bei Bedarf [handover.md](handover.md), [docs/README.md](docs/README.md) und [docs/MVP_ACCEPTANCE.md](docs/MVP_ACCEPTANCE.md) lesen.

## Arbeitsregeln des Benutzers

- Keine Annahmen über Unbekanntes: zuerst Quellcode und lokale Umgebung prüfen, bei zeitabhängigen/externen Fakten primäre Onlinequellen verwenden, danach gezielt nachfragen.
- Große Themen in kleine, überprüfbare Slices zerlegen. Planung/Architektur mit höherwertigem Modell, klar begrenzte Slices nach Möglichkeit günstiger bearbeiten; Ergebnisse vor Integration prüfen.
- MVP bedeutet ein nutzbares Gesamtsystem mit funktionaler, ansprechender Management-UI und Windows-Anwendung – kein bloßes CRUD-/API-Gerüst.
- P0/P1 blockieren den betroffenen Slice. P0/P1-Planung, Sicherheit, Migration, Release und Deployment benötigen ein unabhängiges Review mit testbaren Befunden.
- Quellcode regelmäßig bewusst committen und nach GitHub pushen. Fremde/unabhängige Änderungen im Arbeitsbaum nicht überschreiben.

## Repository und Betrieb

- Lokales Repository: `/home/eluminare/lan_installer`
- GitHub: `git@github.com:containerguy/lan_installer.git`
- Aktiver Branch: `agent/lanready-mvp`
- Management-URL: `https://game-manager.familie-keller.info`
- Docker-VM: `ubuntu@192.168.220.39`
- Produktionspfad: `/home/ubuntu/lanready`
- Der Produktionspfad ist derzeit eine saubere Quellkopie **ohne `.git`**. Deployments deshalb über ein lokales `git archive` in ein isoliertes Remote-Buildverzeichnis testen und erst danach die getrackten Dateien in die Produktionskopie übernehmen. `.env`, `data`, `secrets`, `release-work`, `backups` und Cache niemals überschreiben.
- Nginx Proxy Manager übernimmt TLS und erreicht den Container über das externe Docker-Netz; Compose immer mit `compose.yaml` **und** `compose.npm.yaml` verwenden.
- Produktiver Cache: TrueNAS-NFS `192.168.200.200:/mnt/S01/Shares/GameManager`, auf der VM `/mnt/lanready-cache`, im Container `/cache`. Die Sentinel-Datei `.lanready-cache-volume` und die konfigurierte ID verhindern Writes auf einen fehlenden Mount. Quota: 1 TiB.
- Keine Zugangsdaten oder API-Keys in diese Datei, Git, Logs oder Chat-Antworten kopieren. Der früher im Chat genannte TrueNAS-Key ist als temporär/zu widerrufen zu behandeln und wurde nicht im Repository gespeichert.

## Verbindliche Produktentscheidungen

- Externe Inhalte initial über HTTPS, WebDAV und Nextcloud-WebDAV; SMB/NFS vorerst nur als administrativ gemountete externe Speicher.
- WebDAV-Konfiguration liegt in der dedizierten SQLite-Tabelle `webdav_source_config`; Passwörter sind mit einem externen Docker-Secret verschlüsselt und werden nie angezeigt.
- Initiale Launcher: Steam, EA App und Ubisoft Connect. `standalone`/„Ohne Launcher“ deckt manuell kopierte Spiele ab.
- LANReady speichert keine Launcher-Zugangsdaten und umgeht weder DRM noch Lizenzen.
- Windows-Client läuft portabel oder per-user installiert, niemals als Systemdienst. Das Geräteprofil liegt DPAPI-CurrentUser-geschützt unter `%APPDATA%\LANReady\device.json` und ist unabhängig vom EXE-Ordner.
- Benutzer dürfen vorgeschlagene Installation/Updates ablehnen und erhalten dann eine Warnung. Unattended oder manuelle Bestätigung soll später wählbar sein. Kein automatischer Snapshot vor Clientinstallationen.
- Client darf lokal cachen und später im selben Netz verteilen. P2P ist noch nicht implementiert.
- Ein Admin ist vorhanden. Rollenmodell existiert, aber Benutzer-/Rollen-UI und Entra-ID-SSO sind noch offen.
- Release-Schlüssel sind zweckgebunden: Der bisherige Offline-Key signiert Clientupdates und verifiziert zusätzlich historische Legacy-Events; ein nachweislich verschiedener Online-Event-Key signiert Browser-Events im netzwerklosen Signer-Container. Der Webserver erhält nur Public Keys und Unix-Socket. Der Online-Private-Key bleibt aus normalen Backups ausgeschlossen und benötigt ein verschlüsseltes Offlinebackup. Produktive Clientupdates bleiben ohne vertrauenswürdiges Authenticode-Zertifikat fail-closed.

## Implementierter Stand

- Management-UI auf der Haupt-URL: Dashboard, Quellen, Katalog, Events, Clients.
- Sichere HTTPS/WebDAV-/Nextcloud-Quellen einschließlich SSRF-/DNS-/Redirect-/Credential-Policy und verschlüsselter Secret-Tabelle.
- SQLite-Schema v18, Geräte-Enrollment, persönliche Browser-Einmalcode-Freigabe, authentisierte Inventarsynchronisation und stabile Geräteidentität.
- Windows-GUI erkennt Steam-, EA-App- und Ubisoft-Installationen; manuell kopierte Spiele werden kataloggebunden über eine lokale Haupt-EXE erfasst.
- Clients-Seite unterstützt atomare, idempotente Sammelübernahme von bis zu 100 Inventarfunden.
- Katalog trennt Bezugsplattform und optionales LANReady-Paket. Launcher-Spiele dürfen ohne eigene Paketquelle geführt werden; Standalone-Versionen benötigen eine externe Paketquelle.
- SHA-256-CAS, persistente Cachejobs, Retry/Cancel, Range-Downloads, Quota und manuelle Garbage Collection sind implementiert; NFS-Backend wurde real getestet.
- Signierte monotone Event- und Clientupdate-Releases, Anti-Rollback und lokaler Self-Update-Kern sind implementiert. Produktive Authenticode-E2E-Freigabe fehlt.
- Benutzer-, Installations-, Backup-/Restore- und Betriebsdokumentation liegt unter `docs/`. `make test` enthält Link-/Ankerprüfung sowie Tests für dynamische Secret- und Compose-Cachepfade.

## Aktueller Fix: Event-Spielversionen

- Commit `c3ef4be` (`Fix event game version selection`) ist auf `origin/agent/lanready-mvp` gepusht.
- Ursache: `refreshSelects()` baute Event- und Versionsselect bei jedem Render mit Auswahl-ID 0 neu auf. Deshalb sprang Counter-Strike 2 auf den ersten Eintrag (Age of Empires) zurück. Zugeordnete Versionen wurden nicht eventbezogen gefiltert.
- Fix: Auswahl bleibt über Render, Load und Cache-Poll erhalten; bereits dem aktuell gewählten Event zugeordnete Versionen verschwinden aus dessen Dropdown, bleiben für andere Events verfügbar; Eventwechsel filtert neu; leere Auswahl deaktiviert Select und Submit.
- Regressionshelper/-tests: `internal/webadmin/assets/catalog_assignment_helpers.js` und `.test.js`.
- Unabhängiges P1-Re-Review: PASS, null offene P0/P1/P2.
- Lokale Node-, Dokumentations- und Python-Tests sind grün. Der isolierte Docker-Candidate-Build auf der VM aus einem sauberen `git archive` bestand sämtliche Node- und Go-Tests sowie Linux-/Windows-Builds.
- Produktion läuft auf `c3ef4be`: Container healthy, exakte Candidate-Image-ID, SQLite `quick_check=ok`, null Foreign-Key-Verletzungen, Schema v18, read-only Root-Filesystem, `cap_drop: ALL`, NPM-Netz, NFS-Mount und Sentinel geprüft. Öffentlicher Healthcheck, neue Helper-/Katalogassets und authentifizierte Events-Seite sind über HTTPS grün.
- Vollständiger Rollbackstand vor dem Deployment: `/home/ubuntu/lanready/backups/pre-c3ef4be-20260720T193841Z`. Enthält Daten, Secrets, Konfiguration, altes Image `lanready-server:rollback-pre-c3ef4be`, kompletten 3,9-GB-CAS und `SHA256SUMS`.
- Die getrackte Produktions-Quellkopie wurde anschließend aus `/tmp/lanready-build-c3ef4be` synchronisiert; `.env`, `data`, `secrets`, `backups`, `release-work`, `dist`, `bin` und `.tmp-uiqa` waren ausgeschlossen. `LANREADY_VERSION=c3ef4be` ist gesetzt.

## Noch offene MVP-Schwerpunkte

1. Reale Installations-/Updateorchestrierung für Steam, EA App, Ubisoft Connect und Spiele einschließlich Benutzerbestätigung/Ablehnung.
2. Reale Windows-11-Ende-zu-Ende-Matrix für alle Launcheradapter, manuelle EXE, per-user Task Scheduler, Benachrichtigungen und Self-Update.
3. Öffentlich vertrauenswürdige Authenticode-Signatur und zeitgestempelter Releasebuild.
4. **In Umsetzung/Review:** Event-Releases vollständig im Browser erzeugen, mit getrenntem Online-Event-Key isoliert signieren, veröffentlichen und aktivieren. Providerverwaltete Steam-/EA-/Ubisoft-Versionen benötigen keine Fake-Artefakte und erzwingen Client `>=0.2.0`. Jede Aktivierung verlangt zusätzlich ein kompatibles, offline/Authenticode-signiertes Stable-Clientupdate und passende Laufzeitversionen aller aktiven PCs. Standalone- und paketbasierte Releases sind zentral in allen Publish-/Activate-Pfaden bis zum Windows-Installations-Slice gesperrt. Im produktiven Event betrifft dies FlatOut 2 und WC3 TFT. Beide verweisen zusätzlich mit unterschiedlichen Dateinamen auf denselben SHA-256/Blob; nicht automatisch korrigieren.
5. Benutzer-/Rollenverwaltung in der UI; Entra-ID-SSO danach als optionaler Slice.
6. Scheduler und Bereinigung verwaister/alter Ingest-Temporärdateien; Worker-HTTP-Resume beginnt derzeit nach Neustart wieder bei Byte 0.

## Windows-Testartefakt vor dem aktuellen UI-Fix

- Portable EXE: `C:\Users\Eluminare\Downloads\LANReady-Portable-Test-ad9d9b5\LANReady.exe`
- Version: `0.1.0-test.ad9d9b5`
- SHA-256: `8b87ce4ccc8b8dcec3e59f5682853895b2e2654bfacb514b87e66b0c32b2a166`
- Bewusst nicht Authenticode-signiert. Der aktuelle Event-Dropdown-Fix betrifft nur den Managementserver und benötigt keine neue Windows-EXE.

## Uncommitted Browser-Release-Slice

- Browserformular, Preflight, automatische Signatur, monotone Sequenzen, spätere Aktivierung und idempotenter signierter Rollback sind lokal implementiert.
- Online-Event-Key und Offline-Update-Key sind auf Server, Client, Backup und Build nach Zweck getrennt; der Offline-Public-Key bleibt als Legacy-Event-Verifikationskey erhalten. Identische Online-/Offline-Keys werden vor Build, Start und Backup abgelehnt.
- Lokale Dokumentations-, Python-, JavaScript-, Compose- und Diff-Prüfungen sind grün. Vollständige Go-Tests, Container-Candidate, Windows-GUI-Build, Commit/Push und Deployment fehlen noch, weil der Netzwerkzugriff auf die Docker-VM in dieser Session durch das Codex-Ausführungslimit blockiert wurde. Ohne diese Gates nicht deployen.
- Produktiv läuft weiterhin `c3ef4be`; neue Event-Key-Dateien wurden noch nicht auf den Server übertragen.
