package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var (
	ErrCatalogNotFound      = errors.New("catalog object not found")
	ErrPublishedEventLocked = errors.New("published event is immutable")
	catalogSlugPattern      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	catalogSHA256Pattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type CatalogReferencedError struct {
	Entity     string
	References int64
}

func (e *CatalogReferencedError) Error() string {
	return fmt.Sprintf("%s is referenced %d time(s)", e.Entity, e.References)
}

func normalizeCatalogText(value string, max int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max {
		return "", errors.New("required value is missing or too long")
	}
	return value, nil
}

func normalizeCatalogSlug(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > 100 || !catalogSlugPattern.MatchString(value) {
		return "", errors.New("slug must contain lowercase letters, numbers and single hyphens")
	}
	return value, nil
}

func normalizeArtifactPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\\:?#%") || path.IsAbs(value) {
		return "", errors.New("artifact path must be a relative slash-separated path")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("artifact path contains control characters")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != value {
		return "", errors.New("artifact path escapes its source")
	}
	return cleaned, nil
}

func normalizeSilentArgs(values []string, verified bool) ([]string, error) {
	if len(values) > 64 {
		return nil, errors.New("too many silent arguments")
	}
	total := 0
	cleaned := make([]string, len(values))
	for index, value := range values {
		if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
			return nil, errors.New("silent arguments must be non-empty and contain no surrounding whitespace")
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return nil, errors.New("silent arguments contain control characters")
			}
		}
		total += len(value)
		cleaned[index] = value
	}
	if total > 4096 {
		return nil, errors.New("silent arguments are too long")
	}
	if verified && len(cleaned) == 0 {
		return nil, errors.New("verified silent arguments cannot be empty")
	}
	return cleaned, nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != "" && !catalogSHA256Pattern.MatchString(value) {
		return "", errors.New("sha256 must contain exactly 64 hexadecimal characters")
	}
	return value, nil
}

func parseCatalogEventTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err
}

func catalogResultID(result sql.Result) (int64, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, ErrCatalogNotFound
	}
	return result.LastInsertId()
}

func catalogSavedID(result sql.Result, existingID int64) (int64, error) {
	id, err := catalogResultID(result)
	if existingID == 0 {
		return id, err
	}
	if errors.Is(err, ErrCatalogNotFound) {
		return 0, ErrRevisionConflict
	}
	return existingID, err
}

func catalogPublishedReference(ctx context.Context, tx *sql.Tx, kind string, id int64) (bool, error) {
	query := map[string]string{
		"launcher":     `SELECT EXISTS(SELECT 1 FROM games g JOIN game_versions gv ON gv.game_id=g.id JOIN event_games eg ON eg.game_version_id=gv.id JOIN events e ON e.id=eg.event_id WHERE g.launcher_id=? AND e.status='published')`,
		"game":         `SELECT EXISTS(SELECT 1 FROM game_versions gv JOIN event_games eg ON eg.game_version_id=gv.id JOIN events e ON e.id=eg.event_id WHERE gv.game_id=? AND e.status='published')`,
		"game-version": `SELECT EXISTS(SELECT 1 FROM event_games eg JOIN events e ON e.id=eg.event_id WHERE eg.game_version_id=? AND e.status='published')`,
	}[kind]
	if query == "" {
		return false, nil
	}
	var referenced bool
	return referenced, tx.QueryRowContext(ctx, query, id).Scan(&referenced)
}

func requireActiveCatalogParent(ctx context.Context, tx *sql.Tx, query string, id int64, label string) error {
	var enabled bool
	if err := tx.QueryRowContext(ctx, query, id).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCatalogNotFound
		}
		return err
	}
	if !enabled {
		return fmt.Errorf("disabled %s cannot be assigned", label)
	}
	return nil
}

func saveCatalogAudit(ctx context.Context, tx *sql.Tx, audit *AuditEntry, objectType string, objectID int64) error {
	if audit == nil {
		return nil
	}
	copy := *audit
	copy.ObjectType = objectType
	copy.ObjectID = strconv.FormatInt(objectID, 10)
	return writeAuditTx(ctx, tx, &copy)
}

