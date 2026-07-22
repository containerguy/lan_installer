package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEventReleasePublicationIsAtomicMonotonicAndExplicitlyActivated(t *testing.T) {
	st, err := Open(t.TempDir() + "/releases.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.ExecContext(ctx, `INSERT INTO events(slug,name,status) VALUES('lan-2026','LAN 2026','draft')`); err != nil {
		t.Fatal(err)
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 8, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	release := EventReleaseRecord{EventID: "lan-2026", ReleaseID: "01K0LANREADY00000000000001", KeyID: "ed25519-aaaaaaaaaaaaaaaa", Sequence: 1, EnvelopeJSON: []byte(`{"signed":true}`), PayloadJSON: []byte(`{"payload":true}`), IssuedAt: now, ValidUntil: now.Add(24 * time.Hour), MinimumClientVersion: "0.1.0", Artifacts: []ReleaseArtifactReference{{Digest: digest, SizeBytes: 8, ContentType: "application/zip"}}}
	audit := func() *AuditEntry { return &AuditEntry{ActorUserID: 1, Action: "publish_event_release"} }

	missing := release
	missing.Artifacts = []ReleaseArtifactReference{{Digest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SizeBytes: 8, ContentType: "application/zip"}}
	if err = st.PublishEventRelease(ctx, missing, true, audit()); !errors.Is(err, ErrReleaseArtifact) {
		t.Fatalf("missing artifact: %v", err)
	}
	var status string
	if err = st.db.QueryRowContext(ctx, `SELECT status FROM events WHERE slug='lan-2026'`).Scan(&status); err != nil || status != "draft" {
		t.Fatalf("failed publication changed event: %q %v", status, err)
	}

	if err = st.PublishEventRelease(ctx, release, false, audit()); err != nil {
		t.Fatal(err)
	}
	var normalizedReferences int
	if err = st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_release_artifacts WHERE event_id=? AND sequence=? AND digest=?`, release.EventID, release.Sequence, digest).Scan(&normalizedReferences); err != nil || normalizedReferences != 1 {
		t.Fatalf("normalized release artifact reference: %d %v", normalizedReferences, err)
	}
	prepared := false
	removed, err := st.DeleteArtifactIfUnreferenced(ctx, digest, time.Now().UTC(), func() error { prepared = true; return nil }, &AuditEntry{ActorUserID: 1, Action: "garbage_collect_artifact"})
	if err != nil || removed || !prepared {
		t.Fatalf("release artifact considered collectable: removed=%v prepared=%v err=%v", removed, prepared, err)
	}
	if _, err = st.ActiveEventRelease(ctx); err == nil {
		t.Fatal("release was activated without explicit request")
	}
	if err = st.ActivateEventRelease(ctx, release.EventID, release.Sequence, &AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); err != nil {
		t.Fatalf("activate staged release: %v", err)
	}
	if err = st.PublishEventRelease(ctx, release, true, audit()); !errors.Is(err, ErrReleaseSequence) {
		t.Fatalf("reused sequence: %v", err)
	}
	release.Sequence = 2
	release.ReleaseID = "01K0LANREADY00000000000002"
	if err = st.PublishEventRelease(ctx, release, true, audit()); err != nil {
		t.Fatal(err)
	}
	if err = st.ActivateEventRelease(ctx, release.EventID, 1, &AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); !errors.Is(err, ErrReleaseNotLatest) {
		t.Fatalf("older sequence activated without new rollback sequence: %v", err)
	}
	active, err := st.ActiveEventRelease(ctx)
	if err != nil || active.EventID != "lan-2026" || active.Sequence != 2 {
		t.Fatalf("active release: %#v %v", active, err)
	}
	latest, err := st.EventRelease(ctx, "lan-2026")
	if err != nil || latest.Sequence != 2 {
		t.Fatalf("latest release: %#v %v", latest, err)
	}
}

func TestEventReleaseActivationRejectsExpiredAndArchivedEvents(t *testing.T) {
	st, err := Open(t.TempDir() + "/release-validity.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.ExecContext(ctx, `INSERT INTO events(slug,name,status) VALUES('expired','Expired','draft'),('archived','Archived','archived')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := func(eventID string) EventReleaseRecord {
		return EventReleaseRecord{EventID: eventID, ReleaseID: "01K0LANREADY00000000000090", KeyID: "ed25519-aaaaaaaaaaaaaaaa", Sequence: 1, EnvelopeJSON: []byte(`{"signed":true}`), PayloadJSON: []byte(`{}`), IssuedAt: now.Add(-2 * time.Hour), ValidUntil: now.Add(-time.Hour), MinimumClientVersion: "0.1.0"}
	}
	audit := func() *AuditEntry { return &AuditEntry{ActorUserID: 1, Action: "publish_event_release"} }
	if err = st.PublishEventRelease(ctx, record("expired"), false, audit()); err != nil {
		t.Fatal(err)
	}
	if err = st.ActivateEventRelease(ctx, "expired", 1, &AuditEntry{ActorUserID: 1, Action: "activate_event_release"}); !errors.Is(err, ErrReleaseValidity) {
		t.Fatalf("expired release activation: %v", err)
	}
	if err = st.PublishEventRelease(ctx, record("archived"), false, audit()); !errors.Is(err, ErrReleaseEventArchived) {
		t.Fatalf("archived event publication: %v", err)
	}
}

func TestClientUpdateReleaseRequiresKnownArtifactAndMonotonicSequence(t *testing.T) {
	st, err := Open(t.TempDir() + "/updates.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	digest := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/vnd.microsoft.portable-executable"}); err != nil {
		t.Fatal(err)
	}
	release := ClientUpdateReleaseRecord{Channel: "stable", Sequence: 1, Version: "0.2.0", MinimumVersion: "0.1.0", ArtifactDigest: digest, SizeBytes: 42, KeyID: "ed25519-aaaaaaaaaaaaaaaa", EnvelopeJSON: []byte(`{"signed":true}`), PayloadJSON: []byte(`{"payload":true}`), PublishedAt: time.Now().UTC()}
	audit := func() *AuditEntry { return &AuditEntry{ActorUserID: 1, Action: "publish_client_update"} }
	if err = st.PublishClientUpdateRelease(ctx, release, audit()); err != nil {
		t.Fatal(err)
	}
	if err = st.PublishClientUpdateRelease(ctx, release, audit()); !errors.Is(err, ErrReleaseSequence) {
		t.Fatalf("reused update sequence: %v", err)
	}
	release.Sequence = 2
	release.Version = "0.3.0"
	release.MinimumVersion = "0.0.9"
	if err = st.PublishClientUpdateRelease(ctx, release, audit()); !errors.Is(err, ErrReleaseVersion) {
		t.Fatalf("minimum version floor moved backwards: %v", err)
	}
	release.MinimumVersion = "0.2.0"
	if err = st.PublishClientUpdateRelease(ctx, release, audit()); err != nil {
		t.Fatalf("valid monotonic update: %v", err)
	}
	latest, err := st.LatestClientUpdateRelease(ctx, "stable")
	if err != nil || latest.Sequence != 2 || latest.Version != "0.3.0" || latest.MinimumVersion != "0.2.0" {
		t.Fatalf("latest update: %#v %v", latest, err)
	}
}
