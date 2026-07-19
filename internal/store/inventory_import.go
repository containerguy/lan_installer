package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInventoryCatalogConflict = errors.New("inventory item has a conflicting catalog mapping")

type InventoryCatalogImport struct {
	DeviceID, ScanID    string
	Position            int
	GameID              int64
	Slug, Name, Version string
	CreateVersion       bool
}

type InventoryCatalogImportResult struct {
	GameID, GameVersionID   int64
	GameName                string
	Created, VersionCreated bool
}

type inventoryImportFingerprint struct {
	Launcher, ExternalGameID, DetectedVersion string
	Mode                                      string
	GameID                                    int64
	Slug, Name, Version                       string
}

// ImportInventoryItem explicitly maps a detected installation to an existing
// game or creates a disabled game/version draft. A draft version intentionally
// has no source until an administrator completes it in the catalog editor.
func (s *Store) ImportInventoryItem(ctx context.Context, value InventoryCatalogImport, audit *AuditEntry) (InventoryCatalogImportResult, error) {
	var out InventoryCatalogImportResult
	value.DeviceID = strings.TrimSpace(value.DeviceID)
	value.ScanID = strings.TrimSpace(value.ScanID)
	value.Version = strings.TrimSpace(value.Version)
	if value.DeviceID == "" || value.ScanID == "" || value.Position < 0 || value.Position > 9999 || audit == nil || audit.ActorUserID < 1 {
		return out, errors.New("device, scan, position and actor are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	var launcher, externalID, detectedName string
	var detectedVersion sql.NullString
	var launcherID int64
	err = tx.QueryRowContext(ctx, `SELECT i.launcher,i.external_game_id,i.display_name,i.detected_version,l.id FROM device_inventory_items i JOIN device_inventory_scans s ON s.device_id=i.device_id AND s.id=i.scan_id AND s.current=1 JOIN launchers l ON l.adapter=i.launcher WHERE i.device_id=? AND i.scan_id=? AND i.position=? ORDER BY l.enabled DESC,l.id LIMIT 1`, value.DeviceID, value.ScanID, value.Position).Scan(&launcher, &externalID, &detectedName, &detectedVersion, &launcherID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrCatalogNotFound
	}
	if err != nil {
		return out, err
	}
	detectedRaw := ""
	if detectedVersion.Valid {
		detectedRaw = strings.TrimSpace(detectedVersion.String)
	}
	if value.CreateVersion {
		if detectedRaw == "" {
			return out, errors.New("the current inventory item has no detected version")
		}
		value.Version = detectedRaw
	} else if value.GameID > 0 {
		value.Version = detectedRaw
	} else if value.Version == "" {
		value.Version = detectedRaw
	}
	if len(value.Version) > 256 {
		return out, errors.New("detected version is too long for the catalog")
	}
	mode := "link"
	if value.CreateVersion {
		mode = "version"
	} else if value.GameID == 0 {
		mode = "create"
		if value.Slug, err = normalizeCatalogSlug(value.Slug); err != nil {
			return out, err
		}
		if value.Name == "" {
			value.Name = detectedName
		}
		if value.Name, err = normalizeCatalogText(value.Name, 200); err != nil {
			return out, err
		}
	}
	fingerprintJSON, _ := json.Marshal(inventoryImportFingerprint{Launcher: launcher, ExternalGameID: externalID, DetectedVersion: detectedRaw, Mode: mode, GameID: value.GameID, Slug: value.Slug, Name: value.Name, Version: value.Version})
	requestHash := sha256.Sum256(fingerprintJSON)

	var existingHash []byte
	err = tx.QueryRowContext(ctx, `SELECT m.game_id,m.request_hash,g.name FROM inventory_catalog_mappings m JOIN games g ON g.id=m.game_id WHERE m.launcher=? AND m.external_game_id=?`, launcher, externalID).Scan(&out.GameID, &existingHash, &out.GameName)
	if err == nil {
		if value.CreateVersion {
			return s.importDetectedVersion(ctx, tx, value, launcher, externalID, detectedRaw, requestHash[:], out, audit)
		}
		if bytes.Equal(existingHash, requestHash[:]) {
			return out, tx.Commit()
		}
		return InventoryCatalogImportResult{}, ErrInventoryCatalogConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if value.CreateVersion {
		return out, ErrInventoryCatalogConflict
	}

	if value.GameID > 0 {
		var existingAdapter string
		var existingExternalID sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT g.name,l.adapter,g.external_game_id FROM games g JOIN launchers l ON l.id=g.launcher_id WHERE g.id=?`, value.GameID).Scan(&out.GameName, &existingAdapter, &existingExternalID)
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrCatalogNotFound
		}
		if err != nil {
			return out, err
		}
		if existingAdapter != launcher {
			return out, errors.New("catalog game uses a different launcher")
		}
		if existingExternalID.Valid && strings.TrimSpace(existingExternalID.String) != "" && existingExternalID.String != externalID {
			return out, errors.New("catalog game uses a different external id")
		}
		out.GameID = value.GameID
		if value.Version != "" {
			err = tx.QueryRowContext(ctx, `SELECT id FROM game_versions WHERE game_id=? AND version=?`, out.GameID, value.Version).Scan(&out.GameVersionID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
		}
	} else {
		var launcherEnabled bool
		if err = tx.QueryRowContext(ctx, `SELECT enabled FROM launchers WHERE id=?`, launcherID).Scan(&launcherEnabled); err != nil {
			return out, err
		}
		if !launcherEnabled {
			return out, errors.New("disabled launcher cannot be assigned")
		}
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO games(slug,name,launcher_id,external_game_id,enabled) VALUES(?,?,?,?,0)`, value.Slug, value.Name, launcherID, externalID)
		if insertErr != nil {
			return out, insertErr
		}
		out.GameID, err = result.LastInsertId()
		if err != nil {
			return out, err
		}
		out.GameName = value.Name
		out.Created = true
		if value.Version != "" {
			result, err = tx.ExecContext(ctx, `INSERT INTO game_versions(game_id,version,source_id,source_path,enabled) VALUES(?,?,NULL,'',0)`, out.GameID, value.Version)
			if err != nil {
				return out, err
			}
			out.GameVersionID, err = result.LastInsertId()
			if err != nil {
				return out, err
			}
			out.VersionCreated = true
		}
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO inventory_catalog_mappings(launcher,external_game_id,game_id,game_version_id,detected_version,request_hash,created_by,created_at) VALUES(?,?,?,NULL,NULL,?,?,?)`, launcher, externalID, out.GameID, requestHash[:], audit.ActorUserID, time.Now().UTC().Unix()); err != nil {
		return InventoryCatalogImportResult{}, err
	}
	if out.GameVersionID > 0 && detectedRaw != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO inventory_catalog_version_mappings(launcher,external_game_id,detected_version,game_version_id,request_hash,created_by,created_at) VALUES(?,?,?,?,?,?,?)`, launcher, externalID, detectedRaw, out.GameVersionID, requestHash[:], audit.ActorUserID, time.Now().UTC().Unix()); err != nil {
			return InventoryCatalogImportResult{}, err
		}
	}
	if err = saveInventoryImportAudit(ctx, tx, value, launcher, externalID, out, audit); err != nil {
		return InventoryCatalogImportResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return InventoryCatalogImportResult{}, err
	}
	return out, nil
}

func (s *Store) importDetectedVersion(ctx context.Context, tx *sql.Tx, value InventoryCatalogImport, launcher, externalID, detectedRaw string, requestHash []byte, out InventoryCatalogImportResult, audit *AuditEntry) (InventoryCatalogImportResult, error) {
	var existingHash []byte
	err := tx.QueryRowContext(ctx, `SELECT game_version_id,request_hash FROM inventory_catalog_version_mappings WHERE launcher=? AND external_game_id=? AND detected_version=?`, launcher, externalID, detectedRaw).Scan(&out.GameVersionID, &existingHash)
	if err == nil {
		if bytes.Equal(existingHash, requestHash) {
			return out, tx.Commit()
		}
		return InventoryCatalogImportResult{}, ErrInventoryCatalogConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return InventoryCatalogImportResult{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM game_versions WHERE game_id=? AND version=?`, out.GameID, value.Version).Scan(&out.GameVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO game_versions(game_id,version,source_id,source_path,enabled) VALUES(?,?,NULL,'',0)`, out.GameID, value.Version)
		if insertErr != nil {
			return InventoryCatalogImportResult{}, insertErr
		}
		out.GameVersionID, err = result.LastInsertId()
		out.VersionCreated = err == nil
	}
	if err != nil {
		return InventoryCatalogImportResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO inventory_catalog_version_mappings(launcher,external_game_id,detected_version,game_version_id,request_hash,created_by,created_at) VALUES(?,?,?,?,?,?,?)`, launcher, externalID, detectedRaw, out.GameVersionID, requestHash, audit.ActorUserID, time.Now().UTC().Unix()); err != nil {
		return InventoryCatalogImportResult{}, err
	}
	if err = saveInventoryImportAudit(ctx, tx, value, launcher, externalID, out, audit); err != nil {
		return InventoryCatalogImportResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return InventoryCatalogImportResult{}, err
	}
	return out, nil
}

func saveInventoryImportAudit(ctx context.Context, tx *sql.Tx, value InventoryCatalogImport, launcher, externalID string, out InventoryCatalogImportResult, audit *AuditEntry) error {
	copy := *audit
	copy.ObjectType = "inventory_catalog_mapping"
	copy.ObjectID = launcher + ":" + externalID
	copy.Details = fmt.Sprintf("device=%s scan=%s position=%d game=%d version=%d", value.DeviceID, value.ScanID, value.Position, out.GameID, out.GameVersionID)
	return writeAuditTx(ctx, tx, &copy)
}
