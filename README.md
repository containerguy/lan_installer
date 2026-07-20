# LANReady

LANReady wird als selbst gehostete Plattform zum Vorbereiten von Windows-PCs für eine LAN-Party entwickelt. Der gestaltete Windows-Client kann einen PC sicher enrollen, installierte Spiele erkennen und eine vom Benutzer bestätigte Auswahl nach persönlicher Browser-Anmeldung an den Managementserver synchronisieren. Management-UI und Geräte-API veröffentlichen unveränderliche, offline signierte Event- und Client-Releases. Der Windows-Client lädt Pflichtupdates fortsetzbar, prüft Ed25519-Envelope, Sequenz, Version, Größe, SHA-256 sowie Windows-Authenticode und ersetzt sich mit automatischem Health-Check-Rollback.

LANReady speichert keine Launcher-Zugangsdaten und umgeht weder DRM noch Lizenzprüfungen. Kommerzielle Inhalte dürfen nur an Teilnehmer verteilt werden, die die erforderlichen Nutzungsrechte besitzen.

## Dokumentation

- [Dokumentationsübersicht](docs/README.md)
- [Installation und Aktualisierung](docs/installation.md)
- [Backup, Restore und Rollback](docs/backup-restore.md)
- [Benutzerhandbuch für Web-UI und Windows-Client](docs/user-guide.md)
- [MVP-Abnahmekriterien](docs/MVP_ACCEPTANCE.md)

Die README bietet Schnellstart und technische Referenz. Für produktive Installation, Wiederherstellung und tägliche Bedienung sind die verlinkten Handbücher verbindlich.

## Was der MVP enthält

- `LANReady.exe`: gestalteter portabler Windows-11-x64-Client für Enrollment, Erkennung und Inventarsynchronisation
- `LANReady-…-setup.exe`: per-user Installer ohne Systemdienst und ohne Administratoranforderung
- `bin/LANReady.exe`: vorübergehend weiterhin verfügbarer CLI-Client für den bisherigen Manifest-/Downloadablauf
- `lanready-server`: HTTP-API für Manifeste, Inhalte und Clientberichte
- `lanready-manifest`: Offline-Werkzeug für Ed25519-Schlüssel und signierte Manifeste
- Dockerfile mit getrennten Zielen für Server, Manifestwerkzeug und Binärartefakte
- `compose.yaml` für Managementserver und optionalen nginx-LAN-Mirror
- `windows/Start-LANReady.ps1` als Starthelfer für den vorübergehend getrennten CLI-Ablauf

Der Managementserver enthält eine serverseitige Admin-Web-UI mit lokalem Bootstrap-Admin, Argon2id-Passwortschutz, serverseitigen Sitzungen, CSRF-Schutz, Audit-Log und einem vorbereiteten Rollenmodell. Reports bleiben zusätzlich über die Admin-API abrufbar und werden in `data/reports.ndjson` gespeichert.

## Voraussetzungen

Für den empfohlenen Weg wird nur Docker mit Docker Compose benötigt. Go 1.26.5 oder neuer ist nur für eine lokale Entwicklung ohne Docker erforderlich.

Unter Linux sollten `LANREADY_UID` und `LANREADY_GID` in `.env` dem Besitzer des Projektverzeichnisses entsprechen. Die Werte liefern `id -u` und `id -g`. Unter Docker Desktop funktionieren normalerweise die Vorgaben `1000:1000`.

Bei Rootless Docker müssen beide Werte stattdessen `0` sein. Container-UID 0 wird dort auf den unprivilegierten Hostbenutzer abgebildet; eine Host-UID wie 1000 würde im Container-User-Namespace in einen untergeordneten UID-Bereich übersetzt und könnte den Bind-Mount nicht beschreiben.

## Schnellstart: Managementserver und Demo-Event

### 1. Umgebung konfigurieren

```bash
cp .env.example .env
openssl rand -hex 32
openssl rand -hex 32
mkdir -p secrets
mkdir -p data/cache
openssl rand -base64 32 > secrets/web-admin-password.txt
openssl rand -base64 32 > secrets/webdav-master-key.txt
chmod 600 secrets/web-admin-password.txt
chmod 600 secrets/webdav-master-key.txt
```

Die beiden Ausgaben als unterschiedliche Werte für `LANREADY_CLIENT_TOKEN` und `LANREADY_ADMIN_TOKEN` in `.env` eintragen. `.env` darf nicht veröffentlicht werden.

Das initiale Passwort für `LANREADY_WEB_ADMIN_USER` liegt ausschließlich in `secrets/web-admin-password.txt`. Es wird nur beim Anlegen der ersten Datenbank übernommen; spätere Änderungen der Secret-Datei ändern das bestehende Passwort nicht automatisch.

Der WebDAV-Masterschlüssel liegt ausschließlich in `secrets/webdav-master-key.txt`. Ohne diesen Schlüssel sind gespeicherte WebDAV-Passwörter absichtlich nicht entschlüsselbar; Datei und Backups müssen daher gemeinsam gesichert werden.

