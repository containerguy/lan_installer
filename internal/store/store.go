package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Active       bool
	Roles        []string
}
type Session struct {
	User      User
	CSRFToken string
	ExpiresAt time.Time
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		if chmodErr := os.Chmod(file, 0o600); chmodErr != nil && !errors.Is(chmodErr, os.ErrNotExist) {
			db.Close()
			return nil, chmodErr
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	rows, err := s.db.Query(`PRAGMA table_info(sources)`)
	if err != nil {
		return err
	}
	hasLegacy := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "credential_ref" {
			hasLegacy = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasLegacy {
		if _, err := s.db.Exec(`ALTER TABLE sources DROP COLUMN credential_ref`); err != nil {
			return err
		}
	}
	for _, column := range []struct{ name, definition string }{
		{"revision", "INTEGER NOT NULL DEFAULT 1"},
		{"last_test_state", "TEXT NOT NULL DEFAULT 'never' CHECK(last_test_state IN ('never','succeeded','failed'))"},
		{"last_tested_at", "TEXT"},
		{"last_test_latency_ms", "INTEGER"},
	} {
		if err = s.ensureColumn("sources", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []struct{ table, name, definition string }{
		{"launcher_versions", "enabled", "INTEGER NOT NULL DEFAULT 1"},
		{"game_versions", "enabled", "INTEGER NOT NULL DEFAULT 1"},
	} {
		if err = s.ensureColumn(column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err = s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(2),(3),(4),(5),(7)`); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, column := range []struct{ table, name, definition string }{
		{"launchers", "revision", "INTEGER NOT NULL DEFAULT 1"},
		{"games", "revision", "INTEGER NOT NULL DEFAULT 1"},
		{"launcher_versions", "revision", "INTEGER NOT NULL DEFAULT 1"},
		{"launcher_versions", "silent_args_json", "TEXT NOT NULL DEFAULT '[]'"},
		{"launcher_versions", "silent_args_verified", "INTEGER NOT NULL DEFAULT 0"},
		{"game_versions", "revision", "INTEGER NOT NULL DEFAULT 1"},
		{"events", "revision", "INTEGER NOT NULL DEFAULT 1"},
		{"event_games", "revision", "INTEGER NOT NULL DEFAULT 1"},
	} {
		if err = ensureColumnTx(tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(6)`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = s.ensureColumn("device_inventory_scans", "request_hash", "BLOB NOT NULL DEFAULT X''"); err != nil {
		return err
	}
	if err = s.ensureColumn("inventory_catalog_mappings", "request_hash", "BLOB NOT NULL DEFAULT X''"); err != nil {
		return err
	}
	if err = s.rebuildInventoryCatalogMappingsIfNeeded(); err != nil {
		return err
	}
	if err = s.ensureColumn("devices", "client_version_high_watermark", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err = s.db.Exec(`UPDATE devices SET client_version_high_watermark=client_version WHERE client_version_high_watermark=''`); err != nil {
		return err
	}
	var inconsistentLegacyMappings int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM inventory_catalog_mappings m LEFT JOIN game_versions v ON v.id=m.game_version_id WHERE m.game_version_id IS NOT NULL AND (v.id IS NULL OR v.game_id<>m.game_id)`).Scan(&inconsistentLegacyMappings); err != nil {
		return err
	}
	if inconsistentLegacyMappings > 0 {
		return fmt.Errorf("inventory catalog migration refused %d inconsistent legacy version mapping(s)", inconsistentLegacyMappings)
	}
	if _, err = s.db.Exec(`INSERT OR IGNORE INTO inventory_catalog_version_mappings(launcher,external_game_id,detected_version,game_version_id,request_hash,created_by,created_at) SELECT launcher,external_game_id,detected_version,game_version_id,request_hash,created_by,created_at FROM inventory_catalog_mappings WHERE game_version_id IS NOT NULL AND detected_version IS NOT NULL; UPDATE inventory_catalog_mappings SET game_version_id=NULL,detected_version=NULL WHERE game_version_id IS NOT NULL OR detected_version IS NOT NULL`); err != nil {
		return err
	}
	if _, err = s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(8),(9),(10),(11),(12),(13),(14),(15)`); err != nil {
		return err
	}
	if err = s.backfillEventReleaseArtifacts(); err != nil {
		return err
	}
	if err = s.rebuildCacheJobsSourceFKIfNeeded(); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(16),(17)`)
	return err
}

func (s *Store) BootstrapAdmin(ctx context.Context, username, passwordHash string) (bool, error) {
	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" || passwordHash == "" {
		return false, errors.New("admin username and password hash are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	if count > 0 {
		return false, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,active) VALUES(?,?,1)`, username, passwordHash)
	if err != nil {
		return false, err
	}
	uid, _ := result.LastInsertId()
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_roles(user_id,role_id) SELECT ?,id FROM roles WHERE name='admin'`, uid); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(actor_user_id,action,object_type,object_id,details) VALUES(?,'bootstrap_admin','user',?,?)`, uid, fmt.Sprint(uid), username); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) SetUserRoles(ctx context.Context, userID int64, roles ...string) error {
	if userID < 1 || len(roles) == 0 {
		return errors.New("user and at least one role are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, role := range roles {
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO user_roles(user_id,role_id) SELECT ?,id FROM roles WHERE name=?`, userID, role)
		if insertErr != nil {
			return insertErr
		}
		count, rowsErr := result.RowsAffected()
		if rowsErr != nil || count != 1 {
			return errors.New("unknown role")
		}
	}
	return tx.Commit()
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id,username,password_hash,active FROM users WHERE username=?`, strings.ToLower(strings.TrimSpace(username))).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Active)
	if err != nil {
		return u, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.name FROM roles r JOIN user_roles ur ON ur.role_id=r.id WHERE ur.user_id=? ORDER BY r.name`, u.ID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return u, err
		}
		u.Roles = append(u.Roles, role)
	}
	return u, rows.Err()
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func tokenHash(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }

func (s *Store) CreateSession(ctx context.Context, userID int64, lifetime time.Duration) (token string, session Session, err error) {
	token, err = randomToken()
	if err != nil {
		return
	}
	session.CSRFToken, err = randomToken()
	if err != nil {
		return
	}
	session.ExpiresAt = time.Now().UTC().Add(lifetime)
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at) VALUES(?,?,?,?)`, tokenHash(token), userID, session.CSRFToken, session.ExpiresAt.Unix())
	return
}

func (s *Store) Session(ctx context.Context, token string) (Session, error) {
	var out Session
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT s.csrf_token,s.expires_at,u.id,u.username,u.password_hash,u.active FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, tokenHash(token)).Scan(&out.CSRFToken, &expires, &out.User.ID, &out.User.Username, &out.User.PasswordHash, &out.User.Active)
	if err != nil {
		return out, err
	}
	out.ExpiresAt = time.Unix(expires, 0).UTC()
	if !out.User.Active || time.Now().UTC().After(out.ExpiresAt) {
		_ = s.DeleteSession(ctx, token)
		return Session{}, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.name FROM roles r JOIN user_roles ur ON ur.role_id=r.id WHERE ur.user_id=? ORDER BY r.name`, out.User.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return out, err
		}
		out.User.Roles = append(out.User.Roles, role)
	}
	return out, rows.Err()
}
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash(token))
	return err
}
func (s *Store) Audit(ctx context.Context, userID *int64, action, objectType, objectID, details, remoteAddr string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(actor_user_id,action,object_type,object_id,details,remote_addr) VALUES(?,?,?,?,?,?)`, userID, action, objectType, objectID, details, remoteAddr)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT OR IGNORE INTO schema_migrations(version) VALUES(1);
CREATE TABLE IF NOT EXISTS users(id INTEGER PRIMARY KEY, username TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS roles(id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL);
INSERT OR IGNORE INTO roles(name,description) VALUES('admin','Vollzugriff'),('operator','Spiele und Events verwalten'),('viewer','Nur lesen');
CREATE TABLE IF NOT EXISTS user_roles(user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, role_id INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE, PRIMARY KEY(user_id,role_id));
CREATE TABLE IF NOT EXISTS external_identities(id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, issuer TEXT NOT NULL, subject TEXT NOT NULL, email TEXT, UNIQUE(issuer,subject));
CREATE TABLE IF NOT EXISTS sessions(id INTEGER PRIMARY KEY, token_hash BLOB NOT NULL UNIQUE, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, csrf_token TEXT NOT NULL, expires_at INTEGER NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS audit_log(id INTEGER PRIMARY KEY, actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL, action TEXT NOT NULL, object_type TEXT NOT NULL, object_id TEXT, details TEXT, remote_addr TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS idempotency_records(actor_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, route TEXT NOT NULL, idempotency_key TEXT NOT NULL, request_hash BLOB NOT NULL, state TEXT NOT NULL CHECK(state IN ("pending","complete")), claimed_at INTEGER NOT NULL, status INTEGER, content_type TEXT, etag TEXT, response_body BLOB, expires_at INTEGER NOT NULL, PRIMARY KEY(actor_user_id,route,idempotency_key));
CREATE INDEX IF NOT EXISTS idempotency_expires_idx ON idempotency_records(expires_at);
CREATE TABLE IF NOT EXISTS sources(id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 1, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL CHECK(kind IN ('https','webdav','nextcloud_webdav','mounted')), base_url TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, last_test_state TEXT NOT NULL DEFAULT 'never' CHECK(last_test_state IN ('never','succeeded','failed')), last_tested_at TEXT, last_test_latency_ms INTEGER, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS webdav_source_config(source_id INTEGER PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE, auth_type TEXT NOT NULL CHECK(auth_type IN ('none','basic')), username TEXT NOT NULL DEFAULT '', secret_nonce BLOB, secret_ciphertext BLOB, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS launchers(id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 1, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, adapter TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT OR IGNORE INTO launchers(slug,name,adapter) VALUES('steam','Steam','steam'),('ea-app','EA App','ea_app'),('ubisoft-connect','Ubisoft Connect','ubisoft_connect');
CREATE TABLE IF NOT EXISTS games(id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 1, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, launcher_id INTEGER REFERENCES launchers(id), external_game_id TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS launcher_versions(id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 1, launcher_id INTEGER NOT NULL REFERENCES launchers(id) ON DELETE CASCADE, version TEXT NOT NULL, source_id INTEGER NOT NULL REFERENCES sources(id), source_path TEXT NOT NULL, sha256 TEXT, size_bytes INTEGER, silent_args TEXT, silent_args_json TEXT NOT NULL DEFAULT '[]', silent_args_verified INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(launcher_id,version));
CREATE TABLE IF NOT EXISTS game_versions(id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 1, game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE, version TEXT NOT NULL, source_id INTEGER REFERENCES sources(id), source_path TEXT NOT NULL, sha256 TEXT, size_bytes INTEGER, enabled INTEGER NOT NULL DEFAULT 1, published_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(game_id,version));
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 1, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, starts_at TEXT, ends_at TEXT, status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','published','archived')), created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS event_games(event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE, game_version_id INTEGER NOT NULL REFERENCES game_versions(id), revision INTEGER NOT NULL DEFAULT 1, required INTEGER NOT NULL DEFAULT 1, PRIMARY KEY(event_id,game_version_id));
CREATE TABLE IF NOT EXISTS artifact_blobs(digest TEXT PRIMARY KEY CHECK(length(digest)=64 AND digest NOT GLOB '*[^0-9a-f]*'), size_bytes INTEGER NOT NULL CHECK(size_bytes>=0 AND size_bytes<=1099511627776), content_type TEXT NOT NULL CHECK(length(content_type)>0), created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS cache_jobs(id TEXT PRIMARY KEY, target_type TEXT NOT NULL CHECK(target_type IN ('launcher_version','game_version')), target_id INTEGER NOT NULL, target_revision INTEGER NOT NULL CHECK(target_revision>0), source_id INTEGER NOT NULL, source_revision INTEGER NOT NULL CHECK(source_revision>0), source_path TEXT NOT NULL, expected_sha256 TEXT NOT NULL DEFAULT '' CHECK(expected_sha256='' OR (length(expected_sha256)=64 AND expected_sha256 NOT GLOB '*[^0-9a-f]*')), expected_size INTEGER NOT NULL DEFAULT 0 CHECK(expected_size>=0 AND expected_size<=1099511627776), status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled')), progress_bytes INTEGER NOT NULL DEFAULT 0 CHECK(progress_bytes>=0), attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0), cancel_requested INTEGER NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', artifact_digest TEXT REFERENCES artifact_blobs(digest) ON DELETE RESTRICT, artifact_size INTEGER NOT NULL DEFAULT 0 CHECK(artifact_size>=0), content_type TEXT NOT NULL DEFAULT '', created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_cache_job_per_target ON cache_jobs(target_type,target_id) WHERE status IN ('queued','running');
CREATE INDEX IF NOT EXISTS cache_jobs_status_created_idx ON cache_jobs(status,created_at);
CREATE TABLE IF NOT EXISTS event_releases(event_id TEXT NOT NULL REFERENCES events(slug) ON DELETE RESTRICT, sequence INTEGER NOT NULL CHECK(sequence>0), release_id TEXT NOT NULL UNIQUE, key_id TEXT NOT NULL, envelope_json BLOB NOT NULL, payload_json BLOB NOT NULL, issued_at INTEGER NOT NULL, valid_until INTEGER NOT NULL, minimum_client_version TEXT NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(event_id,sequence));
CREATE TABLE IF NOT EXISTS event_release_artifacts(event_id TEXT NOT NULL, sequence INTEGER NOT NULL, digest TEXT NOT NULL REFERENCES artifact_blobs(digest) ON DELETE RESTRICT, size_bytes INTEGER NOT NULL CHECK(size_bytes>=0), content_type TEXT NOT NULL, PRIMARY KEY(event_id,sequence,digest), FOREIGN KEY(event_id,sequence) REFERENCES event_releases(event_id,sequence) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS active_event_release(singleton INTEGER PRIMARY KEY CHECK(singleton=1), event_id TEXT NOT NULL, sequence INTEGER NOT NULL, FOREIGN KEY(event_id,sequence) REFERENCES event_releases(event_id,sequence) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS client_update_releases(channel TEXT NOT NULL CHECK(channel='stable'), sequence INTEGER NOT NULL CHECK(sequence>0), version TEXT NOT NULL, minimum_version TEXT NOT NULL, artifact_digest TEXT NOT NULL REFERENCES artifact_blobs(digest) ON DELETE RESTRICT, size_bytes INTEGER NOT NULL CHECK(size_bytes>0), key_id TEXT NOT NULL, envelope_json BLOB NOT NULL, payload_json BLOB NOT NULL, published_at INTEGER NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(channel,sequence), UNIQUE(channel,version));
CREATE TABLE IF NOT EXISTS devices(id TEXT PRIMARY KEY, name TEXT NOT NULL, public_key BLOB NOT NULL UNIQUE, windows_version TEXT NOT NULL, client_version TEXT NOT NULL, client_version_high_watermark TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('active','revoked')), enrolled_at INTEGER NOT NULL, last_seen_at INTEGER);
CREATE TABLE IF NOT EXISTS enrollment_codes(id TEXT PRIMARY KEY, code_hash BLOB NOT NULL UNIQUE, expires_at INTEGER NOT NULL, max_uses INTEGER NOT NULL DEFAULT 1, uses INTEGER NOT NULL DEFAULT 0, revoked INTEGER NOT NULL DEFAULT 0, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS enrollment_codes_expiry_idx ON enrollment_codes(expires_at);
CREATE TABLE IF NOT EXISTS enrollment_rate_limits(subject_hash BLOB PRIMARY KEY, attempt_count INTEGER NOT NULL, reset_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS enrollment_rate_limits_reset_idx ON enrollment_rate_limits(reset_at);
CREATE TABLE IF NOT EXISTS user_device_authorizations(id_hash BLOB PRIMARY KEY, code_hash BLOB NOT NULL UNIQUE, device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, status TEXT NOT NULL CHECK(status IN ('pending','approved','denied','consumed')), user_id INTEGER REFERENCES users(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL, next_poll_at INTEGER NOT NULL, approved_at INTEGER, consumed_at INTEGER, created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS user_device_authorizations_expiry_idx ON user_device_authorizations(expires_at);
CREATE TABLE IF NOT EXISTS device_user_tokens(token_hash BLOB PRIMARY KEY, device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS device_user_tokens_expiry_idx ON device_user_tokens(expires_at);
CREATE TABLE IF NOT EXISTS device_nonces(device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, nonce_hash BLOB NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY(device_id,nonce_hash));
CREATE INDEX IF NOT EXISTS device_nonces_expiry_idx ON device_nonces(expires_at);
CREATE TABLE IF NOT EXISTS device_inventory_scans(id TEXT NOT NULL, device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, scanned_at INTEGER NOT NULL, client_version TEXT NOT NULL, request_hash BLOB NOT NULL, current INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, PRIMARY KEY(device_id,id));
CREATE UNIQUE INDEX IF NOT EXISTS one_current_inventory_per_device ON device_inventory_scans(device_id) WHERE current=1;
CREATE TABLE IF NOT EXISTS device_inventory_items(scan_id TEXT NOT NULL, device_id TEXT NOT NULL, position INTEGER NOT NULL, launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect')), external_game_id TEXT NOT NULL, display_name TEXT NOT NULL, detected_version TEXT, version_source TEXT NOT NULL, install_path TEXT NOT NULL, PRIMARY KEY(device_id,scan_id,position), FOREIGN KEY(device_id,scan_id) REFERENCES device_inventory_scans(device_id,id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS inventory_catalog_mappings(launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect')), external_game_id TEXT NOT NULL, game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE RESTRICT, game_version_id INTEGER REFERENCES game_versions(id) ON DELETE RESTRICT, detected_version TEXT, request_hash BLOB NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id));
CREATE TABLE IF NOT EXISTS inventory_catalog_version_mappings(launcher TEXT NOT NULL, external_game_id TEXT NOT NULL, detected_version TEXT NOT NULL, game_version_id INTEGER NOT NULL REFERENCES game_versions(id) ON DELETE RESTRICT, request_hash BLOB NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id,detected_version), FOREIGN KEY(launcher,external_game_id) REFERENCES inventory_catalog_mappings(launcher,external_game_id) ON DELETE CASCADE);
`

func (s *Store) rebuildInventoryCatalogMappingsIfNeeded() error {
	rows, err := s.db.Query(`PRAGMA foreign_key_list(inventory_catalog_mappings)`)
	if err != nil {
		return err
	}
	needsRebuild := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err = rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		if (table == "games" || table == "game_versions") && onDelete != "RESTRICT" {
			needsRebuild = true
		}
	}
	if err = rows.Close(); err != nil || !needsRebuild {
		return err
	}
	if _, err = s.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer s.db.Exec(`PRAGMA foreign_keys=ON`)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE inventory_catalog_mappings_rebuilt(launcher TEXT NOT NULL CHECK(launcher IN ('steam','ea_app','ubisoft_connect')), external_game_id TEXT NOT NULL, game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE RESTRICT, game_version_id INTEGER REFERENCES game_versions(id) ON DELETE RESTRICT, detected_version TEXT, request_hash BLOB NOT NULL, created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, PRIMARY KEY(launcher,external_game_id)); INSERT INTO inventory_catalog_mappings_rebuilt SELECT launcher,external_game_id,game_id,game_version_id,detected_version,request_hash,created_by,created_at FROM inventory_catalog_mappings; DROP TABLE inventory_catalog_mappings; ALTER TABLE inventory_catalog_mappings_rebuilt RENAME TO inventory_catalog_mappings`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = s.db.Exec(`PRAGMA foreign_keys=ON`)
	return err
}

func (s *Store) rebuildCacheJobsSourceFKIfNeeded() error {
	rows, err := s.db.Query(`PRAGMA foreign_key_list(cache_jobs)`)
	if err != nil {
		return err
	}
	needsRebuild := false
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err = rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		if table == "sources" && from == "source_id" {
			needsRebuild = true
		}
	}
	if err = rows.Close(); err != nil || !needsRebuild {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE cache_jobs_rebuilt(id TEXT PRIMARY KEY, target_type TEXT NOT NULL CHECK(target_type IN ('launcher_version','game_version')), target_id INTEGER NOT NULL, target_revision INTEGER NOT NULL CHECK(target_revision>0), source_id INTEGER NOT NULL, source_revision INTEGER NOT NULL CHECK(source_revision>0), source_path TEXT NOT NULL, expected_sha256 TEXT NOT NULL DEFAULT '' CHECK(expected_sha256='' OR (length(expected_sha256)=64 AND expected_sha256 NOT GLOB '*[^0-9a-f]*')), expected_size INTEGER NOT NULL DEFAULT 0 CHECK(expected_size>=0 AND expected_size<=1099511627776), status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled')), progress_bytes INTEGER NOT NULL DEFAULT 0 CHECK(progress_bytes>=0), attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0), cancel_requested INTEGER NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', artifact_digest TEXT REFERENCES artifact_blobs(digest) ON DELETE RESTRICT, artifact_size INTEGER NOT NULL DEFAULT 0 CHECK(artifact_size>=0), content_type TEXT NOT NULL DEFAULT '', created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER);
		INSERT INTO cache_jobs_rebuilt SELECT id,target_type,target_id,target_revision,source_id,source_revision,source_path,expected_sha256,expected_size,status,progress_bytes,attempts,cancel_requested,error_code,error_message,artifact_digest,artifact_size,content_type,created_by,created_at,started_at,finished_at FROM cache_jobs;
		DROP TABLE cache_jobs;
		ALTER TABLE cache_jobs_rebuilt RENAME TO cache_jobs;
		CREATE UNIQUE INDEX one_active_cache_job_per_target ON cache_jobs(target_type,target_id) WHERE status IN ('queued','running');
		CREATE INDEX cache_jobs_status_created_idx ON cache_jobs(status,created_at);`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func ensureColumnTx(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func (s *Store) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}
