# Backup, Restore und Rollback

Stand: 20.07.2026 · geprüft ab Anwendungsstand `ad9d9b5` / Schema v18

Dieses Dokument ist betriebsrelevant. Ein Archiv gilt erst nach Integritätsprüfung und Restore-Test als belastbares Backup. Ein fehlgeschlagener Stop oder eine fehlgeschlagene Prüfung bricht jeden folgenden Schritt ab.

## 1. Sicherungsumfang

| Bestandteil | Standardpfad | Bedeutung |
|---|---|---|
| SQLite und Serverdaten | `./data/` | Benutzer, Rollen, Geräte, Inventar, Quellenmetadaten, Katalog, Jobs, Releases und Audit |
| Vier gesicherte Laufzeit-Secrets | über Compose aufgelöste `file`-Pfade | WebDAV-Masterschlüssel, Admin-Bootstrap-Secret, Offline-Clientupdate-Public-Key und Online-Event-Public-Key |
| Laufzeitkonfiguration | `.env`, `compose.yaml`, `compose.npm.yaml` | Tokens, URLs, UID/GID, Proxy- und Cachekonfiguration |
| CAS-Cache | `LANREADY_CACHE_HOST_PATH` | verifizierte Pakete und Clientupdate-Artefakte |
| Online-Event-Signiermaterial | konfigurierter Private-Key-Pfad plus externes Offlinebackup | nicht im normalen Backup; nur im netzwerklosen Signer gemountet |
| Laufendes Serverimage | komprimiertes `docker image save` | binär identischer Rollback unabhängig von später geänderten Base-Images |
| Versionsbezug | Backup-ID, Git-Commit, Image-Tag, Schema, Cache-Snapshot-ID | ordnet alle Bestandteile demselben Stand zu |

Datenbank und WebDAV-Masterschlüssel bilden eine Einheit. Fehlt der Schlüssel, sind gespeicherte WebDAV-Zugangsdaten nicht mehr entschlüsselbar. Das private Event-Signiermaterial wird bewusst **nicht** durch das normale Secret-Backup kopiert: Es muss separat verschlüsselt und getrennt offline gesichert werden. Beim Backup und Restore prüft das Werkzeug jedoch, dass der Event-Public-Key zum aktuell konfigurierten Private Key passt; bei einer Abweichung wird vor jeder Änderung abgebrochen.

Windows-Profile unter `%APPDATA%\LANReady\device.json` sind per DPAPI an Benutzer und Windows-Kontext gebunden. Sie sind kein portables Serverbackup. Bleibt das Profil erhalten, meldet sich ein Client nach Server-Restore mit derselben Geräte-ID.

## 2. Secret-Pfade zuverlässig behandeln

Secret-Pfade können in `.env` außerhalb von `./secrets` liegen. [scripts/lanready_secrets.py](../scripts/lanready_secrets.py) liest deshalb die tatsächlich durch beide Compose-Dateien aufgelösten Pfade. Es lehnt fehlende Dateien und Symlinks ab, prüft das Online-Event-Schlüsselpaar sowie dessen Verschiedenheit vom Offline-Clientupdate-Key, sichert die vier nicht privaten Dateien mit Modus 0600 samt Pfadmanifest und stellt sie atomar nur an exakt dieselben konfigurierten Pfade zurück. Der private Event-Key bleibt an seinem konfigurierten Ort und muss vor dem Restore separat wiederhergestellt worden sein.

Die Befehle werden immer im Repository ausgeführt:

```bash
python3 scripts/lanready_secrets.py backup /sicheres-backup/STAND/resolved-secrets
python3 scripts/lanready_secrets.py restore /sicheres-backup/STAND/resolved-secrets
```

Der zweite Befehl gehört ausschließlich in den unten beschriebenen Restore. Seine Tests decken vier vollständig außerhalb von `./secrets` konfigurierte Quelldateien, fehlende Backupdateien und nicht passende Event-Schlüssel ab.

## 3. Empfohlene Strategie und Onlinebackup

- täglich: SQLite-Onlinebackup, Konfiguration und aufgelöste Secrets;
- vor Upgrade, Migration oder NFS-Wartung: konsistentes Vollbackup einschließlich Image und Cache-Snapshot;
- mindestens eine verschlüsselte Kopie getrennt von Server und NAS;
- praktikabler privater Startwert: sieben Tages- und vier Wochenstände;
- mindestens vierteljährlich sowie nach größeren Upgrades: isolierter Restore-Test.

