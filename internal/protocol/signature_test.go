package protocol

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type vectors struct {
	ReleaseKey    struct{ KeyID, PublicKey string }
	EventRelease  struct{ Payload, Signature string }
	ClientUpdate  struct{ Payload, Signature string }
	DeviceRequest struct{ PublicKey, Body, CanonicalRequest, Signature string }
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	content, err := os.ReadFile("../../docs/contracts/vectors/signatures-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err = json.Unmarshal(content, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func decode(t *testing.T, value string) []byte {
	t.Helper()
	out, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestReleaseGoldenVectors(t *testing.T) {
	v := loadVectors(t)
	pub := ed25519.PublicKey(decode(t, v.ReleaseKey.PublicKey))
	trusted := map[string]ed25519.PublicKey{v.ReleaseKey.KeyID: pub}
	for _, item := range []struct {
		name      string
		payload   string
		signature string
	}{{"event", v.EventRelease.Payload, v.EventRelease.Signature}, {"update", v.ClientUpdate.Payload, v.ClientUpdate.Signature}} {
		t.Run(item.name, func(t *testing.T) {
			payload, err := VerifyEnvelope(Envelope{FormatVersion: 1, KeyID: v.ReleaseKey.KeyID, Algorithm: "Ed25519", Payload: item.payload, Signature: item.signature}, trusted)
			if err != nil || len(payload) == 0 {
				t.Fatalf("verify: %v", err)
			}
			payload[0] ^= 1
			if ed25519.Verify(pub, payload, decode(t, item.signature)) {
				t.Fatal("tampered payload accepted")
			}
		})
	}
}
func TestDeviceRequestGoldenVector(t *testing.T) {
	v := loadVectors(t)
	body := decode(t, v.DeviceRequest.Body)
	nonce, _ := base64.RawURLEncoding.DecodeString("AQIDBAUGBwgJCgsM")
	canonical, err := CanonicalRequest("POST", "/v2/device/status", "unicode=%C3%A4&tag=z&a=&tag=a", body, 1784188800, nonce)
	if err != nil {
		t.Fatal(err)
	}
	expected := decode(t, v.DeviceRequest.CanonicalRequest)
	if string(canonical) != string(expected) {
		t.Fatalf("canonical mismatch\n%s\n%s", canonical, expected)
	}
	pub := ed25519.PublicKey(decode(t, v.DeviceRequest.PublicKey))
	if !ed25519.Verify(pub, canonical, decode(t, v.DeviceRequest.Signature)) {
		t.Fatal("request signature rejected")
	}
}
func TestCanonicalizationRejectsDangerousPaths(t *testing.T) {
	for _, path := range []string{"relative", "/a/../b", "/a/%2F/b", "/a\\b", "/a//b"} {
		if _, err := CanonicalPath(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
func TestCanonicalizationEdgeCases(t *testing.T) {
	for _, path := range []string{"/v2/%FF", "/v2/%", "/v2/%00"} {
		if _, err := CanonicalPath(path); err == nil {
			t.Fatalf("accepted invalid path %q", path)
		}
	}
	for _, query := range []string{"q=%FF", "q=%", "q=%00"} {
		if _, err := CanonicalQuery(query); err == nil {
			t.Fatalf("accepted invalid query %q", query)
		}
	}
	canonical, err := CanonicalQuery("empty&plus=+&space=%20&dup=z&dup=&dup=a")
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "dup=&dup=a&dup=z&empty=&plus=%2B&space=%20" {
		t.Fatalf("unexpected canonical query %q", canonical)
	}
}

func TestKeyID(t *testing.T) {
	v := loadVectors(t)
	pub := ed25519.PublicKey(decode(t, v.ReleaseKey.PublicKey))
	if KeyID(pub) != v.ReleaseKey.KeyID {
		t.Fatal("key ID mismatch")
	}
	if _, err := hex.DecodeString(v.ReleaseKey.KeyID[len("ed25519-"):]); err != nil {
		t.Fatal(err)
	}
}
