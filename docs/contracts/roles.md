# Rollenmatrix für das MVP

Status: verbindlicher Slice-0-Entwurf

| Fähigkeit | Admin | Operator | Viewer |
|---|:---:|:---:|:---:|
| Dashboard, Status und Audit lesen | ja | ja | ja |
| Launcher, Spiele und Versionen lesen | ja | ja | ja |
| Launcher, Spiele und Versionen anlegen/bearbeiten/deaktivieren | ja | ja | nein |
| Quellen und nicht geheime Verbindungsdaten lesen | ja | ja | ja |
| Quellen anlegen/bearbeiten/deaktivieren | ja | nein | nein |
| WebDAV-Benutzername ändern oder Passwort ersetzen | ja | nein | nein |
| Quellenverbindung testen | ja | ja | nein |
| Cache-Aufträge starten/abbrechen | ja | ja | nein |
| Events anlegen/bearbeiten | ja | ja | nein |
| Event validieren und Vorschau erzeugen | ja | ja | nein |
| Event veröffentlichen/rollbacken | ja | nein | nein |
| Clients und Geräte lesen | ja | ja | ja |
| Enrollment-Code erzeugen, Gerät sperren/löschen | ja | nein | nein |
| Benutzer und Rollen verwalten | ja | nein | nein |
| System-, Schlüssel- und Retentionseinstellungen | ja | nein | nein |

## Durchsetzung

- Rechte werden zentral pro Handler geprüft; ausgeblendete UI-Elemente sind kein Sicherheitsmechanismus.
- Direkte unberechtigte Requests liefern HTTP 403 und erzeugen einen Audit-Eintrag.
- Secrets werden nie an Listen-/Detailantworten oder HTML zurückgegeben. Die UI zeigt nur `nicht gesetzt` oder `verschlüsselt hinterlegt`.
- Der lokale Einzeladmin erfüllt das MVP. Weitere Benutzer und Entra-ID-SSO bleiben Post-MVP, die Matrix wird dennoch ab Beginn getestet.