Ein SQLite-Onlinebackup ist bei laufendem Server konsistent, der CAS gehört aber nicht zum selben Zeitpunkt. Für das tägliche Steuerdatenbackup:

```bash
set -euo pipefail
cd /home/ubuntu/lanready
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="/sicheres-backup/lanready-$stamp"
install -d -m 700 "$backup"

python3 - "$backup/lanready.db" <<'PY'
import sqlite3
import sys

source = sqlite3.connect("file:data/lanready.db?mode=ro", uri=True)
target = sqlite3.connect(sys.argv[1])
source.backup(target)
target.close()
source.close()
PY

cp -a .env compose.yaml compose.npm.yaml "$backup/"
python3 scripts/lanready_secrets.py backup "$backup/resolved-secrets"
git rev-parse HEAD > "$backup/git-commit.txt"
docker compose -f compose.yaml -f compose.npm.yaml images > "$backup/docker-images.txt"
```

Datenbank und Dateien prüfen:

```bash
python3 - "$backup/lanready.db" <<'PY'
import sqlite3
import sys

db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
assert db.execute("PRAGMA quick_check").fetchone()[0] == "ok"
assert db.execute("PRAGMA foreign_key_check").fetchall() == []
print("SQLite: ok; Foreign Keys: ok")
PY

(cd "$backup" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
chmod -R go-rwx "$backup"
```

## 4. Konsistentes Vollbackup

Laufende Cacheaufträge zuerst in der Web-UI beenden oder dokumentieren. Anschließend eine der beiden Stopvarianten in einer Shell mit `set -euo pipefail` ausführen:

```bash
set -euo pipefail
cd /home/ubuntu/lanready
sudo systemctl stop lanready-compose.service
```

Ohne systemd wird stattdessen ausgeführt:

```bash
set -euo pipefail
cd /home/ubuntu/lanready
docker compose -f compose.yaml -f compose.npm.yaml stop server
```

Danach maschinenprüfbar ausschließen, dass der Server noch läuft. Die Prüfung darf keine Ausgabe erzeugen:

```bash
compose=(docker compose -f compose.yaml -f compose.npm.yaml)
container_id="$("${compose[@]}" ps -q server)"
if test -n "$container_id" && test "$(docker inspect -f '{{.State.Running}}' "$container_id")" = true; then
  echo "Abbruch: LANReady-Server läuft noch" >&2
  exit 1
fi
```

Nun denselben Backupstand einschließlich unveränderlichem Image sichern:

```bash
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup="/sicheres-backup/lanready-full-$stamp"
install -d -m 700 "$backup"
cp -a data .env compose.yaml compose.npm.yaml "$backup/"
python3 scripts/lanready_secrets.py backup "$backup/resolved-secrets"
git rev-parse HEAD > "$backup/git-commit.txt"

image_id="$("${compose[@]}" images -q server)"
test -n "$image_id"
image_tag="lanready-server:backup-$stamp"
docker tag "$image_id" "$image_tag"
printf '%s\n' "$image_tag" > "$backup/image-tag.txt"
docker image inspect "$image_tag" --format '{{.Id}}' > "$backup/image-id.txt"
docker image save "$image_tag" | gzip -1 > "$backup/lanready-server-image.tar.gz"
```

Cache- und Datenpfad werden aus der effektiven Compose-Konfiguration ermittelt und kanonisiert. Bei `cache-mode=inside-data` ist der Cache bereits in `data` enthalten. Ein externer Cache muss dagegen genau unter seiner ermittelten Quelle gemountet sein:

```bash
data_path="$(python3 scripts/lanready_compose_paths.py data)"
cache_path="$(python3 scripts/lanready_compose_paths.py cache)"
cache_mode="$(python3 scripts/lanready_compose_paths.py cache-mode)"
if test "$cache_mode" = external; then
  mountpoint -q "$cache_path"
fi
```

Bevorzugt auf TrueNAS/ZFS einen Snapshot erstellen und dessen Namen in `$backup/cache-snapshot.txt` ablegen. Alternativ den vollständigen ruhenden Cache auf getrennten Speicher kopieren:

```bash
test "$cache_mode" = external
rsync -aH --numeric-ids "$cache_path/" "$backup/cache/"
```

