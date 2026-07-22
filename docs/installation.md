# Installation und Aktualisierung

Stand: 20.07.2026 · geprüft ab Anwendungsstand `ad9d9b5` / Schema v18

Dieses Dokument beschreibt eine Neuinstallation des Managementservers auf Ubuntu, den Betrieb hinter Nginx Proxy Manager und die portable beziehungsweise installierte Windows-Anwendung.

## 1. Voraussetzungen

Managementserver:

- unterstütztes Ubuntu-System mit korrekter Uhrzeit und DNS-Auflösung;
- Docker Engine und Docker-Compose-Plugin (`docker compose version`);
- Git, OpenSSL und `curl`;
- eine vom Windows-PC erreichbare HTTPS-URL;
- für Nginx Proxy Manager: derselbe Docker-Daemon und ein vorhandenes externes Proxy-Netz;
- ausreichend Speicher für `/data`, Backups und den Cache. Ein NFS-/NAS-Cache muss vor dem Containerstart gemountet sein.

Windows-Client:

- Windows 11 x64 und Microsoft WebView2 Evergreen;
- ein normales Benutzerkonto; LANReady läuft nicht als Systemdienst;
- die Launcher werden bei Bedarf separat installiert und dort vom Benutzer angemeldet. LANReady speichert keine Launcher-Passwörter.

## 2. Repository und Verzeichnisse

```bash
git clone git@github.com:containerguy/lan_installer.git lanready
cd lanready
git switch agent/lanready-mvp
mkdir -p data/cache data/content data/events data/signer secrets release-work backups
chmod 700 secrets release-work backups
```

Für einen produktiven Host sollte das Repository beispielsweise unter `/home/ubuntu/lanready` liegen. Alle folgenden Befehle werden aus diesem Verzeichnis ausgeführt.

## 3. Konfiguration und Secrets

```bash
cp .env.example .env
umask 077
openssl rand -hex 32
openssl rand -hex 32
openssl rand -base64 32 > secrets/web-admin-password.txt
openssl rand -base64 32 > secrets/webdav-master-key.txt
chmod 600 .env secrets/web-admin-password.txt secrets/webdav-master-key.txt
```

Die beiden Hexwerte werden als unterschiedliche Werte für `LANREADY_CLIENT_TOKEN` und `LANREADY_ADMIN_TOKEN` in `.env` eingetragen. Zusätzlich mindestens diese Werte prüfen:

```dotenv
LANREADY_PUBLIC_URL=https://game-manager.example.org
LANREADY_SECURE_COOKIES=true
LANREADY_VERSION=<git-commit-oder-releaseversion>
LANREADY_UID=1000
LANREADY_GID=1000
LANREADY_CACHE_HOST_PATH=./data/cache
LANREADY_CACHE_QUOTA_BYTES=10737418240
LANREADY_PROXY_NETWORK=nginx-proxy-manager_default
LANREADY_TRUSTED_PROXY_CIDRS=172.20.0.0/16
```

Bei rootless Docker müssen `LANREADY_UID=0` und `LANREADY_GID=0` verwendet werden. UID 0 im Container wird dabei auf den unprivilegierten Hostbenutzer abgebildet. Bei rootful Docker werden die tatsächlichen Besitzer-IDs von `id -u` und `id -g` eingetragen.

Der Server benötigt den Public Key des getrennten Offline-Schlüssels für Clientupdates und ein eigenes Online-Schlüsselpaar für Browser-Event-Releases. Der Webserver erhält ausschließlich beide Public Keys. Nur der Event-Private-Key liegt mit Modus 0600 unter `release-work/` und wird in den netzwerklosen, read-only Signer-Container gemountet. Zusätzlich ist ein verschlüsseltes Offlinebackup dieses Private Keys zwingend:

```bash
install -m 600 /sicherer-transfer/release-public.key secrets/release-public.key
install -m 600 /sicherer-transfer/event-release-public.key secrets/event-release-public.key
install -m 600 /sicherer-transfer/event-release-private.key release-work/event-release-private.key
```

Für einen reinen Entwicklungsaufbau kann ein Testschlüssel mit dem netzwerklosen Toolcontainer erzeugt werden:

```bash
docker compose --profile tools run --rm signer keygen \
  -private-key event-release-private.key \
  -public-key event-release-public.key
cp release-work/event-release-public.key secrets/event-release-public.key
chmod 600 release-work/event-release-private.key secrets/event-release-public.key
```

