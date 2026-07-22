package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"
)

var artifactDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const MaxArtifactSize int64 = 1 << 40

type Artifact struct {
	Digest      string
	SizeBytes   int64
	ContentType string
	CreatedAt   time.Time
}

func (s *Store) ArtifactGCCandidates(ctx context.Context, before time.Time, limit int) ([]Artifact, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.digest,a.size_bytes,a.content_type,a.created_at
		FROM artifact_blobs a
		WHERE a.created_at<=?
		AND NOT EXISTS(SELECT 1 FROM launcher_versions WHERE sha256=a.digest)
		AND NOT EXISTS(SELECT 1 FROM game_versions WHERE sha256=a.digest)
		AND NOT EXISTS(SELECT 1 FROM event_release_artifacts WHERE digest=a.digest)
		AND NOT EXISTS(SELECT 1 FROM client_update_releases WHERE artifact_digest=a.digest)
		AND NOT EXISTS(SELECT 1 FROM cache_jobs WHERE status IN ('queued','running') AND expected_sha256=a.digest)
		AND NOT EXISTS(SELECT 1 FROM cache_jobs WHERE status='running' AND expected_sha256='')
		ORDER BY a.created_at,a.digest LIMIT ?`, before.UTC().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]Artifact, 0)
	for rows.Next() {
		var value Artifact
		var created int64
		if err = rows.Scan(&value.Digest, &value.SizeBytes, &value.ContentType, &created); err != nil {
			return nil, err
		}
		value.CreatedAt = time.Unix(created, 0).UTC()
		candidates = append(candidates, value)
	}
	return candidates, rows.Err()
}

func (s *Store) DeleteArtifactIfUnreferenced(ctx context.Context, digest string, before time.Time, prepare func() error, audit *AuditEntry) (bool, error) {
	if !artifactDigestPattern.MatchString(digest) || before.IsZero() || prepare == nil || audit == nil {
		return false, errors.New("artifact garbage collection request is invalid")
	}
	if err := prepare(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var createdAt int64
	if err = tx.QueryRowContext(ctx, `SELECT created_at FROM artifact_blobs WHERE digest=?`, digest).Scan(&createdAt); err != nil {
		return false, err
	}
	if createdAt > before.UTC().Unix() {
		return false, nil
	}
	var references int64
	if err = tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM launcher_versions WHERE sha256=?)+
		(SELECT COUNT(*) FROM game_versions WHERE sha256=?)+
		(SELECT COUNT(*) FROM event_release_artifacts WHERE digest=?)+
		(SELECT COUNT(*) FROM client_update_releases WHERE artifact_digest=?)+
		(SELECT COUNT(*) FROM cache_jobs WHERE status IN ('queued','running') AND expected_sha256=?)+
		(SELECT COUNT(*) FROM cache_jobs WHERE status='running' AND expected_sha256='')`, digest, digest, digest, digest, digest).Scan(&references); err != nil {
		return false, err
	}
	if references > 0 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cache_jobs SET artifact_digest=NULL,artifact_size=0,content_type='' WHERE artifact_digest=? AND status IN ('succeeded','failed','cancelled')`, digest); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM artifact_blobs WHERE digest=?`, digest)
	if err != nil {
		return false, err
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		if rowsErr != nil {
			return false, rowsErr
		}
		return false, errors.New("artifact disappeared during garbage collection")
	}
	audit.ObjectType, audit.ObjectID = "artifact", digest
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) RegisterArtifact(ctx context.Context, artifact Artifact) error {
	artifact.ContentType = strings.TrimSpace(artifact.ContentType)
	if !artifactDigestPattern.MatchString(artifact.Digest) || artifact.SizeBytes < 0 || artifact.SizeBytes > MaxArtifactSize || artifact.ContentType == "" {
		return errors.New("artifact metadata is invalid")
	}
	createdAt := artifact.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO artifact_blobs(digest,size_bytes,content_type,created_at) VALUES(?,?,?,?) ON CONFLICT(digest) DO NOTHING`, artifact.Digest, artifact.SizeBytes, artifact.ContentType, createdAt.Unix())
	if err != nil {
		return err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil {
		return affectedErr
	} else if affected == 1 {
		return nil
	}
	var existing Artifact
	if err = s.db.QueryRowContext(ctx, `SELECT size_bytes,content_type FROM artifact_blobs WHERE digest=?`, artifact.Digest).Scan(&existing.SizeBytes, &existing.ContentType); err != nil {
		return err
	}
	if existing.SizeBytes != artifact.SizeBytes || existing.ContentType != artifact.ContentType {
		return errors.New("artifact metadata conflicts with existing digest")
	}
	return s.RenewArtifactGCGrace(ctx, artifact.Digest, time.Now().UTC())
}

func (s *Store) RenewArtifactGCGrace(ctx context.Context, digest string, now time.Time) error {
	if !artifactDigestPattern.MatchString(digest) || now.IsZero() {
		return errors.New("artifact garbage collection grace is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE artifact_blobs SET created_at=? WHERE digest=?`, now.UTC().Unix(), digest)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) Artifact(ctx context.Context, digest string) (Artifact, error) {
	var artifact Artifact
	if !artifactDigestPattern.MatchString(digest) {
		return artifact, sql.ErrNoRows
	}
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT digest,size_bytes,content_type,created_at FROM artifact_blobs WHERE digest=?`, digest).Scan(&artifact.Digest, &artifact.SizeBytes, &artifact.ContentType, &createdAt)
	artifact.CreatedAt = time.Unix(createdAt, 0).UTC()
	return artifact, err
}

func (s *Store) ArtifactUsage(ctx context.Context) (int64, error) {
	var usage int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM artifact_blobs`).Scan(&usage)
	return usage, err
}