- `LANREADY_BIND_ADDRESS=0.0.0.0` macht Port 8080 im LAN erreichbar.
- Für einen öffentlichen Server hinter einem TLS-Reverse-Proxy sollte stattdessen `127.0.0.1` verwendet werden.
- `LANREADY_PUBLIC_URL` ist die von Windows-PCs erreichbare HTTPS-Haupt-URL, beispielsweise `https://game-manager.familie-keller.info`. Sie wird ausschließlich für den Browser-Einmalcode-Ablauf verwendet und muss zur Reverse-Proxy-URL passen.
- `LANREADY_TRUSTED_PROXY_CIDRS` enthält ausschließlich die mit `docker network inspect` verifizierten CIDRs des Nginx-Proxy-Manager-Netzes. Nur von dort wird `X-Forwarded-For` für persistente Enrollment-Rate-Limits ausgewertet.
- Der Client-Token sollte für jede Veranstaltung neu erzeugt werden.
- Private, Loopback- und Link-Local-Ziele sind für Quellentests standardmäßig gesperrt. Interne HTTPS-Ziele werden ausschließlich über `LANREADY_SOURCE_PRIVATE_ALLOWLIST=host=cidr,cidr;host=cidr` freigegeben; Hostname und CIDR müssen exakt passen.
- Der CAS liegt im Container immer unter `/cache`. `LANREADY_CACHE_HOST_PATH` bestimmt ausschließlich den Hostpfad; `./data/cache` ist der lokale Entwicklungsfallback. Das Verzeichnis muss für `LANREADY_UID:LANREADY_GID` beschreibbar sein. Die produktive Quota muss mit `LANREADY_CACHE_QUOTA_BYTES` ausdrücklich unterhalb der verfügbaren Volume-Kapazität gesetzt werden.

### Wie der Cache arbeitet

LANReady ist kein transparenter Steam-/EA-/Ubisoft-Proxy. Admin oder Operator planen in der Katalog-UI einen Cache-Auftrag für eine konkrete Launcher- oder Spielversion ein. Der einzelne persistente Worker lädt die konfigurierte HTTPS-/WebDAV-Quelle seriell und wendet dabei DNS-, SSRF-, Redirect- und Credential-Regeln an. Fortschritt, Abbruch, Retry und Neustartwiederaufnahme werden in SQLite geführt; ein Neustart setzt einen unterbrochenen Download neu auf, nicht byteweise fort.

Der Content-Addressed Store (CAS) schreibt zunächst eine private temporäre Datei unter `/cache/.tmp`, begrenzt den Download durch Quota und maximale Artefaktgröße, berechnet SHA-256 und prüft eine deklarierte Größe beziehungsweise Prüfsumme. Erst nach `fsync`, schreibgeschütztem Dateimodus und atomarem Rename wird das Artefakt unter `/cache/sha256/<erste-zwei-hexzeichen>/<sha256>` sichtbar und danach in SQLite registriert. Bereits vorhandene Artefakte werden vor Wiederverwendung vollständig verifiziert. Auch vor einer Clientausgabe wird Größe, regulärer Dateityp und SHA-256 erneut geprüft; Downloads unterstützen einen authentifizierten einzelnen HTTP-Range für Resume.

Die Quota basiert auf den in SQLite registrierten Artefaktgrößen. Admins sehen Verbrauch und Quota in der Katalog-UI und können unreferenzierte Artefakte kontrolliert nach einem Mindestalter von 24 Stunden entfernen; Operator und Viewer sehen den Status nur lesend. Katalog-, Release- und aktive Cachejob-Referenzen werden unmittelbar vor dem Löschen erneut transaktional geprüft. Eine erfolgreiche vollständige Verifikation oder CAS-Wiederverwendung erneuert die 24-Stunden-Gnadenfrist. Der GC ist idempotent, meldet partielle Läufe ausdrücklich und hält während langsamer NFS-Dateisystemoperationen keine SQLite-Verbindung. Eine automatische Zeitplanung und die allgemeine Bereinigung alter Ingest-Temporärdateien sind noch nicht implementiert; deshalb bleibt physische Reserve unterhalb der NAS-Kapazität erforderlich.

### Fail-closed Netzwerk-Cache

Bei einem produktiven NFS-/Netzwerkmount muss `LANREADY_CACHE_VOLUME_ID` gesetzt sein. Auf dem gemounteten Dateisystem liegt dazu eine reguläre Datei `.lanready-cache-volume`, deren getrimmter Inhalt exakt dieser ID entspricht. Fehlt der Mount, die Datei oder stimmt die ID nicht, startet der Managementserver nicht und schreibt nicht versehentlich in das lokale Mountpoint-Verzeichnis. Die administrativ gesetzten Rechte der Mountwurzel werden beim Start nicht verändert; nur die anwendungseigenen Verzeichnisse `.tmp` und `sha256` werden auf Modus 0700 gesetzt.

