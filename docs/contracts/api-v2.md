# LANReady API v2

Status: verbindlicher Slice-0-Vertrag. `/v1/events/...`, `/v1/reports` und `/content/...` sind Legacy-MVP-Endpunkte und werden von v2-Clients nicht verwendet. Sie bleiben bis zum erfolgreichen v2-Client-Rollout lesbar und erhalten danach mindestens einen Releasezyklus Abkündigungsfrist.

## Gemeinsame Regeln

- Basis für Geräte: `/v2`; Management-JSON: `/admin/api/v1`.
- JSON ist UTF-8, maximal 1 MiB je Steueranfrage und sendet `Content-Type: application/json`.
- Antworten senden `LANReady-API-Version: 2` und `X-Request-ID`.
- Fehler: `{"code":"stable_code","message":"deutscher Text","fieldErrors":{},"requestId":"..."}`. Keine SQL-, Secret-, URL-Credential- oder Stacktrace-Ausgabe.
- Mutierende POSTs akzeptieren `Idempotency-Key` (UUID/ULID, 24 h je Actor+Route). Wiederholung liefert dieselbe fachliche Antwort.
- Änderbare Ressourcen tragen ganzzahlige `revision` und HTTP `ETag: "<revision>"`; Update/Delete erfordern `If-Match`, sonst 428, bei Konflikt 412.

## Managementauthentisierung

Browser-Sitzung, Rollenprüfung und CSRF-Doppelschutz gelten für jede Mutation. Viewer erhält 403. Rollen im Vertrag `roles.md` gelten auch bei direktem Request.

## Geräte-Bootstrap

`GET /v2/device/bootstrap` ist der einzige Einstieg eines enrollten v2-Clients und erfordert Geräteauthentisierung. Antwort `200`:

```json
{
  "serverTime": "2026-07-16T08:00:00Z",
  "apiVersion": 2,
  "capabilities": ["range-download", "status-v1", "client-update-v1"],
  "activeEvent": {"eventId": "kellerlan-2026", "releaseUrl": "/v2/events/kellerlan-2026/release"},
  "clientUpdate": {"required": false, "releaseUrl": "/v2/client/releases/latest?channel=stable"}
}
```

Ohne aktives Event ist `activeEvent: null`; das ist ein betrieblicher Leerzustand, kein Fehler. Ist die Clientversion unterhalb der serverseitigen Mindestversion, antwortet Bootstrap `426 client_update_required`, liefert kein Event und nennt ausschließlich `clientUpdate.releaseUrl` zum signierten Update-Envelope. Ein Client darf aus 426 weder eine beliebige URL öffnen noch direkt `Bereit` erreichen. Vertragschecks: Happy Path, `activeEvent: null` und 426 ausschließlich über den signierten Updatepfad.

## Sources

### Modell

```json
{
  "id": 12,
  "revision": 3,
  "name": "Nextcloud Hauptquelle",
  "kind": "nextcloud_webdav",
  "baseUrl": "https://cloud.example/remote.php/dav/files/lanready",
  "enabled": true,
  "auth": {"type": "basic", "username": "lanready-service", "secretState": "stored"},
  "lastTest": {"state": "succeeded", "testedAt": "2026-07-16T08:00:00Z", "latencyMs": 184}
}
```

`secretState` ist `missing` oder `stored`; ein Passwort wird nie zurückgegeben.

### Endpunkte

- `GET /admin/api/v1/sources?query=&kind=&enabled=&cursor=` → 200, paginierte Liste.
- `GET /admin/api/v1/sources/{id}` → 200 oder 404.
- `POST /admin/api/v1/sources/test` → 200 TestResult oder 422/504; persistiert nichts.
- `POST /admin/api/v1/sources` → 201; benötigt gültiges `testToken` für exakt dieselbe Konfiguration.
- `PUT /admin/api/v1/sources/{id}` → 200; `If-Match` und passendes `testToken`, falls Verbindungseigenschaften geändert wurden.
- `POST /admin/api/v1/sources/{id}/deactivate` → 200.
- `DELETE /admin/api/v1/sources/{id}` → 204 oder 409 `source_referenced` mit anonymisierten Referenzanzahlen.

Testeingabe:

