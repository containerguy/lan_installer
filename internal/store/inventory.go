package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
)

var (
	ErrDeviceNotFound        = errors.New("device not found")
	ErrAuthorizationPending  = errors.New("authorization pending")
	ErrAuthorizationDenied   = errors.New("authorization denied")
	ErrAuthorizationExpired  = errors.New("authorization expired")
	ErrAuthorizationUsed     = errors.New("authorization already consumed")
	ErrAuthorizationSlowDown = errors.New("authorization polling too fast")
	ErrUserTokenInvalid      = errors.New("user token invalid")
	ErrInventoryConflict     = errors.New("inventory scan id reused with different content")
	ErrNonceReplay           = errors.New("nonce already used")
	ErrDeviceVersionRollback = errors.New("device client version cannot move backwards")
)

type Device struct {
	ID, Name, WindowsVersion, ClientVersion, ClientVersionHighWatermark, Status string
	PublicKey                                                                   []byte
	EnrolledAt, LastSeenAt                                                      time.Time
}

type DeviceAuthorization struct {
	AuthorizationID, UserCode, VerificationURL string
	ExpiresAt                                  time.Time
	PollInterval                               time.Duration
}

type InventoryInstallation struct {
	Position         int     `json:"-"`
	Launcher         string  `json:"launcher"`
	ExternalGameID   string  `json:"externalGameId"`
	DisplayName      string  `json:"displayName"`
	DetectedVersion  *string `json:"detectedVersion"`
	VersionSource    string  `json:"versionSource"`
	InstallPath      string  `json:"installPath"`
	CatalogGameID    int64   `json:"-"`
	CatalogVersionID int64   `json:"-"`
	CatalogGameName  string  `json:"-"`
}

type InventoryScan struct {
	ID, ClientVersion string
	ScannedAt         time.Time
	Installations     []InventoryInstallation
}

type DeviceInventory struct {
	Device
	ScanID, ScanClientVersion string
	ScannedAt                 time.Time
	Installations             []InventoryInstallation
}

type PendingDeviceAuthorization struct {
	DeviceID, DeviceName, UserCode string
	ExpiresAt                      time.Time
}

func (s *Store) CreateDevice(ctx context.Context, device Device) error {
	if strings.TrimSpace(device.ID) == "" || strings.TrimSpace(device.Name) == "" || len(device.PublicKey) != 32 {
		return errors.New("device id, name and 32-byte public key are required")
	}
	if device.Status == "" {
		device.Status = "active"
	}
	if device.Status != "active" && device.Status != "revoked" {
		return errors.New("invalid device status")
	}
	if device.EnrolledAt.IsZero() {
		device.EnrolledAt = time.Now().UTC()
	}
	if device.ClientVersionHighWatermark == "" {
		device.ClientVersionHighWatermark = device.ClientVersion
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO devices(id,name,public_key,windows_version,client_version,client_version_high_watermark,status,enrolled_at) VALUES(?,?,?,?,?,?,?,?)`,
		device.ID, strings.TrimSpace(device.Name), device.PublicKey, strings.TrimSpace(device.WindowsVersion), strings.TrimSpace(device.ClientVersion), strings.TrimSpace(device.ClientVersionHighWatermark), device.Status, device.EnrolledAt.Unix())
	return err
}

func (s *Store) ActiveDevicePublicKey(ctx context.Context, deviceID string) ([]byte, error) {
	var key []byte
	err := s.db.QueryRowContext(ctx, `SELECT public_key FROM devices WHERE id=? AND status='active'`, deviceID).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDeviceNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("stored device public key is invalid")
	}
	return key, nil
}

func (s *Store) ActiveDevice(ctx context.Context, deviceID string) (Device, error) {
	var device Device
	var enrolledAt int64
	var lastSeenAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id,name,public_key,windows_version,client_version,client_version_high_watermark,status,enrolled_at,last_seen_at FROM devices WHERE id=? AND status='active'`, deviceID).Scan(&device.ID, &device.Name, &device.PublicKey, &device.WindowsVersion, &device.ClientVersion, &device.ClientVersionHighWatermark, &device.Status, &enrolledAt, &lastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, err
	}
	device.EnrolledAt = time.Unix(enrolledAt, 0).UTC()
	if lastSeenAt.Valid {
		device.LastSeenAt = time.Unix(lastSeenAt.Int64, 0).UTC()
	}
	return device, nil
}

