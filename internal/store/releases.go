package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
)

var (
	ErrReleaseSequence      = errors.New("release sequence is not the next monotonic value")
	ErrReleaseEventUnknown  = errors.New("release event does not exist")
	ErrReleaseArtifact      = errors.New("release artifact is missing or has conflicting metadata")
	ErrReleaseValidity      = errors.New("release is not currently valid for activation")
	ErrReleaseVersion       = errors.New("client update version is not a monotonic upgrade")
	ErrReleaseEventArchived = errors.New("archived event cannot be published")
	ErrReleaseNotLatest     = errors.New("only the latest event release can be activated")
)

type ReleaseArtifactReference struct {
	Digest, ContentType string
	SizeBytes           int64
}

type EventReleaseRecord struct {
	EventID, ReleaseID, KeyID, MinimumClientVersion string
	Sequence                                        int64
	EnvelopeJSON, PayloadJSON                       []byte
	IssuedAt, ValidUntil                            time.Time
	Artifacts                                       []ReleaseArtifactReference
}

type ClientUpdateReleaseRecord struct {
	Channel, Version, MinimumVersion, ArtifactDigest, KeyID string
	Sequence, SizeBytes                                     int64
	EnvelopeJSON, PayloadJSON                               []byte
	PublishedAt                                             time.Time
}

type StoredRelease struct {
	EventID, ReleaseID, Channel, Version, MinimumVersion string
	Sequence                                             int64
	EnvelopeJSON                                         []byte
	IssuedAt, ValidUntil                                 time.Time
}

