# Verbindliche v2-Vertragschecks

Status: Slice-0-Gate. Jeder Endpunkt wird in seinem Implementierungsslice als Integrationstest umgesetzt; Schema-/Signatur-/Semantikchecks laufen bereits in `internal/protocol`.

## Bootstrap und Update

- Authentisiertes Bootstrap mit aktivem Event: 200, Schema grün, Releasepfad gehört zur Event-ID.
- Kein aktives Event: 200 mit `activeEvent: null`; Client zeigt Leerzustand und meldet niemals `Bereit` für ein Event.
- Zu alter Client: 426 nach `upgradeRequired`-Schema, kein Eventpayload, nur fester Same-Origin-Pfad zum signierten Update-Envelope.
- Unbekannter Key, ungültige Signatur, falscher Digestpfad, Redirect, unbekannter Artefakttyp/Updater-Protokollstand, abweichender Authenticode-Zertifikatsfingerprint oder Sequenz-Rollback blockieren Update und Event.
- Updateartefaktprüfung auf Windows bindet signierte Größe/SHA-256, Windows-Dateiversion, gültigen RFC-3161-Zeitstempel und erwartetes Signerzertifikat. Ein inkompatibles Artefakt ohne Apply-Handshake beendet den laufenden Client nicht.
- Health-Timeout, konkurrierender Apply-Prozess und fehlgeschlagener Profilcommit erhalten die alte EXE und erhöhen die geschützte Update-Sequenz nicht.

## Enrollment

- Management erstellt mit Adminrolle einen 130-Bit-Code mit zehn Minuten TTL; DB enthält nur Hash und Metadaten, der Klartext erscheint nur in der 201-Antwort. Operator und Viewer erhalten 403 samt Audit-Eintrag.
- Gültiger Code + neuer Ed25519-Key liefert genau einmal 201 nach `enrollResponse`-Schema.
- Zwei echte parallele DB-Transaktionen konsumieren denselben Code: exakt eine 201, exakt eine 409; es entsteht genau ein Device und keine halbe Schlüsselzuordnung.
- Ungültig, abgelaufen, widerrufen und rate-limitiert aktivieren kein Device. Re-Enrollment ersetzt kein aktives Device still.

## Event-Publish

- Envelope und Payload erfüllen Schema; Signatur wird gegen Golden-Vektor geprüft.
- Publish validiert alle Digest-, Launcher-, Game-, Adapter- und Actionreferenzen vor der Sequenzvergabe.
- Fehlendes Artefakt, fehlender Launcher, doppelter Identifier, falscher Adapter, unsicherer Pfad oder unzulässige Operation/Payload-Kombination: kein Release und keine neue Sequenz.
- Rollback erzeugt eine neue höhere Sequenz und mutiert keine vorhandene.

## Sources

- Test speichert weder Source noch Secret; Logs und Fehler sind redigiert.
- Save mit gebundenem Testtoken schreibt Source und verschlüsseltes Secret in einer Transaktion; jeder simulierte Fehler lässt beide Tabellen unverändert.
- SSRF-, Redirect-, DNS-Rebinding-, TLS-, Timeout- und Oversize-Negativfälle aus `api-v2.md` laufen gegen einen kontrollierten Testserver.
