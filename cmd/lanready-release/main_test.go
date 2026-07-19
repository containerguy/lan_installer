package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/containerguy/lan_installer/internal/protocol"
)

func TestKeygenSignAndVerifyUpdate(t *testing.T) {
	originalVerifier := verifyClientUpdateArtifact
	verifyClientUpdateArtifact = func(string, protocol.ClientUpdateMetadata) error { return nil }
	defer func() { verifyClientUpdateArtifact = originalVerifier }()
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "private.key")
	publicPath := filepath.Join(directory, "public.key")
	if err := keygen([]string{"-private-key", privatePath, "-public-key", publicPath}); err != nil {
		t.Fatal(err)
	}
	privateInfo, err := os.Stat(privatePath)
	if err != nil || privateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private key permissions: %v %v", privateInfo, err)
	}
	if err = keygen([]string{"-private-key", privatePath, "-public-key", publicPath}); err == nil {
		t.Fatal("existing key was overwritten")
	}
	payloadPath := filepath.Join(directory, "update.json")
	envelopePath := filepath.Join(directory, "update-envelope.json")
	payload := []byte(`{"formatVersion":1,"channel":"stable","sequence":1,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-07-18T12:00:00Z"}`)
	if err = os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	schemaDirectory, err := filepath.Abs("../../docs/contracts/schemas")
	if err != nil {
		t.Fatal(err)
	}
	if err = signRelease([]string{"-in", payloadPath, "-out", envelopePath, "-private-key", privatePath, "-schema-dir", schemaDirectory, "-artifact", filepath.Join(directory, "LANReady.exe")}, false); err != nil {
		t.Fatal(err)
	}
	if err = verifyRelease([]string{"-in", envelopePath, "-public-key", publicPath, "-schema-dir", schemaDirectory}, false); err != nil {
		t.Fatal(err)
	}
	if err = signRelease([]string{"-in", payloadPath, "-out", envelopePath, "-private-key", privatePath, "-schema-dir", schemaDirectory, "-artifact", filepath.Join(directory, "LANReady.exe")}, false); err == nil {
		t.Fatal("existing envelope was overwritten")
	}
}

func TestSignerRejectsInsecurePrivateKeyPermissions(t *testing.T) {
	directory := t.TempDir()
	privatePath := filepath.Join(directory, "private.key")
	publicPath := filepath.Join(directory, "public.key")
	if err := keygen([]string{"-private-key", privatePath, "-public-key", publicPath}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privatePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecurePrivateKey(privatePath); err == nil {
		t.Fatal("insecure private key permissions were accepted")
	}
}
