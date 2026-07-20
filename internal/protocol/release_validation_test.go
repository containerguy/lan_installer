package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	contractschemas "github.com/containerguy/lan_installer/docs/contracts/schemas"
)

func testReleaseValidator(t *testing.T) *ReleaseValidator {
	t.Helper()
	directory, err := filepath.Abs("../../docs/contracts/schemas")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewReleaseValidator(directory)
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func TestEmbeddedReleaseSchemasMatchRuntimeValidation(t *testing.T) {
	validator, err := NewReleaseValidatorFromJSON(contractschemas.EventReleaseEnvelope, contractschemas.ClientUpdateEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, trusted := signedReleaseJSON(t, []byte(validEventRelease), privateKey)
	if _, payload, metadata, validateErr := validator.ValidateEventEnvelope(raw, trusted); validateErr != nil || string(payload) != validEventRelease || metadata.EventID != "kellerlan-2026" {
		t.Fatalf("embedded event validation: %q %#v %v", payload, metadata, validateErr)
	}
}

func signedReleaseJSON(t *testing.T, payload []byte, privateKey ed25519.PrivateKey) ([]byte, map[string]ed25519.PublicKey) {
	t.Helper()
	envelope, err := SignEnvelope(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return raw, map[string]ed25519.PublicKey{KeyID(publicKey): publicKey}
}

func TestValidateSignedEventAndClientUpdateEnvelopes(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	validator := testReleaseValidator(t)
	raw, trusted := signedReleaseJSON(t, []byte(validEventRelease), privateKey)
	envelope, payload, metadata, err := validator.ValidateEventEnvelope(raw, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.KeyID == "" || string(payload) != validEventRelease || metadata.EventID != "kellerlan-2026" || metadata.Sequence != 4 || len(metadata.Artifacts) != 1 {
		t.Fatalf("event validation result: %#v %#v", envelope, metadata)
	}

	update := []byte(`{"formatVersion":1,"channel":"stable","sequence":2,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1234,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-07-18T12:00:00Z"}`)
	raw, trusted = signedReleaseJSON(t, update, privateKey)
	_, gotPayload, updateMetadata, err := validator.ValidateClientUpdateEnvelope(raw, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPayload) != string(update) || updateMetadata.Channel != "stable" || updateMetadata.Sequence != 2 || updateMetadata.Version != "0.2.0" {
		t.Fatalf("update validation result: %#v", updateMetadata)
	}
	if _, runtimePayload, runtimeMetadata, runtimeErr := ValidateClientUpdateEnvelopeRuntime(raw, trusted); runtimeErr != nil || string(runtimePayload) != string(update) || runtimeMetadata.Sequence != 2 {
		t.Fatalf("portable runtime validation: %#v %v", runtimeMetadata, runtimeErr)
	}
}

func TestRuntimeUpdateValidationRejectsUnknownAndDuplicateFields(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"formatVersion":1,"channel":"stable","sequence":2,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1234,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-07-18T12:00:00Z","unexpected":true}`)
	raw, trusted := signedReleaseJSON(t, payload, privateKey)
	if _, _, _, err = ValidateClientUpdateEnvelopeRuntime(raw, trusted); err == nil {
		t.Fatal("runtime validator accepted an unknown payload field")
	}
	duplicate := []byte(strings.Replace(string(raw), `"formatVersion":1`, `"formatVersion":1,"formatVersion":1`, 1))
	if _, _, _, err = ValidateClientUpdateEnvelopeRuntime(duplicate, trusted); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("runtime validator accepted duplicate envelope field: %v", err)
	}
}

func TestReleaseValidationRejectsAmbiguousOrUntrustedData(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	validator := testReleaseValidator(t)
	raw, trusted := signedReleaseJSON(t, []byte(validEventRelease), privateKey)
	if _, _, _, err = validator.ValidateEventEnvelope(raw, map[string]ed25519.PublicKey{}); err == nil {
		t.Fatal("untrusted release key accepted")
	}
	duplicate := []byte(strings.Replace(string(raw), `"formatVersion":1`, `"formatVersion":1,"formatVersion":1`, 1))
	if _, _, _, err = validator.ValidateEventEnvelope(duplicate, trusted); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate envelope field accepted: %v", err)
	}

	invalidWindow := []byte(strings.Replace(validEventRelease, `"validUntil":"2026-07-20T08:00:00Z"`, `"validUntil":"2026-07-15T08:00:00Z"`, 1))
	raw, trusted = signedReleaseJSON(t, invalidWindow, privateKey)
	if _, _, _, err = validator.ValidateEventEnvelope(raw, trusted); err == nil || !strings.Contains(err.Error(), "validity window") {
		t.Fatalf("invalid validity window accepted: %v", err)
	}
}
