package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestArtifactMetadataIsImmutableForDigest(t *testing.T) {
	st, err := Open(t.TempDir() + "/artifacts.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip"}); err != nil {
		t.Fatalf("idempotent registration: %v", err)
	}
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 43, ContentType: "application/zip"}); err == nil {
		t.Fatal("conflicting metadata was accepted")
	}
	if _, err = st.Artifact(ctx, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid digest lookup: %v", err)
	}
}

func TestArtifactGCCandidatesDoNotStarveBehindReferences(t *testing.T) {
	st, err := Open(t.TempDir() + "/gc-candidates.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	created := time.Now().UTC().Add(-48 * time.Hour)
	referenced := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	unreferenced := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, digest := range []string{referenced, unreferenced} {
		if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip", CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	source, err := st.SaveSourceAtomic(ctx, Source{Name: "GC Source", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, err := st.Launchers(ctx)
	if err != nil || len(launchers) == 0 {
		t.Fatal(err)
	}
	gameID, err := st.SaveGameAtomic(ctx, Game{Slug: "gc-game", Name: "GC Game", LauncherID: launchers[0].ID, ExternalGameID: "gc-game", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "game.zip", SHA256: referenced, SizeBytes: 42, Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	candidates, err := st.ArtifactGCCandidates(ctx, time.Now().UTC(), 1)
	if err != nil || len(candidates) != 1 || candidates[0].Digest != unreferenced {
		t.Fatalf("candidates: %#v %v", candidates, err)
	}
}

func TestArtifactGCHoldsNoDatabaseConnectionDuringPrepare(t *testing.T) {
	st, err := Open(t.TempDir() + "/gc-prepare.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	user, err := st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	digest := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip", CreatedAt: time.Now().UTC().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, deleteErr := st.DeleteArtifactIfUnreferenced(ctx, digest, time.Now().UTC(), func() error {
			close(started)
			<-release
			return nil
		}, &AuditEntry{ActorUserID: user.ID, Action: "artifact_gc"})
		result <- deleteErr
	}()
	<-started
	usageContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err = st.ArtifactUsage(usageContext); err != nil {
		close(release)
		t.Fatalf("prepare held the only database connection: %v", err)
	}
	close(release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}

func TestArtifactGCRechecksReferenceCreatedDuringPrepare(t *testing.T) {
	st, err := Open(t.TempDir() + "/gc-reference-race.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	user, err := st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	source, err := st.SaveSourceAtomic(ctx, Source{Name: "Race Source", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, err := st.Launchers(ctx)
	if err != nil || len(launchers) == 0 {
		t.Fatalf("launchers: %#v %v", launchers, err)
	}
	gameID, err := st.SaveGameAtomic(ctx, Game{Slug: "gc-race", Name: "GC Race", LauncherID: launchers[0].ID, ExternalGameID: "gc-race", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip", CreatedAt: time.Now().UTC().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	removed, err := st.DeleteArtifactIfUnreferenced(ctx, digest, time.Now().UTC(), func() error {
		_, saveErr := st.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "game.zip", SHA256: digest, SizeBytes: 42, Enabled: true}, nil)
		return saveErr
	}, &AuditEntry{ActorUserID: user.ID, Action: "artifact_gc"})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("artifact was removed after becoming referenced")
	}
	if _, err = st.Artifact(ctx, digest); err != nil {
		t.Fatalf("referenced artifact metadata was removed: %v", err)
	}
}

func TestArtifactGCBlockedByRunningUnknownDigestJob(t *testing.T) {
	st, _, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	user, err := st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.EnqueueCacheJob(ctx, "game_version", versionID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextCacheJob(ctx); err != nil {
		t.Fatal(err)
	}
	digest := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip", CreatedAt: time.Now().UTC().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	candidates, err := st.ArtifactGCCandidates(ctx, time.Now().UTC(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("unknown-digest running job did not block GC: %#v", candidates)
	}
}

func TestArtifactGCRechecksRenewedGraceAfterPrepare(t *testing.T) {
	st, err := Open(t.TempDir() + "/gc-grace-race.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	user, err := st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	digest := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	removed, err := st.DeleteArtifactIfUnreferenced(ctx, digest, cutoff, func() error {
		return st.RenewArtifactGCGrace(ctx, digest, time.Now().UTC())
	}, &AuditEntry{ActorUserID: user.ID, Action: "artifact_gc"})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("artifact was removed after its GC grace was renewed")
	}
	if _, err = st.Artifact(ctx, digest); err != nil {
		t.Fatalf("artifact with renewed grace was removed: %v", err)
	}
	if err = st.RenewArtifactGCGrace(ctx, digest, old); err != nil {
		t.Fatal(err)
	}
	candidates, err := st.ArtifactGCCandidates(ctx, cutoff, 100)
	if err != nil || len(candidates) != 1 || candidates[0].Digest != digest {
		t.Fatalf("expired grace did not become collectable: %#v %v", candidates, err)
	}
}