```json
{
  "sourceId": 12,
  "name": "Nextcloud Hauptquelle",
  "kind": "nextcloud_webdav",
  "baseUrl": "https://cloud.example/remote.php/dav/files/lanready",
  "auth": {"type": "basic", "username": "lanready-service", "secretMode": "reuse", "password": null}
}
```

Bei neuer Quelle oder `secretMode=replace` enthält `password` das flüchtige App-Passwort. Es lebt nur für die Anfrage, wird nie geloggt und bei Testfehler nicht gespeichert. `reuse` ist nur mit vorhandener Source-ID erlaubt.

TestResult enthält `state`, `testedAt`, `latencyMs`, `capabilities`, redigierten `code/message` und bei Erfolg ein zufälliges, serverseitig gehasht gespeichertes `testToken`. Token: fünf Minuten, einmal für Save nutzbar, an Actor, normalisierte Felder und aktuelle Secret-Version gebunden. Jede Änderung an Name, Typ, URL, Benutzername, Secretmodus oder Passwort macht es ungültig. Source und verschlüsselte WebDAV-Konfiguration werden in genau einer DB-Transaktion gespeichert; Fehler hinterlassen keine Änderung.

## Management-Katalog

`GET /admin/api/v1/catalog` liefert Launcher, Spiele, Launcher-/Spielversionen, Events und Event-Zuordnungen einschließlich ihrer ganzzahligen `Revision`. Die Snapshot-Feldnamen bleiben für den bestehenden Browser-Client PascalCase.

- `POST /admin/api/v1/catalog/{kind}` und `POST /admin/api/v1/event-games` legen mit Revision 1 an; ein vom Browser stabil erzeugter `Idempotency-Key` verhindert persistiert doppelte Datensätze.
- `PUT /admin/api/v1/catalog/{kind}/{id}`, `POST .../{id}/deactivate` und alle Katalog-/Zuordnungs-`DELETE`s erfordern `If-Match: "<Revision>"`. Erfolgreiche Updates und Deaktivierungen erhöhen die Revision und liefern sie im Body sowie als `ETag`; fehlende Vorbedingung ergibt 428, eine veraltete Revision 412. Erfolgreiche Deletes antworten 204 ohne Body.
- Artefaktpfade sind normalisierte relative Slash-Pfade. Absolute Pfade, URI-/Drive-Syntax, Backslash, Query/Fragment, Prozent-Encoding, Control-Zeichen, Dot-Segmente und nicht normalisierte Varianten werden abgewiesen.
- `SilentArgs` ist eine serverseitig strukturierte Argumentliste mit `SilentArgsVerified`. Beide Felder sind in der Management-UI ausschließlich lesbar: Create erzwingt `[]`/`false`, Update erhält die gespeicherten Werte. Es existiert in diesem Slice bewusst kein öffentlicher Verifikationspfad und kein frei ausführbarer Argumentstring.
- Neue Event-Zeitwerte müssen RFC3339 mit Zeitzone sein. Sobald ein Event veröffentlicht ist, sind das Event, seine Zuordnungen und der von ihm referenzierte Launcher-/Spiel-/Spielversionsgraph unveränderlich.

## Enrollment und Geräteauthentisierung

`POST /admin/api/v1/enrollment-codes` erzeugt mit Adminrolle einen Code und antwortet `201 {"id":"01K0ENROLLMENT00000000001","code":"K7M4P-9Q2XR-T6V8W-3Y5ZA-BCDEFG","expiresAt":"2026-07-16T08:10:00Z","maxUses":1}`. Der Klartextcode wird nur in dieser Antwort gezeigt. `DELETE /admin/api/v1/enrollment-codes/{id}` widerruft einen noch unbenutzten Code mit 204; Listen geben niemals den Code selbst zurück.