`LANREADY_EVENT_RELEASE_PRIVATE_KEY_FILE` zeigt auf diese Datei. Der Webserver mountet sie nicht; nur der Signer sieht sie read-only und kommuniziert ohne Netzwerk über einen Unix-Socket in `data/signer/`. `LANREADY_RELEASE_PUBLIC_KEY_FILE` bleibt der Public Key des offline verwahrten Clientupdate-Schlüssels. Zur Migration historischer Event-Releases wird dieser Public Key zusätzlich als Legacy-Verifikationskey in den Event-Keyring geladen; nur der neue, nachweislich verschiedene Event-Key darf online signieren. Der vollständige Ablauf steht unter [Signierte Event- und Client-Releases](../README.md#signierte-event--und-client-releases).

Das Bootstrap-Passwort wird nur beim ersten Anlegen der Datenbank in einen Argon2id-Hash umgewandelt. Ein späteres Ändern von `secrets/web-admin-password.txt` ändert das vorhandene Webpasswort nicht.

## 4. Cache wählen

### Lokaler Cache

Für Entwicklung und kleine Tests bleibt:

```dotenv
LANREADY_CACHE_HOST_PATH=./data/cache
LANREADY_CACHE_VOLUME_ID=
```

### NFS-/NAS-Cache

Der Cache liegt im Container immer unter `/cache`; nur der Hostpfad wird konfiguriert. Beispiel für `/etc/fstab`:

```fstab
nas.example:/mnt/pool/LANReady /mnt/lanready-cache nfs4 rw,hard,vers=4.2,proto=tcp,sec=sys,_netdev,nofail,x-systemd.automount,x-systemd.mount-timeout=30s 0 0
```

Nach dem Mount eine eindeutige ID erzeugen und exakt als `LANREADY_CACHE_VOLUME_ID` in `.env` eintragen:

```bash
set -euo pipefail
cache_path=/mnt/lanready-cache
sudo mount /mnt/lanready-cache
mountpoint -q "$cache_path"
openssl rand -hex 32
```

Der ausgegebene Wert kommt exakt in `.env`:

```dotenv
LANREADY_CACHE_HOST_PATH=/mnt/lanready-cache
LANREADY_CACHE_VOLUME_ID=<exakter-sentinel-inhalt>
LANREADY_CACHE_QUOTA_BYTES=1099511627776
```

Sentinel und Schreibtest werden nicht mit `sudo tee` oder einer geratenen Host-UID angelegt. Stattdessen führt Compose sie als exakt dem Benutzer aus, der später den Server betreibt:

```bash
set -euo pipefail
cache_path="$(python3 scripts/lanready_compose_paths.py cache)"
mountpoint -q "$cache_path"
docker compose -f compose.yaml -f compose.npm.yaml build server
docker compose -f compose.yaml -f compose.npm.yaml run --rm --no-deps \
  --entrypoint /bin/sh server -eu -c '
    test -n "$LANREADY_CACHE_VOLUME_ID"
    umask 077
    printf "%s\n" "$LANREADY_CACHE_VOLUME_ID" > /cache/.lanready-cache-volume
    test -r /cache/.lanready-cache-volume
    probe="/cache/.lanready-write-test.$$"
    trap "rm -f \"$probe\"" EXIT
    printf ok > "$probe"
    test "$(cat "$probe")" = ok
  '
```

Die unmittelbar vorherige `mountpoint`-Assertion verhindert, dass bei ausgefallenem NFS versehentlich ein lokales Verzeichnis getestet und beschrieben wird. [scripts/lanready_compose_paths.py](../scripts/lanready_compose_paths.py) liest dafür die effektive, kanonische `/cache`-Bind-Quelle aus Compose. Der Compose-Test ist für rootful und rootless Docker identisch und prüft das tatsächlich wirksame NFS-/TrueNAS-Mapping. Schlägt er fehl, werden ACL, `root_squash` beziehungsweise TrueNAS-Mapall-Ziel korrigiert; Sentinel oder Cachewurzel werden nicht pauschal weltlesbar gemacht. Die Quota muss unterhalb der real verfügbaren Kapazität bleiben. Fehlt der Mount, ist er nicht schreibbar oder stimmt die Sentinel-ID nicht, darf LANReady nicht gestartet werden.

## 5. HTTPS ist für Clients verpflichtend

`LANREADY_PUBLIC_URL` akzeptiert ausschließlich eine zugangsdatenfreie HTTPS-Origin ohne Pfad, Query oder Fragment. Die Windows-Anwendung lehnt HTTP ebenfalls ab. Eine nutzbare Installation benötigt deshalb TLS, in diesem Projekt über Nginx Proxy Manager.

Der intern veröffentlichte Port kann unabhängig davon für einen lokalen Healthcheck verwendet werden:

```bash
curl -fsS http://127.0.0.1:8080/healthz
```

Dieser Aufruf prüft nur den Serverprozess und ist weder Login-URL noch Clientkonfiguration.

## 6. Installation mit Nginx Proxy Manager

Zuerst Netzwerk und CIDR verifizieren:

```bash
docker network inspect nginx-proxy-manager_default
```

`LANREADY_TRUSTED_PROXY_CIDRS` darf ausschließlich das dabei festgestellte Proxy-CIDR enthalten. Anschließend:

```bash
docker compose -f compose.yaml -f compose.npm.yaml config -q
docker compose -f compose.yaml -f compose.npm.yaml up -d --build server
docker compose -f compose.yaml -f compose.npm.yaml ps
```

Nginx Proxy Manager:

- Scheme: `http`
- Forward Hostname: `lanready-server`
- Forward Port: `8080`
- gültiges Zertifikat auswählen und **Force SSL** aktivieren
- Advanced:

```nginx
client_max_body_size 1g;
proxy_request_buffering off;
```

Prüfung:

```bash
curl -fsS https://game-manager.example.org/healthz
```

Die Weboberfläche liegt auf der Haupt-URL. Der initiale Benutzername steht in `LANREADY_WEB_ADMIN_USER`, das einmalig erzeugte Passwort in `secrets/web-admin-password.txt`.

## 7. Bootreihenfolge mit systemd und NFS

[deploy/systemd/lanready-compose.service](../deploy/systemd/lanready-compose.service) ist eine Vorlage mit den produktionsnahen Werten `User=ubuntu`, `/home/ubuntu/lanready`, rootless Docker-Socket und `/mnt/lanready-cache`. Vor Installation jeden dieser Werte prüfen und anpassen.

Für rootless Docker:

```bash
systemctl --user enable --now docker
sudo loginctl enable-linger ubuntu
sudo install -m 644 deploy/systemd/lanready-compose.service /etc/systemd/system/lanready-compose.service
sudo systemctl daemon-reload
sudo systemctl enable --now lanready-compose.service
sudo systemctl status lanready-compose.service
```

Der Dienst erzwingt, dass der Cache gemountet ist, und startet Compose immer mit `compose.yaml` und `compose.npm.yaml`. Ein manueller Compose-Aufruf darf die Override-Datei ebenfalls nicht weglassen.

## 8. Portable Windows-Anwendung

1. Das portable ZIP in einen lokalen Ordner entpacken oder die bereitgestellte `LANReady.exe` dorthin kopieren.
2. Eine alte LANReady-Instanz vollständig beenden. Durch den Single-Instance-Schutz würde sonst die bereits laufende Version angezeigt.
3. `LANReady.exe` starten. Bei unsignierten Testbuilds kann SmartScreen warnen; solche Dateien sind keine Produktionsfreigabe.
4. Unter **Clients** in der Web-UI einen Enrollment-Code erzeugen.
5. Im Windows-Client **PC verbinden** wählen und den Code eingeben.

Das DPAPI-geschützte Geräteprofil liegt EXE-unabhängig unter `%APPDATA%\LANReady\device.json`. Eine neue portable Version in einem anderen Ordner verwendet dieselbe Identität. Die portable Variante installiert keinen Autostart.

## 9. Installierte Windows-Anwendung

Ein freigegebener `LANReady-…-setup.exe` installiert pro Benutzer nach `%LOCALAPPDATA%\Programs\LANReady`, legt Startmenü-/Desktop-Verknüpfungen an und registriert einen Task mit `LIMITED`-Rechten bei der Benutzeranmeldung. Es wird kein Systemdienst eingerichtet. Der Agent läuft im Benutzerkontext, prüft Updates regelmäßig und kann in den Einstellungen für die aktuelle Sitzung beendet werden.

Aktuell vorhandene unsignierte Testartefakte dürfen nicht als produktives Self-Update veröffentlicht werden. Der Releasebuild benötigt ein vertrauenswürdiges Authenticode-Zertifikat und den in [README.md](../README.md#windows-gui-bauen-und-verteilen) beschriebenen fail-closed Buildpfad.

## 10. Server aktualisieren

Vor jedem Upgrade das konsistente Vollbackup einschließlich altem Container-Image nach [backup-restore.md](backup-restore.md#4-konsistentes-vollbackup) erstellen und prüfen. Danach:

```bash
cd /home/ubuntu/lanready
git fetch --all --prune
git switch agent/lanready-mvp
git pull --ff-only
```

`LANREADY_VERSION` in `.env` auf Commit oder Release setzen und das neue Image zunächst bauen:

```bash
docker compose -f compose.yaml -f compose.npm.yaml build server
sudo systemctl restart lanready-compose.service
```

Ohne systemd:

```bash
docker compose -f compose.yaml -f compose.npm.yaml up -d --build --force-recreate server
```

Danach mindestens prüfen:

```bash
docker compose -f compose.yaml -f compose.npm.yaml ps
docker compose -f compose.yaml -f compose.npm.yaml exec server lanready-server -version
curl -fsS https://game-manager.example.org/healthz
```

Zusätzlich Admin-Login, **Clients**, **Katalog**, Cache-Mount und SQLite-Konsistenz prüfen. Datenbankschemata werden beim Start vorwärts migriert; deshalb muss vor dem Upgrade ein zum alten Image passendes Backup existieren.

## 11. Deinstallation

Servercontainer entfernen, Daten aber behalten:

```bash
docker compose -f compose.yaml -f compose.npm.yaml down
```

Erst nach geprüftem Backup dürfen `data`, `secrets`, `.env`, `release-work` und der Cache gelöscht werden. Der systemd-Dienst wird mit `sudo systemctl disable --now lanready-compose.service` entfernt.

Unter Windows die installierte Variante über **Installierte Apps** deinstallieren. Die portable EXE wird durch Löschen ihres Ordners entfernt. `%APPDATA%\LANReady\device.json` enthält die Geräteidentität und wird nur gelöscht, wenn der PC bewusst neu enrollt werden soll.
