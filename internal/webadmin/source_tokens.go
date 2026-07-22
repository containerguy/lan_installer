package webadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	lanrelease "github.com/containerguy/lan_installer/internal/release"
	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/sourceprobe"
	"github.com/containerguy/lan_installer/internal/store"
)

type SourceTester interface {
	Test(context.Context, sourceprobe.Input) (sourceprobe.Result, error)
}

type ArtifactStore interface {
	Ingest(context.Context, string, int64, string, io.Reader) (store.Artifact, error)
	Verify(context.Context, string, int64, string) error
	Usage(context.Context) (int64, int64, error)
	GarbageCollect(context.Context, time.Time, int, int64, string) (artifact.GCResult, error)
}

type Option func(*Admin)

func WithSourceTester(tester SourceTester) Option {
	return func(a *Admin) {
		if tester != nil {
			a.sourceTester = tester
		}
	}
}

func WithReleaseService(service *lanrelease.Service) Option {
	return func(a *Admin) { a.releases = service }
}

func WithArtifactStore(artifacts ArtifactStore) Option {
	return func(a *Admin) { a.artifacts = artifacts }
}

type sourceAuthRequest struct {
	Type       string `json:"type"`
	Username   string `json:"username"`
	SecretMode string `json:"secretMode"`
	Password   string `json:"password"`
}