func (s *Store) SaveLauncherAtomic(ctx context.Context, value Launcher, audit *AuditEntry) (int64, error) {
	var err error
	if value.Slug, err = normalizeCatalogSlug(value.Slug); err != nil {
		return 0, err
	}
	if value.Name, err = normalizeCatalogText(value.Name, 200); err != nil {
		return 0, err
	}
	value.Adapter = strings.ToLower(strings.TrimSpace(value.Adapter))
	switch value.Adapter {
	case "steam", "ea_app", "ubisoft_connect", "standalone":
	default:
		return 0, errors.New("unsupported launcher adapter")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if value.ID == 0 && value.Adapter == "standalone" {
		return 0, errors.New("the standalone launcher is managed by LANReady")
	}
	if value.ID > 0 {
		var existingAdapter string
		if err = tx.QueryRowContext(ctx, `SELECT adapter FROM launchers WHERE id=?`, value.ID).Scan(&existingAdapter); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrCatalogNotFound
			}
			return 0, err
		}
		if existingAdapter == "standalone" || value.Adapter == "standalone" {
			return 0, errors.New("the standalone launcher is managed by LANReady")
		}
		locked, graphErr := catalogPublishedReference(ctx, tx, "launcher", value.ID)
		if graphErr != nil {
			return 0, graphErr
		}
		if locked {
			return 0, ErrPublishedEventLocked
		}
	}
	var result sql.Result
	if value.ID == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO launchers(slug,name,adapter,enabled) VALUES(?,?,?,?)`, value.Slug, value.Name, value.Adapter, value.Enabled)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE launchers SET slug=?,name=?,adapter=?,enabled=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND revision=?`, value.Slug, value.Name, value.Adapter, value.Enabled, value.ID, value.Revision)
	}
	if err != nil {
		return 0, err
	}
	id, err := catalogSavedID(result, value.ID)
	if err != nil {
		return 0, err
	}
	if err = saveCatalogAudit(ctx, tx, audit, "launcher", id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) SaveGameAtomic(ctx context.Context, value Game, audit *AuditEntry) (int64, error) {
	var err error
	if value.Slug, err = normalizeCatalogSlug(value.Slug); err != nil {
		return 0, err
	}
	if value.Name, err = normalizeCatalogText(value.Name, 200); err != nil {
		return 0, err
	}
	value.ExternalGameID = strings.TrimSpace(value.ExternalGameID)
	if value.LauncherID < 1 || len(value.ExternalGameID) > 256 {
		return 0, errors.New("launcher is required or external id is too long")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var targetAdapter string
	if err = tx.QueryRowContext(ctx, `SELECT adapter FROM launchers WHERE id=?`, value.LauncherID).Scan(&targetAdapter); err != nil {
		return 0, err
	}
	if targetAdapter == "standalone" {
		if len(value.Slug) > 64 {
			return 0, errors.New("standalone game slug is too long for event identity")
		}
		value.ExternalGameID = value.Slug
	}
	launcherChanged := value.ID == 0
	externalIDChanged := value.ID == 0
	if value.ID > 0 {
		var existingLauncherID int64
		var existingExternalID, existingSlug, existingAdapter string
		err = tx.QueryRowContext(ctx, `SELECT g.launcher_id,COALESCE(g.external_game_id,''),g.slug,l.adapter FROM games g JOIN launchers l ON l.id=g.launcher_id WHERE g.id=?`, value.ID).Scan(&existingLauncherID, &existingExternalID, &existingSlug, &existingAdapter)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrCatalogNotFound
		}
		if err != nil {
			return 0, err
		}
		if existingAdapter == "standalone" && (existingSlug != value.Slug || existingLauncherID != value.LauncherID) {
			return 0, errors.New("standalone game identity is immutable; deactivate the game instead")
		}
		launcherChanged = existingLauncherID != value.LauncherID
		externalIDChanged = existingExternalID != value.ExternalGameID
		if launcherChanged || externalIDChanged {
			var incompatibleMappings int64
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM inventory_catalog_mappings m JOIN launchers l ON l.id=? WHERE m.game_id=? AND (m.launcher<>l.adapter OR m.external_game_id<>?)`, value.LauncherID, value.ID, value.ExternalGameID).Scan(&incompatibleMappings); err != nil {
				return 0, err
			}
			if incompatibleMappings > 0 {
				return 0, &CatalogReferencedError{Entity: "inventory mapping", References: incompatibleMappings}
			}
		}
	}
	if launcherChanged {
		if err = requireActiveCatalogParent(ctx, tx, `SELECT enabled FROM launchers WHERE id=?`, value.LauncherID, "launcher"); err != nil {
			return 0, err
		}
	}
	if value.ID > 0 {
		locked, graphErr := catalogPublishedReference(ctx, tx, "game", value.ID)
		if graphErr != nil {
			return 0, graphErr
		}
		if locked {
			return 0, ErrPublishedEventLocked
		}
	}
	var result sql.Result
	if value.ID == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO games(slug,name,launcher_id,external_game_id,enabled) VALUES(?,?,?,?,?)`, value.Slug, value.Name, value.LauncherID, value.ExternalGameID, value.Enabled)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE games SET slug=?,name=?,launcher_id=?,external_game_id=?,enabled=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND revision=?`, value.Slug, value.Name, value.LauncherID, value.ExternalGameID, value.Enabled, value.ID, value.Revision)
	}
	if err != nil {
		return 0, err
	}
	id, err := catalogSavedID(result, value.ID)
	if err != nil {
		return 0, err
	}
	if err = saveCatalogAudit(ctx, tx, audit, "game", id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) SaveLauncherVersionAtomic(ctx context.Context, value LauncherVersion, audit *AuditEntry) (int64, error) {
	var err error
	if value.Version, err = normalizeCatalogText(value.Version, 256); err != nil {
		return 0, err
	}
	if value.SourcePath, err = normalizeArtifactPath(value.SourcePath); err != nil {
		return 0, err
	}
	if value.SHA256, err = normalizeSHA256(value.SHA256); err != nil {
		return 0, err
	}
	if value.LauncherID < 1 || value.SourceID < 1 || value.SizeBytes < 0 {
		return 0, errors.New("launcher, source and a non-negative size are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	silentArgsJSON := "[]"
	value.SilentArgsVerified = false
	launcherChanged := value.ID == 0
	sourceChanged := value.ID == 0
	if value.ID > 0 {
		var existingLauncherID, existingSourceID int64
		err = tx.QueryRowContext(ctx, `SELECT launcher_id,source_id,silent_args_json,silent_args_verified FROM launcher_versions WHERE id=?`, value.ID).Scan(&existingLauncherID, &existingSourceID, &silentArgsJSON, &value.SilentArgsVerified)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrCatalogNotFound
		}
		if err != nil {
			return 0, err
		}
		launcherChanged = existingLauncherID != value.LauncherID
		sourceChanged = existingSourceID != value.SourceID
	}
	if launcherChanged {
		if err = requireActiveCatalogParent(ctx, tx, `SELECT enabled FROM launchers WHERE id=?`, value.LauncherID, "launcher"); err != nil {
			return 0, err
		}
	}
	if sourceChanged {
		if err = requireActiveCatalogParent(ctx, tx, `SELECT enabled FROM sources WHERE id=?`, value.SourceID, "source"); err != nil {
			return 0, err
		}
	}
	var result sql.Result
	if value.ID == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO launcher_versions(launcher_id,version,source_id,source_path,sha256,size_bytes,silent_args_json,silent_args_verified,enabled) VALUES(?,?,?,?,?,?,?,?,?)`, value.LauncherID, value.Version, value.SourceID, value.SourcePath, value.SHA256, value.SizeBytes, silentArgsJSON, value.SilentArgsVerified, value.Enabled)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE launcher_versions SET launcher_id=?,version=?,source_id=?,source_path=?,sha256=?,size_bytes=?,silent_args_json=?,silent_args_verified=?,enabled=?,revision=revision+1 WHERE id=? AND revision=?`, value.LauncherID, value.Version, value.SourceID, value.SourcePath, value.SHA256, value.SizeBytes, silentArgsJSON, value.SilentArgsVerified, value.Enabled, value.ID, value.Revision)
	}
	if err != nil {
		return 0, err
	}
	id, err := catalogSavedID(result, value.ID)
	if err != nil {
		return 0, err
	}
	if err = saveCatalogAudit(ctx, tx, audit, "launcher_version", id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) SaveGameVersionAtomic(ctx context.Context, value GameVersion, audit *AuditEntry) (int64, error) {
	var err error
	if value.Version, err = normalizeCatalogText(value.Version, 256); err != nil {
		return 0, err
	}
	if value.SourcePath, err = normalizeArtifactPath(value.SourcePath); err != nil {
		return 0, err
	}
	if value.SHA256, err = normalizeSHA256(value.SHA256); err != nil {
		return 0, err
	}
	if value.GameID < 1 || value.SourceID < 1 || value.SizeBytes < 0 {
		return 0, errors.New("game, source and a non-negative size are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	gameChanged := value.ID == 0
	sourceChanged := value.ID == 0
	if value.ID > 0 {
		var existingGameID int64
		var existingSourceID sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT game_id,source_id FROM game_versions WHERE id=?`, value.ID).Scan(&existingGameID, &existingSourceID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrCatalogNotFound
		}
		if err != nil {
			return 0, err
		}
		gameChanged = existingGameID != value.GameID
		sourceChanged = !existingSourceID.Valid || existingSourceID.Int64 != value.SourceID
		if gameChanged {
			var incompatibleMappings int64
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM inventory_catalog_version_mappings vm JOIN inventory_catalog_mappings m ON m.launcher=vm.launcher AND m.external_game_id=vm.external_game_id WHERE vm.game_version_id=? AND m.game_id<>?`, value.ID, value.GameID).Scan(&incompatibleMappings); err != nil {
				return 0, err
			}
			if incompatibleMappings > 0 {
				return 0, &CatalogReferencedError{Entity: "inventory mapping", References: incompatibleMappings}
			}
		}
	}
	if gameChanged {
		if err = requireActiveCatalogParent(ctx, tx, `SELECT enabled FROM games WHERE id=?`, value.GameID, "game"); err != nil {
			return 0, err
		}
	}
	if sourceChanged {
		if err = requireActiveCatalogParent(ctx, tx, `SELECT enabled FROM sources WHERE id=?`, value.SourceID, "source"); err != nil {
			return 0, err
		}
	}
	if value.ID > 0 {
		locked, graphErr := catalogPublishedReference(ctx, tx, "game-version", value.ID)
		if graphErr != nil {
			return 0, graphErr
		}
		if locked {
			return 0, ErrPublishedEventLocked
		}
	}
	var result sql.Result
	if value.ID == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO game_versions(game_id,version,source_id,source_path,sha256,size_bytes,enabled) VALUES(?,?,?,?,?,?,?)`, value.GameID, value.Version, value.SourceID, value.SourcePath, value.SHA256, value.SizeBytes, value.Enabled)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE game_versions SET game_id=?,version=?,source_id=?,source_path=?,sha256=?,size_bytes=?,enabled=?,revision=revision+1 WHERE id=? AND revision=?`, value.GameID, value.Version, value.SourceID, value.SourcePath, value.SHA256, value.SizeBytes, value.Enabled, value.ID, value.Revision)
	}
	if err != nil {
		return 0, err
	}
	id, err := catalogSavedID(result, value.ID)
	if err != nil {
		return 0, err
	}
	if err = saveCatalogAudit(ctx, tx, audit, "game_version", id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) SaveEventAtomic(ctx context.Context, value Event, audit *AuditEntry) (int64, error) {
	var err error
	if value.Slug, err = normalizeCatalogSlug(value.Slug); err != nil {
		return 0, err
	}
	if value.Name, err = normalizeCatalogText(value.Name, 200); err != nil {
		return 0, err
	}
	value.StartsAt = strings.TrimSpace(value.StartsAt)
	value.EndsAt = strings.TrimSpace(value.EndsAt)
	start, startErr := parseCatalogEventTime(value.StartsAt)
	end, endErr := parseCatalogEventTime(value.EndsAt)
	if startErr != nil || endErr != nil || (!start.IsZero() && !end.IsZero() && !end.After(start)) {
		return 0, errors.New("invalid event date range")
	}
	switch value.Status {
	case "", "draft":
		value.Status = "draft"
	case "archived":
	default:
		return 0, errors.New("events can only be saved as draft or archived before the publish slice")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var result sql.Result
	if value.ID == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO events(slug,name,starts_at,ends_at,status) VALUES(?,?,?,?,?)`, value.Slug, value.Name, nullable(value.StartsAt), nullable(value.EndsAt), value.Status)
	} else {
		var current string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM events WHERE id=?`, value.ID).Scan(&current); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrCatalogNotFound
			}
			return 0, err
		}
		if current == "published" {
			return 0, ErrPublishedEventLocked
		}
		result, err = tx.ExecContext(ctx, `UPDATE events SET slug=?,name=?,starts_at=?,ends_at=?,status=?,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND revision=?`, value.Slug, value.Name, nullable(value.StartsAt), nullable(value.EndsAt), value.Status, value.ID, value.Revision)
	}
	if err != nil {
		return 0, err
	}
	id, err := catalogSavedID(result, value.ID)
	if err != nil {
		return 0, err
	}
	if err = saveCatalogAudit(ctx, tx, audit, "event", id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) SetCatalogEnabledAtomic(ctx context.Context, kind string, id, expectedRevision int64, enabled bool, audit *AuditEntry) error {
	table := map[string]string{"launcher": "launchers", "game": "games", "launcher-version": "launcher_versions", "game-version": "game_versions"}[kind]
	if table == "" || id < 1 {
		return errors.New("unsupported catalog type")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if kind == "launcher" {
		var adapter string
		if err = tx.QueryRowContext(ctx, `SELECT adapter FROM launchers WHERE id=?`, id).Scan(&adapter); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCatalogNotFound
			}
			return err
		}
		if adapter == "standalone" {
			return errors.New("the standalone launcher is managed by LANReady")
		}
	}
	if !enabled {
		locked, graphErr := catalogPublishedReference(ctx, tx, kind, id)
		if graphErr != nil {
			return graphErr
		}
		if locked {
			return ErrPublishedEventLocked
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE `+table+` SET enabled=?,revision=revision+1 WHERE id=? AND revision=?`, enabled, id, expectedRevision)
	if err != nil {
		return err
	}
	if _, err = catalogResultID(result); err != nil {
		return ErrRevisionConflict
	}
	if err = saveCatalogAudit(ctx, tx, audit, kind, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteCatalogAtomic(ctx context.Context, kind string, id, expectedRevision int64, audit *AuditEntry) error {
	if id < 1 {
		return ErrCatalogNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if kind == "launcher" {
		var adapter string
		if err = tx.QueryRowContext(ctx, `SELECT adapter FROM launchers WHERE id=?`, id).Scan(&adapter); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCatalogNotFound
			}
			return err
		}
		if adapter == "standalone" {
			return errors.New("the standalone launcher is managed by LANReady")
		}
	}
	if kind == "game" {
		var adapter string
		if err = tx.QueryRowContext(ctx, `SELECT l.adapter FROM games g JOIN launchers l ON l.id=g.launcher_id WHERE g.id=?`, id).Scan(&adapter); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCatalogNotFound
			}
			return err
		}
		if adapter == "standalone" {
			return errors.New("standalone games cannot be deleted; deactivate the game instead")
		}
	}
	var table string
	var refs int64
	switch kind {
	case "launcher":
		table = "launchers"
		err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM games WHERE launcher_id=?)+(SELECT COUNT(*) FROM launcher_versions WHERE launcher_id=?)`, id, id).Scan(&refs)
	case "game":
		table = "games"
		err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM game_versions WHERE game_id=?)+(SELECT COUNT(*) FROM inventory_catalog_mappings WHERE game_id=?)`, id, id).Scan(&refs)
	case "launcher-version":
		table = "launcher_versions"
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cache_jobs WHERE target_type='launcher_version' AND target_id=? AND status IN ('queued','running')`, id).Scan(&refs)
	case "game-version":
		table = "game_versions"
		err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM event_games WHERE game_version_id=?)+(SELECT COUNT(*) FROM inventory_catalog_version_mappings WHERE game_version_id=?)+(SELECT COUNT(*) FROM inventory_catalog_mappings WHERE game_version_id=?)+(SELECT COUNT(*) FROM cache_jobs WHERE target_type='game_version' AND target_id=? AND status IN ('queued','running'))`, id, id, id, id).Scan(&refs)
	case "event":
		table = "events"
		var status string
		err = tx.QueryRowContext(ctx, `SELECT status FROM events WHERE id=?`, id).Scan(&status)
		if err == nil && status == "published" {
			return ErrPublishedEventLocked
		}
	default:
		return errors.New("unsupported catalog type")
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCatalogNotFound
		}
		return err
	}
	if refs > 0 {
		return &CatalogReferencedError{Entity: kind, References: refs}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id=? AND revision=?`, id, expectedRevision)
	if err != nil {
		return err
	}
	if _, err = catalogResultID(result); err != nil {
		return ErrRevisionConflict
	}
	if err = saveCatalogAudit(ctx, tx, audit, kind, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveEventGameAtomic(ctx context.Context, value EventGame, audit *AuditEntry) error {
	if value.EventID < 1 || value.GameVersionID < 1 {
		return errors.New("event and game version are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM events WHERE id=?`, value.EventID).Scan(&status); err != nil {
		return err
	}
	if status != "draft" {
		return ErrPublishedEventLocked
	}
	if value.Revision == 0 {
		var gameVersionEnabled, gameEnabled, launcherEnabled, sourceEnabled bool
		err = tx.QueryRowContext(ctx, `SELECT gv.enabled,g.enabled,l.enabled,s.enabled FROM game_versions gv JOIN games g ON g.id=gv.game_id JOIN launchers l ON l.id=g.launcher_id JOIN sources s ON s.id=gv.source_id WHERE gv.id=?`, value.GameVersionID).Scan(&gameVersionEnabled, &gameEnabled, &launcherEnabled, &sourceEnabled)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCatalogNotFound
		}
		if err != nil {
			return err
		}
		switch {
		case !gameVersionEnabled:
			return errors.New("disabled game version cannot be assigned")
		case !gameEnabled:
			return errors.New("disabled game cannot be assigned")
		case !launcherEnabled:
			return errors.New("disabled launcher cannot be assigned")
		case !sourceEnabled:
			return errors.New("disabled source cannot be assigned")
		}
	}
	var result sql.Result
	if value.Revision == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO event_games(event_id,game_version_id,required) VALUES(?,?,?)`, value.EventID, value.GameVersionID, value.Required)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE event_games SET required=?,revision=revision+1 WHERE event_id=? AND game_version_id=? AND revision=?`, value.Required, value.EventID, value.GameVersionID, value.Revision)
	}
	if err != nil {
		return err
	}
	if _, err = catalogResultID(result); err != nil {
		if value.Revision > 0 {
			return ErrRevisionConflict
		}
		return err
	}
	objectID := fmt.Sprintf("%d:%d", value.EventID, value.GameVersionID)
	if audit != nil {
		copy := *audit
		copy.ObjectType = "event_game"
		copy.ObjectID = objectID
		if err = writeAuditTx(ctx, tx, &copy); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteEventGameAtomic(ctx context.Context, eventID, gameVersionID, expectedRevision int64, audit *AuditEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM events WHERE id=?`, eventID).Scan(&status); err != nil {
		return err
	}
	if status != "draft" {
		return ErrPublishedEventLocked
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM event_games WHERE event_id=? AND game_version_id=? AND revision=?`, eventID, gameVersionID, expectedRevision)
	if err != nil {
		return err
	}
	if _, err = catalogResultID(result); err != nil {
		return ErrRevisionConflict
	}
	if audit != nil {
		copy := *audit
		copy.ObjectType = "event_game"
		copy.ObjectID = fmt.Sprintf("%d:%d", eventID, gameVersionID)
		if err = writeAuditTx(ctx, tx, &copy); err != nil {
			return err
		}
	}
	return tx.Commit()
}
