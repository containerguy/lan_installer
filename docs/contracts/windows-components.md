# Windows-Komponenten- und UX-Vertrag

Status: verbindlicher Slice-0-Entwurf

## Portable und installiert

`LANReady.exe` bietet dieselbe gestaltete GUI in beiden Modi. Portabel bedeutet keine Installation und keinen Autostart, nicht zwingend zustandslos. Geräteidentität und Einstellungen liegen pro Benutzer unter `%AppData%\LANReady` und werden mit Windows DPAPI CurrentUser geschützt.

Die installierte Variante ergänzt einen per-user Task-Scheduler-Start bei Anmeldung. Es gibt keinen dauerhaften Windows-Systemdienst.

## Prozesse

- GUI: Eventauswahl, Bereitschaft, Zustimmung, Fortschritt, Fehlerbehebung und Status.
- User Agent: gleiche Kernbibliothek, Hintergrundprüfung im Benutzerkontext, Benachrichtigungen und persistente Warnung nach abgelehntem Update.
- Elevation Helper: nur bei konkret bestätigter Launcher-/Installeraktion, beendet sich danach; keine allgemeinen Shellbefehle.
- Updater: prüft Ed25519-Metadaten, Sequenz, SemVer, signierten Artefakttyp/Protokollstand, Größe/Digest sowie Windows-Authenticode gegen den eingebetteten Zertifikatsfingerprint. Ein Apply-Handshake verhindert das Beenden für inkompatible EXEs. Ein Cross-Process-Lock schützt die einzige Sicherung; erst ein Frontend-Ready-Signal schreibt die geschützte Sequenz fest. Bei fehlendem Signal wird die letzte Version atomar wiederhergestellt.

## Kernablauf

1. Server verbinden und API-Kompatibilität prüfen.
2. Falls nötig: einmaligen Enrollment-Code eingeben; lokales Ed25519-Schlüsselpaar erzeugen und privaten Schlüssel DPAPI-geschützt speichern.
3. Aktives Event und signierte Sequenz laden; Signatur, Gültigkeit, Sequenz und Mindestclient prüfen.
4. Spiele und erforderliche Launcher mit Speicherbedarf und Status anzeigen.
5. Kopierte oder manuell installierte Spiele werden ausschließlich an ein zuvor vom Admin angelegtes aktives Katalogspiel des festen Typs `standalone` („Ohne Launcher“) gebunden. Der Benutzer wählt dessen reguläre lokale `.exe` über den nativen Windows-Dateidialog. Die Registrierung mit Katalog-ID, Slug, Anzeigename und EXE-Pfad liegt DPAPI-CurrentUser-geschützt im Geräteprofil; UNC-Pfade, gemappte Netzlaufwerke, Windows-Gerätenamen, Alternate Data Streams und freie unbekannte Katalogeinträge sind unzulässig. Eine vorhandene Windows-Dateiversion wird übernommen, andernfalls bleibt die Version ausdrücklich unverifiziert.
6. Benutzer wählt Spiele. Erforderliche Komponenten sind klar gekennzeichnet.
7. Für jeden fehlenden Launcher Wahl zwischen `unattended`, sofern verifiziert, und `manuell bestätigen`. Ablehnung bleibt möglich und erzeugt eine verständliche persistente Warnung.
8. Downloads zeigen Gesamt-/Einzelfortschritt, Quelle, Geschwindigkeit, Restzeit, Pause/Fortsetzen und Abbrechen. Teilstände bleiben wiederaufnehmbar.
9. Import/Installation erfolgt mit expliziter Zustimmung. UAC wird nur für die konkrete Aktion angefordert.
10. Erkennung und Hash-/Launcherprüfung bestimmen den finalen Zustand `bereit`, `Warnung` oder `Aktion erforderlich`.
11. Authentisierter Status wird an den Managementserver gemeldet.

## Fehler- und Sicherheitsregeln

- Keine Speicherung oder Erfassung von Steam-, EA- oder Ubisoft-Zugangsdaten.
- Nicht dokumentierte oder nicht praktisch verifizierte Silent-Installer fallen immer auf interaktive Installation zurück.
- Ablehnung, fehlende Lizenz, Launcher-Login oder Neustart werden nicht als technischer Fehler dargestellt.
- Ein Download überschreitet weder signierte Größe noch konfiguriertes Limit; abweichende Inhalte werden verworfen.
- Jede UI-Aktion ist mit Tastatur erreichbar; Fortschritt wird nicht nur über Farbe vermittelt.

## Keyring, API und Releasezustand

Die GUI enthält den v2-Keyring und die Golden-Vektoren aus `contracts/vectors`. Unbekannte oder widerrufene Releasekeys, v1-Manifeste und ältere Sequenzen im jeweiligen Event-/Updatekanal werden mit einem behebbaren Sicherheitsstatus abgewiesen. API 426 führt ausschließlich in den signierten Updateablauf. Ein optionales neueres Stable-Release wird ebenfalls angezeigt; der Download besitzt sichtbaren Bytefortschritt und kann abgebrochen beziehungsweise später per Range fortgesetzt werden.

