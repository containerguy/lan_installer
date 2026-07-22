package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrCacheJobNotFound      = errors.New("cache job not found")
	ErrCacheJobAlreadyActive = errors.New("cache job already active")
	ErrCacheJobState         = errors.New("cache job state does not allow this operation")
	ErrCacheJobCancelled     = errors.New("cache job cancellation was requested")
	ErrCacheTargetChanged    = errors.New("cache target changed while job was running")
)

type CacheJob struct {
	ID                               string
	TargetType                       string
	TargetID, TargetRevision         int64
	SourceID, SourceRevision         int64
	SourcePath, ExpectedSHA256       string
	ExpectedSize                     int64
	Status                           string
	ProgressBytes                    int64
	Attempts                         int64
	CancelRequested                  bool
	ErrorCode, ErrorMessage          string
	ArtifactDigest                   string
	ArtifactSize                     int64
	ContentType                      string
	CreatedBy                        int64
	CreatedAt, StartedAt, FinishedAt time.Time
}

func (s *Store) EnqueueCacheJob(ctx context.Context, targetType string, targetID, createdBy int64) (CacheJob, error) {
	return s.EnqueueCacheJobWithAudit(ctx, targetType, targetID, createdBy, nil)
}

func (s *Store) EnqueueCacheJobWithAudit(ctx context.Context, targetType string, targetID, createdBy int64, audit *AuditEntry) (CacheJob, error) {
	if targetID < 1 || createdBy < 1 {
		return CacheJob{}, errors.New("cache target and creator are required")
	}
	table := ""
	switch targetType {
	case "launcher_version":
		table = "launcher_versions"
	case "game_version":
		table = "game_versions"
	default:
		return CacheJob{}, errors.New("unsupported cache target")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CacheJob{}, err
	}
	defer tx.Rollback()
	var job CacheJob
	job.TargetType, job.TargetID, job.CreatedBy = targetType, targetID, createdBy
	var targetEnabled, sourceEnabled bool
	query := `SELECT v.revision,v.source_id,v.source_path,COALESCE(v.sha256,''),COALESCE(v.size_bytes,0),v.enabled,s.revision,s.enabled FROM ` + table + ` v JOIN sources s ON s.id=v.source_id WHERE v.id=?`
	if err = tx.QueryRowContext(ctx, query, targetID).Scan(&job.TargetRevision, &job.SourceID, &job.SourcePath, &job.ExpectedSHA256, &job.ExpectedSize, &targetEnabled, &job.SourceRevision, &sourceEnabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CacheJob{}, ErrCatalogNotFound
		}
		return CacheJob{}, err
	}
	if !targetEnabled || !sourceEnabled {
		return CacheJob{}, errors.New("cache target and source must be active")
	}
	if err = tx.QueryRowContext(ctx, `SELECT id FROM cache_jobs WHERE target_type=? AND target_id=? AND status IN ('queued','running')`, targetType, targetID).Scan(&job.ID); err == nil {
		return CacheJob{}, ErrCacheJobAlreadyActive
	} else if !errors.Is(err, sql.ErrNoRows) {
		return CacheJob{}, err
	}
	job.ID, err = randomUUID()
	if err != nil {
		return CacheJob{}, err
	}
	job.Status = "queued"
	job.CreatedAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO cache_jobs(id,target_type,target_id,target_revision,source_id,source_revision,source_path,expected_sha256,expected_size,status,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, job.ID, job.TargetType, job.TargetID, job.TargetRevision, job.SourceID, job.SourceRevision, job.SourcePath, job.ExpectedSHA256, job.ExpectedSize, job.Status, job.CreatedBy, job.CreatedAt.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return CacheJob{}, ErrCacheJobAlreadyActive
		}
		return CacheJob{}, err
	}
	if audit != nil && audit.ObjectID == "" {
		audit.ObjectID = job.ID
	}
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return CacheJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return CacheJob{}, err
	}
	return job, nil
}

func (s *Store) CacheJobs(ctx context.Context, limit int) ([]CacheJob, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, cacheJobSelect+` ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := make([]CacheJob, 0)
	for rows.Next() {
		job, scanErr := scanCacheJob(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) CacheJob(ctx context.Context, id string) (CacheJob, error) {
	job, err := scanCacheJob(s.db.QueryRowContext(ctx, cacheJobSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CacheJob{}, ErrCacheJobNotFound
	}
	return job, err
}

func (s *Store) ClaimNextCacheJob(ctx context.Context) (CacheJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CacheJob{}, err
	}
	defer tx.Rollback()
	var id string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM cache_jobs WHERE status='queued' ORDER BY created_at,id LIMIT 1`).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CacheJob{}, ErrCacheJobNotFound
		}
		return CacheJob{}, err
	}
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE cache_jobs SET status='running',attempts=attempts+1,started_at=?,finished_at=NULL,error_code='',error_message='' WHERE id=? AND status='queued'`, now, id)
	if err != nil {
		return CacheJob{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CacheJob{}, ErrCacheJobState
	}
	job, err := scanCacheJob(tx.QueryRowContext(ctx, cacheJobSelect+` WHERE id=?`, id))
	if err != nil {
		return CacheJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return CacheJob{}, err
	}
	return job, nil
}

func (s *Store) UpdateCacheJobProgress(ctx context.Context, id string, progress int64) error {
	if progress < 0 {
		return errors.New("cache progress cannot be negative")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cache_jobs SET progress_bytes=? WHERE id=? AND status='running' AND progress_bytes<=?`, progress, id, progress)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCacheJobState
	}
	return nil
}

