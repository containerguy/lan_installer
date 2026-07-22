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
	// Only archive extraction below the user games root is executable today.
	// Every other action shape must stay out of a signed release, whatever the
	// signer was willing to sign.
	prefixedDigest := "sha256:" + digest
	for name, action := range map[string]string{
		"materialize_tree":      `{"adapter":"lanready_tree","operation":"materialize_tree","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"game"}`,
		"import_library":        `{"adapter":"steam","operation":"import_library","artifactDigest":"DIGEST","targetRoot":"steam_library","relativePath":"game"}`,
		"extract_outside_games": `{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"steam_library","relativePath":"game"}`,
		"extract_to_temp":       `{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"temp","relativePath":"game"}`,
	} {
		body := strings.NewReplacer("ISSUED", now.Add(-time.Hour).Format(time.RFC3339), "VALID", now.Add(48*time.Hour).Format(time.RFC3339), "ACTION", strings.ReplaceAll(action, "DIGEST", prefixedDigest), "DIGEST", prefixedDigest).Replace(`{"formatVersion":2,"eventId":"kellerlan-2026","releaseId":"01K0LANREADY00000000000999","sequence":1,"issuedAt":"ISSUED","validUntil":"VALID","minimumClientVersion":"0.2.0","artifacts":[{"digest":"DIGEST","size":42,"mediaType":"application/zip","fileName":"game.zip"}],"launchers":[],"games":[{"gameId":"standalone-game","name":"Standalone Game","launcherId":"standalone","externalGameId":"standalone-game","version":"1","required":true,"payloads":[{"type":"archive","artifacts":["DIGEST"],"actions":[ACTION]}]}]}`)
		unsupportedEnvelope, signErr := protocol.SignEnvelope([]byte(body), privateKey)
		if signErr != nil {
			t.Fatalf("%s: %v", name, signErr)
		}
		unsupportedRaw, _ := json.Marshal(unsupportedEnvelope)
		// Rejection may come from the schema/semantics layer or from the
		// capability gate depending on the shape; what matters is that no such
		// action can ever reach a stored, signed release.
		if _, err = service.PublishEvent(ctx, unsupportedRaw, false, &store.AuditEntry{ActorUserID: 1, Action: "publish_event_release"}); err == nil {
			t.Fatalf("%s bypassed central client capability policy", name)
		}
	}
	// A launcher-managed game must never carry a LANReady payload.
	launcherPayloadBody := strings.NewReplacer("ISSUED", now.Add(-time.Hour).Format(time.RFC3339), "VALID", now.Add(48*time.Hour).Format(time.RFC3339), "DIGEST", prefixedDigest).Replace(`{"formatVersion":2,"eventId":"kellerlan-2026","releaseId":"01K0LANREADY00000000000998","sequence":1,"issuedAt":"ISSUED","validUntil":"VALID","minimumClientVersion":"0.2.0","artifacts":[{"digest":"DIGEST","size":42,"mediaType":"application/zip","fileName":"game.zip"}],"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"detect"}]}],"games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","externalGameId":"730","version":"1","required":true,"payloads":[{"type":"archive","artifacts":["DIGEST"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"cs2"}]}]}]}`)
	launcherPayloadEnvelope, err := protocol.SignEnvelope([]byte(launcherPayloadBody), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	launcherPayloadRaw, _ := json.Marshal(launcherPayloadEnvelope)
	if _, err = service.PublishEvent(ctx, launcherPayloadRaw, false, &store.AuditEntry{ActorUserID: 1, Action: "publish_event_release"}); err == nil {
		t.Fatal("launcher game with payload bypassed capability policy")
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

// The standalone install path is the first one where a signed release makes the
// client execute something, so prove the generated payload is exactly the
// narrow shape the capability gate admits.
func TestGenerateAndPublishStandaloneArchiveEvent(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/standalone.db")
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
	var standaloneID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "standalone" {
			standaloneID = launcher.ID
		}
	}
	if standaloneID == 0 {
		t.Fatal("standalone launcher missing")
	}
	source, err := st.SaveSourceAtomic(ctx, store.Source{Name: "Pakete", Kind: "https", BaseURL: "https://packages.example.test", Enabled: true}, 0, func(int64, *store.WebDAVConfig) (*store.WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	sourceID := source.ID
	digest := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err = st.RegisterArtifact(ctx, store.Artifact{Digest: digest, SizeBytes: 4096, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	gameID, err := st.SaveGameAtomic(ctx, store.Game{Slug: "flatout2", Name: "FlatOut 2", LauncherID: standaloneID, ExternalGameID: "flatout2", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(ctx, store.GameVersion{GameID: gameID, Version: "V1", SourceID: sourceID, SourcePath: "sub/dir/FlatOut2.zip", SHA256: digest, SizeBytes: 4096, Enabled: true}, nil)
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
	preflight, err := service.PreflightEvent(ctx, eventID)
	if err != nil || !preflight.Ready {
		t.Fatalf("cached standalone package is not publishable: %#v err=%v", preflight.Issues, err)
	}
	result, err := service.GenerateAndPublishEvent(ctx, eventID, "0.2.0", time.Now().UTC().Add(24*time.Hour), false, &store.AuditEntry{ActorUserID: 1, Action: "generate_publish_event_release"})
	if err != nil {
		t.Fatalf("generate standalone release: %v", err)
	}
	stored, err := st.EventReleaseSequence(ctx, result.EventID, result.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, metadata, err := validator.ValidateEventEnvelope(stored.EnvelopeJSON, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey})
	if err != nil {
		t.Fatalf("stored standalone release is invalid: %v", err)
	}
	if len(metadata.Artifacts) != 1 || metadata.Artifacts[0].Digest != "sha256:"+digest || metadata.Artifacts[0].Size != 4096 {
		t.Fatalf("artifact metadata: %#v", metadata.Artifacts)
	}
	var decoded struct {
		Launchers []any `json:"launchers"`
		Artifacts []struct {
			FileName  string `json:"fileName"`
			MediaType string `json:"mediaType"`
		} `json:"artifacts"`
		Games []struct {
			LauncherID string `json:"launcherId"`
			Payloads   []struct {
				Type    string `json:"type"`
				Actions []struct {
					Adapter      string `json:"adapter"`
					Operation    string `json:"operation"`
					TargetRoot   string `json:"targetRoot"`
					RelativePath string `json:"relativePath"`
				} `json:"actions"`
			} `json:"payloads"`
		} `json:"games"`
	}
	if err = json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	// A standalone game needs no launcher entry: there is nothing to detect.
	if len(decoded.Launchers) != 0 {
		t.Fatalf("standalone release declared launchers: %s", payload)
	}
	// The source path is reduced to a bare file name; separators would violate
	// the release schema and could escape the extraction directory.
	if decoded.Artifacts[0].FileName != "FlatOut2.zip" || decoded.Artifacts[0].MediaType != "application/zip" {
		t.Fatalf("artifact file name: %#v", decoded.Artifacts[0])
	}
	if len(decoded.Games) != 1 || decoded.Games[0].LauncherID != "standalone" || len(decoded.Games[0].Payloads) != 1 {
		t.Fatalf("games: %s", payload)
	}
	archive := decoded.Games[0].Payloads[0]
	if archive.Type != "archive" || len(archive.Actions) != 1 {
		t.Fatalf("payload: %#v", archive)
	}
	action := archive.Actions[0]
	if action.Adapter != "lanready_archive" || action.Operation != "extract_archive" || action.TargetRoot != "user_games" || action.RelativePath != "flatout2" {
		t.Fatalf("action: %#v", action)
	}
}
