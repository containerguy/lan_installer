package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

var ErrRevisionConflict = errors.New("source revision conflict")

type SourceReferencedError struct {
	LauncherVersions int64
	GameVersions     int64
	CacheJobs        int64
}

func (e *SourceReferencedError) Error() string { return "source is referenced" }

type AuditEntry struct {
	ActorUserID                                       int64
	Action, ObjectType, ObjectID, Details, RemoteAddr string
}

type WebDAVConfigBuilder func(sourceID int64, existing *WebDAVConfig) (*WebDAVConfig, error)

func normalizeSource(v Source) (Source, error) {
	v.Name = strings.TrimSpace(v.Name)
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	v.BaseURL = strings.TrimRight(strings.TrimSpace(v.BaseURL), "/")
	if blank(v.Name, v.Kind, v.BaseURL) {
		return Source{}, errors.New("name, kind and base URL are required")
	}
	u, err := url.Parse(v.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return Source{}, errors.New("source must be an HTTPS URL without embedded credentials, query or fragment")
	}
	switch v.Kind {
	case "https", "webdav", "nextcloud_webdav":
	default:
		return Source{}, errors.New("unsupported source kind")
	}
	if v.LastTestState == "" {
		v.LastTestState = "never"
	}
	switch v.LastTestState {
	case "never", "succeeded", "failed":
	default:
		return Source{}, errors.New("invalid source test state")
	}
	return v, nil
}

func (s *Store) Source(ctx context.Context, id int64) (Source, error) {
	var v Source
	err := s.db.QueryRowContext(ctx, `SELECT s.id,s.revision,s.name,s.kind,s.base_url,s.enabled,EXISTS(SELECT 1 FROM webdav_source_config w WHERE w.source_id=s.id AND w.auth_type='basic' AND length(w.secret_ciphertext)>0),s.last_test_state,COALESCE(s.last_tested_at,''),COALESCE(s.last_test_latency_ms,0) FROM sources s WHERE s.id=?`, id).Scan(&v.ID, &v.Revision, &v.Name, &v.Kind, &v.BaseURL, &v.Enabled, &v.AuthConfigured, &v.LastTestState, &v.LastTestedAt, &v.LastTestLatencyMS)
	return v, err
}

func (s *Store) SaveSourceAtomic(ctx context.Context, value Source, expectedRevision int64, build WebDAVConfigBuilder) (Source, error) {
	return s.SaveSourceAtomicWithAudit(ctx, value, expectedRevision, build, nil)
}

