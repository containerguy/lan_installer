package store

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func seededInventoryImportStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/inventory-import.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`INSERT INTO users(id,username,password_hash,active) VALUES(1,'admin','unused',1)`); err != nil {
		st.Close()
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err = st.CreateDevice(context.Background(), Device{ID: "pc-1", Name: "PC 1", PublicKey: key, WindowsVersion: "11", ClientVersion: "1.0.0"}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err = st.db.Exec(`INSERT INTO device_inventory_scans(id,device_id,user_id,scanned_at,client_version,request_hash,current,created_at) VALUES('scan-1','pc-1',1,?,'1.0.0',X'01',1,?); INSERT INTO device_inventory_items(scan_id,device_id,position,launcher,external_game_id,display_name,detected_version,version_source,install_path) VALUES('scan-1','pc-1',0,'steam','730','Counter-Strike 2','build-123','steam-buildid','C:\\Games\\CS2'),('scan-1','pc-1',1,'ea_app','EA-42','EA Game',NULL,'unknown','C:\\Games\\EA')`, now, now); err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st
}

func TestImportInventoryItemCreatesDisabledCatalogDraftAtomically(t *testing.T) {
	st := seededInventoryImportStore(t)
	defer st.Close()
	ctx := context.Background()
	result, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "counter-strike-2", Name: "Counter-Strike 2"}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.GameID < 1 || result.GameVersionID < 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	games, err := st.Games(ctx)
	if err != nil || len(games) != 1 || games[0].Enabled || games[0].ExternalGameID != "730" {
		t.Fatalf("unexpected game draft: %#v err=%v", games, err)
	}
	versions, err := st.GameVersions(ctx)
	if err != nil || len(versions) != 1 || versions[0].Enabled || versions[0].Version != "build-123" || versions[0].SourceID != 0 || versions[0].SourcePath != "" {
		t.Fatalf("unexpected version draft: %#v err=%v", versions, err)
	}
	inventory, err := st.DeviceInventory(ctx, "pc-1")
	if err != nil || inventory.Installations[0].CatalogGameID != result.GameID || inventory.Installations[0].CatalogVersionID != result.GameVersionID {
		t.Fatalf("mapping not visible in inventory: %#v err=%v", inventory, err)
	}
	var auditCount int
	if err = st.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='import_inventory_item' AND object_type='inventory_catalog_mapping'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}
	second, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "counter-strike-2", Name: "Counter-Strike 2"}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"})
	if err != nil || second.GameID != result.GameID || second.Created {
		t.Fatalf("idempotent repeat: %#v err=%v", second, err)
	}
}

func TestImportInventoryItemLinksOnlyCompatibleCatalogGame(t *testing.T) {
	st := seededInventoryImportStore(t)
	defer st.Close()
	ctx := context.Background()
	launchers, err := st.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var steamID, eaID int64
	for _, launcher := range launchers {
		switch launcher.Adapter {
		case "steam":
			steamID = launcher.ID
		case "ea_app":
			eaID = launcher.ID
		}
	}
	steamGame, err := st.SaveGameAtomic(ctx, Game{Slug: "steam-game", Name: "Steam Game", LauncherID: steamID, ExternalGameID: "730", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eaGame, err := st.SaveGameAtomic(ctx, Game{Slug: "ea-game", Name: "EA Game", LauncherID: eaID, ExternalGameID: "EA-42", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, GameID: eaGame}, &AuditEntry{ActorUserID: 1, Action: "link_inventory_item"}); err == nil {
		t.Fatal("different launcher was accepted")
	}
	linked, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, GameID: steamGame}, &AuditEntry{ActorUserID: 1, Action: "link_inventory_item"})
	if err != nil || linked.GameID != steamGame || linked.Created {
		t.Fatalf("link failed: %#v err=%v", linked, err)
	}
	if _, err = st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, GameID: eaGame}, &AuditEntry{ActorUserID: 1, Action: "link_inventory_item"}); !errors.Is(err, ErrInventoryCatalogConflict) {
		t.Fatalf("mapping conflict not detected: %v", err)
	}
}

