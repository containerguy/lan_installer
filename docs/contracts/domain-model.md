# Domänen- und Releasevertrag

Status: verbindlicher Slice-0-Entwurf

## Aggregate

### Source

Allgemeine Quelle mit stabiler ID, Name, Typ (`https`, `webdav`, `nextcloud_webdav`), HTTPS-Basis-URL, Aktivstatus und Zeitstempeln. WebDAV-Authentifizierung liegt ausschließlich in `webdav_source_config`; Quelle plus Protokollkonfiguration werden atomar gespeichert.

Eine Quelle kann nur gelöscht werden, wenn keine Version oder laufender Job sie referenziert. Andernfalls ist ausschließlich Deaktivierung erlaubt.

### ContentArtifact

Unveränderlicher Cacheinhalt. Identität ist `sha256:<64-hex>`; ein WebDAV-ETag ist nur Upstream-Metadatum und niemals Inhaltsidentität.

Pflichtfelder: Digest, exakte Bytegröße, lokaler CAS-Pfad, Medienart, ursprünglicher Dateiname, Erstellzeit und Prüfstatus. Ein Artefakt wird erst nach begrenztem Download, exakter Größen- und SHA-256-Prüfung atomar sichtbar. Temporäre Dateien sind keine Artefakte.

### Launcher und LauncherRelease

Launcher beschreibt Adapter und Anbieter. LauncherRelease fixiert Version, Installer-Artefakt, erkannte Installerart, verifizierte interaktive Argumente, optional verifizierte Silent-Argumente, erwartete Erkennungsmerkmale und Neustartverhalten.

### Game und GameRelease

Game beschreibt Titel, Launcher und externe Anbieter-ID. GameRelease ist unveränderlich und besitzt eine sichtbare Versionsbezeichnung sowie ein oder mehrere Payloads.

Ein Payload enthält:

Freie Befehle, Skripte, Shellstrings und beliebige Executable-Pfade sind verboten. Aktionen stammen ausschließlich aus den im JSON-Schema definierten Adapter- und Operations-Enums; Argumente sind getrennte normalisierte Strings und werden adapterbezogen allowlist-validiert.


- Typ: `launcher_library`, `archive`, `directory_tree` oder `installer`
- ein oder mehrere ContentArtifacts
- launcherbezogene Bibliotheks-/Depot-Metadaten
- relative Zielwurzel und erlaubte Zielpfade
- Installations-/Importstrategie
- Erkennungs- und abschließende Prüfregeln

### Event und EventRelease

Event ist der bearbeitbare Entwurf. EventRelease ist eine unveränderliche, signierte Veröffentlichung mit je Event-ID monoton steigender Sequenz, Event-ID, Release-ID, Erstellzeit, Gültigkeitsfenster, Mindestclientversion, LauncherReleases, GameReleases und ausschließlich validierten CAS-Digests.

Eine Änderung an einem veröffentlichten Event erzeugt einen neuen Entwurf und anschließend einen neuen EventRelease. Rollback bedeutet: Ein früherer vollständiger Inhalt wird als neuer EventRelease mit höherer Sequenz erneut aktiviert. Alte Sequenzen werden niemals überschrieben.

### Device

Authentisiertes Windows-Gerät mit zufälliger Server-ID, öffentlichem Ed25519-Geräteschlüssel, Anzeigename, Betriebssystem-/Clientversion, Status (`pending`, `active`, `revoked`), Enrollment- und Last-Seen-Zeit. Eine Geräte-ID allein ist keine Authentisierung.

### EnrollmentCode

Einmaliger, kryptographisch zufälliger Code mit Zweck, Ersteller, Ablaufzeit (höchstens zehn Minuten), maximal einem Verbrauch und gehashtem Speicherwert.

### Job

Persistenter Hintergrundauftrag für Quellentest, Download, Hashprüfung, Cache-Import, Garbage Collection oder Publish. Zustände: `queued`, `running`, `succeeded`, `failed`, `cancelled`. Fortschritt, verständlicher Fehlercode, redigierte Meldung, Retryzahl und Zeitstempel sind sichtbar. Secrets erscheinen weder in Status noch Audit.

### ClientRelease

Unveränderliche Windows-Veröffentlichung mit Version, Kanal, Binärdigest, Größe, Mindest-/Zielversion, Release-Sequenz, Downloadreferenz und signierten Update-Metadaten. Ein Client akzeptiert keine kleinere Sequenz als zuletzt erfolgreich installierte.

## Komponenten- und Vertrauensgrenzen

- Managementserver: UI, API, Datenmodell und Jobsteuerung; besitzt keinen Signaturschlüssel.
- Fetch/Cache-Worker: ausgehender Quellzugriff nach Netzwerkpolicy; schreibt nur temporär und in CAS.
- Signer: liest ausschließlich eine vollständig validierte Publish-Anfrage mit CAS-Digests, signiert und hat keinen allgemeinen Web-, Quellen- oder Datenbankzugriff.
- Windows-GUI/Agent: läuft im Benutzerkontext.
- Elevation Helper: kurzlebig, UAC-bestätigt, allowlist-basierte Installationsbefehle; kein dauerhafter Systemdienst. Die IPC akzeptiert nur strukturierte Operationen `install_verified_launcher`, `materialize_verified_tree` und `remove_lanready_temp` mit CAS-Digest, erlaubter Zielwurzel und Argumentarray; sie akzeptiert keine Shellzeile.
- Updater: minimale separate Grenze für signierte, atomare Clientupdates und Rollback.
