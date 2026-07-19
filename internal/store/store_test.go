package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBootstrapAndSession(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created, err := s.BootstrapAdmin(context.Background(), "Admin", "hash")
	if err != nil || !created {
		t.Fatalf("bootstrap: %v %v", created, err)
	}
	created, err = s.BootstrapAdmin(context.Background(), "other", "other")
	if err != nil || created {
		t.Fatalf("second bootstrap: %v %v", created, err)
	}
	u, err := s.UserByUsername(context.Background(), "ADMIN")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Roles) != 1 || u.Roles[0] != "admin" {
		t.Fatalf("roles: %#v", u.Roles)
	}
	token, _, err := s.CreateSession(context.Background(), u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Session(context.Background(), token)
	if err != nil || got.User.ID != u.ID {
		t.Fatalf("session: %#v %v", got, err)
	}
	if err = s.DeleteSession(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(context.Background(), token); err == nil {
		t.Fatal("deleted session accepted")
	}
}

func TestWebDAVConfigUsesDedicatedEncryptedColumns(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "webdav.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cipher := []byte{1, 2, 3, 4}
	created, err := s.SaveSourceAtomic(context.Background(), Source{Name: "Cloud", Kind: "nextcloud_webdav", BaseURL: "https://cloud.example.test/remote.php/dav/files/user", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) {
		return &WebDAVConfig{AuthType: "basic", Username: "user", SecretNonce: []byte{9, 8, 7}, SecretCiphertext: cipher}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID
	got, err := s.WebDAVConfig(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "user" || string(got.SecretCiphertext) != string(cipher) {
		t.Fatalf("unexpected config: %#v", got)
	}
	rows, err := s.db.Query(`PRAGMA table_info(sources)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "credential_ref" {
			t.Fatal("legacy credential column still exists")
		}
	}
}

func TestOpenMigratesLegacySourcesWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL,
		base_url TEXT NOT NULL,
		credential_ref TEXT,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	); INSERT INTO sources(name,kind,base_url,credential_ref,enabled) VALUES("Legacy Cloud","webdav","https://cloud.example.test/dav","old-ref",1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	values, err := migrated.Sources(context.Background())
	if err != nil || len(values) != 1 {
		t.Fatalf("legacy source lost: %#v %v", values, err)
	}
	if values[0].Name != "Legacy Cloud" || values[0].Revision != 1 || values[0].LastTestState != "never" {
		t.Fatalf("unexpected migrated source: %#v", values[0])
	}
	rows, err := migrated.db.Query(`PRAGMA table_info(sources)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "credential_ref" {
			t.Fatal("legacy credential_ref survived migration")
		}
	}
}

func TestOpenMigratesCatalogV5RowsToV6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog-v5.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`ALTER TABLE launchers DROP COLUMN revision`,
		`ALTER TABLE games DROP COLUMN revision`,
		`ALTER TABLE launcher_versions DROP COLUMN revision`,
		`ALTER TABLE launcher_versions DROP COLUMN silent_args_json`,
		`ALTER TABLE launcher_versions DROP COLUMN silent_args_verified`,
		`ALTER TABLE game_versions DROP COLUMN revision`,
		`ALTER TABLE events DROP COLUMN revision`,
		`ALTER TABLE event_games DROP COLUMN revision`,
		`DELETE FROM schema_migrations`,
		`INSERT INTO schema_migrations(version) VALUES(1),(2),(3),(4),(5)`,
		`INSERT INTO sources(id,name,kind,base_url,enabled) VALUES(10,"Legacy Source","https","https://legacy.example.test",1)`,
		`INSERT INTO games(id,slug,name,launcher_id,enabled) VALUES(20,"legacy-game","Legacy Game",1,1)`,
		`INSERT INTO launcher_versions(id,launcher_id,version,source_id,source_path,silent_args,enabled) VALUES(30,1,"1",10,"launcher/setup.exe","/S",0)`,
		`INSERT INTO game_versions(id,game_id,version,source_id,source_path,enabled) VALUES(40,20,"1",10,"games/legacy.zip",0)`,
		`INSERT INTO events(id,slug,name,status) VALUES(50,"legacy-event","Legacy Event","draft")`,
		`INSERT INTO event_games(event_id,game_version_id,required) VALUES(50,40,1)`,
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatalf("legacy setup %q: %v", statement, err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	launcherVersions, err := migrated.LauncherVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var launcherVersion LauncherVersion
	for _, value := range launcherVersions {
		if value.ID == 30 {
			launcherVersion = value
		}
	}
	if launcherVersion.ID != 30 || launcherVersion.Revision != 1 || launcherVersion.Enabled || launcherVersion.SilentArgsVerified || len(launcherVersion.SilentArgs) != 0 {
		t.Fatalf("unexpected migrated launcher version: %#v", launcherVersion)
	}
	gameVersions, err := migrated.GameVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(gameVersions) != 1 || gameVersions[0].Revision != 1 || gameVersions[0].Enabled {
		t.Fatalf("unexpected migrated game versions: %#v", gameVersions)
	}
	events, err := migrated.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Revision != 1 {
		t.Fatalf("unexpected migrated events: %#v", events)
	}
	assignments, err := migrated.EventGames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 || assignments[0].Revision != 1 {
		t.Fatalf("unexpected migrated assignments: %#v", assignments)
	}
	var marker int
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=6`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("v6 marker=%d err=%v", marker, err)
	}
}

func TestOpenMigratesV6DatabaseToDeviceSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE device_inventory_items`, `DROP TABLE device_inventory_scans`, `DROP TABLE device_nonces`, `DROP TABLE device_user_tokens`, `DROP TABLE user_device_authorizations`, `DROP TABLE enrollment_rate_limits`, `DROP TABLE enrollment_codes`, `DROP TABLE devices`, `DELETE FROM schema_migrations WHERE version=7`,
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatalf("v6 setup %q: %v", statement, err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	for _, table := range []string{"devices", "enrollment_codes", "enrollment_rate_limits", "user_device_authorizations", "device_user_tokens", "device_nonces", "device_inventory_scans", "device_inventory_items", "inventory_catalog_mappings", "inventory_catalog_version_mappings"} {
		var found int
		if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found); err != nil || found != 1 {
			t.Fatalf("table %s: found=%d err=%v", table, found, err)
		}
	}
	var marker int
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=7`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("v7 marker=%d err=%v", marker, err)
	}
}

func TestOpenMigratesIntermediateV7InventoryHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE device_inventory_items; DROP TABLE device_inventory_scans; CREATE TABLE device_inventory_scans(id TEXT NOT NULL, device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, scanned_at INTEGER NOT NULL, client_version TEXT NOT NULL, current INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, PRIMARY KEY(device_id,id)); DELETE FROM schema_migrations WHERE version=8`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	rows, err := migrated.db.Query(`PRAGMA table_info(device_inventory_scans)`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "request_hash" {
			found = true
		}
	}
	if !found {
		t.Fatal("request_hash column missing after v8 migration")
	}
}

func TestOpenMigratesV8DatabaseToInventoryCatalogMappings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v8.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE inventory_catalog_mappings; DELETE FROM schema_migrations WHERE version=9`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var table, marker int
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='inventory_catalog_mappings'`).Scan(&table); err != nil || table != 1 {
		t.Fatalf("mapping table=%d err=%v", table, err)
	}
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=9`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("v9 marker=%d err=%v", marker, err)
	}
}

func TestOpenMigratesV9MappingFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v9.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE inventory_catalog_version_mappings; DROP TABLE inventory_catalog_mappings; CREATE TABLE inventory_catalog_mappings(launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect')), external_game_id TEXT NOT NULL, game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE, game_version_id INTEGER REFERENCES game_versions(id) ON DELETE SET NULL, detected_version TEXT, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id)); INSERT INTO users(id,username,password_hash,active) VALUES(1,'migration-admin','unused',1); INSERT INTO games(id,slug,name,launcher_id,external_game_id,enabled) VALUES(100,'legacy-mapped','Legacy Mapped',1,'730',0); INSERT INTO game_versions(id,game_id,version,source_id,source_path,enabled) VALUES(101,100,'build-123',NULL,'',0); INSERT INTO inventory_catalog_mappings(launcher,external_game_id,game_id,game_version_id,detected_version,created_by,created_at) VALUES('steam','730',100,101,'build-123',1,1); DELETE FROM schema_migrations WHERE version>=10`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	rows, err := migrated.db.Query(`PRAGMA table_info(inventory_catalog_mappings)`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "request_hash" {
			found = true
		}
	}
	if !found {
		rows.Close()
		t.Fatal("request_hash missing after v10 migration")
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	fkRows, err := migrated.db.Query(`PRAGMA foreign_key_list(inventory_catalog_mappings)`)
	if err != nil {
		t.Fatal(err)
	}
	restricts := 0
	for fkRows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err = fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			fkRows.Close()
			t.Fatal(err)
		}
		if (table == "games" || table == "game_versions") && onDelete == "RESTRICT" {
			restricts++
		}
	}
	if err = fkRows.Close(); err != nil || restricts != 2 {
		t.Fatalf("restrict foreign keys=%d err=%v", restricts, err)
	}
	var versionMappings int
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM inventory_catalog_version_mappings WHERE launcher='steam' AND external_game_id='730' AND detected_version='build-123' AND game_version_id=101`).Scan(&versionMappings); err != nil || versionMappings != 1 {
		t.Fatalf("migrated version mappings=%d err=%v", versionMappings, err)
	}
	var legacyVersion, legacyDetected any
	if err = migrated.db.QueryRow(`SELECT game_version_id,detected_version FROM inventory_catalog_mappings WHERE launcher='steam' AND external_game_id='730'`).Scan(&legacyVersion, &legacyDetected); err != nil || legacyVersion != nil || legacyDetected != nil {
		t.Fatalf("legacy version columns not cleared: version=%v detected=%v err=%v", legacyVersion, legacyDetected, err)
	}
	var marker int
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=11`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("v11 marker=%d err=%v", marker, err)
	}
}