func (s *Store) SaveSourceAtomicWithAudit(ctx context.Context, value Source, expectedRevision int64, build WebDAVConfigBuilder, audit *AuditEntry) (Source, error) {
	v, err := normalizeSource(value)
	if err != nil {
		return Source{}, err
	}
	if build == nil {
		return Source{}, errors.New("WebDAV config builder is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Source{}, err
	}
	defer tx.Rollback()
	if v.ID == 0 {
		err = tx.QueryRowContext(ctx, `INSERT INTO sources(name,kind,base_url,enabled,revision,last_test_state,last_tested_at,last_test_latency_ms) VALUES(?,?,?,?,1,?,?,?) RETURNING id,revision`, v.Name, v.Kind, v.BaseURL, v.Enabled, v.LastTestState, nullable(v.LastTestedAt), v.LastTestLatencyMS).Scan(&v.ID, &v.Revision)
	} else {
		if expectedRevision < 1 {
			return Source{}, ErrRevisionConflict
		}
		err = tx.QueryRowContext(ctx, `UPDATE sources SET name=?,kind=?,base_url=?,enabled=?,revision=revision+1,last_test_state=?,last_tested_at=?,last_test_latency_ms=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND revision=? RETURNING revision`, v.Name, v.Kind, v.BaseURL, v.Enabled, v.LastTestState, nullable(v.LastTestedAt), v.LastTestLatencyMS, v.ID, expectedRevision).Scan(&v.Revision)
		if errors.Is(err, sql.ErrNoRows) {
			return Source{}, ErrRevisionConflict
		}
	}
	if err != nil {
		return Source{}, err
	}
	var existing *WebDAVConfig
	var current WebDAVConfig
	err = tx.QueryRowContext(ctx, `SELECT source_id,auth_type,username,COALESCE(secret_nonce,X''),COALESCE(secret_ciphertext,X'') FROM webdav_source_config WHERE source_id=?`, v.ID).Scan(&current.SourceID, &current.AuthType, &current.Username, &current.SecretNonce, &current.SecretCiphertext)
	if err == nil {
		existing = &current
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Source{}, err
	}
	config, err := build(v.ID, existing)
	if err != nil {
		return Source{}, err
	}
	if config == nil {
		if _, err = tx.ExecContext(ctx, `DELETE FROM webdav_source_config WHERE source_id=?`, v.ID); err != nil {
			return Source{}, err
		}
	} else {
		config.SourceID = v.ID
		if err = validateWebDAVConfig(*config); err != nil {
			return Source{}, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO webdav_source_config(source_id,auth_type,username,secret_nonce,secret_ciphertext) VALUES(?,?,?,?,?) ON CONFLICT(source_id) DO UPDATE SET auth_type=excluded.auth_type,username=excluded.username,secret_nonce=excluded.secret_nonce,secret_ciphertext=excluded.secret_ciphertext,updated_at=CURRENT_TIMESTAMP`, config.SourceID, config.AuthType, config.Username, config.SecretNonce, config.SecretCiphertext); err != nil {
			return Source{}, err
		}
	}
	if audit != nil && audit.ObjectID == "" {
		audit.ObjectID = strconv.FormatInt(v.ID, 10)
	}
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return Source{}, err
	}
	if err = tx.Commit(); err != nil {
		return Source{}, err
	}
	v.AuthConfigured = config != nil && config.AuthType == "basic" && len(config.SecretCiphertext) > 0
	return v, nil
}

func validateWebDAVConfig(v WebDAVConfig) error {
	switch v.AuthType {
	case "none":
		if v.Username != "" || len(v.SecretNonce) != 0 || len(v.SecretCiphertext) != 0 {
			return errors.New("none authentication cannot contain credentials")
		}
	case "basic":
		if strings.TrimSpace(v.Username) == "" || len(v.SecretNonce) == 0 || len(v.SecretCiphertext) == 0 {
			return errors.New("username and encrypted password are required")
		}
	default:
		return errors.New("unsupported WebDAV authentication")
	}
	return nil
}

func (s *Store) DeactivateSource(ctx context.Context, id, expectedRevision int64) (int64, error) {
	return s.DeactivateSourceWithAudit(ctx, id, expectedRevision, nil)
}

func (s *Store) DeactivateSourceWithAudit(ctx context.Context, id, expectedRevision int64, audit *AuditEntry) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE sources SET enabled=0,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND revision=?`, id, expectedRevision)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed != 1 {
		return 0, ErrRevisionConflict
	}
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return expectedRevision + 1, nil
}

func (s *Store) DeleteSource(ctx context.Context, id, expectedRevision int64) error {
	return s.DeleteSourceWithAudit(ctx, id, expectedRevision, nil)
}

func (s *Store) DeleteSourceWithAudit(ctx context.Context, id, expectedRevision int64, audit *AuditEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var launcherCount, gameCount, cacheJobCount int64
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM launcher_versions WHERE source_id=?),(SELECT COUNT(*) FROM game_versions WHERE source_id=?),(SELECT COUNT(*) FROM cache_jobs WHERE source_id=? AND status IN ('queued','running'))`, id, id, id).Scan(&launcherCount, &gameCount, &cacheJobCount); err != nil {
		return err
	}
	if launcherCount > 0 || gameCount > 0 || cacheJobCount > 0 {
		return &SourceReferencedError{LauncherVersions: launcherCount, GameVersions: gameCount, CacheJobs: cacheJobCount}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE id=? AND revision=?`, id, expectedRevision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRevisionConflict
	}
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func writeAuditTx(ctx context.Context, tx *sql.Tx, audit *AuditEntry) error {
	if audit == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(actor_user_id,action,object_type,object_id,details,remote_addr) VALUES(?,?,?,?,?,?)`, audit.ActorUserID, audit.Action, audit.ObjectType, audit.ObjectID, audit.Details, audit.RemoteAddr)
	return err
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

type SourceListOptions struct {
	Query   string
	Kind    string
	Enabled *bool
	Cursor  int64
	Limit   int
}

func (s *Store) ListSources(ctx context.Context, options SourceListOptions) ([]Source, int64, error) {
	limit := options.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	clauses := []string{"s.id>?"}
	arguments := []any{options.Cursor}
	if query := strings.ToLower(strings.TrimSpace(options.Query)); query != "" {
		clauses = append(clauses, `LOWER(s.name || " " || s.kind || " " || s.base_url) LIKE ? ESCAPE "\"`)
		query = strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(query)
		arguments = append(arguments, "%"+query+"%")
	}
	if options.Kind != "" {
		switch options.Kind {
		case "https", "webdav", "nextcloud_webdav":
		default:
			return nil, 0, errors.New("invalid source kind filter")
		}
		clauses = append(clauses, "s.kind=?")
		arguments = append(arguments, options.Kind)
	}
	if options.Enabled != nil {
		clauses = append(clauses, "s.enabled=?")
		arguments = append(arguments, *options.Enabled)
	}
	arguments = append(arguments, limit+1)
	query := `SELECT s.id,s.revision,s.name,s.kind,s.base_url,s.enabled,EXISTS(SELECT 1 FROM webdav_source_config w WHERE w.source_id=s.id AND w.auth_type="basic" AND length(w.secret_ciphertext)>0),s.last_test_state,COALESCE(s.last_tested_at,""),COALESCE(s.last_test_latency_ms,0) FROM sources s WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY s.id LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	values := make([]Source, 0, limit)
	for rows.Next() {
		var value Source
		if err = rows.Scan(&value.ID, &value.Revision, &value.Name, &value.Kind, &value.BaseURL, &value.Enabled, &value.AuthConfigured, &value.LastTestState, &value.LastTestedAt, &value.LastTestLatencyMS); err != nil {
			return nil, 0, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	var nextCursor int64
	if len(values) > limit {
		nextCursor = values[limit-1].ID
		values = values[:limit]
	}
	return values, nextCursor, nil
}
