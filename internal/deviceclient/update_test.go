package deviceclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
)

func TestPrepareUpdateVerifiesSignatureResumesAndChecksDigest(t *testing.T) {
	content := []byte("portable-lanready-update-binary")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"formatVersion":1,"channel":"stable","sequence":2,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/%s","size":%d,"sha256":"%s","publishedAt":"%s"}`, digest, len(content), digest, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)))
	envelope, err := protocol.SignEnvelope(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	rawEnvelope, _ := json.Marshal(envelope)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/client/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(rawEnvelope)
		case "/v2/client/artifacts/sha256/" + digest:
			if r.Header.Get("Range") != "bytes=8-30" {
				t.Errorf("resume range = %q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Range", "bytes 8-30/31")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[8:])
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, deviceKey, _ := ed25519.GenerateKey(rand.Reader)
	client := &Client{Profile: Profile{ServerURL: server.URL, DeviceID: "device", DeviceName: "PC", ClientVersion: "0.1.0", PrivateKey: deviceKey}, HTTP: server.Client()}
	staging := t.TempDir()
	if err = os.WriteFile(filepath.Join(staging, digest+".part"), content[:8], 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := client.PrepareUpdate(context.Background(), "0.1.0", 1, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}, staging)
	if err != nil || prepared.Sequence != 2 || prepared.Version != "0.2.0" {
		t.Fatalf("prepared update: %#v %v", prepared, err)
	}
	got, err := os.ReadFile(prepared.StagedPath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("staged update mismatch: %q %v", got, err)
	}
}

func TestPrepareUpdateRejectsUntrustedEnvelope(t *testing.T) {
	_, privateKey, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte(`{"formatVersion":1,"channel":"stable","sequence":2,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-07-18T12:00:00Z"}`)
	envelope, _ := protocol.SignEnvelope(payload, privateKey)
	raw, _ := json.Marshal(envelope)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	_, deviceKey, _ := ed25519.GenerateKey(rand.Reader)
	client := &Client{Profile: Profile{ServerURL: server.URL, DeviceID: "device", DeviceName: "PC", ClientVersion: "0.1.0", PrivateKey: deviceKey}, HTTP: server.Client()}
	if _, err := client.PrepareUpdate(context.Background(), "0.1.0", 0, map[string]ed25519.PublicKey{}, t.TempDir()); err == nil {
		t.Fatal("untrusted client update was accepted")
	}
}
