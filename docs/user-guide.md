# Benutzerhandbuch

Stand: 20.07.2026 · geprüft ab Anwendungsstand `ad9d9b5` / Windows-Testbuild `0.1.0-test.ad9d9b5`

LANReady besteht aus der Management-Weboberfläche und einer Windows-Anwendung. Die Weboberfläche verwaltet Quellen, Spiele, Versionen, Events und Clients. Die Windows-Anwendung erkennt lokale Installationen und synchronisiert sie nach persönlicher Bestätigung.

## 1. Wichtige Produktgrenzen

- LANReady speichert keine Steam-, EA- oder Ubisoft-Zugangsdaten.
- DRM, Lizenzen und Launcher-Anmeldung bleiben beim jeweiligen Anbieter.
- Der aktuelle Stand erkennt Installationen und verwaltet Bereitschaft, stößt aber noch keine realen Steam-/EA-/Ubisoft-Installationen oder -Updates an.
- Ein Launcher-Spiel kann über seine Bezugsplattform geführt werden. Eine zusätzliche HTTPS-/WebDAV-Paketquelle ist optional.
- Ein Spiel **Ohne Launcher** benötigt für eine durch LANReady verteilbare Version eine eigene Paketquelle.
- Unsigned Testbuilds sind keine produktive Self-Update-Freigabe.

## 2. Anmeldung und Navigation

Die Haupt-URL öffnet den geschützten Adminbereich. Der initiale Benutzername wird bei der Installation über `LANREADY_WEB_ADMIN_USER` festgelegt. Das Bootstrap-Passwort liegt auf dem Server in `secrets/web-admin-password.txt` und soll nicht per Chat oder unverschlüsselt weitergegeben werden.

Navigation:

- **Dashboard**: Einstieg und Systemüberblick;
- **Quellen**: externe HTTPS-/WebDAV-/Nextcloud-WebDAV-Ziele;
- **Katalog**: Launcher, Spiele und Versionen;
- **Events**: Eventzuordnungen und signierte Releases;
- **Clients**: Geräte, Enrollment und synchronisiertes Inventar.

Rollen sind technisch vorbereitet: Admin verwaltet alle Kernobjekte, Operator bearbeitet Katalogobjekte und Cacheaufträge sowie unveränderte Verbindungstests, Viewer liest. Der aktuelle Bootstrap beginnt mit einem Admin. Eine UI zum Anlegen weiterer Benutzer oder Zuweisen der Rollen und SSO/Entra ID sind noch nicht implementiert.

## 3. Externe Quelle anlegen

1. **Quellen** öffnen und **Quelle hinzufügen** wählen.
2. Typ, Anzeigename und HTTPS-Basis-URL eintragen.
3. Für WebDAV beziehungsweise Nextcloud den passenden WebDAV-Pfad und bei Bedarf Basic-/App-Zugangsdaten ergänzen.
4. **Verbindung testen** ausführen.
5. Erst nach erfolgreichem Test speichern.

Passwörter werden nie wieder im Browser angezeigt. Sie liegen verschlüsselt in der dedizierten SQLite-Tabelle `webdav_source_config`; der Schlüssel liegt außerhalb der Datenbank als Docker Secret. Ein fehlgeschlagener Test speichert weder Quelle noch neue Credentials.

Interne Ziele sind standardmäßig gesperrt. Wenn eine private Nextcloud-Adresse benötigt wird, muss der Betreiber Host und Ziel-CIDR explizit über `LANREADY_SOURCE_PRIVATE_ALLOWLIST` freigeben.

## 4. Launcher und Spiele verwalten

Steam, EA App und Ubisoft Connect sind initial vorhanden. **Ohne Launcher** ist ein geschützter Systemtyp für kopierte oder manuell installierte Spiele.

Für ein Launcher-Spiel:

1. Unter **Katalog → Spiele** Namen, Launcher und externe Spiel-ID prüfen oder anlegen.
2. Unter **Spiel-Versionen** die erkannte beziehungsweise gewünschte Version anlegen.
3. Bei **Bezugsplattform / LANReady-Paketquelle** entweder **Über Steam/EA App/Ubisoft Connect beziehen** wählen oder optional ein eigenes Paket aus einer externen Quelle konfigurieren.
4. Entwurf prüfen und erst dann aktivieren.

Die externe ID muss zum Launcher passen. Für Steam ist das beispielsweise die App-ID. LANReady rät keine IDs und verknüpft unbekannte Spiele nicht automatisch.

Für ein kopiertes Spiel:

1. Unter **Spiele** den Typ **Ohne Launcher** wählen.
2. Einen dauerhaften Slug festlegen; er wird als externe Identität verwendet und kann später nicht umbenannt werden.
3. Eine Version mit externer Paketquelle und relativem Paketpfad anlegen.
4. Auf jedem Windows-PC das Spiel wie im Abschnitt „Manuelles Spiel“ lokal mit seiner Haupt-EXE verbinden.