Event-Sequenzstände liegen im DPAPI-CurrentUser-geschützten Geräteprofil. Der Client schreibt das Profil zunächst vollständig in eine neue Datei, erzwingt deren Flush und ersetzt die bisherige Datei mit einer dauerhaften atomaren Operation. Damit überlebt der Höchststand normale Neustarts und Stromausfälle. Ein einmal gespeicherter Höchststand wird durch normale Clientlogik nicht verworfen, auch nicht nach vielen späteren Events. Eine absichtliche Wiederherstellung oder Löschung des gesamten Benutzerprofils ist ausdrücklich keine lokal lösbare Anti-Rollback-Grenze und entspricht sicherheitstechnisch einer erneuten Geräteanmeldung. Schutz gegen einen Angreifer, der lokale Profilsicherungen zurückspielen kann, benötigt einen monotonen gerätebezogenen Höchststand auf dem Managementserver und ist ein eigener Hardening-Slice.

Registry- und Dateisystemerkennung läuft nie unter dem globalen App-Zustandsmutex. Statusprüfung und manuelle Erkennung haben feste Zeitlimits. Ein nicht abbrechbarer Windows-/UNC-Zugriff wird höchstens einmal im Hintergrund weitergeführt; weitere Aufrufe warten mit eigenem Zeitlimit auf denselben Lauf, statt zusätzliche blockierte Erkennungen zu starten. Die GUI zeigt den Timeout als Fehler und bleibt bedienbar.

## Objektive UX-Abnahme

Eine repräsentative Testperson muss ohne Anleitung folgende Aufgaben ohne kritischen Fehler abschließen: verbinden/enrollen, Spiele auswählen, manuelle oder verifizierte unbeaufsichtigte Installation wählen, erforderlichen Launcher ablehnen und Warnung verstehen, Ablehnung korrigieren, Download pausieren/fortsetzen/abbrechen und finalen Bereitschaftsstatus erklären. Geprüft werden Tastaturbedienung, 200-Prozent-Zoom, verständliche Fehler und Windows-11-Standardskalierung.

## Verbindliche Windows-Code-Signing-Entscheidung

Ein veröffentlichter LANReady-MVP-Build muss Authenticode-signiert und mit SHA-256/RFC-3161 zeitgestempelt sein. Bevorzugt wird Microsoft Artifact Signing mit `Public Trust`, sofern die Identitätsprüfung des Herausgebers möglich ist; Schlüsselmaterial bleibt dabei HSM-geschützt. Falls der Herausgeber dafür nicht berechtigt ist, ist ein öffentlich vertrauenswürdiges OV/EV-Code-Signing-Zertifikat mit hardware- oder HSM-geschütztem Private Key der Fallback. Unsigned oder nur privat/testsignierte Builds sind ausschließlich Entwicklungsartefakte und erfüllen die MVP-Abnahme nicht.

Release-Gate auf einem sauberen Windows-11-System mit Standardbenutzer: `signtool verify /pa /v` ist erfolgreich, Zeitstempel ist gültig, Portable- und Installer-Build zeigen denselben erwarteten Herausgeber, Smart App Control/SmartScreen erzeugt keinen Dialog „Unbekannter Herausgeber“, UAC nennt denselben Herausgeber, danach funktionieren Enrollment, Update und Rollback. Die Auswahl des konkreten Signing-Anbieters wird in Slice 8 anhand der nachgewiesenen Herausgeberberechtigung abgeschlossen; die Sicherheits- und UX-Anforderung ist hiervon unabhängig.

Primärquellen: Microsoft Learn „Artifact Signing trust models“, „What is Artifact Signing?“ und „SignTool“ (Abruf 2026-07-16).

## Kritische Fehlerzustände

- Ungültiger oder abgelaufener Enrollment-Code bleibt im Verbindungsdialog, erklärt den Grund und bietet neuen Code an; kein Geräteschlüssel wird serverseitig aktiviert.
- HTTP 426 zeigt „Clientupdate erforderlich“ und erlaubt ausschließlich die Prüfung des signierten Update-Envelopes samt Digestpfad.
- Unbekannter Key, ungültige Signatur, ältere Sequenz oder Digestabweichung zeigt einen blockierenden Sicherheitsstatus; weder Installation noch `Bereit` sind erreichbar.
- Unzureichender Speicher zeigt benötigte/freie Größe sowie „Auswahl ändern“ und „Speicher erneut prüfen“.
- Verbindungsabbruch bewahrt den verifizierten Teilstand. „Erneut versuchen“ setzt per Range fort; Digestprüfung läuft über das vollständige Artefakt. Erst danach kann die Vorbereitung weitergehen.
