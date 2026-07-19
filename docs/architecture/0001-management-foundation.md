# ADR 0001: Management-Grundlage

Status: angenommen (Slice 1)

## Verifizierte Anforderungen

- Verwaltung über eine Web-UI; zunächst ein lokaler Admin.
- Das Schema muss weitere Benutzer, Rollen und spätere Entra-ID-Identitäten zulassen.
- Rollen ab Start: `admin`, `operator`, `viewer`.
- Keine Speicherung von Steam-, EA- oder Ubisoft-Zugangsdaten.
- Quellen starten mit HTTPS, WebDAV und Nextcloud WebDAV; SMB/NFS folgen als extern eingehängte Quellen.
- Der Ubuntu-Managementserver darf Inhalte bedarfsgesteuert cachen und im LAN ausliefern.
- Der installierte Windows-Client läuft im Benutzerkontext und startet bei Anmeldung; kein Windows-Systemdienst.

## Entscheidung

Der bestehende Go-Prozess liefert eine serverseitig gerenderte Admin-Oberfläche und nutzt eine eingebettete SQLite-Datenbank. Das initiale Passwort wird ausschließlich aus einer Docker-Secret-Datei gelesen und mit Argon2id gespeichert. Sitzungen sind zufällige, serverseitig gehashte Tokens in `HttpOnly`-, `Secure`- und `SameSite=Strict`-Cookies. Zustandsänderungen benötigen CSRF-Tokens. Audit-Ereignisse werden persistent gespeichert.

Das Schema enthält bereits Benutzer, Rollen, externe Identitäten, Quellen, Launcher, Spiele, Spielversionen und Events. Fachliche CRUD-Oberflächen werden in Slice 2 ergänzt. WebDAV- und Nextcloud-App-Passwörter werden verschlüsselt in der dedizierten SQLite-Tabelle `webdav_source_config` gespeichert; der 256-Bit-Masterschlüssel bleibt als Docker Secret außerhalb der Datenbank.

## Sicherheitsgrenze

Der Ed25519-Signaturschlüssel wird nicht in diesen Webprozess aufgenommen. Der spätere Signer erhält eine separate, minimale Container- und Dateisystemgrenze. Bis dahin bleibt der vorhandene Offline-Signierweg bestehen.