## 5. Cache verwenden

Bei Versionen mit LANReady-Paketquelle kann ein Admin oder Operator einen Cacheauftrag einplanen. Zustände:

- **Wartet/Läuft**: Download ist geplant oder aktiv;
- **Verifiziert**: Größe und SHA-256 wurden geprüft und das Artefakt liegt im CAS;
- **Fehlgeschlagen**: Ursache lesen, Quelle/Pfad korrigieren und Retry ausführen;
- **Nicht erforderlich**: Launcherverwaltete Version ohne eigenes LANReady-Paket.

Abbruch und Retry wirken auf persistente Aufträge. Ein Serverneustart erhält den Auftrag, ein unterbrochener Quelldownload beginnt derzeit jedoch wieder bei Byte 0. Die Garbage-Collection entfernt nur ausreichend alte, unreferenzierte Artefakte und muss ausdrücklich durch einen Admin gestartet werden.

## 6. Windows-PC verbinden

1. In der Web-UI **Clients** öffnen.
2. **Enrollment-Code erzeugen** wählen. Der Code gilt zehn Minuten und nur einmal.
3. `LANReady.exe` unter demselben Windows-Benutzer starten, der das Programm später verwendet.
4. **PC verbinden** wählen, Server-URL und Code bestätigen.

Das Geräteprofil liegt DPAPI-geschützt unter `%APPDATA%\LANReady\device.json`. Portable Updates in anderen Ordnern benutzen dieses Profil weiter. Bei einer vorübergehenden Serverstörung darf das Profil nicht gelöscht werden. **Verbindung trennen** in den Einstellungen löscht den lokalen Geräteschlüssel bewusst; danach ist ein neues Enrollment nötig und der alte Servereintrag bleibt zunächst als historisches Gerät sichtbar.

## 7. Installierte Spiele suchen und synchronisieren

1. Im Client **Spiele suchen** wählen.
2. Funde und Warnungen prüfen. Angezeigt werden Launcher, externe ID, erkannte Version und Installationspfad.
3. Gewünschte Spiele auswählen.
4. **Auswahl synchronisieren** wählen.
5. Im geöffneten Browser den Einmalcode beziehungsweise die Gerätefreigabe bestätigen.
6. Die Zusammenfassung im Client ein letztes Mal bestätigen.

Erst danach ersetzt der neue Scan atomar das sichtbare Inventar dieses Geräts. Das Benutzerpasswort wird nicht an die Windows-Anwendung übertragen; das persönliche Zugriffstoken bleibt nur im Arbeitsspeicher.

## 8. Manuell kopiertes Spiel hinzufügen

Voraussetzung: Ein Admin hat das Spiel bereits als **Ohne Launcher** im Katalog angelegt.

1. Im Windows-Client **Spiel manuell hinzufügen** wählen.
2. Das vom Server angebotene Katalogspiel auswählen.
3. Über den nativen Dateidialog die lokale Haupt-EXE wählen.
4. Erneut suchen und den Fund synchronisieren.

LANReady akzeptiert keine freie unbekannte Identität, keine UNC-/Netzlaufwerk-EXE und keine Windows-Gerätepfade. Fehlt eine auslesbare Windows-Dateiversion, bleibt die Version ausdrücklich unverifiziert.

## 9. Inventarfunde im Management übernehmen

Unter **Clients** zeigt jeder PC seinen letzten authentisierten Scan.

Einzelübernahme:

- einem bereits vorhandenen, kompatiblen Spiel zuordnen;
- oder bei Launcher-Spielen einen deaktivierten Spiel-/Versionsentwurf anlegen;
- eine später neu erkannte Version separat als Entwurf übernehmen.

Mehrfachübernahme:

1. bis zu 100 offene Einträge eines Clients auswählen;
2. Anzahl und Aktionen prüfen;
3. Sammelübernahme bestätigen.

Die Operation ist atomar: Ist ein Eintrag veraltet oder ungültig, wird nichts aus der Auswahl gespeichert. Eine wiederholte Übertragung nach unklarer Netzantwort ist idempotent. Standalone-Funde werden ausschließlich ihrem bereits existierenden Katalogspiel zugeordnet und erzeugen keine Duplikate.

## 10. Signiertes Event-Release veröffentlichen

Die Eventzuordnung allein wird noch nicht an Clients ausgeliefert. Der aktuelle Stand besitzt noch keinen Generator, der aus Katalog und Zuordnungen automatisch einen initialen `event-release.json`-Kandidaten baut. Ein Payload für vollständig im CAS vorhandene Pakete wird deshalb entsprechend dem verbindlichen Schema manuell erstellt, im netzwerklosen Signer signiert und direkt wieder geprüft:

```bash
docker compose --profile tools run --rm signer sign-event \
  -in event-release.json \
  -out event-envelope.json \
  -private-key release-private.key

docker compose --profile tools run --rm signer verify-event \
  -in event-envelope.json \
  -public-key release-public.key
```