Die Kopie muss `.lanready-cache-volume`, `.tmp` und `sha256` enthalten. Kein `rsync --delete` gegen bestehende Sicherungen verwenden. Die Datenbank `$backup/data/lanready.db` wie im Onlinebackup prüfen. Danach über alle tatsächlich kopierten Dateien Prüfsummen bilden; bei einem ZFS-Snapshot zusätzlich dessen Integritätsstatus dokumentieren:

```bash
(cd "$backup" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
```

Erst nach erfolgreicher Prüfung wieder starten und HTTPS, Login und Cache kontrollieren:

```bash
sudo systemctl start lanready-compose.service
curl -fsS https://game-manager.example.org/healthz
```

## 5. Restore-Voraussetzungen

- Wartungsfenster oder isolierter Zielhost; keine öffentliche Freigabe während des Restores;
- ausreichend dimensionierter, vertrauenswürdiger Docker-Host;
- im Backupverzeichnis erfolgreich ausgeführtes `sha256sum -c SHA256SUMS`;
- Backup-ID, Datenbank, Image und Cachekopie/-snapshot gehören nachweislich zusammen;
- vor dem Überschreiben wird der aktuelle Fehlerstand separat erhalten;
- bei NFS ist der konfigurierte Pfad ein echter Mount und nie das leere lokale Mountpoint-Verzeichnis.

Ein altes Image darf nicht gegen eine bereits vorwärts migrierte neue Datenbank gestartet werden. Rollback verwendet immer Datenbank, Image und gegebenenfalls Cache aus demselben Vor-Upgrade-Backup.

## 6. Vollständiger Restore

Backup wählen, Prüfsummen verifizieren, Server stoppen und den Stillstand wie im Vollbackup maschinenprüfbar bestätigen:

```bash
set -euo pipefail
cd /home/ubuntu/lanready
backup=/sicheres-backup/GEWAEHLTER-STAND
(cd "$backup" && sha256sum -c SHA256SUMS)
sudo systemctl stop lanready-compose.service
compose=(docker compose -f compose.yaml -f compose.npm.yaml)
container_id="$("${compose[@]}" ps -q server)"
if test -n "$container_id" && test "$(docker inspect -f '{{.State.Running}}' "$container_id")" = true; then
  echo "Abbruch: LANReady-Server läuft noch" >&2
  exit 1
fi
```

Die Daten werden vollständig in ein neues Staging-Verzeichnis kopiert und dort geprüft. Es wird nie in den vorhandenen `data`-Baum hineinkopiert:

```bash
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
stage=".restore-$stamp"
test ! -e "$stage"
install -d -m 700 "$stage"
cp -a "$backup/data" "$stage/data"
cp -a "$backup/.env" "$backup/compose.yaml" "$backup/compose.npm.yaml" "$stage/"

python3 - "$stage/data/lanready.db" <<'PY'
import sqlite3
import sys

db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
assert db.execute("PRAGMA quick_check").fetchone()[0] == "ok"
assert db.execute("PRAGMA foreign_key_check").fetchall() == []
PY
```

Das gesicherte Image laden und auf den von Compose erwarteten Namen taggen:

```bash
gzip -dc "$backup/lanready-server-image.tar.gz" | docker image load
image_tag="$(cat "$backup/image-tag.txt")"
test -n "$image_tag"
docker image inspect "$image_tag" >/dev/null
docker tag "$image_tag" lanready-server:latest
```

Vor jeder Konfigurationsänderung den vollständigen aktuellen Fehlerstand einschließlich der mit der **alten** Compose-Konfiguration aufgelösten Secrets sichern. Erst danach die restaurierte Konfiguration atomar aktivieren:

```bash
failed="failed-$stamp"
test ! -e "$failed"
install -d -m 700 "$failed"
cp -a .env compose.yaml compose.npm.yaml "$failed/"
python3 scripts/lanready_secrets.py backup "$failed/resolved-secrets"
test -f "$failed/resolved-secrets/manifest.json"

test ! -e ".env.restore-$stamp"
test ! -e "compose.yaml.restore-$stamp"
test ! -e "compose.npm.yaml.restore-$stamp"
install -m 600 "$stage/.env" ".env.restore-$stamp"
mv ".env.restore-$stamp" .env
install -m 644 "$stage/compose.yaml" "compose.yaml.restore-$stamp"
mv "compose.yaml.restore-$stamp" compose.yaml
install -m 644 "$stage/compose.npm.yaml" "compose.npm.yaml.restore-$stamp"
mv "compose.npm.yaml.restore-$stamp" compose.npm.yaml
```