Beispiel für einen persistenten NFSv4.2-Mount auf dem Docker-Host:

```fstab
nas.example:/mnt/pool/LANReady /mnt/lanready-cache nfs4 rw,hard,vers=4.2,proto=tcp,sec=sys,_netdev,nofail,x-systemd.automount,x-systemd.mount-timeout=30s 0 0
```

Danach zeigen die nicht geheimen Umgebungswerte auf Mount und Sentinel:

```dotenv
LANREADY_CACHE_HOST_PATH=/mnt/lanready-cache
LANREADY_CACHE_VOLUME_ID=<eindeutige-volume-id>
LANREADY_CACHE_QUOTA_BYTES=1099511627776
```

Der Export darf bei einem aktiven Hard-Mount nicht umkonfiguriert werden. Vor Wartung Cache-Aufträge stoppen, Container beenden und den Mount aushängen; andernfalls sind längere RPC-Wartezeiten erwartbar.

Auf einem Host mit rootless Docker darf die Docker-Restartpolicy nicht die einzige Bootsteuerung sein: Ein bereits erstellter Bind-Mount kann sonst das lokale Verzeichnis festhalten, wenn Docker vor NFS startet. Für das dokumentierte Ubuntu-/NPM-Deployment liegt deshalb [deploy/systemd/lanready-compose.service](deploy/systemd/lanready-compose.service) bei. Vor der Installation müssen der Docker-User-Service aktiviert (`systemctl --user enable docker`) und für den Dienstbenutzer Linger eingeschaltet sein (`sudo loginctl enable-linger ubuntu`). Nginx Proxy Manager und LANReady müssen nachweislich über denselben rootless Socket und dasselbe externe Docker-Netz laufen. Der Systemdienst verlangt `/mnt/lanready-cache`, versucht bei noch nicht verfügbarem rootless Docker-Socket erneut zu starten und erzwingt nach erfolgreichem Mount eine Container-Neuerstellung mit `compose.yaml` **und** `compose.npm.yaml`. Dadurch bleibt die Verbindung zum externen Proxy-Netz auch nach einem Recreate erhalten. Benutzername, UID, Socket, Laufzeit-, Arbeits-, Mountpfad und Proxy-Netzname müssen vor Installation zum Zielhost passen.

Vor Wartung am NPM-Netz zuerst LANReady stoppen; andernfalls verhindert dessen Attachment möglicherweise das Löschen des externen Netzes. Bei einer NFS-Störung zunächst den NAS-Zugriff wiederherstellen: Prozesse an einem `hard`-Mount können bis dahin trotz systemd-Stop-Timeout im Kernel warten.

### 2. Signaturschlüssel erzeugen

Der private Schlüssel bleibt offline und gehört weder in den Servercontainer noch auf einen öffentlichen Host.

```bash
docker compose --profile tools run --rm manifest keygen \
  -private-key lanready-private.key \
  -public-key lanready-public.key
cp lanready-public.key secrets/release-public.key
chmod 600 secrets/release-public.key
```