`POST /v2/devices/enroll` erhält JSON nach `schemas/device-api.schema.json#/$defs/enrollRequest`: `code`, `publicKey`, `deviceName`, `windowsVersion`, `clientVersion`. `publicKey` sind exakt 32 rohe Ed25519-Public-Key-Bytes als 43 Zeichen base64url ohne Padding; PEM, DER, Padding und abweichende Längen werden abgewiesen. Erfolgreich antwortet es `201` mit `deviceId`, `serverTime`, `apiVersion: 2`, derselben `capabilities`-Liste wie Bootstrap und `bootstrapUrl: "/v2/device/bootstrap"`; der Code wird in derselben Transaktion konsumiert. Ein Code besitzt 26 zufällige Base32-Zeichen (130 Bit Entropie; gruppierte Darstellung 5-5-5-5-6), wird gehasht gespeichert, läuft nach höchstens zehn Minuten ab, ist transaktional genau einmal konsumierbar und ist pro IP/Code rate-limitiert. Zwei parallele Verwendungen ergeben genau einmal 201 und einmal 409. Re-Enrollment nach Schlüsselverlust erfordert Admin-Widerruf plus neuen Code; eine vorhandene aktive Device-ID wird nie still ersetzt.

Authentisierte Requests senden `LANReady-Device-ID` (kanonische UUID), `LANReady-Timestamp` (Unixsekunden als dezimale ASCII-Zahl), `LANReady-Nonce` (12 bis 32 Zufallsbytes als base64url ohne Padding) und `LANReady-Signature` (64 Ed25519-Signaturbytes als base64url ohne Padding). Andere Codierungen werden abgewiesen. Nonces haben mindestens 96 Bit Entropie, werden je Gerät bis Ablauf des Fünf-Minuten-Fensters persistiert und genau einmal akzeptiert.

### Kanonischer Request

- Methode: uppercase ASCII.
- Pfad: nur absolute API-Pfade; UTF-8-Segmente werden RFC-3986-percent-encoded mit uppercase Hex. Dot-Segmente, leere Segmente außer führendem Slash, Backslash und percent-encodete `/` oder `\\` werden abgewiesen.
- Query: Raw-Paare werden strikt percent-decodiert (`+` ist ein Plus, kein Leerzeichen), anschließend mit RFC-3986 unreserved `A-Z a-z 0-9 - . _ ~` und uppercase Hex neu encodiert. Sortierung nach encodiertem Namen, dann encodiertem Wert; Duplikate und Leerwerte bleiben erhalten. Ausgabe immer `name=value`, verbunden mit `&`. Ohne Query entfällt `?` vollständig.
- Bodydigest: lowercase SHA-256-Hex der exakt übertragenen Bytes; leerer Body nutzt SHA-256 des leeren Bytearrays.

Signierte Bytes:

```text
LANREADY-REQUEST-V1\n
<METHOD>\n
<PATH>[?<CANONICAL-QUERY>]\n
<BODY-SHA256>\n
<UNIX-SECONDS>\n
<BASE64URL-NONCE>
```

Die sprachunabhängigen Golden-Vektoren liegen in `vectors/signatures-v1.json`.

## Gerätestatus

`POST /v2/device/status` enthält eindeutige `statusId` (ULID), `clientRunId`, Event-ID/Sequenz, monotone gerätelokale `statusSequence`, Komponentenstatus und redigierte Fehlercodes. `(deviceId,statusId)` ist eindeutig. Duplikate ändern keine Zähler; kleinere `statusSequence` darf den sichtbaren neueren Zustand nicht überschreiben und liefert 202 `stale_accepted`.

## Persönliche Client-Anmeldung und Geräteinventar

Geräte-Enrollment und persönliche Anmeldung sind getrennte Vertrauensgrenzen. Enrollment authentisiert das Windows-Gerät; eine Inventarsynchronisation benötigt zusätzlich eine zeitlich begrenzte Benutzerfreigabe. Der native Client erhält und speichert niemals das Benutzerpasswort.

`POST /v2/user-device-authorizations` erfordert Geräteauthentisierung und erzeugt einen einmalig verwendbaren Browser-Autorisierungsauftrag. Die Antwort enthält eine zufällige `authorizationId`, einen kurz dargestellten `userCode`, `verificationUrl`, `expiresAt` und `pollIntervalSeconds`. Der Client öffnet `verificationUrl`, der Benutzer meldet sich über die bestehende Management-Anmeldung an und bestätigt dort das anfragende Gerät. Dieselbe Browserfreigabe kann später durch Entra ID erfüllt werden, ohne den nativen Client oder das Inventarprotokoll zu ändern.