func (s *Store) PublishEventRelease(ctx context.Context, release EventReleaseRecord, activate bool, audit *AuditEntry) error {
	if strings.TrimSpace(release.EventID) == "" || strings.TrimSpace(release.ReleaseID) == "" || release.Sequence < 1 || len(release.EnvelopeJSON) == 0 || len(release.PayloadJSON) == 0 || !release.ValidUntil.After(release.IssuedAt) || audit == nil {
		return errors.New("event release is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var eventStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM events WHERE slug=?`, release.EventID).Scan(&eventStatus); errors.Is(err, sql.ErrNoRows) {
		return ErrReleaseEventUnknown
	} else if err != nil {
		return err
	}
	if eventStatus == "archived" {
		return ErrReleaseEventArchived
	}
	var nextSequence int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM event_releases WHERE event_id=?`, release.EventID).Scan(&nextSequence); err != nil {
		return err
	}
	if release.Sequence != nextSequence {
		return ErrReleaseSequence
	}
	seen := make(map[string]struct{}, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		if _, duplicate := seen[artifact.Digest]; duplicate {
			return ErrReleaseArtifact
		}
		seen[artifact.Digest] = struct{}{}
		var size int64
		var contentType string
		if err = tx.QueryRowContext(ctx, `SELECT size_bytes,content_type FROM artifact_blobs WHERE digest=?`, artifact.Digest).Scan(&size, &contentType); err != nil || size != artifact.SizeBytes || contentType != artifact.ContentType {
			return ErrReleaseArtifact
		}
	}
	nowTime := time.Now().UTC()
	if activate && (nowTime.Before(release.IssuedAt) || !nowTime.Before(release.ValidUntil)) {
		return ErrReleaseValidity
	}
	now := nowTime.Unix()
	if _, err = tx.ExecContext(ctx, `INSERT INTO event_releases(event_id,sequence,release_id,key_id,envelope_json,payload_json,issued_at,valid_until,minimum_client_version,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, release.EventID, release.Sequence, release.ReleaseID, release.KeyID, release.EnvelopeJSON, release.PayloadJSON, release.IssuedAt.UTC().Unix(), release.ValidUntil.UTC().Unix(), release.MinimumClientVersion, audit.ActorUserID, now); err != nil {
		return err
	}
	for _, artifact := range release.Artifacts {
		if _, err = tx.ExecContext(ctx, `INSERT INTO event_release_artifacts(event_id,sequence,digest,size_bytes,content_type) VALUES(?,?,?,?,?)`, release.EventID, release.Sequence, artifact.Digest, artifact.SizeBytes, artifact.ContentType); err != nil {
			return err
		}
	}
	if eventStatus != "published" {
		if _, err = tx.ExecContext(ctx, `UPDATE events SET status='published',revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE slug=?`, release.EventID); err != nil {
			return err
		}
	}
	if activate {
		if _, err = tx.ExecContext(ctx, `INSERT INTO active_event_release(singleton,event_id,sequence) VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET event_id=excluded.event_id,sequence=excluded.sequence`, release.EventID, release.Sequence); err != nil {
			return err
		}
	}
	audit.ObjectType = "event_release"
	audit.ObjectID = fmt.Sprintf("%s:%d", release.EventID, release.Sequence)
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) backfillEventReleaseArtifacts() error {
	rows, err := s.db.Query(`SELECT event_id,sequence,payload_json FROM event_releases WHERE NOT EXISTS(SELECT 1 FROM event_release_artifacts a WHERE a.event_id=event_releases.event_id AND a.sequence=event_releases.sequence)`)
	if err != nil {
		return err
	}
	type pendingRelease struct {
		eventID  string
		sequence int64
		payload  []byte
	}
	pending := make([]pendingRelease, 0)
	for rows.Next() {
		var release pendingRelease
		if err = rows.Scan(&release.eventID, &release.sequence, &release.payload); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, release)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, release := range pending {
		var payload struct {
			Artifacts []struct {
				Digest    string `json:"digest"`
				Size      int64  `json:"size"`
				MediaType string `json:"mediaType"`
			} `json:"artifacts"`
		}
		if err = json.Unmarshal(release.payload, &payload); err != nil {
			return fmt.Errorf("event release %s/%d artifact backfill failed", release.eventID, release.sequence)
		}
		if len(payload.Artifacts) == 0 {
			continue
		}
		tx, beginErr := s.db.Begin()
		if beginErr != nil {
			return beginErr
		}
		for _, reference := range payload.Artifacts {
			digest, ok := strings.CutPrefix(reference.Digest, "sha256:")
			if !ok || !artifactDigestPattern.MatchString(digest) {
				tx.Rollback()
				return fmt.Errorf("event release %s/%d contains invalid artifact", release.eventID, release.sequence)
			}
			result, insertErr := tx.Exec(`INSERT OR IGNORE INTO event_release_artifacts(event_id,sequence,digest,size_bytes,content_type) SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM artifact_blobs WHERE digest=? AND size_bytes=? AND content_type=?)`, release.eventID, release.sequence, digest, reference.Size, reference.MediaType, digest, reference.Size, reference.MediaType)
			if insertErr != nil {
				tx.Rollback()
				return insertErr
			}
			if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
				tx.Rollback()
				return fmt.Errorf("event release %s/%d references missing artifact", release.eventID, release.sequence)
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) PublishClientUpdateRelease(ctx context.Context, release ClientUpdateReleaseRecord, audit *AuditEntry) error {
	if release.Channel != "stable" || release.Sequence < 1 || strings.TrimSpace(release.Version) == "" || strings.TrimSpace(release.MinimumVersion) == "" || len(release.EnvelopeJSON) == 0 || len(release.PayloadJSON) == 0 || audit == nil {
		return errors.New("client update release is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	minimumComparison, versionErr := protocol.CompareSemanticVersions(release.MinimumVersion, release.Version)
	if versionErr != nil || minimumComparison > 0 {
		return ErrReleaseVersion
	}
	if release.PublishedAt.After(time.Now().UTC()) {
		return ErrReleaseValidity
	}
	var nextSequence int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM client_update_releases WHERE channel=?`, release.Channel).Scan(&nextSequence); err != nil {
		return err
	}
	if release.Sequence != nextSequence {
		return ErrReleaseSequence
	}
	var previousVersion, previousMinimumVersion string
	previousErr := tx.QueryRowContext(ctx, `SELECT version,minimum_version FROM client_update_releases WHERE channel=? ORDER BY sequence DESC LIMIT 1`, release.Channel).Scan(&previousVersion, &previousMinimumVersion)
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		return previousErr
	}
	if previousErr == nil {
		comparison, compareErr := protocol.CompareSemanticVersions(release.Version, previousVersion)
		if compareErr != nil || comparison <= 0 {
			return ErrReleaseVersion
		}
		minimumComparison, compareErr := protocol.CompareSemanticVersions(release.MinimumVersion, previousMinimumVersion)
		if compareErr != nil || minimumComparison < 0 {
			return ErrReleaseVersion
		}
	}
	var size int64
	if err = tx.QueryRowContext(ctx, `SELECT size_bytes FROM artifact_blobs WHERE digest=?`, release.ArtifactDigest).Scan(&size); err != nil || size != release.SizeBytes {
		return ErrReleaseArtifact
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO client_update_releases(channel,sequence,version,minimum_version,artifact_digest,size_bytes,key_id,envelope_json,payload_json,published_at,created_by,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, release.Channel, release.Sequence, release.Version, release.MinimumVersion, release.ArtifactDigest, release.SizeBytes, release.KeyID, release.EnvelopeJSON, release.PayloadJSON, release.PublishedAt.UTC().Unix(), audit.ActorUserID, time.Now().UTC().Unix()); err != nil {
		return err
	}
	audit.ObjectType = "client_update_release"
	audit.ObjectID = fmt.Sprintf("%s:%d", release.Channel, release.Sequence)
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EventRelease(ctx context.Context, eventID string) (StoredRelease, error) {
	var release StoredRelease
	var issuedAt, validUntil int64
	err := s.db.QueryRowContext(ctx, `SELECT event_id,release_id,sequence,envelope_json,minimum_client_version,issued_at,valid_until FROM event_releases WHERE event_id=? ORDER BY sequence DESC LIMIT 1`, eventID).Scan(&release.EventID, &release.ReleaseID, &release.Sequence, &release.EnvelopeJSON, &release.MinimumVersion, &issuedAt, &validUntil)
	if err == nil {
		release.IssuedAt = time.Unix(issuedAt, 0).UTC()
		release.ValidUntil = time.Unix(validUntil, 0).UTC()
	}
	return release, err
}

func (s *Store) NextEventReleaseSequence(ctx context.Context, eventID string) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM event_releases WHERE event_id=?`, eventID).Scan(&sequence)
	return sequence, err
}

func (s *Store) EventReleaseSequence(ctx context.Context, eventID string, sequence int64) (StoredRelease, error) {
	var release StoredRelease
	var issuedAt, validUntil int64
	err := s.db.QueryRowContext(ctx, `SELECT event_id,release_id,sequence,envelope_json,minimum_client_version,issued_at,valid_until FROM event_releases WHERE event_id=? AND sequence=?`, eventID, sequence).Scan(&release.EventID, &release.ReleaseID, &release.Sequence, &release.EnvelopeJSON, &release.MinimumVersion, &issuedAt, &validUntil)
	if err == nil {
		release.IssuedAt = time.Unix(issuedAt, 0).UTC()
		release.ValidUntil = time.Unix(validUntil, 0).UTC()
	}
	return release, err
}

func (s *Store) EventReleases(ctx context.Context) ([]StoredRelease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id,release_id,sequence,minimum_client_version,issued_at,valid_until FROM event_releases ORDER BY created_at DESC,event_id,sequence DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var releases []StoredRelease
	for rows.Next() {
		var release StoredRelease
		var issuedAt, validUntil int64
		if err = rows.Scan(&release.EventID, &release.ReleaseID, &release.Sequence, &release.MinimumVersion, &issuedAt, &validUntil); err != nil {
			return nil, err
		}
		release.IssuedAt = time.Unix(issuedAt, 0).UTC()
		release.ValidUntil = time.Unix(validUntil, 0).UTC()
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

func (s *Store) ActivateEventRelease(ctx context.Context, eventID string, sequence int64, audit *AuditEntry) error {
	if strings.TrimSpace(eventID) == "" || sequence < 1 || audit == nil {
		return errors.New("event release activation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM events WHERE slug=?`, eventID).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return ErrReleaseEventUnknown
	} else if err != nil {
		return err
	}
	if status == "archived" {
		return ErrReleaseEventArchived
	}
	var latest, issuedAt, validUntil int64
	if err = tx.QueryRowContext(ctx, `SELECT sequence,issued_at,valid_until FROM event_releases WHERE event_id=? ORDER BY sequence DESC LIMIT 1`, eventID).Scan(&latest, &issuedAt, &validUntil); err != nil {
		return err
	}
	if sequence != latest {
		return ErrReleaseNotLatest
	}
	now := time.Now().UTC()
	if now.Before(time.Unix(issuedAt, 0).UTC()) || !now.Before(time.Unix(validUntil, 0).UTC()) {
		return ErrReleaseValidity
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO active_event_release(singleton,event_id,sequence) VALUES(1,?,?) ON CONFLICT(singleton) DO UPDATE SET event_id=excluded.event_id,sequence=excluded.sequence`, eventID, sequence); err != nil {
		return err
	}
	audit.ObjectType = "event_release"
	audit.ObjectID = fmt.Sprintf("%s:%d", eventID, sequence)
	if err = writeAuditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ActiveEventRelease(ctx context.Context) (StoredRelease, error) {
	var release StoredRelease
	var issuedAt, validUntil int64
	err := s.db.QueryRowContext(ctx, `SELECT r.event_id,r.release_id,r.sequence,r.envelope_json,r.minimum_client_version,r.issued_at,r.valid_until FROM active_event_release a JOIN event_releases r ON r.event_id=a.event_id AND r.sequence=a.sequence WHERE a.singleton=1`).Scan(&release.EventID, &release.ReleaseID, &release.Sequence, &release.EnvelopeJSON, &release.MinimumVersion, &issuedAt, &validUntil)
	release.IssuedAt = time.Unix(issuedAt, 0).UTC()
	release.ValidUntil = time.Unix(validUntil, 0).UTC()
	return release, err
}

func (s *Store) LatestClientUpdateRelease(ctx context.Context, channel string) (StoredRelease, error) {
	var release StoredRelease
	err := s.db.QueryRowContext(ctx, `SELECT channel,sequence,version,minimum_version,envelope_json FROM client_update_releases WHERE channel=? ORDER BY sequence DESC LIMIT 1`, channel).Scan(&release.Channel, &release.Sequence, &release.Version, &release.MinimumVersion, &release.EnvelopeJSON)
	return release, err
}
