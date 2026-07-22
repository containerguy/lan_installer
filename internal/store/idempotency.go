package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrIdempotencyConflict = errors.New("idempotency key request mismatch")

type IdempotencyRecord struct {
	RequestHash  []byte
	State        string
	Status       int
	ContentType  string
	ETag         string
	ResponseBody []byte
	ExpiresAt    time.Time
}

func (s *Store) ClaimIdempotency(ctx context.Context, actorUserID int64, route, key string, requestHash []byte, expiresAt time.Time) (IdempotencyRecord, bool, error) {
	now := time.Now().UTC().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM idempotency_records WHERE expires_at<=? OR (state="pending" AND claimed_at<?)`, now, now-60); err != nil {
		return IdempotencyRecord{}, false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO idempotency_records(actor_user_id,route,idempotency_key,request_hash,state,claimed_at,expires_at) VALUES(?,?,?,?,"pending",?,?)`, actorUserID, route, key, requestHash, now, expiresAt.UTC().Unix())
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	if inserted == 1 {
		if err = tx.Commit(); err != nil {
			return IdempotencyRecord{}, false, err
		}
		return IdempotencyRecord{RequestHash: append([]byte(nil), requestHash...), State: "pending", ExpiresAt: expiresAt.UTC()}, true, nil
	}
	record, err := readIdempotencyTx(ctx, tx, actorUserID, route, key)
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	if !bytes.Equal(record.RequestHash, requestHash) {
		return IdempotencyRecord{}, false, ErrIdempotencyConflict
	}
	if err = tx.Commit(); err != nil {
		return IdempotencyRecord{}, false, err
	}
	return record, false, nil
}

func (s *Store) Idempotency(ctx context.Context, actorUserID int64, route, key string) (IdempotencyRecord, error) {
	return readIdempotencyRow(s.db.QueryRowContext(ctx, `SELECT request_hash,state,COALESCE(status,0),COALESCE(content_type,""),COALESCE(etag,""),COALESCE(response_body,zeroblob(0)),expires_at FROM idempotency_records WHERE actor_user_id=? AND route=? AND idempotency_key=?`, actorUserID, route, key))
}

func readIdempotencyTx(ctx context.Context, tx *sql.Tx, actorUserID int64, route, key string) (IdempotencyRecord, error) {
	return readIdempotencyRow(tx.QueryRowContext(ctx, `SELECT request_hash,state,COALESCE(status,0),COALESCE(content_type,""),COALESCE(etag,""),COALESCE(response_body,zeroblob(0)),expires_at FROM idempotency_records WHERE actor_user_id=? AND route=? AND idempotency_key=?`, actorUserID, route, key))
}

type rowScanner interface{ Scan(...any) error }

func readIdempotencyRow(row rowScanner) (IdempotencyRecord, error) {
	var record IdempotencyRecord
	var expires int64
	err := row.Scan(&record.RequestHash, &record.State, &record.Status, &record.ContentType, &record.ETag, &record.ResponseBody, &expires)
	record.ExpiresAt = time.Unix(expires, 0).UTC()
	return record, err
}

func (s *Store) CompleteIdempotency(ctx context.Context, actorUserID int64, route, key string, status int, contentType, etag string, body []byte) error {
	result, err := s.db.ExecContext(ctx, `UPDATE idempotency_records SET state="complete",status=?,content_type=?,etag=?,response_body=? WHERE actor_user_id=? AND route=? AND idempotency_key=? AND state="pending"`, status, contentType, etag, body, actorUserID, route, key)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("idempotency claim is unavailable")
	}
	return nil
}

func (s *Store) ReleaseIdempotency(ctx context.Context, actorUserID int64, route, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM idempotency_records WHERE actor_user_id=? AND route=? AND idempotency_key=? AND state="pending"`, actorUserID, route, key)
	return err
}