func (s *Store) UpdateActiveDeviceClientVersion(ctx context.Context, deviceID, version string) error {
	if _, err := protocol.CompareSemanticVersions(version, "0.0.0"); err != nil {
		return errors.New("device client version is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var highWatermark string
	if err = tx.QueryRowContext(ctx, `SELECT client_version_high_watermark FROM devices WHERE id=? AND status='active'`, deviceID).Scan(&highWatermark); errors.Is(err, sql.ErrNoRows) {
		return ErrDeviceNotFound
	} else if err != nil {
		return err
	}
	downgraded := false
	comparison, compareErr := protocol.CompareSemanticVersions(version, highWatermark)
	if compareErr != nil {
		highWatermark = version
	} else if comparison < 0 {
		downgraded = true
	} else {
		highWatermark = version
	}
	now := time.Now().UTC().Unix()
	if _, err = tx.ExecContext(ctx, `UPDATE devices SET client_version=?,client_version_high_watermark=?,last_seen_at=? WHERE id=? AND status='active'`, version, highWatermark, now, deviceID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if downgraded {
		return ErrDeviceVersionRollback
	}
	return nil
}

func (s *Store) ConsumeDeviceNonce(ctx context.Context, deviceID string, nonce []byte, expiresAt time.Time) error {
	if len(nonce) < 12 || len(nonce) > 32 || expiresAt.IsZero() {
		return errors.New("invalid nonce")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Unix()
	if _, err = tx.ExecContext(ctx, `DELETE FROM device_nonces WHERE expires_at<?`, now); err != nil {
		return err
	}
	sum := sha256.Sum256(nonce)
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_nonces(device_id,nonce_hash,expires_at) VALUES(?,?,?)`, deviceID, sum[:], expiresAt.Unix()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrNonceReplay
		}
		return err
	}
	return tx.Commit()
}

func shortUserCode() (string, error) {
	raw := make([]byte, 5)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return value[:4] + "-" + value[4:], nil
}

func (s *Store) CreateDeviceAuthorization(ctx context.Context, deviceID, verificationURL string, lifetime time.Duration) (DeviceAuthorization, error) {
	var out DeviceAuthorization
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(verificationURL) == "" || lifetime <= 0 || lifetime > 15*time.Minute {
		return out, errors.New("device, verification URL and lifetime up to 15 minutes are required")
	}
	for attempt := 0; attempt < 4; attempt++ {
		id, err := randomToken()
		if err != nil {
			return out, err
		}
		code, err := shortUserCode()
		if err != nil {
			return out, err
		}
		now := time.Now().UTC()
		expires := now.Add(lifetime)
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return out, err
		}
		var status string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM devices WHERE id=?`, deviceID).Scan(&status); errors.Is(err, sql.ErrNoRows) || status != "active" {
			tx.Rollback()
			return out, ErrDeviceNotFound
		} else if err != nil {
			tx.Rollback()
			return out, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM user_device_authorizations WHERE expires_at<?`, now.Unix()); err != nil {
			tx.Rollback()
			return out, err
		}
		var outstanding int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_device_authorizations WHERE device_id=? AND status IN ('pending','approved')`, deviceID).Scan(&outstanding); err != nil {
			tx.Rollback()
			return out, err
		}
		if outstanding >= 5 {
			tx.Rollback()
			return out, errors.New("too many outstanding device authorizations")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO user_device_authorizations(id_hash,code_hash,device_id,status,expires_at,next_poll_at,created_at) VALUES(?,?,?,'pending',?,?,?)`, tokenHash(id), tokenHash(strings.ToUpper(code)), deviceID, expires.Unix(), now.Unix(), now.Unix())
		if err != nil {
			tx.Rollback()
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				continue
			}
			return out, err
		}
		if err = tx.Commit(); err != nil {
			return out, err
		}
		return DeviceAuthorization{AuthorizationID: id, UserCode: code, VerificationURL: strings.TrimRight(verificationURL, "/") + "/admin/device", ExpiresAt: expires, PollInterval: 5 * time.Second}, nil
	}
	return out, errors.New("could not allocate unique authorization")
}

func (s *Store) ApproveDeviceAuthorization(ctx context.Context, userCode string, userID int64) error {
	if userID < 1 || strings.TrimSpace(userCode) == "" {
		return ErrAuthorizationExpired
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE user_device_authorizations SET status='approved',user_id=?,approved_at=? WHERE code_hash=? AND status='pending' AND expires_at>=?`, userID, now, tokenHash(strings.ToUpper(strings.TrimSpace(userCode))), now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrAuthorizationExpired
	}
	return nil
}

func (s *Store) DenyDeviceAuthorization(ctx context.Context, userCode string, userID int64) error {
	if userID < 1 || strings.TrimSpace(userCode) == "" {
		return ErrAuthorizationExpired
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE user_device_authorizations SET status='denied',user_id=?,approved_at=? WHERE code_hash=? AND status='pending' AND expires_at>=?`, userID, now, tokenHash(strings.ToUpper(strings.TrimSpace(userCode))), now)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrAuthorizationExpired
	}
	return nil
}

func (s *Store) PendingDeviceAuthorization(ctx context.Context, userCode string) (PendingDeviceAuthorization, error) {
	var out PendingDeviceAuthorization
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT a.device_id,d.name,a.expires_at FROM user_device_authorizations a JOIN devices d ON d.id=a.device_id WHERE a.code_hash=? AND a.status='pending' AND a.expires_at>=? AND d.status='active'`, tokenHash(strings.ToUpper(strings.TrimSpace(userCode))), time.Now().UTC().Unix()).Scan(&out.DeviceID, &out.DeviceName, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrAuthorizationExpired
	}
	if err != nil {
		return out, err
	}
	out.UserCode = strings.ToUpper(strings.TrimSpace(userCode))
	out.ExpiresAt = time.Unix(expires, 0).UTC()
	return out, nil
}

func (s *Store) PollDeviceAuthorization(ctx context.Context, deviceID, authorizationID string, tokenLifetime time.Duration) (string, error) {
	if tokenLifetime <= 0 || tokenLifetime > time.Hour {
		return "", errors.New("token lifetime must be between zero and one hour")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var status string
	var userID sql.NullInt64
	var expires, nextPoll int64
	err = tx.QueryRowContext(ctx, `SELECT status,user_id,expires_at,next_poll_at FROM user_device_authorizations WHERE id_hash=? AND device_id=?`, tokenHash(authorizationID), deviceID).Scan(&status, &userID, &expires, &nextPoll)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAuthorizationExpired
	}
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if now.Unix() > expires {
		return "", ErrAuthorizationExpired
	}
	switch status {
	case "pending":
		if now.Unix() < nextPoll {
			return "", ErrAuthorizationSlowDown
		}
		if _, err = tx.ExecContext(ctx, `UPDATE user_device_authorizations SET next_poll_at=? WHERE id_hash=?`, now.Add(5*time.Second).Unix(), tokenHash(authorizationID)); err != nil {
			return "", err
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return "", ErrAuthorizationPending
	case "denied":
		return "", ErrAuthorizationDenied
	case "consumed":
		return "", ErrAuthorizationUsed
	case "approved":
		if !userID.Valid {
			return "", errors.New("approved authorization has no user")
		}
	default:
		return "", errors.New("invalid authorization state")
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE user_device_authorizations SET status='consumed',consumed_at=? WHERE id_hash=? AND status='approved'`, now.Unix(), tokenHash(authorizationID))
	if err != nil {
		return "", err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return "", ErrAuthorizationUsed
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_user_tokens(token_hash,device_id,user_id,expires_at,created_at) VALUES(?,?,?,?,?)`, tokenHash(token), deviceID, userID.Int64, now.Add(tokenLifetime).Unix(), now.Unix()); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func validateInventory(scan InventoryScan) error {
	if strings.TrimSpace(scan.ID) == "" || len(scan.ID) > 64 || scan.ScannedAt.IsZero() || strings.TrimSpace(scan.ClientVersion) == "" || len(scan.Installations) > 10000 {
		return errors.New("invalid inventory scan metadata")
	}
	if _, err := protocol.CompareSemanticVersions(scan.ClientVersion, "0.0.0"); err != nil {
		return errors.New("inventory client version is invalid")
	}
	for i, item := range scan.Installations {
		if item.Launcher != "steam" && item.Launcher != "ea_app" && item.Launcher != "ubisoft_connect" {
			return fmt.Errorf("installation %d has invalid launcher", i)
		}
		if strings.TrimSpace(item.ExternalGameID) == "" || len(item.ExternalGameID) > 256 || strings.TrimSpace(item.DisplayName) == "" || len(item.DisplayName) > 500 || strings.TrimSpace(item.VersionSource) == "" || len(item.VersionSource) > 100 || strings.TrimSpace(item.InstallPath) == "" || len(item.InstallPath) > 4096 {
			return fmt.Errorf("installation %d is invalid", i)
		}
		if item.DetectedVersion != nil && len(*item.DetectedVersion) > 256 {
			return fmt.Errorf("installation %d version is too long", i)
		}
	}
	return nil
}

func (s *Store) SaveDeviceInventory(ctx context.Context, deviceID, userToken string, scan InventoryScan) error {
	if err := validateInventory(scan); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	encoded, err := json.Marshal(scan)
	if err != nil {
		return err
	}
	requestHash := sha256.Sum256(encoded)
	var userID int64
	var highWatermark string
	err = tx.QueryRowContext(ctx, `SELECT t.user_id,d.client_version_high_watermark FROM device_user_tokens t JOIN users u ON u.id=t.user_id JOIN devices d ON d.id=t.device_id WHERE t.token_hash=? AND t.device_id=? AND t.expires_at>=? AND u.active=1 AND d.status='active'`, tokenHash(userToken), deviceID, time.Now().UTC().Unix()).Scan(&userID, &highWatermark)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserTokenInvalid
	}
	if err != nil {
		return err
	}
	comparison, compareErr := protocol.CompareSemanticVersions(scan.ClientVersion, highWatermark)
	if compareErr != nil {
		highWatermark = scan.ClientVersion
	} else if comparison < 0 {
		now := time.Now().UTC().Unix()
		if _, err = tx.ExecContext(ctx, `UPDATE devices SET client_version=?,last_seen_at=? WHERE id=?`, scan.ClientVersion, now, deviceID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrDeviceVersionRollback
	} else {
		highWatermark = scan.ClientVersion
	}
	var existingHash []byte
	err = tx.QueryRowContext(ctx, `SELECT request_hash FROM device_inventory_scans WHERE device_id=? AND id=?`, deviceID, scan.ID).Scan(&existingHash)
	if err == nil {
		if len(existingHash) != len(requestHash) || subtle.ConstantTimeCompare(existingHash, requestHash[:]) != 1 {
			return ErrInventoryConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE device_inventory_scans SET current=0 WHERE device_id=? AND current=1`, deviceID); err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_inventory_scans(id,device_id,user_id,scanned_at,client_version,request_hash,current,created_at) VALUES(?,?,?,?,?,?,1,?)`, scan.ID, deviceID, userID, scan.ScannedAt.Unix(), scan.ClientVersion, requestHash[:], now); err != nil {
		return err
	}
	for position, item := range scan.Installations {
		if _, err = tx.ExecContext(ctx, `INSERT INTO device_inventory_items(scan_id,device_id,position,launcher,external_game_id,display_name,detected_version,version_source,install_path) VALUES(?,?,?,?,?,?,?,?,?)`, scan.ID, deviceID, position, item.Launcher, item.ExternalGameID, item.DisplayName, item.DetectedVersion, item.VersionSource, item.InstallPath); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE devices SET last_seen_at=?,client_version=?,client_version_high_watermark=? WHERE id=?`, now, scan.ClientVersion, highWatermark, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeviceInventory(ctx context.Context, deviceID string) (DeviceInventory, error) {
	var out DeviceInventory
	var enrolled, lastSeen sql.NullInt64
	var scanned sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT d.id,d.name,d.public_key,d.windows_version,d.client_version,d.client_version_high_watermark,d.status,d.enrolled_at,d.last_seen_at,COALESCE(i.id,''),COALESCE(i.client_version,''),i.scanned_at FROM devices d LEFT JOIN device_inventory_scans i ON i.device_id=d.id AND i.current=1 WHERE d.id=?`, deviceID).Scan(&out.ID, &out.Name, &out.PublicKey, &out.WindowsVersion, &out.ClientVersion, &out.ClientVersionHighWatermark, &out.Status, &enrolled, &lastSeen, &out.ScanID, &out.ScanClientVersion, &scanned)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrDeviceNotFound
	}
	if err != nil {
		return out, err
	}
	if enrolled.Valid {
		out.EnrolledAt = time.Unix(enrolled.Int64, 0).UTC()
	}
	if lastSeen.Valid {
		out.LastSeenAt = time.Unix(lastSeen.Int64, 0).UTC()
	}
	if scanned.Valid {
		out.ScannedAt = time.Unix(scanned.Int64, 0).UTC()
	}
	if out.ScanID == "" {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT i.position,i.launcher,i.external_game_id,i.display_name,i.detected_version,i.version_source,i.install_path,COALESCE(m.game_id,0),COALESCE(vm.game_version_id,0),COALESCE(g.name,'') FROM device_inventory_items i LEFT JOIN inventory_catalog_mappings m ON m.launcher=i.launcher AND m.external_game_id=i.external_game_id LEFT JOIN inventory_catalog_version_mappings vm ON vm.launcher=i.launcher AND vm.external_game_id=i.external_game_id AND vm.detected_version=i.detected_version LEFT JOIN games g ON g.id=m.game_id WHERE i.device_id=? AND i.scan_id=? ORDER BY i.position`, deviceID, out.ScanID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item InventoryInstallation
		var version sql.NullString
		if err = rows.Scan(&item.Position, &item.Launcher, &item.ExternalGameID, &item.DisplayName, &version, &item.VersionSource, &item.InstallPath, &item.CatalogGameID, &item.CatalogVersionID, &item.CatalogGameName); err != nil {
			return out, err
		}
		if version.Valid {
			item.DetectedVersion = &version.String
		}
		out.Installations = append(out.Installations, item)
	}
	return out, rows.Err()
}

func (s *Store) DeviceInventories(ctx context.Context) ([]DeviceInventory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM devices ORDER BY COALESCE(last_seen_at,enrolled_at) DESC,name`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	out := make([]DeviceInventory, 0, len(ids))
	for _, id := range ids {
		inventory, readErr := s.DeviceInventory(ctx, id)
		if readErr != nil {
			return nil, readErr
		}
		out = append(out, inventory)
	}
	return out, nil
}
