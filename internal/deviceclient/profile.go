package deviceclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// PreflightProfile verifies the CurrentUser key protection and the target
// directory before a one-time server enrollment code is consumed.
func PreflightProfile(path string) error {
	if path == "" {
		return errors.New("device profile path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".lanready-profile-preflight-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	if err = file.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err = os.Remove(temporary); err != nil {
		return err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err == nil {
		err = SaveProfile(temporary, Profile{ServerURL: "https://preflight.invalid", DeviceID: "preflight", DeviceName: "preflight", ClientVersion: "preflight", PrivateKey: privateKey})
	}
	removeErr := os.Remove(temporary)
	if err != nil {
		return err
	}
	return removeErr
}

type Profile struct {
	ServerURL, DeviceID, DeviceName, ClientVersion string
	PrivateKey                                     ed25519.PrivateKey `json:"-"`
	UpdateSequence                                 int64              `json:"-"`
}

type storedProfile struct {
	FormatVersion    int    `json:"formatVersion"`
	ProtectedPayload string `json:"protectedPayload"`
}

type profilePayload struct {
	ServerURL      string `json:"serverUrl"`
	DeviceID       string `json:"deviceId"`
	DeviceName     string `json:"deviceName"`
	ClientVersion  string `json:"clientVersion"`
	PrivateKey     string `json:"privateKey"`
	UpdateSequence int64  `json:"updateSequence"`
}

func SaveProfile(path string, profile Profile) error {
	if path == "" {
		return errors.New("device profile path is required")
	}
	if err := validateProfile(profile); err != nil {
		return err
	}
	payload, err := json.Marshal(profilePayload{ServerURL: profile.ServerURL, DeviceID: profile.DeviceID, DeviceName: profile.DeviceName, ClientVersion: profile.ClientVersion, PrivateKey: base64.RawStdEncoding.EncodeToString(profile.PrivateKey), UpdateSequence: profile.UpdateSequence})
	if err != nil {
		return err
	}
	protected, err := protectPrivateKey(payload)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(storedProfile{FormatVersion: 2, ProtectedPayload: base64.RawStdEncoding.EncodeToString(protected)}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err = os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Chmod(path, 0o600)
}

func LoadProfile(path string) (Profile, error) {
	var out Profile
	content, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	var stored storedProfile
	if err = json.Unmarshal(content, &stored); err != nil {
		return out, errors.New("device profile is invalid")
	}
	if stored.FormatVersion != 2 || stored.ProtectedPayload == "" {
		return out, errors.New("device profile uses an unsupported unprotected format; reconnect this device")
	}
	protected, err := base64.RawStdEncoding.DecodeString(stored.ProtectedPayload)
	if err != nil {
		return out, errors.New("protected device profile is invalid")
	}
	payloadBytes, err := unprotectPrivateKey(protected)
	if err != nil {
		return out, errors.New("device profile cannot be unlocked for this user")
	}
	var payload profilePayload
	if err = json.Unmarshal(payloadBytes, &payload); err != nil {
		return out, errors.New("protected device profile payload is invalid")
	}
	privateKey, err := base64.RawStdEncoding.DecodeString(payload.PrivateKey)
	if err != nil {
		return out, errors.New("protected device key is invalid")
	}
	out = Profile{ServerURL: payload.ServerURL, DeviceID: payload.DeviceID, DeviceName: payload.DeviceName, ClientVersion: payload.ClientVersion, PrivateKey: ed25519.PrivateKey(privateKey), UpdateSequence: payload.UpdateSequence}
	if err = validateProfile(out); err != nil {
		return Profile{}, errors.New("protected device profile fields are invalid")
	}
	return out, nil
}

func validateProfile(profile Profile) error {
	parsed, err := url.Parse(profile.ServerURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("device profile requires a credential-free HTTPS server origin")
	}
	if profile.DeviceID == "" || len(profile.DeviceID) > 128 || profile.DeviceName == "" || len(profile.DeviceName) > 128 || profile.ClientVersion == "" || len(profile.ClientVersion) > 64 || len(profile.PrivateKey) != ed25519.PrivateKeySize || profile.UpdateSequence < 0 {
		return errors.New("complete device profile is required")
	}
	return nil
}
