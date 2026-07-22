package signing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/containerguy/lan_installer/internal/model"
)

func Generate() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func WriteKey(path string, key []byte, permission os.FileMode) error {
	encoded := base64.StdEncoding.EncodeToString(key) + "\n"
	return os.WriteFile(path, []byte(encoded), permission)
}

func ReadPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := readKey(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key has %d bytes, expected %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

func ReadPrivateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := readKey(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key has %d bytes, expected %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}

func readKey(path string) ([]byte, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	return raw, nil
}

func Sign(rawManifest []byte, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(rawManifest) == 0 {
		return nil, errors.New("manifest is empty")
	}
	if !json.Valid(rawManifest) {
		return nil, errors.New("manifest is not valid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, rawManifest); err != nil {
		return nil, fmt.Errorf("compact manifest: %w", err)
	}
	canonical := compact.Bytes()
	envelope := model.Envelope{
		Manifest:  json.RawMessage(canonical),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical)),
	}
	return json.Marshal(envelope)
}

func Verify(envelopeBytes []byte, publicKey ed25519.PublicKey) (model.EventManifest, error) {
	var envelope model.Envelope
	if err := json.Unmarshal(envelopeBytes, &envelope); err != nil {
		return model.EventManifest{}, fmt.Errorf("decode envelope: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return model.EventManifest{}, fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(publicKey, envelope.Manifest, signature) {
		return model.EventManifest{}, errors.New("manifest signature is invalid")
	}
	var manifest model.EventManifest
	if err := json.Unmarshal(envelope.Manifest, &manifest); err != nil {
		return model.EventManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := Validate(manifest); err != nil {
		return model.EventManifest{}, err
	}
	return manifest, nil
}

func Validate(manifest model.EventManifest) error {
	if manifest.Version != model.ManifestVersion {
		return fmt.Errorf("unsupported manifest version %d", manifest.Version)
	}
	if manifest.ID == "" || manifest.Name == "" {
		return errors.New("manifest id and name are required")
	}
	if manifest.ExpiresAt.IsZero() {
		return errors.New("manifest expiration is required")
	}
	if len(manifest.Mirrors) == 0 {
		return errors.New("at least one mirror is required")
	}
	for _, mirror := range manifest.Mirrors {
		parsed, err := url.Parse(mirror.BaseURL)
		if mirror.Name == "" || err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid mirror %q", mirror.Name)
		}
	}
	seenGames := make(map[string]bool)
	for _, game := range manifest.Games {
		if game.ID == "" || game.Name == "" || !safePortableRelative(game.Target) {
			return errors.New("every game requires id, name and target")
		}
		if seenGames[game.ID] {
			return fmt.Errorf("duplicate game id %q", game.ID)
		}
		seenGames[game.ID] = true
		for _, file := range game.Files {
			decodedHash, hashErr := hex.DecodeString(file.SHA256)
			if !safePortableRelative(file.Path) || !safePortableRelative(file.Source) || hashErr != nil || len(decodedHash) != 32 || file.Size < 0 {
				return fmt.Errorf("invalid file in game %q", game.ID)
			}
		}
	}
	return nil
}

func safePortableRelative(value string) bool {
	clean := strings.ReplaceAll(value, "\\", "/")
	if clean == "" || strings.HasPrefix(clean, "/") || (len(clean) > 1 && clean[1] == ':') {
		return false
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
