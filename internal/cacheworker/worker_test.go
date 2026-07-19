package cacheworker

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/sourceprobe"
	"github.com/containerguy/lan_installer/internal/store"
)

type fakeFetcher struct {
	content     []byte
	contentType string
	input       sourceprobe.Input
	path        string
	calls       int
}

type blockingFetcher struct{ started chan struct{} }

func (f *blockingFetcher) Fetch(ctx context.Context, _ sourceprobe.Input, _ string, _ int64, _ func(sourceprobe.FetchResult, io.Reader) error) (sourceprobe.FetchResult, error) {
	close(f.started)
	<-ctx.Done()
	return sourceprobe.FetchResult{}, ctx.Err()
}

func (f *fakeFetcher) Fetch(_ context.Context, input sourceprobe.Input, relativePath string, maxBytes int64, consume func(sourceprobe.FetchResult, io.Reader) error) (sourceprobe.FetchResult, error) {
	f.calls++
	f.input, f.path = input, relativePath
	result := sourceprobe.FetchResult{ContentType: f.contentType, ContentLength: int64(len(f.content)), FinalURL: "https://example.test/" + relativePath}
	if int64(len(f.content)) > maxBytes {
		return sourceprobe.FetchResult{}, artifact.ErrTooLarge
	}
	if err := consume(result, bytes.NewReader(f.content)); err != nil {
		return sourceprobe.FetchResult{}, err
	}
	return result, nil
}

func workerFixture(t *testing.T, sha string, size int64) (*store.Store, *artifact.Store, *secretbox.Box, int64, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.BootstrapAdmin(context.Background(), "admin", "hash"); err != nil {
		st.Close()
		t.Fatal(err)
	}
	user, err := st.UserByUsername(context.Background(), "admin")
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	vault, _ := secretbox.New(make([]byte, 32))
	source, err := st.SaveSourceAtomic(context.Background(), store.Source{Name: "Worker Source", Kind: "webdav", BaseURL: "https://example.test/files", Enabled: true}, 0, func(sourceID int64, _ *store.WebDAVConfig) (*store.WebDAVConfig, error) {
		nonce, ciphertext, sealErr := vault.Seal([]byte("app-password"), secretbox.WebDAVAAD(sourceID))
		return &store.WebDAVConfig{AuthType: "basic", Username: "source-user", SecretNonce: nonce, SecretCiphertext: ciphertext}, sealErr
	})
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	launchers, _ := st.Launchers(context.Background())
	gameID, err := st.SaveGameAtomic(context.Background(), store.Game{Slug: "worker-game", Name: "Worker Game", LauncherID: launchers[0].ID, Enabled: true}, nil)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(context.Background(), store.GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "games/payload.zip", SHA256: sha, SizeBytes: size, Enabled: true}, nil)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	artifacts, err := artifact.New(filepath.Join(t.TempDir(), "artifacts"), st)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, artifacts, vault, user.ID, versionID
}

func TestWorkerFetchesDerivesAndCompletesArtifact(t *testing.T) {
	st, artifacts, vault, userID, versionID := workerFixture(t, "", 0)
	defer st.Close()
	fetcher := &fakeFetcher{content: []byte("game archive bytes"), contentType: "application/zip"}
	worker, err := New(st, artifacts, fetcher, vault, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.EnqueueCacheJob(context.Background(), "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !worker.processNext(context.Background()) {
		t.Fatal("worker did not process queued job")
	}
	completed, err := st.CacheJob(context.Background(), job.ID)
	if err != nil || completed.Status != "succeeded" || completed.ArtifactDigest == "" || completed.ArtifactSize != int64(len(fetcher.content)) {
		t.Fatalf("completed job: %#v %v", completed, err)
	}
	if fetcher.calls != 1 || fetcher.path != "games/payload.zip" || fetcher.input.Username != "source-user" || fetcher.input.Password != "app-password" {
		t.Fatalf("fetch input: %#v path=%q calls=%d", fetcher.input, fetcher.path, fetcher.calls)
	}
	if err = artifacts.Verify(context.Background(), completed.ArtifactDigest, completed.ArtifactSize, "application/zip"); err != nil {
		t.Fatal(err)
	}
	second, err := st.EnqueueCacheJob(context.Background(), "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	worker.processNext(context.Background())
	reused, err := st.CacheJob(context.Background(), second.ID)
	if err != nil || reused.Status != "succeeded" || fetcher.calls != 1 {
		t.Fatalf("existing CAS artifact was downloaded again: %#v calls=%d err=%v", reused, fetcher.calls, err)
	}
}

func TestWorkerFailsClosedOnDigestMismatch(t *testing.T) {
	st, artifacts, vault, userID, versionID := workerFixture(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 7)
	defer st.Close()
	fetcher := &fakeFetcher{content: []byte("payload"), contentType: "application/zip"}
	worker, _ := New(st, artifacts, fetcher, vault, 1<<20)
	job, err := st.EnqueueCacheJob(context.Background(), "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	worker.processNext(context.Background())
	failed, err := st.CacheJob(context.Background(), job.ID)
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "artifact_digest_mismatch" || failed.ArtifactDigest != "" {
		t.Fatalf("digest mismatch job: %#v %v", failed, err)
	}
	usage, err := st.ArtifactUsage(context.Background())
	if err != nil || usage != 0 {
		t.Fatalf("mismatched artifact persisted: usage=%d err=%v", usage, err)
	}
}

func TestWorkerEnforcesQuotaBeforeFetching(t *testing.T) {
	st, artifacts, vault, userID, versionID := workerFixture(t, "", 1024)
	defer st.Close()
	fetcher := &fakeFetcher{content: []byte("payload"), contentType: "application/zip"}
	worker, _ := New(st, artifacts, fetcher, vault, 512)
	job, err := st.EnqueueCacheJob(context.Background(), "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	worker.processNext(context.Background())
	failed, err := st.CacheJob(context.Background(), job.ID)
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "cache_quota_exceeded" || fetcher.calls != 0 {
		t.Fatalf("quota job: %#v calls=%d err=%v", failed, fetcher.calls, err)
	}
}

func TestWorkerCancellationInterruptsBlockedFetch(t *testing.T) {
	st, artifacts, vault, userID, versionID := workerFixture(t, "", 0)
	defer st.Close()
	fetcher := &blockingFetcher{started: make(chan struct{})}
	worker, _ := New(st, artifacts, fetcher, vault, 1<<20)
	job, err := st.EnqueueCacheJob(context.Background(), "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		worker.processNext(context.Background())
		close(done)
	}()
	<-fetcher.started
	if _, err = st.RequestCacheJobCancel(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked fetch did not react to cancellation")
	}
	cancelled, err := st.CacheJob(context.Background(), job.ID)
	if err != nil || cancelled.Status != "cancelled" || cancelled.ErrorCode != "cancelled" {
		t.Fatalf("cancelled job: %#v %v", cancelled, err)
	}
}
