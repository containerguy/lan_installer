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
	if err = st.RegisterArtifact(ctx, store.Artifact{Digest: digest, SizeBytes: 1024, ContentType: "application/octet-stream"}); err != nil {
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
	artifacts := &controlledArtifactVerifier{}
	service, err := New(st, validator, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload := []byte(strings.NewReplacer("2026-07-18T08:00:00Z", now.Add(-time.Hour).Format(time.RFC3339), "2026-07-20T08:00:00Z", now.Add(48*time.Hour).Format(time.RFC3339)).Replace(`{"formatVersion":2,"eventId":"kellerlan-2026","releaseId":"01K0LANREADY00000000000002","sequence":1,"issuedAt":"2026-07-18T08:00:00Z","validUntil":"2026-07-20T08:00:00Z","minimumClientVersion":"0.1.0","artifacts":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1024,"mediaType":"application/octet-stream","fileName":"payload.zip"}],"launchers":[{"launcherId":"steam","version":"1.0","required":true,"actions":[{"adapter":"steam","operation":"install_launcher","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}],"games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","version":"1","required":true,"payloads":[{"type":"archive","artifacts":["sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetRoot":"steam_library","relativePath":"steamapps/common/cs2"}]}]}]}`))
	envelope, err := protocol.SignEnvelope(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(envelope, "", "  ")
	metadata, err := service.PublishEvent(ctx, raw, false, &store.AuditEntry{ActorUserID: 1, Action: "publish_event_release"})
	if err != nil || metadata.Sequence != 1 {
		t.Fatalf("publish: %#v %v", metadata, err)
	}
	artifacts.reject = true
	if _, err = service.ActivateEvent(ctx, metadata.EventID, metadata.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); !errors.Is(err, store.ErrReleaseArtifact) {
		t.Fatalf("activation accepted corrupt CAS artifact: %v", err)
	}
	artifacts.reject = false
	if _, err = service.ActivateEvent(ctx, metadata.EventID, metadata.Sequence, &store.AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err != nil {
		t.Fatalf("activate staged release: %v", err)
	}
	if artifacts.calls != 3 {
		t.Fatalf("CAS was not reverified at activation: %d calls", artifacts.calls)
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
	candidate, candidateMetadata, err := service.BuildRollbackCandidate(ctx, metadata.EventID, 1, time.Now().UTC().Add(48*time.Hour))
	if err != nil || candidateMetadata.Sequence != 3 || candidateMetadata.ReleaseID == metadata.ReleaseID || !json.Valid(candidate) {
		t.Fatalf("rollback candidate: metadata=%#v valid=%v err=%v", candidateMetadata, json.Valid(candidate), err)
	}
}
