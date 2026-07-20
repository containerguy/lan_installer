package store

import (
	"errors"
	"strings"
)

func (s *Store) migrateStandaloneInventoryV18() error {
	var applied int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=18`).Scan(&applied); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if applied == 0 {
		var createItems, createMappings string
		if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='device_inventory_items'`).Scan(&createItems); err != nil {
			return err
		}
		if err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='inventory_catalog_mappings'`).Scan(&createMappings); err != nil {
			return err
		}
		if !strings.Contains(createItems, "'standalone'") {
			if _, err = tx.Exec(`CREATE TABLE device_inventory_items_v18(scan_id TEXT NOT NULL, device_id TEXT NOT NULL, position INTEGER NOT NULL, launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect','standalone')), external_game_id TEXT NOT NULL, display_name TEXT NOT NULL, detected_version TEXT, version_source TEXT NOT NULL, install_path TEXT NOT NULL, PRIMARY KEY(device_id,scan_id,position), FOREIGN KEY(device_id,scan_id) REFERENCES device_inventory_scans(device_id,id) ON DELETE CASCADE); INSERT INTO device_inventory_items_v18 SELECT * FROM device_inventory_items; DROP TABLE device_inventory_items; ALTER TABLE device_inventory_items_v18 RENAME TO device_inventory_items`); err != nil {
				return err
			}
		}
		if !strings.Contains(createMappings, "'standalone'") {
			if _, err = tx.Exec(`CREATE TEMP TABLE inventory_catalog_version_mappings_v18_copy AS SELECT * FROM inventory_catalog_version_mappings; DROP TABLE inventory_catalog_version_mappings; CREATE TABLE inventory_catalog_mappings_v18(launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect','standalone')), external_game_id TEXT NOT NULL, game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE RESTRICT, game_version_id INTEGER REFERENCES game_versions(id) ON DELETE RESTRICT, detected_version TEXT, request_hash BLOB NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id)); INSERT INTO inventory_catalog_mappings_v18 SELECT * FROM inventory_catalog_mappings; DROP TABLE inventory_catalog_mappings; ALTER TABLE inventory_catalog_mappings_v18 RENAME TO inventory_catalog_mappings; CREATE TABLE inventory_catalog_version_mappings(launcher TEXT NOT NULL, external_game_id TEXT NOT NULL, detected_version TEXT NOT NULL, game_version_id INTEGER NOT NULL REFERENCES game_versions(id) ON DELETE RESTRICT, request_hash BLOB NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id,detected_version), FOREIGN KEY(launcher,external_game_id) REFERENCES inventory_catalog_mappings(launcher,external_game_id) ON DELETE CASCADE); INSERT INTO inventory_catalog_version_mappings SELECT * FROM inventory_catalog_version_mappings_v18_copy; DROP TABLE inventory_catalog_version_mappings_v18_copy`); err != nil {
				return err
			}
		}
	}
	var conflicts int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM launchers WHERE (slug='standalone' OR adapter='standalone') AND NOT (slug='standalone' AND adapter='standalone')`).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts != 0 {
		return errors.New("standalone launcher migration conflicts with an existing launcher slug or adapter")
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO launchers(slug,name,adapter,enabled) VALUES('standalone','Ohne Launcher','standalone',1); UPDATE launchers SET name='Ohne Launcher',enabled=1 WHERE slug='standalone' AND adapter='standalone'; INSERT OR IGNORE INTO schema_migrations(version) VALUES(18)`); err != nil {
		return err
	}
	return tx.Commit()
}