func TestImportInventoryItemRequiresExplicitDraftForNewDetectedVersion(t *testing.T) {
	st := seededInventoryImportStore(t)
	defer st.Close()
	ctx := context.Background()
	initial, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "counter-strike-2", Name: "Counter-Strike 2"}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Unix()
	if _, err = st.db.Exec(`UPDATE device_inventory_scans SET current=0 WHERE device_id='pc-1'; INSERT INTO device_inventory_scans(id,device_id,user_id,scanned_at,client_version,request_hash,current,created_at) VALUES('scan-2','pc-1',1,?,'1.1.0',X'02',1,?); INSERT INTO device_inventory_items(scan_id,device_id,position,launcher,external_game_id,display_name,detected_version,version_source,install_path) VALUES('scan-2','pc-1',0,'steam','730','Counter-Strike 2','build-124','steam-buildid','C:\\Games\\CS2')`, now, now); err != nil {
		t.Fatal(err)
	}
	inventory, err := st.DeviceInventory(ctx, "pc-1")
	if err != nil || inventory.Installations[0].CatalogGameID != initial.GameID || inventory.Installations[0].CatalogVersionID != 0 {
		t.Fatalf("new build incorrectly mapped to old version: %#v err=%v", inventory, err)
	}
	if _, err = st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, CreateVersion: true}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_version"}); !errors.Is(err, ErrCatalogNotFound) {
		t.Fatalf("stale scan accepted: %v", err)
	}
	created, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-2", Position: 0, CreateVersion: true}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_version"})
	if err != nil || !created.VersionCreated || created.GameID != initial.GameID || created.GameVersionID == initial.GameVersionID {
		t.Fatalf("new version draft: %#v err=%v", created, err)
	}
	repeated, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-2", Position: 0, CreateVersion: true}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_version"})
	if err != nil || repeated.GameVersionID != created.GameVersionID || repeated.VersionCreated {
		t.Fatalf("version double-submit: %#v err=%v", repeated, err)
	}
	inventory, err = st.DeviceInventory(ctx, "pc-1")
	if err != nil || inventory.Installations[0].CatalogVersionID != created.GameVersionID {
		t.Fatalf("new version mapping missing: %#v err=%v", inventory, err)
	}
	secondKey := make([]byte, 32)
	_, _ = rand.Read(secondKey)
	if err = st.CreateDevice(ctx, Device{ID: "pc-2", Name: "PC 2", PublicKey: secondKey, WindowsVersion: "11", ClientVersion: "1.1.0"}); err != nil {
		t.Fatal(err)
	}
	now = time.Now().UTC().Unix()
	if _, err = st.db.Exec(`INSERT INTO device_inventory_scans(id,device_id,user_id,scanned_at,client_version,request_hash,current,created_at) VALUES('scan-pc2','pc-2',1,?,'1.1.0',X'03',1,?); INSERT INTO device_inventory_items(scan_id,device_id,position,launcher,external_game_id,display_name,detected_version,version_source,install_path) VALUES('scan-pc2','pc-2',0,'steam','730','Counter-Strike 2','build-125','steam-buildid','D:\\Games\\CS2')`, now, now); err != nil {
		t.Fatal(err)
	}
	secondBuild, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-2", ScanID: "scan-pc2", Position: 0, CreateVersion: true}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_version"})
	if err != nil || !secondBuild.VersionCreated || secondBuild.GameVersionID == created.GameVersionID {
		t.Fatalf("second device build: %#v err=%v", secondBuild, err)
	}
	firstInventory, err := st.DeviceInventory(ctx, "pc-1")
	if err != nil || firstInventory.Installations[0].CatalogVersionID != created.GameVersionID {
		t.Fatalf("first device build mapping was overwritten: %#v err=%v", firstInventory, err)
	}
	secondInventory, err := st.DeviceInventory(ctx, "pc-2")
	if err != nil || secondInventory.Installations[0].CatalogVersionID != secondBuild.GameVersionID {
		t.Fatalf("second device build mapping missing: %#v err=%v", secondInventory, err)
	}
}

func TestImportInventoryItemKeepsDetectedAndCatalogVersionSeparate(t *testing.T) {
	st := seededInventoryImportStore(t)
	defer st.Close()
	result, err := st.ImportInventoryItem(context.Background(), InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "counter-strike-2", Name: "Counter-Strike 2", Version: "LAN Release Alpha"}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"})
	if err != nil || result.GameVersionID < 1 {
		t.Fatalf("custom catalog version: %#v err=%v", result, err)
	}
	inventory, err := st.DeviceInventory(context.Background(), "pc-1")
	if err != nil || inventory.Installations[0].CatalogVersionID != result.GameVersionID || *inventory.Installations[0].DetectedVersion != "build-123" {
		t.Fatalf("detected build no longer resolves to custom version: %#v err=%v", inventory, err)
	}
	versions, err := st.GameVersions(context.Background())
	if err != nil || len(versions) != 1 || versions[0].Version != "LAN Release Alpha" {
		t.Fatalf("catalog version label was not preserved: %#v err=%v", versions, err)
	}
}