`GET /v2/user-device-authorizations/{authorizationId}` erfordert dasselbe authentisierte Gerät und liefert bis zur Entscheidung `202 authorization_pending`, bei Ablehnung `403 authorization_denied`, bei Ablauf `410 authorization_expired` und nach Freigabe genau einmal ein kurzlebiges, an Benutzer und Geräte-ID gebundenes Zugriffstoken. Rohwerte von Autorisierungsauftrag, Code und Zugriffstoken werden serverseitig nur gehasht gespeichert. Polling oberhalb des vorgegebenen Intervalls wird mit `429` begrenzt.

`POST /v2/device/inventory-scans` erfordert Geräteauthentisierung sowie das gebundene Benutzer-Zugriffstoken als `Authorization: Bearer <token>`. Ein Scan besitzt `scanId`, `scannedAt`, `clientVersion` und eine vollständige Liste gefundener Installationen mit `launcher`, `externalGameId`, `displayName`, `detectedVersion`, `versionSource` und `installPath`. Zulässige Launcher sind zunächst `steam`, `ea_app` und `ubisoft_connect`. Unbekannte Versionen werden als `detectedVersion: null` mit begründetem `versionSource` übertragen; der Client erfindet keine Version. Pfade sind nur für berechtigte Managementbenutzer sichtbar und werden nie in Logs oder Auditdetails geschrieben.

Ein erfolgreicher Upload ersetzt das sichtbare Inventar dieses Geräts atomar und idempotent anhand `(deviceId,scanId)`. Er legt weder `games` noch `game_versions` an und verändert sie nicht.

`POST /admin/clients/catalog-import` ist eine separate HTML-Form-Mutation mit Management-Sitzung, CSRF und zwingender Adminrolle. Sie referenziert den aktuellen Fund exakt über `device_id`, `scan_id` und `position`. Modus `link` ordnet ihn einem vorhandenen Spiel desselben Launcheradapters und derselben beziehungsweise noch leeren externen ID zu. Modus `create` legt ein deaktiviertes Spiel und – sofern bekannt – eine deaktivierte Versionszeile mit Revision 1 an. Diese Versionszeile besitzt zunächst bewusst keine Quelle und keinen Quellpfad und wird im Katalog erst nach Auswahl beider Werte installierbar. Die Zuordnung wird eindeutig über `(launcher,external_game_id)` gespeichert, innerhalb derselben Transaktion auditiert und mit einem Request-Fingerprint gegen identische Doppelübermittlung sowie konkurrierende abweichende Aufträge abgesichert. Historische oder inzwischen ersetzte Scans sind nicht importierbar.

Das Spielmapping bleibt über spätere Scans bestehen. Versionszuordnungen liegen separat unter `(launcher, external_game_id, detected_version)`, sodass mehrere PCs gleichzeitig verschiedene Builds desselben Spiels korrekt abbilden. Der unveränderte Scan-Rohwert `detected_version` bleibt dabei vom frei benennbaren Katalogfeld `game_versions.version` getrennt. Erkennt der Client später eine andere Version, zeigt das Management sie als offen; erst Modus `version` legt nach einem weiteren ausdrücklichen Admin-Klick einen neuen deaktivierten Versionsentwurf an oder bindet eine bereits vorhandene exakt gleiche Version. Gemappte Spiele dürfen nicht auf einen widersprechenden Launcher/eine widersprechende externe ID geändert, gemappte Versionen nicht auf ein anderes Spiel verschoben und beide nicht still gelöscht werden.

## EventRelease und Sequenzen

- `GET /v2/events/{eventId}/release` → signierter Envelope.
- Sequenz ist monoton **pro Event-ID**. Der Client speichert die höchste Sequenz je Event. Ein expliziter Eventwechsel startet den separaten Scope des anderen Events.
- Rollback publiziert früheren Inhalt mit einer neuen höheren Sequenz.
- Envelope: `formatVersion=1`, `keyId`, `algorithm=Ed25519`, `payload` und `signature` als base64url ohne Padding. Signiert werden exakt die decodierten Payloadbytes; keine JSON-Kanonisierung findet beim Verifizieren statt.
- Zweistufige Validierung: Envelope-Schema, base64url/Signatur/Key, dann decodiertes Payload gegen `$defs.eventRelease` in `schemas/event-release-envelope.schema.json`.

### Verbindliche Publish-Validierung