Jetzt Cache- und Datenquelle aus der **restaurierten** Compose-Konfiguration auflösen. Bei `inside-data` ist der Cache im gestagten `data` enthalten. Bei `external` zuerst den zugehörigen NAS-Snapshot zurückrollen. Für eine Dateikopie ein neues beziehungsweise zuvor separat gesichertes und danach geleertes Ziel verwenden; diese Assertion verhindert das Mischen zweier Stände:

```bash
cache_path="$(python3 scripts/lanready_compose_paths.py cache)"
cache_mode="$(python3 scripts/lanready_compose_paths.py cache-mode)"
test "$cache_mode" = external
mountpoint -q "$cache_path"
test -z "$(find "$cache_path" -mindepth 1 -maxdepth 1 -print -quit)"
rsync -aH --numeric-ids "$backup/cache/" "$cache_path/"
```

Bei einem Cache innerhalb von `data` wird der vorige Block übersprungen. Danach die exakt aufgelösten Secrets wiederherstellen und erst zuletzt den vollständig gestagten Datenbaum umschalten:

```bash
python3 scripts/lanready_secrets.py restore "$backup/resolved-secrets"
mv data "$failed/data"
mv "$stage/data" data
```

Bei externem Cache Zugriffsrechte, Sentinelinhalt und realen Schreibzugriff als Compose-Benutzer prüfen. Der Test verändert außer einer sofort entfernten Prüfdatei nichts:

```bash
cache_path="$(python3 scripts/lanready_compose_paths.py cache)"
test "$(python3 scripts/lanready_compose_paths.py cache-mode)" = external
mountpoint -q "$cache_path"
docker compose -f compose.yaml -f compose.npm.yaml run --rm --no-deps \
  --entrypoint /bin/sh server -eu -c '
    test -n "$LANREADY_CACHE_VOLUME_ID"
    test "$(tr -d "\r\n" < /cache/.lanready-cache-volume)" = "$LANREADY_CACHE_VOLUME_ID"
    probe="/cache/.lanready-restore-test.$$"
    trap "rm -f \"$probe\"" EXIT
    printf ok > "$probe"
    test "$(cat "$probe")" = ok
  '
```

Jeder Fehler lässt den Server gestoppt; der alte Datenstand liegt in `$failed`, der neue gegebenenfalls in `$stage`. Erst nach allen erfolgreichen Prüfungen starten:

```bash
sudo systemctl start lanready-compose.service
```

## 7. Restore-Abnahme

```bash
docker compose -f compose.yaml -f compose.npm.yaml ps
docker compose -f compose.yaml -f compose.npm.yaml exec server lanready-server -version
curl -fsS https://game-manager.example.org/healthz
```

Zusätzlich prüfen:

- SQLite `quick_check=ok` und keine Foreign-Key-Verletzung;
- Anmeldung über die öffentliche HTTPS-URL;
- Quellenliste und Entschlüsselbarkeit der WebDAV-Konfiguration;
- Katalog, Events, Clients und letzter Inventarscan;
- Cacheverbrauch und Verifikation mindestens eines vorhandenen Artefakts;
- NPM-Netz, read-only Root-Filesystem, `cap_drop: ALL` und `no-new-privileges`;
- vorhandener Windows-Client meldet sich mit derselben Geräte-ID ohne neues Enrollment.

## 8. Rollback nach fehlgeschlagenem Upgrade

1. neuen Container fail-closed stoppen und Stillstand bestätigen;
2. neuen Zustand als Fehlerstand erhalten;
3. Datenbank, Secrets, Konfiguration und Cache aus dem unmittelbar vor dem Upgrade erzeugten Vollbackup wiederherstellen;
4. das per `docker image save` gesicherte alte Image laden und taggen;
5. vollständige Restore-Abnahme ausführen.

Nur Image oder nur Datenbank zurückzusetzen ist nach Migration beziehungsweise CAS-Änderung unzureichend. Der Rollbackstand ist eine zusammengehörige Einheit.

## 9. Regelmäßiger Restore-Test

Ein Restore-Test verwendet eine Backupkopie, eine eigene `.env`, ein isoliertes Docker-Netz und einen separaten Cachepfad. Er schreibt niemals auf die produktive NFS-Freigabe. Protokolliert werden Datum, Backup-ID, Commit/Image, benötigte Zeit, Prüfergebnisse und Abweichungen. Ein gescheiterter Test ist mindestens P1 und blockiert Upgrades, bis Ursache und Sicherungsprozess korrigiert sind.