func TestOpenRejectsInconsistentLegacyInventoryMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inconsistent-v9.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE inventory_catalog_version_mappings; DROP TABLE inventory_catalog_mappings; CREATE TABLE inventory_catalog_mappings(launcher TEXT NOT NULL, external_game_id TEXT NOT NULL, game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE, game_version_id INTEGER REFERENCES game_versions(id) ON DELETE SET NULL, detected_version TEXT, created_by INTEGER NOT NULL REFERENCES users(id), created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id)); INSERT INTO users(id,username,password_hash,active) VALUES(1,'migration-admin','unused',1); INSERT INTO games(id,slug,name,launcher_id,external_game_id,enabled) VALUES(100,'mapped-game','Mapped Game',1,'730',0),(200,'wrong-game','Wrong Game',1,'999',0); INSERT INTO game_versions(id,game_id,version,source_id,source_path,enabled) VALUES(201,200,'build-123',NULL,'',0); INSERT INTO inventory_catalog_mappings(launcher,external_game_id,game_id,game_version_id,detected_version,created_by,created_at) VALUES('steam','730',100,201,'build-123',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if migrated, openErr := Open(path); openErr == nil {
		migrated.Close()
		t.Fatal("inconsistent legacy mapping was migrated")
	} else if !strings.Contains(openErr.Error(), "inconsistent legacy version mapping") {
		t.Fatalf("unexpected migration error: %v", openErr)
	}
}