Danach unter **Events → Signiertes Event-Release veröffentlichen** ausschließlich `event-envelope.json` auswählen, Vorschau und Key-ID prüfen und entscheiden, ob die neue Sequenz sofort aktiviert werden soll. Der Server prüft Signatur, Key-ID, Schema, Event-ID, monotone Sequenz, Gültigkeitsfenster und alle CAS-Referenzen atomar. Bei einem Fehler wird nichts veröffentlicht oder aktiviert.

Bekannter P1-Blocker: Das derzeitige Release-Schema verlangt für jedes Spiel mindestens ein Artefakt-Payload. Eine über Steam, EA App oder Ubisoft Connect geführte Version ohne eigenes LANReady-Paket kann daher noch nicht sinnvoll veröffentlicht werden, obwohl sie im Katalog einem Event zugeordnet werden darf. Keine Dummy-Artefakte eintragen. Dieser Vertragskonflikt und der fehlende Kandidatengenerator müssen vor der MVP-Abnahme geschlossen werden.

## 11. Events und Bereitschaft

Ein Event ordnet konkrete Spielversionen als erforderlich oder optional zu. Nur aktive und konsistente Abhängigkeiten können zugeordnet werden. Der Windows-Client zeigt das aktive signierte Event, erforderliche Launcher/Spiele und lokale Versionsabweichungen.

Statusbeispiele:

- **Bereit**: geforderte Spielversion wurde lokal passend erkannt;
- **Aktion erforderlich**: Spiel fehlt oder Version weicht ab;
- **Version ungeprüft**: Launcher ist erkennbar, seine eigene Version aber nicht zuverlässig bestimmt;
- **Sicherheitsfehler**: Signatur, Sequenz, Schema oder Gültigkeitsfenster wurde abgelehnt.

Der reale Installations-/Updateknopf für Launcher-Spiele ist noch nicht implementiert. Ein Eventstatus ist daher derzeit eine verifizierte Inventar-/Bereitschaftsanzeige, keine Garantie, dass LANReady das Spiel selbst installieren kann.

## 12. Portable und installierte Variante

Portable Variante:

- EXE in einen lokalen Ordner kopieren und direkt starten;
- kein Autostart und keine Installation;
- Profil bleibt trotzdem zentral im Windows-Benutzerprofil.

Installierte Variante:

- per-user Installation ohne Systemdienst;
- Programm unter `%LOCALAPPDATA%\Programs\LANReady`;
- Task-Scheduler-Start mit eingeschränkten Benutzerrechten;
- Schließen blendet einen laufenden Agenten aus; in Einstellungen kann er für die Sitzung beendet werden.

Vor dem Start einer neuen portablen EXE eine alte Instanz vollständig beenden, weil der Single-Instance-Schutz sonst das alte Fenster aktiviert.

## 13. Häufige Probleme

### Die neue EXE verlangt erneut einen Code

- prüfen, ob die alte EXE noch läuft;
- unter `%APPDATA%\LANReady\device.json` prüfen, ob ein Profil existiert;
- das Profil nicht vorschnell löschen;
- bei einem konkreten Profilfehler den Wortlaut an den Administrator geben.

LANReady migriert frühere relative Profile aus typischen Ordnern fail-closed. Werden mehrere unterschiedliche alte Profile gefunden, muss ein Administrator entscheiden; die Anwendung wählt keine möglicherweise ältere Anti-Rollback-Sequenz.

### EA-Spiel fehlt

EA App einmal starten und Installation reparieren/validieren. LANReady übernimmt nur eindeutige Content-IDs und Versionen aus begrenzten Registry-/Manifestdaten. Mehrdeutige Editionen werden ausgelassen statt geraten.

### Quelle ist nicht erreichbar

HTTPS-Zertifikat, URL und WebDAV-Pfad prüfen. Interne IP-Ziele benötigen eine exakte Allowlist. Redirects zu einer anderen Origin erhalten keine Credentials.

### Cacheauftrag schlägt fehl

Fehlercode in der Katalog-UI lesen, NAS-Mount und freien Speicher prüfen, Sentinel-ID mit `.env` vergleichen und erst danach Retry wählen.

### Eventbereitschaft ändert sich nicht

Im Client erneut nach Spielen suchen und Inventar synchronisieren. Danach Eventstatus neu laden. Prüfen, ob im Event genau die erkannte Katalogversion zugeordnet und aktiviert ist.

## 14. Hilfeinformationen für Fehlermeldungen

Bei einer Störungsmeldung niemals Tokens, Passwörter, WebDAV-Secrets oder private Schlüssel mitsenden. Hilfreich sind:

- Server- und Clientversion;
- Geräte-ID, aber kein Private Key;
- genauer Zeitpunkt und Aktion;
- sichtbarer Fehlertext;
- Launcher, externe Spiel-ID und erkannte Version;
- relevanter Containerstatus beziehungsweise redigierte Logs.