func TestImportInventoryItemConflictingCreateFingerprintAndCatalogInvariants(t *testing.T) {
	st := seededInventoryImportStore(t)
	defer st.Close()
	ctx := context.Background()
	created, err := st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "counter-strike-2", Name: "Counter-Strike 2"}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ImportInventoryItem(ctx, InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "different-request", Name: "Different"}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"}); !errors.Is(err, ErrInventoryCatalogConflict) {
		t.Fatalf("different create request silently accepted: %v", err)
	}
	games, err := st.Games(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var imported Game
	for _, game := range games {
		if game.ID == created.GameID {
			imported = game
		}
	}
	imported.ExternalGameID = "999"
	if _, err = st.SaveGameAtomic(ctx, imported, nil); err == nil {
		t.Fatal("mapped game external id was changed")
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
	otherGame, err := st.SaveGameAtomic(ctx, Game{Slug: "other-game", Name: "Other Game", LauncherID: steamID, ExternalGameID: "other", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := st.SaveSourceAtomic(ctx, Source{Name: "Packages", Kind: "https", BaseURL: "https://packages.example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveGameVersionAtomic(ctx, GameVersion{ID: created.GameVersionID, Revision: 1, GameID: otherGame, Version: "build-123", SourceID: source.ID, SourcePath: "other.zip", Enabled: false}, nil); err == nil {
		t.Fatal("mapped version was moved to another game")
	}
	if err = st.DeleteCatalogAtomic(ctx, "game-version", created.GameVersionID, 1, nil); err == nil {
		t.Fatal("mapped game version was deleted")
	}
	if err = st.DeleteCatalogAtomic(ctx, "game", created.GameID, 1, nil); err == nil {
		t.Fatal("mapped game was deleted")
	}
}

func TestImportInventoryItemAcceptsMaximumInventoryIdentifiers(t *testing.T) {
	st := seededInventoryImportStore(t)
	defer st.Close()
	externalID := strings.Repeat("x", 256)
	version := strings.Repeat("v", 256)
	if _, err := st.db.Exec(`UPDATE device_inventory_items SET external_game_id=?,detected_version=? WHERE device_id='pc-1' AND scan_id='scan-1' AND position=1`, externalID, version); err != nil {
		t.Fatal(err)
	}
	result, err := st.ImportInventoryItem(context.Background(), InventoryCatalogImport{DeviceID: "pc-1", ScanID: "scan-1", Position: 1, Slug: "maximum-identifiers", Name: "Maximum Identifiers"}, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"})
	if err != nil || !result.Created || !result.VersionCreated {
		t.Fatalf("maximum valid inventory values were not importable: %#v err=%v", result, err)
	}
	games, err := st.Games(context.Background())
	if err != nil || len(games) != 1 || games[0].ExternalGameID != externalID {
		t.Fatalf("external id was not preserved: %#v err=%v", games, err)
	}
	versions, err := st.GameVersions(context.Background())
	if err != nil || len(versions) != 1 || versions[0].Version != version {
		t.Fatalf("version was not preserved: %#v err=%v", versions, err)
	}
}

func TestImportInventoryItemConcurrentDifferentCreatesConflict(t *testing.T) {
	st := seededInventoryImportStore(t)
	defer st.Close()
	requests := []InventoryCatalogImport{
		{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "first-choice", Name: "First Choice"},
		{DeviceID: "pc-1", ScanID: "scan-1", Position: 0, Slug: "second-choice", Name: "Second Choice"},
	}
	errorsOut := make(chan error, len(requests))
	for _, request := range requests {
		request := request
		go func() {
			_, err := st.ImportInventoryItem(context.Background(), request, &AuditEntry{ActorUserID: 1, Action: "import_inventory_item"})
			errorsOut <- err
		}()
	}
	successes, conflicts := 0, 0
	for range requests {
		err := <-errorsOut
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrInventoryCatalogConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}