Vor dem Signieren validiert der Server Schema **und** Semantik in einer Transaktion: jede `artifactDigest`- und Payload-Referenz muss genau einmal in `eventRelease.artifacts` existieren; jede Game-`launcherId` benötigt einen passenden LauncherRelease; Launcheradapter müssen zur Launcher-ID passen; Payloadtyp und Actionoperation müssen der diskriminierten Kombination im Schema entsprechen; doppelte Artifact-, Launcher- oder Game-IDs sind verboten. Ein Fehler blockiert Publish vollständig und erzeugt keinen Release/keine Sequenz. Negative Vertragschecks decken fehlende Artefakte, fehlende Launcher, falsche Adapter und unzulässige Operation/Payload-Kombinationen ab.

## Artefaktdownload

`HEAD|GET /v2/artifacts/sha256/{64-lowerhex}` erfordert Geräteauthentisierung. Antwort: `Content-Length` exakt erwartete Größe, `ETag: "sha256:<digest>"`, `Accept-Ranges: bytes`, `Content-Type` aus EventRelease. Kein Redirect.

Eine einzelne Range `bytes=start-end` wird unterstützt; mehrere/suffix/ungültige Range → 416 mit `Content-Range: bytes */<size>`. `If-Range` muss exakt dem Digest-ETag entsprechen, sonst 200 Vollantwort. Server streamt nie mehr als signierte Größe; Client bricht bei Überschreitung ab und prüft abschließend Größe+SHA-256. Tests: HEAD, Vollantwort, Resume, falsches If-Range, 416, Oversize und Digestabweichung.

## Clientupdate

`GET /v2/client/releases/latest?channel=stable` liefert den Update-Envelope. Sequenz ist monoton pro Kanal; MVP kennt nur `stable`. Zweistufige Schema-/Signaturprüfung wie EventRelease. Der signierte Payload bindet ausschließlich `artifactKind: portable_exe`, `updaterProtocol: 1` und den SHA-256-Fingerprint des erwarteten DER-Authenticode-Herausgeberzertifikats. Der Server akzeptiert Clientupdates nur, wenn dieser Fingerprint seiner expliziten Releasepolicy entspricht. Binärdownload: `GET /v2/client/artifacts/sha256/{digest}` mit denselben Range-/Längenregeln.

## Schlüsselvertrauen

Clients enthalten eine Keyring-Map aus `keyId` und Ed25519-Public-Key. `keyId = "ed25519-" + lowercase_hex(SHA256(raw_public_key)[0:8])`. Unbekannte, abweichende oder lokal widerrufene Keys werden abgelehnt.

Rotation: Ein Clientrelease muss den neuen Public Key bereits enthalten und erfolgreich ausgerollt sein, bevor der Signer ihn verwendet. Der alte Key bleibt für mindestens einen Releasezyklus verifizierbar. Notfallwiderruf erfolgt durch ein mit einem noch vertrauenswürdigen Key signiertes Keyring-Update oder, falls alle Onlinepfade kompromittiert sind, durch ein manuell bereitgestelltes Clientrelease. Die Seeds in `vectors/` sind absichtlich öffentlich und ausschließlich deterministische Testschlüssel; sie dürfen niemals als Produktionsschlüssel verwendet werden.

## Netzwerkpolicy für Sources

- ausschließlich HTTPS; Zertifikat und Hostname werden immer geprüft.
- Default: öffentliche Zieladressen. Private/Loopback/Link-local/Metadaten-Netze benötigen eine explizite Admin-Allowlist aus exaktem Host plus optionalen CIDRs.
- DNS wird für jede neue TCP-Verbindung aufgelöst; jede Ziel-IP muss die Policy erfüllen. Verbunden wird nur zu der geprüften IP, TLS-SNI bleibt der Host.
- höchstens drei Redirects; Credentials werden ausschließlich bei gleicher Origin (Scheme, Host, Port) weitergesendet. Redirects werden erneut auf DNS/IP-Policy geprüft.
- Verbindung 5 s, TLS 5 s, Header 10 s, Gesamttest 30 s; Testantwort höchstens 1 MiB. Downloadlimit ist die deklarierte erwartete Größe plus höchstens Protokolloverhead, nie unbeschränkt.
- Negativtests: Redirect auf verbotenen Host, Cross-Origin-Credential-Leak, DNS-Wechsel/Rebinding, Zertifikatsfehler, Timeout und Oversize.