type sourceRequest struct {
	SourceID  int64             `json:"sourceId"`
	Revision  int64             `json:"revision"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	BaseURL   string            `json:"baseUrl"`
	Enabled   bool              `json:"enabled"`
	Auth      sourceAuthRequest `json:"auth"`
	TestToken string            `json:"testToken"`
}

type sourceTestRecord struct {
	UserID      int64
	Fingerprint [32]byte
	Result      sourceprobe.Result
	ExpiresAt   time.Time
}

type sourceTestTokens struct {
	mu      sync.Mutex
	records map[[32]byte]sourceTestRecord
}

func newSourceTestTokens() *sourceTestTokens {
	return &sourceTestTokens{records: map[[32]byte]sourceTestRecord{}}
}

func (t *sourceTestTokens) issue(record sourceTestRecord) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(token))
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for key, value := range t.records {
		if now.After(value.ExpiresAt) {
			delete(t.records, key)
		}
	}
	userCount := 0
	for _, value := range t.records {
		if value.UserID == record.UserID {
			userCount++
		}
	}
	if userCount >= 32 || len(t.records) >= 1024 {
		return "", errors.New("too many outstanding source test tokens")
	}
	t.records[hash] = record
	return token, nil
}

func (t *sourceTestTokens) take(token string, userID int64, fingerprint [32]byte) (sourceTestRecord, bool) {
	hash := sha256.Sum256([]byte(token))
	t.mu.Lock()
	defer t.mu.Unlock()
	record, ok := t.records[hash]
	if !ok || record.UserID != userID || record.Fingerprint != fingerprint || time.Now().After(record.ExpiresAt) {
		return sourceTestRecord{}, false
	}
	delete(t.records, hash)
	return record, true
}

func (t *sourceTestTokens) restore(token string, record sourceTestRecord) {
	hash := sha256.Sum256([]byte(token))
	t.mu.Lock()
	t.records[hash] = record
	t.mu.Unlock()
}

func (a *Admin) sourceFingerprint(ctx context.Context, request sourceRequest) ([32]byte, string, error) {
	request = normalizeSourceRequest(request)
	if request.Name == "" || len(request.Name) > 200 {
		return [32]byte{}, "", errors.New("Name ist erforderlich und darf höchstens 200 Zeichen enthalten.")
	}
	if request.BaseURL == "" || len(request.BaseURL) > 2048 {
		return [32]byte{}, "", errors.New("Basis-URL ist erforderlich und darf höchstens 2048 Zeichen enthalten.")
	}
	if request.Kind == "https" {
		request.Auth.Type = "none"
		request.Auth.Username = ""
		request.Auth.SecretMode = "none"
		request.Auth.Password = ""
	}
	if request.Kind != "https" && request.Kind != "webdav" && request.Kind != "nextcloud_webdav" {
		return [32]byte{}, "", errors.New("Quellentyp ist ungültig.")
	}
	password := ""
	secretMarker := "none"
	if request.Auth.Type == "basic" {
		if request.Auth.Username == "" || len(request.Auth.Username) > 320 {
			return [32]byte{}, "", errors.New("Benutzername ist für Basic-Authentifizierung erforderlich.")
		}
		switch request.Auth.SecretMode {
		case "replace":
			if request.Auth.Password == "" || len(request.Auth.Password) > 4096 {
				return [32]byte{}, "", errors.New("App-Passwort ist erforderlich oder zu lang.")
			}
			password = request.Auth.Password
			digest := sha256.Sum256([]byte(password))
			secretMarker = "replace:" + hex.EncodeToString(digest[:])
		case "reuse":
			if request.SourceID < 1 || a.vault == nil {
				return [32]byte{}, "", errors.New("Gespeichertes Secret kann nicht wiederverwendet werden.")
			}
			config, err := a.store.WebDAVConfig(ctx, request.SourceID)
			if err != nil || config.AuthType != "basic" || len(config.SecretCiphertext) == 0 {
				return [32]byte{}, "", errors.New("Gespeichertes Secret fehlt.")
			}
			persisted, err := a.store.Source(ctx, request.SourceID)
			if err != nil || persisted.Kind != request.Kind || persisted.BaseURL != request.BaseURL || config.Username != request.Auth.Username {
				return [32]byte{}, "", errors.New("Gespeichertes Secret ist an die bestehende Quelle gebunden; bei URL-, Typ- oder Benutzerwechsel muss es neu eingegeben werden.")
			}
			plain, err := a.vault.Open(config.SecretNonce, config.SecretCiphertext, secretbox.WebDAVAAD(request.SourceID))
			if err != nil {
				return [32]byte{}, "", errors.New("Gespeichertes Secret konnte nicht entschlüsselt werden.")
			}
			password = string(plain)
			digest := sha256.Sum256(config.SecretCiphertext)
			secretMarker = "reuse:" + hex.EncodeToString(digest[:])
		default:
			return [32]byte{}, "", errors.New("Secretmodus muss replace oder reuse sein.")
		}
	} else if request.Auth.Type != "none" {
		return [32]byte{}, "", errors.New("Authentifizierung muss none oder basic sein.")
	}
	fingerprintInput := struct {
		SourceID                                int64
		Name, Kind, BaseURL, AuthType, Username string
		Secret                                  string
		Enabled                                 bool
	}{
		request.SourceID, request.Name, request.Kind, request.BaseURL,
		request.Auth.Type, request.Auth.Username, secretMarker, request.Enabled,
	}
	encoded, _ := json.Marshal(fingerprintInput)
	return sha256.Sum256(encoded), password, nil
}

func (a *Admin) sourceConfigBuilder(request sourceRequest) store.WebDAVConfigBuilder {
	return func(sourceID int64, existing *store.WebDAVConfig) (*store.WebDAVConfig, error) {
		if request.Kind == "https" {
			return nil, nil
		}
		if request.Auth.Type == "none" {
			return &store.WebDAVConfig{AuthType: "none"}, nil
		}
		if request.Auth.SecretMode == "reuse" {
			if existing == nil || existing.AuthType != "basic" || len(existing.SecretCiphertext) == 0 {
				return nil, errors.New("stored secret is missing")
			}
			copy := *existing
			copy.Username = strings.TrimSpace(request.Auth.Username)
			return &copy, nil
		}
		if a.vault == nil {
			return nil, errors.New("credential encryption unavailable")
		}
		nonce, ciphertext, err := a.vault.Seal([]byte(request.Auth.Password), secretbox.WebDAVAAD(sourceID))
		if err != nil {
			return nil, err
		}
		return &store.WebDAVConfig{
			AuthType: "basic", Username: strings.TrimSpace(request.Auth.Username),
			SecretNonce: nonce, SecretCiphertext: ciphertext,
		}, nil
	}
}

func normalizeSourceRequest(request sourceRequest) sourceRequest {
	request.Name = strings.TrimSpace(request.Name)
	request.Kind = strings.ToLower(strings.TrimSpace(request.Kind))
	request.BaseURL = strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	request.Auth.Type = strings.ToLower(strings.TrimSpace(request.Auth.Type))
	request.Auth.Username = strings.TrimSpace(request.Auth.Username)
	request.Auth.SecretMode = strings.ToLower(strings.TrimSpace(request.Auth.SecretMode))
	if request.Kind == "https" {
		request.Auth.Type = "none"
		request.Auth.Username = ""
		request.Auth.SecretMode = "none"
		request.Auth.Password = ""
	}
	return request
}