Diese Wiederverwendung ist ausschließlich für den Entwicklungs-Schnellstart gedacht. Produktive Release-Schlüssel werden getrennt und wie in der [Installationsanleitung](docs/installation.md#3-konfiguration-und-secrets) beschrieben verwaltet.

### 3. Demo-Inhalt und Manifest vorbereiten

```bash
cp -R examples/data/content/. data/content/
```

In `examples/manifest.json` müssen die `baseUrl`-Werte auf die tatsächlich erreichbaren Server zeigen. Für einen einzelnen Server im LAN genügt beispielsweise ein Mirror:

```json
"mirrors": [
  {
    "name": "LANReady Server",
    "baseUrl": "http://192.168.1.10:8080",
    "priority": 10,
    "local": true
  }
]
```

Anschließend das Manifest signieren und sofort gegen den öffentlichen Schlüssel prüfen:

```bash
mkdir -p data/events/demo
docker compose --profile tools run --rm manifest sign \
  -in examples/manifest.json \
  -out data/events/demo/envelope.json \
  -private-key lanready-private.key

docker compose --profile tools run --rm manifest verify \
  -in data/events/demo/envelope.json \
  -public-key lanready-public.key
```

Nach jeder Änderung am Manifest muss es neu signiert werden. Die Dateigröße und der SHA-256-Wert in `examples/manifest.json` passen bereits zur enthaltenen Demo-Datei.

### 4. Server starten und prüfen

```bash
docker compose up -d --build server
docker compose ps
curl http://127.0.0.1:8080/healthz
```

Der lokale HTTP-Aufruf eignet sich nur für `/healthz`: `LANREADY_PUBLIC_URL` und der Windows-Client verlangen eine HTTPS-Origin. Für die Web-UI und Windows-Anwendung den Server nach der [Nginx-Proxy-Manager-Anleitung](docs/installation.md#6-installation-mit-nginx-proxy-manager) starten; dort werden beide Compose-Dateien verwendet und `LANREADY_SECURE_COOKIES=true` beibehalten.

Erwartete Health-Antwort:

```json
{"status":"ok"}
```

Manifest und Content benötigen den Client-Token:

```bash
curl -H "Authorization: Bearer $LANREADY_CLIENT_TOKEN" \
  http://127.0.0.1:8080/v1/events/demo/manifest
```

Da Compose Variablen aus `.env` nicht automatisch in die aktuelle Shell exportiert, muss der Token für diesen `curl`-Befehl gegebenenfalls aus `.env` übernommen werden.

Logs und Stoppen:

```bash
docker compose logs -f server
docker compose down
```

## Betrieb mit Nginx Proxy Manager

Auf einem Host mit Nginx Proxy Manager wird die zusätzliche Override-Datei verwendet. Dadurch veröffentlicht LANReady keinen eigenen Host-Port, sondern hängt ausschließlich im vorhandenen Proxy-Netzwerk:

```bash
docker compose -f compose.yaml -f compose.npm.yaml up -d --build server
```

Im Nginx Proxy Manager einen Proxy Host mit folgenden Zielwerten anlegen:

- Scheme: `http`
- Forward Hostname: `lanready-server`
- Forward Port: `8080`
- Websocket Support: nicht erforderlich
- SSL Certificate: passendes Zertifikat auswählen und `Force SSL` aktivieren

Damit die fertig signierte Windows-EXE über **Events → Signiertes Clientupdate veröffentlichen** ohne Proxy-Pufferung direkt in den verifizierenden CAS gestreamt werden kann, im Feld **Advanced** des Proxy Hosts ergänzen:

```nginx
client_max_body_size 1g;
proxy_request_buffering off;
```

LANReady akzeptiert an diesem Endpunkt höchstens 1 GiB und nur eine vorab im signierten Envelope festgelegte Größe und SHA-256-Prüfsumme. Nginx Proxy Manager muss weiterhin ausschließlich auf `lanready-server:8080` im internen Proxy-Netz zeigen.

Der DNS-Name des Proxy Hosts muss anschließend als `baseUrl` im Eventmanifest und als `ServerUrl` des Windows-Clients verwendet werden. Nach einer Änderung der URL muss das Manifest erneut signiert werden. Der Healthcheck kann ohne öffentlichen Backend-Port direkt im Container geprüft werden:

```bash
docker compose -f compose.yaml -f compose.npm.yaml exec server \
  wget -q -O - http://127.0.0.1:8080/healthz
```

## Windows-GUI bauen und verteilen

Die GUI basiert auf der stabilen Wails-v2-Linie und nutzt unter Windows 11 Microsoft WebView2. Ein nicht veröffentlichbares Entwicklungsartefakt lässt sich mit Go 1.26.5 und Wails 2.13.0 bauen:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
make windows-gui VERSION=0.1.0
```

Ein veröffentlichbares Paket wird ausschließlich auf einer vertrauenswürdigen Windows-Buildstation mit Windows SDK/SignTool, NSIS und einem öffentlich vertrauenswürdigen Code-Signing-Zertifikat erzeugt. `VERSION` muss mit `wails.json` übereinstimmen; EXE, Uninstaller und Installer werden mit SHA-256 signiert, RFC-3161-zeitgestempelt und anschließend gegen die Authenticode-Policy sowie den fest erwarteten SHA-256-Fingerprint des DER-Zertifikats geprüft:

```powershell
make windows-package VERSION=0.1.0 `
  AUTHENTICODE_PUBLISHER_SHA256=<64-lowercase-hex> `
  CERTIFICATE_THUMBPRINT=<40-hex> `
  TIMESTAMP_URL=<CA-RFC3161-URL> `
  POWERSHELL_BIN=pwsh
```

Der Build erzeugt:

```text
dist/
├── LANReady-0.1.0-windows-x64-portable.zip
└── LANReady-0.1.0-windows-x64-setup.exe
```

Der Installer arbeitet ausschließlich im aktuellen Benutzerkontext, installiert nach `%LOCALAPPDATA%\Programs\LANReady`, erzeugt Desktop- und Startmenüeinträge und registriert einen Task-Scheduler-Start bei Benutzeranmeldung mit `--lanready-agent` und `LIMITED`-Rechten. Der Agent startet versteckt, prüft Stable-Updates alle 30 Minuten, benachrichtigt den Benutzer und zeigt bei einem zweiten normalen Start dieselbe GUI. Schließen blendet den Agenten aus; unter Einstellungen kann er für die laufende Sitzung vollständig beendet werden. Die portable Variante erzeugt weder Installation noch Autostart. Falls WebView2 fehlt, verwendet das Setup ausschließlich den offiziellen Microsoft-Evergreen-Bootstrapper. Das NSIS-Paket nutzt einen soliden LZMA-Datenstrom und wird nach dem Build vollständig als Archiv geprüft.

Die aktuellen `0.1.0`-Artefakte sind noch nicht Authenticode-signiert und deshalb Entwicklungsartefakte, kein freigegebenes MVP-Release. Der Self-Updater lehnt solche unsignierten Dateien ausdrücklich ab. Vor der Freigabe müssen EXE und Installer mit demselben öffentlich vertrauenswürdigen Herausgeberzertifikat signiert, zeitgestempelt und auf einem sauberen Windows-11-Standardbenutzer geprüft werden. Das Release-Envelope bleibt eine zweite, unabhängige Signaturgrenze und bindet exakt Sequenz, Version, Größe und SHA-256 des Artefakts.

## Signierte Event- und Client-Releases

LANReady trennt Signer und Managementserver. Der Server liest ausschließlich einen oder mehrere Ed25519-Public-Keys; der Private Key wird nur in einem netzwerklosen, nicht privilegierten Tool-Container verwendet. Er darf weder in `/data`, ein Serverimage noch ein Server-Secret gelangen.

Ein Schlüsselpaar wird einmalig im separaten Arbeitsverzeichnis erzeugt:

```bash
mkdir -p release-work secrets
docker compose --profile tools run --rm signer keygen \
  -private-key release-private.key \
  -public-key release-public.key
cp release-work/release-public.key secrets/release-public.key
chmod 600 release-work/release-private.key
```

Der Private Key benötigt ein verschlüsseltes, offline geprüftes Backup. Sein Verlust verhindert neue Releases; sein Bekanntwerden erfordert eine dokumentierte Schlüsselrotation. Der Public Key wird über `LANREADY_RELEASE_PUBLIC_KEY_FILE` als Docker Secret ausschließlich lesbar in den Server gemountet.

Ein Event-Payload wird ohne Netzwerkzugriff signiert und anschließend noch einmal lokal gegen Schema, Semantik und Signatur geprüft:

```bash
docker compose --profile tools run --rm signer sign-event \
  -in event-release.json \
  -out event-envelope.json \
  -private-key release-private.key

docker compose --profile tools run --rm signer verify-event \
  -in event-envelope.json \
  -public-key release-public.key
```

Unter **Events → Signiertes Event-Release veröffentlichen** wird nur `event-envelope.json` ausgewählt. Die UI zeigt Event-ID, Sequenz, Zahl der Spiele und Artefakte sowie die Key-ID zur Kontrolle. Der Server prüft anschließend das Originalschema, doppelte JSON-Felder, Ed25519-Signatur und Key-ID, monotone Sequenz, Gültigkeitsfenster und alle CAS-Referenzen in einer Transaktion. Ein Fehler veröffentlicht und aktiviert nichts.

Clientupdates verwenden entsprechend `sign-update` und `verify-update`. Der Payload bindet zusätzlich `artifactKind: "portable_exe"`, `updaterProtocol: 1` und `publisherCertificateSHA256`. Derselbe Zertifikatsfingerprint muss beim GUI-Build eingebettet und auf dem Server als `LANREADY_AUTHENTICODE_PUBLISHER_SHA256` konfiguriert sein; ohne ihn verweigert der Server Clientupdate-Publishing. Ed25519 bindet die Release-Policy und den exakten SHA-256-Digest, Authenticode prüft unabhängig Windows-Vertrauenskette und erwarteten Herausgeber.

In der Admin-UI wird zuerst das fertige Update-Envelope gewählt. Danach prüft die UI, ob die exakt referenzierte EXE bereits im CAS liegt. Fehlt sie, wird die bereits Authenticode-signierte portable EXE ausgewählt und über **EXE sicher in den Cache laden** gestreamt. Der Server übernimmt sie erst nach exakter Größen- und SHA-256-Prüfung atomar. **Clientupdate prüfen und veröffentlichen** bleibt bis zu dieser erneuten CAS-Verifikation gesperrt; der Release-Service prüft Artefakt, Envelope, Publisher-Policy und Sequenz beim Publish nochmals serverseitig.

`sign-update` ist bewusst nur im Windows-Build des Releasewerkzeugs freigeschaltet und verlangt das bereits fertig signierte Artefakt. Vor der Envelope-Ausgabe prüft es Größe, SHA-256, Windows-Authenticode-Policy, den gepinnten Zertifikatsfingerprint und die Windows-Dateiversion gegen den Payload. Das von `windows-package` erzeugte `dist\lanready-release.exe` wird deshalb auf der isolierten Windows-Signierstation verwendet:

```powershell
.\dist\lanready-release.exe sign-update `
  -in .\release-work\client-update.json `
  -artifact .\cmd\lanready-gui\build\bin\LANReady.exe `
  -private-key .\release-work\release-private.key `
  -out .\release-work\client-update-envelope.json `
  -schema-dir .\docs\contracts\schemas

.\dist\lanready-release.exe verify-update `
  -in .\release-work\client-update-envelope.json `
  -public-key .\release-work\release-public.key `
  -schema-dir .\docs\contracts\schemas
```

Auf Windows erzeugt `keygen` für den Private Key eine geschützte, nicht geerbte DACL für den aktuellen Benutzer; SYSTEM und lokale Administratoren erhalten nur Leserechte. `sign-update` verweigert Schlüsseldateien mit abweichendem Besitzer, geerbter DACL oder zusätzlichen berechtigten Principals.

### GUI verwenden

1. Portable ZIP entpacken und `LANReady.exe` starten oder das per-user Setup ausführen.
2. Als Admin unter `/admin/clients` einen zehn Minuten gültigen Enrollment-Code erzeugen.
3. Im Client **PC verbinden** wählen und den einmaligen Code eingeben.
4. Nach erfolgreichem Statusabruf zeigt die Startseite das aktive signierte Event, erforderliche Launcher und Spiele sowie den lokal ermittelten Bereitschaftsgrad. Fehlende, abweichende oder nicht sicher bestimmbare Versionen bleiben ausdrücklich als Aktion beziehungsweise Warnung sichtbar.
5. **Spiele suchen** wählen. LANReady zeigt Launcher, externe ID, Version und Installationspfad aller Funde.
6. Für ein kopiertes oder manuell installiertes Spiel legt ein Admin zuerst unter `/admin/catalog` ein Spiel mit **Launcher / Typ: Ohne Launcher** an. Der Clientbenutzer wählt danach unter **Spiel manuell hinzufügen** dieses Katalogspiel und über den nativen Windows-Dialog dessen Hauptprogramm (`.exe`). Freie unbekannte Spiele werden nicht automatisch in den Managementkatalog übernommen.
7. Die gewünschten Einträge auswählen, **Auswahl synchronisieren** wählen, die Browser-Anmeldung bestätigen und die angezeigten Übertragungsdaten ein letztes Mal freigeben.

Das gesamte Geräteprofil einschließlich Serverorigin, Ed25519-Geräteschlüssel sowie niemals sinkender Event- und Update-Sequenzstände wird unter `%AppData%\LANReady\device.json` gespeichert und mit Windows DPAPI integritätsgeschützt an den aktuellen Benutzer gebunden. Profiländerungen werden vor dem atomaren Austausch dauerhaft geflusht; übergroße Watermark-Sätze ersetzen den lesbaren Altstand nicht. Registry-/Dateisystemerkennung hat ein festes Zeitlimit und blockiert weder Status-UI noch andere Clientaktionen dauerhaft. Persönliche Zugriffstokens existieren nur im Arbeitsspeicher und werden vor dem einmaligen Upload verworfen. Launcher-Passwörter werden weder gelesen noch gespeichert.

### Vorübergehenden CLI-Downloadablauf bauen

Bis Event-Publishing und Installationsorchestrierung in die GUI integriert sind, lässt sich der bestehende CLI-Client weiterhin über das Docker-Artefaktziel bauen:

```bash
docker build --target artifacts --output type=local,dest=./bin .
```

Auf dem Windows-PC kann damit zunächst nur geprüft werden:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\Start-LANReady.ps1 `
  -ServerUrl 'http://192.168.1.10:8080' `
  -EventId 'demo' `
  -TargetRoot 'C:\LAN-Games' `
  -DryRun `
  -Yes
```

Das Skript fragt den Event-Token verdeckt ab. Alternativ kann er vorab nur für den aktuellen Prozess gesetzt werden:

```powershell
$env:LANREADY_TOKEN = 'EVENT-TOKEN'
```

Wenn der Dry-Run Rückgabecode 3 meldet, fehlen oder unterscheiden sich Dateien. Die echte Synchronisation erfolgt ohne `-DryRun`:

```powershell
.\Start-LANReady.ps1 `
  -ServerUrl 'http://192.168.1.10:8080' `
  -EventId 'demo' `
  -TargetRoot 'C:\LAN-Games' `
  -Yes
```

Ohne `-Yes` fragt der portable Modus jedes Spiel einzeln ab. `-Mode managed -Yes` wählt alle Spiele automatisch aus.

### CLI: Windows-PC registrieren und Spieleinventar synchronisieren

1. Als Administrator unter `/admin/clients` **Enrollment-Code erzeugen** wählen. Der Code ist zehn Minuten gültig und genau einmal nutzbar.
2. Auf demselben Windows-Benutzerkonto, in dem LANReady später verwendet wird, ausführen:

```powershell
.\LANReady.exe `
  -server 'https://game-manager.familie-keller.info' `
  -enrollment-code 'CODE-AUS-DER-WEB-UI'
```

Der dabei erzeugte Ed25519-Geräteschlüssel wird im Benutzerprofil unter `%AppData%\LANReady\device.json` abgelegt und mit Windows DPAPI an den aktuellen Benutzer gebunden. Launcher-Passwörter werden weder gelesen noch gespeichert.

Die lokale Erkennung kann ohne Serveränderung geprüft werden:

```powershell
.\LANReady.exe -discover-installed
```

Anschließend die angezeigten Steam-, EA-App- und Ubisoft-Connect-Funde bestätigen und synchronisieren:

```powershell
.\LANReady.exe -sync-inventory
```

LANReady öffnet die Haupt-URL im Browser, zeigt einen Einmalcode und wartet auf die persönliche Bestätigung. Erst danach ersetzt der Scan atomar das sichtbare Inventar dieses Geräts. `-yes` überspringt nur die lokale Rückfrage; die persönliche Browserfreigabe bleibt erforderlich. Für normale Benutzer ist stattdessen die oben beschriebene GUI vorgesehen.

### Rückgabecodes des Clients

| Code | Bedeutung |
|---:|---|
| 0 | ausgewählte Inhalte sind bereit |
| 1 | technischer Fehler |
| 2 | ungültige Parameter oder Konfiguration |
| 3 | Prüfung erfolgreich, Inhalte aber noch nicht bereit |

### Windows Update

Windows-Updates sind standardmäßig deaktiviert. Verfügbare Modi:

- `-WindowsUpdate off`: keine Prüfung
- `-WindowsUpdate check`: fehlende Updates ermitteln
- `-WindowsUpdate install`: Updates installieren

`install` benötigt administrative Rechte und kann einen Neustart erfordern. Auf fremden PCs sollte diese Option nur nach ausdrücklicher Zustimmung verwendet werden.

## Management-Web-UI

Die Haupt-URL leitet nach `/admin/` weiter. Nach der Anmeldung öffnet **Externe Quellen verwalten** den vollständigen Quellenablauf unter `/admin/sources`:

- HTTPS-, WebDAV- und Nextcloud-WebDAV-Quellen anlegen und vorbefüllt bearbeiten
- Verbindung vor dem Speichern zwingend testen; ein fehlgeschlagener Test persistiert weder Quelle noch Zugangsdaten
- Basic- beziehungsweise App-Passwörter niemals zurückgeben oder vorbefüllen und XChaCha20-Poly1305-verschlüsselt in der dedizierten SQLite-Tabelle `webdav_source_config` speichern
- Quelle und Zugangsdaten in genau einer SQLite-Transaktion speichern oder gemeinsam zurückrollen
- Quellen deaktivieren oder nur ohne Launcher-/Spielversionsreferenzen endgültig löschen

Admin darf Quellen speichern, deaktivieren und löschen; Operator darf Verbindungen testen; Viewer sieht keine Mutationsaktionen. Die Netzwerkpolicy erlaubt nur HTTPS, begrenzt Zeit, Redirects und Antwortgröße und sperrt private Zielnetze ohne explizite Host-/CIDR-Allowlist. Zugangsdaten werden bei Redirects nur innerhalb derselben Origin weitergegeben.

Der Katalog unter `/admin/catalog` verwaltet Launcher, Spiele, Versionen und Events. Steam, EA App und Ubisoft Connect sind als initiale Adapter angelegt; **Ohne Launcher** ist ein unveränderlicher Systemtyp für kopierte oder manuell installierte Spiele. Bei diesen Spielen erzwingt der Server den Spiel-Slug als stabile externe ID. Launcher- und Spielversionen zeigen ihren persistenten Cachezustand direkt in der Tabelle. Admin und Operator können einen Download einplanen, abbrechen, wiederholen oder ein bereits vorhandenes CAS-Artefakt erneut verifizieren. Der Worker lädt ausschließlich über HTTPS, wendet dieselbe DNS-/SSRF- und Redirect-Policy wie der Quellentest an, entfernt Basic-Auth bei Originwechseln und akzeptiert nur die bytegenaue Identität. Ist SHA-256 oder Größe noch leer, werden beide beim atomaren Import ermittelt und revisionsgesichert in die Version übernommen. Zugangsdaten werden dafür nur im Arbeitsspeicher entschlüsselt und weder Jobstatus noch Audit hinzugefügt. Die Erkennung ist implementiert; ihre Registry-/Dateisystempfade müssen vor der MVP-Abnahme noch auf realen Windows-11-Systemen mit den drei Launchern verifiziert werden.

Unter `/admin/clients` können Administratoren einen zehn Minuten gültigen, genau einmal verwendbaren Enrollment-Code erzeugen. Registrierte Geräte erscheinen dort mit Windows-/Clientversion, letztem Kontakt und dem zuletzt authentisiert synchronisierten Spieleinventar. Die persönliche Freigabe erfolgt unter `/admin/device` per Browser-Einmalcode; das Benutzerpasswort wird nie an den nativen Client übertragen. Ein Inventarfund verändert den Katalog niemals automatisch: Nur ein Admin kann ihn ausdrücklich einem kompatiblen Katalogspiel zuordnen, bis zu 100 offene Funde atomar übernehmen oder ein deaktiviertes Spiel samt erkannter Version als Entwurf anlegen. Bei Steam-, EA- und Ubisoft-Spielen ist die Bezugsplattform bereits bekannt; eine zusätzliche LANReady-Paketquelle ist optional. Versionen des Systemtyps **Ohne Launcher** benötigen weiterhin eine externe Paketquelle. Erkennt ein späterer Scan einen anderen Build, bleibt die Spielzuordnung erhalten, aber die neue Version wird erst nach einem weiteren Admin-Klick als Entwurf übernommen.

## Reports abrufen

Der Client meldet Hostname, zufällige Lauf-ID, Betriebssystem, Spielstatus und optional den Windows-Update-Status. Reports benötigen zum Lesen den getrennten Admin-Token:

```bash
curl -H "Authorization: Bearer $LANREADY_ADMIN_TOKEN" \
  'http://127.0.0.1:8080/v1/reports?event=demo'
```

Die persistente NDJSON-Datei liegt unter `data/reports.ndjson`.

## Optionaler LAN-Mirror

Der nginx-Mirror liefert nur `data/content` aus und benötigt keinen Token. Jede Datei wird vom Client anhand des signierten Manifests und ihres SHA-256-Werts geprüft.

```bash
docker compose --profile mirror up -d mirror
curl http://127.0.0.1:8081/healthz
```

Im Manifest wird der Mirror mit `http://SERVER-IP:8081` und einer niedrigeren Prioritätszahl als der Internetserver eingetragen. Der Client sendet den Management-Token nicht an Hosts, deren Hostname vom Managementserver abweicht.

## API

| Methode | Pfad | Authentifizierung | Beschreibung |
|---|---|---|---|
| `GET` | `/healthz` | keine | Containerzustand |
| `GET/POST` | `/admin/*` | Sitzung + CSRF | Management-Web-UI |
| `GET` | `/v1/events/{id}/manifest` | Client-Token | signiertes Manifest |
| `GET` | `/content/{path}` | Client-Token | Inhalte mit HTTP-Range |
| `POST` | `/v1/reports` | Client-Token | Clientstatus speichern |
| `GET` | `/v1/reports?event={id}` | Admin-Token | Reports lesen |
| `POST` | `/v2/devices/enroll` | einmaliger Enrollment-Code | Geräteschlüssel registrieren |
| `GET` | `/v2/device/bootstrap` | signierte Geräteanfrage | API-/Updatezustand abrufen |
| `POST/GET` | `/v2/user-device-authorizations...` | signierte Geräteanfrage | persönliche Browserfreigabe starten/abfragen |
| `POST` | `/v2/device/inventory-scans` | signierte Geräteanfrage + kurzlebiges Benutzertoken | erkanntes Spieleinventar atomar synchronisieren |
| `GET` | `/v2/device/standalone-games` | signierte Geräteanfrage | aktive kataloggebundene Spiele ohne Launcher laden |
| `POST` | `/admin/clients/catalog-import` | Admin-Sitzung + CSRF | Inventarfund zuordnen oder Katalogentwurf anlegen |
| `GET/POST` | `/admin/api/v1/cache-jobs` | Sitzung; Mutation zusätzlich CSRF + Operator/Admin | Cache-Aufträge anzeigen oder versionbezogen einplanen |
| `POST` | `/admin/api/v1/cache-jobs/{id}/cancel` | Sitzung + CSRF + Operator/Admin | laufenden oder wartenden Cache-Auftrag abbrechen |
| `POST` | `/admin/api/v1/cache-jobs/{id}/retry` | Sitzung + CSRF + Operator/Admin | fehlgeschlagenen/abgebrochenen Auftrag erneut einplanen |
| `GET` | `/admin/api/v1/cache-status` | Sitzung | registrierten Cacheverbrauch und Quota lesen |
| `POST` | `/admin/api/v1/cache-gc` | Admin-Sitzung + CSRF + Idempotency-Key | alte unreferenzierte CAS-Artefakte kontrolliert entfernen |

Tokens werden als `Authorization: Bearer TOKEN` gesendet.

## Entwicklung ohne Docker

```bash
go fmt ./...
make test
make vet
go vet ./...
make windows linux
```

Die Ergebnisse liegen in `bin/`.

## Sicherheits- und Betriebsgrenzen

- Der öffentliche Server muss für Internetbetrieb hinter TLS, Firewall und regelmäßigen Updates stehen.
- Der private Ed25519-Schlüssel bleibt offline.
- Downloads landen zunächst in `.lanready.part` und ersetzen Dateien erst nach Größen- und SHA-256-Prüfung.
- Das MVP führt keine heruntergeladenen Skripte aus und speichert keine Launcher-Zugangsdaten.
- Die sichere Admin-Basis, Katalog-/Events-CRUD, persistente HTTPS/WebDAV-Cache-Aufträge, manuelle Cache-Garbage-Collection mit Status-UI, Launcher-Erkennung, kataloggebundene manuelle Spielregistrierung und per-user Client-Autostart sind vorhanden. Automatische unsignierte Event-Kandidaten, Installationsorchestrierung und differenzielles Chunking folgen in weiteren Slices.
- Launcher können eigene Updates erzwingen; LANReady umgeht diese Vorgaben nicht.

## Lizenz

MIT