func (s *Store) RequestCacheJobCancel(ctx context.Context, id string) (CacheJob, error) {
	return s.RequestCacheJobCancelWithAudit(ctx, id, nil)
}

func (s *Store) RequestCacheJobCancelWithAudit(ctx context.Context, id string, audit *AuditEntry) (CacheJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CacheJob{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE cache_jobs SET status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,cancel_requested=1,finished_at=CASE WHEN status='queued' THEN ? ELSE finished_at END WHERE id=? AND status IN ('queued','running')`, now, id)
	if err != nil {
		return CacheJob{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CacheJob{}, ErrCacheJobState
	}
	job, err := scanCacheJob(tx.QueryRowContext(ctx, cacheJobSelect+` WHERE id=?`, id))
	if err != nil {
		return CacheJob{}, err
	}
	if audit != nil && audit.ObjectID == "" {
		audit.ObjectID = job.ID
	}
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return CacheJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return CacheJob{}, err
	}
	return job, nil
}

func (s *Store) RetryCacheJob(ctx context.Context, id string) (CacheJob, error) {
	return s.RetryCacheJobWithAudit(ctx, id, nil)
}

func (s *Store) RetryCacheJobWithAudit(ctx context.Context, id string, audit *AuditEntry) (CacheJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CacheJob{}, err
	}
	defer tx.Rollback()
	job, err := scanCacheJob(tx.QueryRowContext(ctx, cacheJobSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CacheJob{}, ErrCacheJobNotFound
	}
	if err != nil {
		return CacheJob{}, err
	}
	if job.Status != "failed" && job.Status != "cancelled" {
		return CacheJob{}, ErrCacheJobState
	}
	table := "launcher_versions"
	if job.TargetType == "game_version" {
		table = "game_versions"
	}
	var targetEnabled, sourceEnabled bool
	query := `SELECT v.revision,v.source_id,v.source_path,COALESCE(v.sha256,''),COALESCE(v.size_bytes,0),v.enabled,s.revision,s.enabled FROM ` + table + ` v JOIN sources s ON s.id=v.source_id WHERE v.id=?`
	if err = tx.QueryRowContext(ctx, query, job.TargetID).Scan(&job.TargetRevision, &job.SourceID, &job.SourcePath, &job.ExpectedSHA256, &job.ExpectedSize, &targetEnabled, &job.SourceRevision, &sourceEnabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CacheJob{}, ErrCatalogNotFound
		}
		return CacheJob{}, err
	}
	if !targetEnabled || !sourceEnabled {
		return CacheJob{}, errors.New("cache target and source must be active")
	}
	result, err := tx.ExecContext(ctx, `UPDATE cache_jobs SET target_revision=?,source_id=?,source_revision=?,source_path=?,expected_sha256=?,expected_size=?,status='queued',progress_bytes=0,cancel_requested=0,error_code='',error_message='',artifact_digest=NULL,artifact_size=0,content_type='',started_at=NULL,finished_at=NULL WHERE id=? AND status IN ('failed','cancelled')`, job.TargetRevision, job.SourceID, job.SourceRevision, job.SourcePath, job.ExpectedSHA256, job.ExpectedSize, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return CacheJob{}, ErrCacheJobAlreadyActive
		}
		return CacheJob{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CacheJob{}, ErrCacheJobState
	}
	job, err = scanCacheJob(tx.QueryRowContext(ctx, cacheJobSelect+` WHERE id=?`, id))
	if err != nil {
		return CacheJob{}, err
	}
	if audit != nil && audit.ObjectID == "" {
		audit.ObjectID = job.ID
	}
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return CacheJob{}, err
	}
	if err = tx.Commit(); err != nil {
		return CacheJob{}, err
	}
	return job, nil
}

func (s *Store) FailCacheJob(ctx context.Context, id, code, message string, cancelled bool) error {
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	if len(code) > 100 || len(message) > 500 {
		return errors.New("cache job failure metadata is too long")
	}
	status := "failed"
	if cancelled {
		status = "cancelled"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE cache_jobs SET status=?,error_code=?,error_message=?,finished_at=? WHERE id=? AND status='running'`, status, code, message, time.Now().UTC().Unix(), id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCacheJobState
	}
	return nil
}

