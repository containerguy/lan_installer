package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/store"
)

type acceptingArtifactVerifier struct{}

func (acceptingArtifactVerifier) Verify(context.Context, string, int64, string) error { return nil }

type testEventSigner struct{ key ed25519.PrivateKey }

func (s testEventSigner) SignEvent(_ context.Context, payload []byte) ([]byte, error) {
	envelope, err := protocol.SignEnvelope(payload, s.key)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

type controlledArtifactVerifier struct {
	calls  int
	reject bool
}

func (v *controlledArtifactVerifier) Verify(context.Context, string, int64, string) error {
	v.calls++
	if v.reject {
		return errors.New("blob mismatch")
	}
	return nil
}

func TestServicePublishesOnlyTrustedValidatedRelease(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/release.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveEventAtomic(ctx, store.Event{Slug: "kellerlan-2026", Name: "Keller-LAN 2026", Status: "draft"}, nil); err != nil {
		t.Fatal(err)
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = st.RegisterArtifact(ctx, store.Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/vnd.microsoft.portable-executable"}); err != nil {
		t.Fatal(err)
	}
	schemaDir, err := filepath.Abs("../../docs/contracts/schemas")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewReleaseValidator(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	updatePublicKey, updatePrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := &controlledArtifactVerifier{}
	service, err := NewWithPurposeKeyrings(st, validator, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey, protocol.KeyID(updatePublicKey): updatePublicKey}, map[string]ed25519.PublicKey{protocol.KeyID(updatePublicKey): updatePublicKey}, artifacts, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SetEventSigner(testEventSigner{key: privateKey}, protocol.KeyID(publicKey)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload := []byte(strings.NewReplacer("2026-07-18T08:00:00Z", now.Add(-time.Hour).Format(time.RFC3339), "2026-07-20T08:00:00Z", now.Add(48*time.Hour).Format(time.RFC3339)).Replace(`{"formatVersion":2,"eventId":"kellerlan-2026","releaseId":"01K0LANREADY00000000000002","sequence":1,"issuedAt":"2026-07-18T08:00:00Z","validUntil":"2026-07-20T08:00:00Z","minimumClientVersion":"0.2.0","artifacts":[],"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"detect"}]}],"games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","externalGameId":"730","version":"1","required":true,"payloads":[]}]}`))
	updatePayload := []byte(`{"formatVersion":1,"channel":"stable","sequence":1,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-07-20T12:00:00Z"}`)
	updateEnvelope, err := protocol.SignEnvelope(updatePayload, updatePrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	updateRaw, _ := json.Marshal(updateEnvelope)
	if _, err = service.PublishClientUpdate(ctx, updateRaw, &store.AuditEntry{ActorUserID: 1, Action: "publish_client_update_release"}); err != nil {
		t.Fatalf("publish compatible update: %v", err)
	}
	unsupportedPayload := []byte(strings.NewReplacer("ISSUED", now.Add(-time.Hour).Format(time.RFC3339), "VALID", now.Add(48*time.Hour).Format(time.RFC3339)).Replace(`{"formatVersion":2,"eventId":"kellerlan-2026","releaseId":"01K0LANREADY00000000000999","sequence":1,"issuedAt":"ISSUED","validUntil":"VALID","minimumClientVersion":"0.2.0","artifacts":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42,"mediaType":"application/zip","fileName":"game.zip"}],"launchers":[],"games":[{"gameId":"standalone-game","name":"Standalone Game","launcherId":"standalone","externalGameId":"standalone-game","version":"1","required":true,"payloads":[{"type":"archive","artifacts":["sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetRoot":"user_games","relativePath":"Standalone Game"}]}]}]}`))
	unsupportedEnvelope, err := protocol.SignEnvelope(unsupportedPayload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	unsupportedRaw, _ := json.Marshal(unsupportedEnvelope)
	if _, err = service.PublishEvent(ctx, unsupportedRaw, false, &store.AuditEntry{ActorUserID: 1, Action: "publish_event_release"}); !errors.Is(err, ErrEventActionsUnsupported) {
		t.Fatalf("package event bypassed central client capability policy: %v", err)
	}
	envelope, err := protocol.SignEnvelope(payload, updatePrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(envelope, "", "  ")
	metadata, err := service.PublishEvent(ctx, raw, false, &store.AuditEntry{ActorUserID: 1, Action: "publish_event_release"})
	if err != nil || metadata.Sequence != 1 {
		t.Fatalf("publish: %#v %v", metadata, err)
	}
	// An outdated client keeps the client-update path live, so activation has to
	// reverify that update's CAS bytes before forcing anyone to upgrade.
	if err = st.CreateDevice(ctx, store.Device{ID: "old-pc", Name: "Alter PC", PublicKey: make([]byte, ed25519.PublicKeySize), WindowsVersion: "11", ClientVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	artifacts.reject = true
	if _, err = service.ActivateEvent(ctx, metadata.EventID, metadata.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err == nil {
		t.Fatalf("activation accepted corrupt CAS artifact: %v", err)
	}
	if artifacts.calls != 2 {
		t.Fatalf("CAS was not reverified at activation: %d calls", artifacts.calls)
	}
	artifacts.reject = false
	if err = st.UpdateActiveDeviceClientVersion(ctx, "old-pc", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ActivateEvent(ctx, metadata.EventID, metadata.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err != nil {
		t.Fatalf("activate staged release: %v", err)
	}
	active, err := st.ActiveEventRelease(ctx)
	if err != nil || active.EventID != "kellerlan-2026" || active.Sequence != 1 || len(active.EnvelopeJSON) == 0 {
		t.Fatalf("active: %#v %v", active, err)
	}
	var nextPayload map[string]any
	if err = json.Unmarshal(payload, &nextPayload); err != nil {
		t.Fatal(err)
	}
	nextPayload["releaseId"] = "01K0LANREADY00000000000003"
	nextPayload["sequence"] = float64(2)
	nextRaw, _ := json.Marshal(nextPayload)
	nextEnvelope, err := protocol.SignEnvelope(nextRaw, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	nextSigned, _ := json.Marshal(nextEnvelope)
	if _, err = service.PublishEvent(ctx, nextSigned, false, &store.AuditEntry{ActorUserID: 1, Action: "publish_event_release"}); err != nil {
		t.Fatalf("publish next staged release: %v", err)
	}
	candidateMetadata, err := service.GenerateAndPublishRollback(ctx, metadata.EventID, 1, time.Now().UTC().Add(48*time.Hour), false, &store.AuditEntry{ActorUserID: 1, Action: "generate_publish_event_rollback"})
	if err != nil || candidateMetadata.Sequence != 3 || candidateMetadata.ReleaseID == metadata.ReleaseID || candidateMetadata.MinimumClientVersion != "0.2.0" {
		t.Fatalf("legacy rollback: metadata=%#v err=%v", candidateMetadata, err)
	}
	rollback, err := st.EventReleaseSequence(ctx, metadata.EventID, 3)
	if err != nil {
		t.Fatal(err)
	}
	rollbackEnvelope, _, _, err := validator.ValidateEventEnvelope(rollback.EnvelopeJSON, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey})
	if err != nil || rollbackEnvelope.KeyID != protocol.KeyID(publicKey) {
		t.Fatalf("legacy rollback was not re-signed with online event key: key=%s err=%v", rollbackEnvelope.KeyID, err)
	}
}

func TestGenerateAndPublishProviderManagedEvent(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/generated.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	launchers, err := st.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var steamID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "steam" {
			steamID = launcher.ID
		}
	}
	gameID, err := st.SaveGameAtomic(ctx, store.Game{Slug: "cs2", Name: "Counter-Strike 2", LauncherID: steamID, ExternalGameID: "730", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(ctx, store.GameVersion{GameID: gameID, Version: "latest", SourceID: 0, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.SaveEventAtomic(ctx, store.Event{Slug: "lan-2026", Name: "LAN 2026", Status: "draft"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveEventGameAtomic(ctx, store.EventGame{EventID: eventID, GameVersionID: versionID, Required: true}, nil); err != nil {
		t.Fatal(err)
	}
	schemaDir, err := filepath.Abs("../../docs/contracts/schemas")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewReleaseValidator(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	updatePublicKey, updatePrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithPurposeKeyrings(st, validator, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}, map[string]ed25519.PublicKey{protocol.KeyID(updatePublicKey): updatePublicKey}, acceptingArtifactVerifier{}, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SetEventSigner(testEventSigner{key: privateKey}, protocol.KeyID(publicKey)); err != nil {
		t.Fatal(err)
	}
	result, err := service.GenerateAndPublishEvent(ctx, eventID, "0.2.0", time.Now().UTC().Add(24*time.Hour), false, &store.AuditEntry{ActorUserID: 1, Action: "generate_publish_event_release"})
	if err != nil || result.EventID != "lan-2026" || result.Sequence != 1 || result.GameCount != 1 || result.LauncherCount != 1 {
		t.Fatalf("generated release: %#v err=%v", result, err)
	}
	if err = st.CreateDevice(ctx, store.Device{ID: "old-pc", Name: "Alter PC", PublicKey: make([]byte, ed25519.PublicKeySize), WindowsVersion: "11", ClientVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ActivateEvent(ctx, result.EventID, result.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err == nil {
		t.Fatal("activation accepted a missing compatible stable update")
	} else {
		var updateCompatibility *CompatibleClientUpdateError
		if !errors.As(err, &updateCompatibility) {
			t.Fatalf("unexpected update compatibility error: %v", err)
		}
	}
	updateDigest := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err = st.RegisterArtifact(ctx, store.Artifact{Digest: updateDigest, SizeBytes: 42, ContentType: "application/vnd.microsoft.portable-executable"}); err != nil {
		t.Fatal(err)
	}
	updatePayload := []byte(`{"formatVersion":1,"channel":"stable","sequence":1,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":42,"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","publishedAt":"2026-07-20T12:00:00Z"}`)
	updateEnvelope, err := protocol.SignEnvelope(updatePayload, updatePrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	updateRaw, _ := json.Marshal(updateEnvelope)
	if _, err = service.PublishClientUpdate(ctx, updateRaw, &store.AuditEntry{ActorUserID: 1, Action: "publish_client_update_release"}); err != nil {
		t.Fatalf("publish compatible update: %v", err)
	}
	if _, err = service.ActivateEvent(ctx, result.EventID, result.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err == nil {
		t.Fatal("activation accepted an incompatible active client")
	} else {
		var compatibility *ClientCompatibilityError
		if !errors.As(err, &compatibility) || len(compatibility.Devices) != 1 {
			t.Fatalf("unexpected compatibility error: %v", err)
		}
	}
	if err = st.UpdateActiveDeviceClientVersion(ctx, "old-pc", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ActivateEvent(ctx, result.EventID, result.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err != nil {
		t.Fatalf("compatible activation failed: %v", err)
	}
	active, err := st.ActiveEventRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, metadata, err := validator.ValidateEventEnvelope(active.EnvelopeJSON, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey})
	if err != nil || metadata.EventID != "lan-2026" || len(metadata.Artifacts) != 0 || !strings.Contains(string(payload), `"payloads":[]`) || !strings.Contains(string(payload), `"operation":"detect"`) {
		t.Fatalf("provider release payload=%s metadata=%#v err=%v", payload, metadata, err)
	}
}

// Manually distributed clients never publish a self-update artifact. Once every
// active device already reports the minimum version there is nobody left to
// update, so activation must not keep demanding one.
func TestActivateWithoutStableUpdateWhenAllClientsAreCurrent(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/manual.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	launchers, err := st.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var steamID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "steam" {
			steamID = launcher.ID
		}
	}
	gameID, err := st.SaveGameAtomic(ctx, store.Game{Slug: "cs2", Name: "Counter-Strike 2", LauncherID: steamID, ExternalGameID: "730", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(ctx, store.GameVersion{GameID: gameID, Version: "latest", SourceID: 0, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := st.SaveEventAtomic(ctx, store.Event{Slug: "lan-2026", Name: "LAN 2026", Status: "draft"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveEventGameAtomic(ctx, store.EventGame{EventID: eventID, GameVersionID: versionID, Required: true}, nil); err != nil {
		t.Fatal(err)
	}
	schemaDir, err := filepath.Abs("../../docs/contracts/schemas")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewReleaseValidator(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	updatePublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithPurposeKeyrings(st, validator, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}, map[string]ed25519.PublicKey{protocol.KeyID(updatePublicKey): updatePublicKey}, acceptingArtifactVerifier{}, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SetEventSigner(testEventSigner{key: privateKey}, protocol.KeyID(publicKey)); err != nil {
		t.Fatal(err)
	}
	result, err := service.GenerateAndPublishEvent(ctx, eventID, "0.2.0", time.Now().UTC().Add(24*time.Hour), false, &store.AuditEntry{ActorUserID: 1, Action: "generate_publish_event_release"})
	if err != nil {
		t.Fatalf("generate release: %v", err)
	}
	// A device below the minimum version still blocks: it would be stranded
	// without any way to upgrade.
	if err = st.CreateDevice(ctx, store.Device{ID: "old-pc", Name: "Alter PC", PublicKey: make([]byte, ed25519.PublicKeySize), WindowsVersion: "11", ClientVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ActivateEvent(ctx, result.EventID, result.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err == nil {
		t.Fatal("activation accepted an outdated client without any stable update")
	} else {
		var updateCompatibility *CompatibleClientUpdateError
		if !errors.As(err, &updateCompatibility) {
			t.Fatalf("unexpected error for outdated client: %v", err)
		}
	}
	// Once that device is manually upgraded, activation succeeds even though no
	// client update release was ever published.
	if err = st.UpdateActiveDeviceClientVersion(ctx, "old-pc", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ActivateEvent(ctx, result.EventID, result.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err != nil {
		t.Fatalf("activation with current clients but no stable update failed: %v", err)
	}
	active, err := st.ActiveEventRelease(ctx)
	if err != nil || active.Sequence != result.Sequence {
		t.Fatalf("release was not activated: seq=%d err=%v", active.Sequence, err)
	}
}
