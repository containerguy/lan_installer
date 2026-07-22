# LANReady-Dokumentation

Stand: 20.07.2026 · geprüft ab Anwendungsstand `ad9d9b5` / Schema v18

Diese Seite ist der Einstieg für Betrieb, Administration und Nutzung. `handover.md` ist dagegen ein Entwicklungs- und Deploymentprotokoll und ersetzt kein Betriebshandbuch.

| Zielgruppe | Dokument | Inhalt |
|---|---|---|
| Betreiber | [Installation und Aktualisierung](installation.md) | Managementserver, Nginx Proxy Manager, NFS-Cache, Windows-Client, Upgrade und Deinstallation |
| Betreiber | [Backup und Restore](backup-restore.md) | Sicherungsumfang, konsistente Sicherung, Wiederherstellung, Rollback und Restore-Test |
| Administratoren und Teilnehmer | [Benutzerhandbuch](user-guide.md) | Web-UI, Quellen, Katalog, Clients, Inventar und Windows-GUI |
| Entwickler und Abnahme | [MVP-Abnahmekriterien](MVP_ACCEPTANCE.md) | verbindlicher Funktions- und Qualitätsumfang |
| Neue Arbeitssession | [Session Memory](../SESSION_MEMORY.md) | kompakter secretsfreier Produktionsstand, Entscheidungen und nächste Schritte |
| Entwickler | [Architekturentscheidungen](architecture/) | Managementbasis und Secret-Speicherung |
| Entwickler | [API- und Domänenverträge](contracts/README.md) | API v1/v2, Rollen, Schemas und Windows-Komponenten |

## Dokumentationsregeln

- Befehle mit `compose.npm.yaml` gelten für den Betrieb hinter Nginx Proxy Manager. Die Override-Datei darf bei Recreate, Upgrade und Restore nicht weggelassen werden.
- Geheimnisse, `.env`, Datenbank, Cache und private Signaturschlüssel werden niemals eingecheckt.
- Pfade und Benutzernamen in Beispielen müssen vor Ausführung an den Zielhost angepasst werden.
- Ein Backup gilt erst nach einem erfolgreichen Restore-Test als belastbar.
- Nicht implementierte Produktfunktionen werden ausdrücklich als Grenze genannt. Insbesondere stößt der aktuelle Stand noch keine realen Steam-/EA-/Ubisoft-Installationen an.