func (s *Store) CompleteCacheJob(ctx context.Context, id, digest string, size int64, contentType string) error {
	if !artifactDigestPattern.MatchString(digest) || size < 0 || size > MaxArtifactSize || strings.TrimSpace(contentType) == "" {
		return errors.New("invalid completed cache artifact")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanCacheJob(tx.QueryRowContext(ctx, cacheJobSelect+` WHERE id=?`, id))
	if err != nil {
		return err
	}
	if job.Status != "running" || (job.ExpectedSHA256 != "" && job.ExpectedSHA256 != digest) || (job.ExpectedSize > 0 && job.ExpectedSize != size) {
		return ErrCacheJobState
	}
	if job.CancelRequested {
		return ErrCacheJobCancelled
	}
	var sourceRevision int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM sources WHERE id=?`, job.SourceID).Scan(&sourceRevision); err != nil {
		return err
	}
	if sourceRevision != job.SourceRevision {
		return ErrCacheTargetChanged
	}
	table := "launcher_versions"
	if job.TargetType == "game_version" {
		table = "game_versions"
	}
	var revision int64
	var currentDigest string
	var currentSize int64
	if err = tx.QueryRowContext(ctx, `SELECT revision,COALESCE(sha256,''),COALESCE(size_bytes,0) FROM `+table+` WHERE id=?`, job.TargetID).Scan(&revision, &currentDigest, &currentSize); err != nil {
		return err
	}
	if revision != job.TargetRevision || (currentDigest != "" && currentDigest != digest) || (currentSize > 0 && currentSize != size) {
		return ErrCacheTargetChanged
	}
	if currentDigest == "" || currentSize == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET sha256=?,size_bytes=?,revision=revision+1 WHERE id=? AND revision=?`, digest, size, job.TargetID, job.TargetRevision); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE cache_jobs SET status='succeeded',progress_bytes=?,artifact_digest=?,artifact_size=?,content_type=?,finished_at=? WHERE id=? AND status='running'`, size, digest, size, strings.TrimSpace(contentType), time.Now().UTC().Unix(), id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCacheJobState
	}
	return tx.Commit()
}

func (s *Store) CacheJobCancelRequested(ctx context.Context, id string) (bool, error) {
	var requested bool
	err := s.db.QueryRowContext(ctx, `SELECT cancel_requested FROM cache_jobs WHERE id=? AND status='running'`, id).Scan(&requested)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrCacheJobState
	}
	return requested, err
}

func (s *Store) RecoverCacheJobs(ctx context.Context) error {
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `UPDATE cache_jobs SET status=CASE WHEN cancel_requested=1 THEN 'cancelled' ELSE 'queued' END,progress_bytes=CASE WHEN cancel_requested=1 THEN progress_bytes ELSE 0 END,started_at=NULL,finished_at=CASE WHEN cancel_requested=1 THEN ? ELSE NULL END,error_code=CASE WHEN cancel_requested=1 THEN 'cancelled' ELSE '' END,error_message=CASE WHEN cancel_requested=1 THEN 'Vom Benutzer abgebrochen.' ELSE '' END WHERE status='running'`, now)
	return err
}

func (s *Store) RecoverCacheJob(ctx context.Context, id string) error {
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE cache_jobs SET status=CASE WHEN cancel_requested=1 THEN 'cancelled' ELSE 'queued' END,progress_bytes=CASE WHEN cancel_requested=1 THEN progress_bytes ELSE 0 END,started_at=NULL,finished_at=CASE WHEN cancel_requested=1 THEN ? ELSE NULL END,error_code=CASE WHEN cancel_requested=1 THEN 'cancelled' ELSE '' END,error_message=CASE WHEN cancel_requested=1 THEN 'Vom Benutzer abgebrochen.' ELSE '' END WHERE id=? AND status='running'`, now, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCacheJobState
	}
	return nil
}

const cacheJobSelect = `SELECT id,target_type,target_id,target_revision,source_id,source_revision,source_path,expected_sha256,expected_size,status,progress_bytes,attempts,cancel_requested,error_code,error_message,COALESCE(artifact_digest,''),artifact_size,content_type,created_by,created_at,started_at,finished_at FROM cache_jobs`

type cacheJobScanner interface{ Scan(...any) error }

func scanCacheJob(scanner cacheJobScanner) (CacheJob, error) {
	var job CacheJob
	var created int64
	var started, finished sql.NullInt64
	err := scanner.Scan(&job.ID, &job.TargetType, &job.TargetID, &job.TargetRevision, &job.SourceID, &job.SourceRevision, &job.SourcePath, &job.ExpectedSHA256, &job.ExpectedSize, &job.Status, &job.ProgressBytes, &job.Attempts, &job.CancelRequested, &job.ErrorCode, &job.ErrorMessage, &job.ArtifactDigest, &job.ArtifactSize, &job.ContentType, &job.CreatedBy, &created, &started, &finished)
	if err != nil {
		return CacheJob{}, err
	}
	job.CreatedAt = time.Unix(created, 0).UTC()
	if started.Valid {
		job.StartedAt = time.Unix(started.Int64, 0).UTC()
	}
	if finished.Valid {
		job.FinishedAt = time.Unix(finished.Int64, 0).UTC()
	}
	return job, nil
}

func (j CacheJob) String() string {
	return fmt.Sprintf("%s/%d %s", j.TargetType, j.TargetID, j.Status)
}
