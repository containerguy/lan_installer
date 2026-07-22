package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
)

var ErrEnrollmentCodeInvalid = errors.New("enrollment code invalid")

type EnrollmentCode struct {
	ID, Code  string
	ExpiresAt time.Time
	MaxUses   int
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return hex.EncodeToString(value[0:4]) + "-" + hex.EncodeToString(value[4:6]) + "-" + hex.EncodeToString(value[6:8]) + "-" + hex.EncodeToString(value[8:10]) + "-" + hex.EncodeToString(value[10:16]), nil
}

func randomEnrollmentCode() (string, error) {
	value := make([]byte, 17)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value)[:26]
	return encoded[:5] + "-" + encoded[5:10] + "-" + encoded[10:15] + "-" + encoded[15:20] + "-" + encoded[20:], nil
}

func normalizeEnrollmentCode(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func (s *Store) AllowEnrollmentAttempt(ctx context.Context, clientIP, code string, now time.Time) (bool, error) {
	clientIP = strings.TrimSpace(clientIP)
	if clientIP == "" {
		return false, errors.New("client IP is required")
	}
	codeHash := sha256.Sum256([]byte(normalizeEnrollmentCode(code)))
	subjects := []struct {
		value string
		limit int
	}{{"ip:" + clientIP, 30}, {"pair:" + clientIP + ":" + hex.EncodeToString(codeHash[:8]), 8}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM enrollment_rate_limits WHERE reset_at<=?`, now.Unix()); err != nil {
		return false, err
	}
	allowed := true
	for _, subject := range subjects {
		hash := sha256.Sum256([]byte(subject.value))
		var count int
		var reset int64
		err = tx.QueryRowContext(ctx, `SELECT attempt_count,reset_at FROM enrollment_rate_limits WHERE subject_hash=?`, hash[:]).Scan(&count, &reset)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO enrollment_rate_limits(subject_hash,attempt_count,reset_at) VALUES(?,1,?)`, hash[:], now.Add(5*time.Minute).Unix())
		} else if err == nil {
			if count >= subject.limit {
				allowed = false
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE enrollment_rate_limits SET attempt_count=attempt_count+1 WHERE subject_hash=?`, hash[:])
			}
		}
		if err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return allowed, nil
}

func (s *Store) CreateEnrollmentCode(ctx context.Context, userID int64, lifetime time.Duration) (EnrollmentCode, error) {
	var out EnrollmentCode
	if userID < 1 || lifetime <= 0 || lifetime > 10*time.Minute {
		return out, errors.New("user and lifetime up to ten minutes are required")
	}
	for attempt := 0; attempt < 4; attempt++ {
		id, err := randomToken()
		if err != nil {
			return out, err
		}
		code, err := randomEnrollmentCode()
		if err != nil {
			return out, err
		}
		now := time.Now().UTC()
		expires := now.Add(lifetime)
		_, err = s.db.ExecContext(ctx, `INSERT INTO enrollment_codes(id,code_hash,expires_at,max_uses,created_by,created_at) VALUES(?,?,?,1,?,?)`, id, tokenHash(normalizeEnrollmentCode(code)), expires.Unix(), userID, now.Unix())
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				continue
			}
			return out, err
		}
		return EnrollmentCode{ID: id, Code: code, ExpiresAt: expires, MaxUses: 1}, nil
	}
	return out, errors.New("could not allocate enrollment code")
}

func (s *Store) RevokeEnrollmentCode(ctx context.Context, id string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE enrollment_codes SET revoked=1 WHERE id=? AND revoked=0 AND uses=0 AND expires_at>=?`, strings.TrimSpace(id), time.Now().UTC().Unix())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) EnrollDevice(ctx context.Context, code string, publicKey []byte, name, windowsVersion, clientVersion string) (Device, error) {
	var out Device
	if len(publicKey) != 32 || strings.TrimSpace(name) == "" || len(name) > 200 || strings.TrimSpace(windowsVersion) == "" || len(windowsVersion) > 200 || strings.TrimSpace(clientVersion) == "" || len(clientVersion) > 100 {
		return out, errors.New("invalid enrollment data")
	}
	clientVersion = strings.TrimSpace(clientVersion)
	if _, err := protocol.CompareSemanticVersions(clientVersion, "0.0.0"); err != nil {
		return out, errors.New("enrollment client version is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE enrollment_codes SET uses=uses+1 WHERE code_hash=? AND revoked=0 AND expires_at>=? AND uses<max_uses`, tokenHash(normalizeEnrollmentCode(code)), now.Unix())
	if err != nil {
		return out, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return out, ErrEnrollmentCodeInvalid
	}
	id, err := randomUUID()
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO devices(id,name,public_key,windows_version,client_version,client_version_high_watermark,status,enrolled_at) VALUES(?,?,?,?,?,?,'active',?)`, id, strings.TrimSpace(name), publicKey, strings.TrimSpace(windowsVersion), clientVersion, clientVersion, now.Unix())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return out, errors.New("device key already enrolled")
		}
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return Device{ID: id, Name: strings.TrimSpace(name), PublicKey: append([]byte(nil), publicKey...), WindowsVersion: strings.TrimSpace(windowsVersion), ClientVersion: clientVersion, ClientVersionHighWatermark: clientVersion, Status: "active", EnrolledAt: now}, nil
}
