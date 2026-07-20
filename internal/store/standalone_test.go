package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStandaloneCatalogAndInventory(t *testing.T) {
	t.Parallel()
	st, err := Open(filepath.Join(t.TempDir(), "standalone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	launchers, err := st.Launchers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var standaloneID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "standalone" {
			standaloneID = launcher.ID
		}
	}
	if standaloneID == 0 {
		t.Fatal("seeded standalone launcher missing")
	}
	if _, err = st.SaveLauncherAtomic(t.Context(), Launcher{Slug: "other-standalone", Name: "Other", Adapter: "standalone", Enabled: true}, nil); err == nil {
		t.Fatal("duplicate standalone system launcher was accepted")
	}
	if err = st.SetCatalogEnabledAtomic(t.Context(), "launcher", standaloneID, 1, false, nil); err == nil {
		t.Fatal("standalone system launcher was deactivated")
	}
	if err = st.DeleteCatalogAtomic(t.Context(), "launcher", standaloneID, 1, nil); err == nil {
		t.Fatal("standalone system launcher was deleted")
	}
	gameID, err := st.SaveGameAtomic(t.Context(), Game{Slug: "open-ra", Name: "OpenRA", LauncherID: standaloneID, ExternalGameID: "ignored", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	games, err := st.StandaloneGames(t.Context())
	if err != nil || len(games) != 1 || games[0].ID != gameID || games[0].ExternalGameID != "open-ra" {
		t.Fatalf("standalone games: %#v %v", games, err)
	}
	if _, err = st.SaveGameAtomic(t.Context(), Game{ID: gameID, Revision: 1, Slug: "renamed-open-ra", Name: "OpenRA", LauncherID: standaloneID, Enabled: true}, nil); err == nil {
		t.Fatal("standalone game identity was renamed")
	}
	if err = st.DeleteCatalogAtomic(t.Context(), "game", gameID, 1, nil); err == nil {
		t.Fatal("standalone game identity was deleted and made reusable")
	}
	scan := InventoryScan{ID: "manual-1", ClientVersion: "1.0.0", ScannedAt: time.Now(), Installations: []InventoryInstallation{{Launcher: "standalone", ExternalGameID: "open-ra", DisplayName: "OpenRA", VersionSource: "manual-registration-unverified", InstallPath: `D:\Games\OpenRA`}}}
	if err = validateInventory(scan); err != nil {
		t.Fatalf("standalone inventory rejected: %v", err)
	}
	var marker int
	if err = st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=18`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("migration marker: %d %v", marker, err)
	}
}

func TestStandaloneMigrationPreservesV17InventoryMappings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "standalone-v17.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = st.db.Exec(`INSERT INTO users(id,username,password_hash,active) VALUES(1,'admin','unused',1); INSERT INTO sources(id,name,kind,base_url,enabled) VALUES(1,'Source','https','https://source.invalid',1)`); err != nil {
		t.Fatal(err)
	}
	launchers, err := st.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var steamID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "steam" {
			steamID = launcher.ID
		}
	}
	gameID, err := st.SaveGameAtomic(ctx, Game{Slug: "legacy-game", Name: "Legacy Game", LauncherID: steamID, ExternalGameID: "42", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "build-1", SourceID: 1, SourcePath: "legacy.zip", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if err = st.CreateDevice(ctx, Device{ID: "legacy-pc", Name: "Legacy PC", PublicKey: key, WindowsVersion: "11", ClientVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO device_inventory_scans(id,device_id,user_id,scanned_at,client_version,request_hash,current,created_at) VALUES('legacy-scan','legacy-pc',1,?,'1.0.0',X'01',1,?)`, []any{now, now}},
		{`INSERT INTO device_inventory_items(scan_id,device_id,position,launcher,external_game_id,display_name,detected_version,version_source,install_path) VALUES('legacy-scan','legacy-pc',0,'steam','42','Legacy Game','build-1','steam-buildid','C:\\Games\\Legacy')`, nil},
		{`INSERT INTO inventory_catalog_mappings(launcher,external_game_id,game_id,request_hash,created_by,created_at) VALUES('steam','42',?,X'02',1,?)`, []any{gameID, now}},
		{`INSERT INTO inventory_catalog_version_mappings(launcher,external_game_id,detected_version,game_version_id,request_hash,created_by,created_at) VALUES('steam','42','build-1',?,X'03',1,?)`, []any{versionID, now}},
	}
	for _, statement := range statements {
		if _, err = st.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("legacy fixture %q: %v", statement.query, err)
		}
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy.SetMaxOpenConns(1)
	if _, err = legacy.Exec(`PRAGMA foreign_keys=OFF; CREATE TABLE device_inventory_items_v17(scan_id TEXT NOT NULL, device_id TEXT NOT NULL, position INTEGER NOT NULL, launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect')), external_game_id TEXT NOT NULL, display_name TEXT NOT NULL, detected_version TEXT, version_source TEXT NOT NULL, install_path TEXT NOT NULL, PRIMARY KEY(device_id,scan_id,position), FOREIGN KEY(device_id,scan_id) REFERENCES device_inventory_scans(device_id,id) ON DELETE CASCADE); INSERT INTO device_inventory_items_v17 SELECT * FROM device_inventory_items; DROP TABLE device_inventory_items; ALTER TABLE device_inventory_items_v17 RENAME TO device_inventory_items; CREATE TABLE inventory_catalog_mappings_v17(launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect')), external_game_id TEXT NOT NULL, game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE RESTRICT, game_version_id INTEGER REFERENCES game_versions(id) ON DELETE RESTRICT, detected_version TEXT, request_hash BLOB NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id)); INSERT INTO inventory_catalog_mappings_v17 SELECT * FROM inventory_catalog_mappings; DROP TABLE inventory_catalog_mappings; ALTER TABLE inventory_catalog_mappings_v17 RENAME TO inventory_catalog_mappings; DELETE FROM launchers WHERE adapter='standalone'; DELETE FROM schema_migrations WHERE version=18; PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var itemCount, mappingCount, versionMappingCount int
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM device_inventory_items WHERE launcher='steam' AND external_game_id='42'`).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM inventory_catalog_mappings WHERE launcher='steam' AND external_game_id='42' AND game_id=?`, gameID).Scan(&mappingCount); err != nil {
		t.Fatal(err)
	}
	if err = migrated.db.QueryRow(`SELECT COUNT(*) FROM inventory_catalog_version_mappings WHERE launcher='steam' AND external_game_id='42' AND detected_version='build-1' AND game_version_id=?`, versionID).Scan(&versionMappingCount); err != nil {
		t.Fatal(err)
	}
	if itemCount != 1 || mappingCount != 1 || versionMappingCount != 1 {
		t.Fatalf("migration lost data: items=%d mappings=%d versionMappings=%d", itemCount, mappingCount, versionMappingCount)
	}
	for _, table := range []string{"device_inventory_items", "inventory_catalog_mappings"} {
		var createSQL string
		if err = migrated.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&createSQL); err != nil || !strings.Contains(createSQL, "'standalone'") {
			t.Fatalf("%s was not migrated: %q %v", table, createSQL, err)
		}
	}
	rows, err := migrated.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign-key violation")
	}
}
