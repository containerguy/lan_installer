package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	lanstore "github.com/containerguy/lan_installer/internal/store"
)

func TestNewPreservesAdministrativeRootMode(t *testing.T) {
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	root := filepath.Join(t.TempDir(), "mounted-cache")
	if err = os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err = New(root, metadata); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("administrative root mode changed to %o", info.Mode().Perm())
	}
	for _, name := range []string{".tmp", "sha256"} {
		info, err = os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("private directory %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %s has mode %o", name, info.Mode().Perm())
		}
	}
}

func TestNewRejectsSymlinkRoot(t *testing.T) {
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err = os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "cache")
	if err = os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	if _, err = New(root, metadata); err == nil {
		t.Fatal("symlink artifact root accepted")
	}
}

func TestNewRejectsSymlinkPrivateDirectoriesWithoutChangingTarget(t *testing.T) {
	for _, name := range []string{".tmp", "sha256"} {
		t.Run(name, func(t *testing.T) {
			metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
			if err != nil {
				t.Fatal(err)
			}
			defer metadata.Close()
			parent := t.TempDir()
			root := filepath.Join(parent, "cache")
			outside := filepath.Join(parent, "outside")
			if err = os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(outside, 0o750); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(outside, filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			if _, err = New(root, metadata); err == nil {
				t.Fatalf("symlink %s accepted", name)
			}
			info, err := os.Stat(outside)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o750 {
				t.Fatalf("outside target mode changed to %o", info.Mode().Perm())
			}
		})
	}
}

func TestIngestRejectsSymlinkDigestPrefixWithoutOutsideWrite(t *testing.T) {
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	parent := t.TempDir()
	root := filepath.Join(parent, "cache")
	artifacts, err := New(root, metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("symlink boundary payload")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	outside := filepath.Join(parent, "outside")
	if err = os.Mkdir(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "sha256", digest[:2])); err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.Ingest(context.Background(), digest, int64(len(content)), "application/octet-stream", bytes.NewReader(content)); err == nil {
		t.Fatal("symlink digest prefix accepted")
	}
	if _, err = os.Stat(filepath.Join(outside, digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact escaped cache boundary: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("outside target mode changed to %o", info.Mode().Perm())
	}
}

func TestExternalNFSStoreLifecycle(t *testing.T) {
	root := os.Getenv("LANREADY_NFS_INTEGRATION_ROOT")
	if root == "" {
		t.Skip("LANREADY_NFS_INTEGRATION_ROOT is not set")
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !rootInfo.IsDir() {
		t.Fatal("integration root is not a directory")
	}
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	metadata, err := lanstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	created, err := metadata.BootstrapAdmin(context.Background(), "nfs-integration", "integration-test-hash")
	if err != nil || !created {
		metadata.Close()
		t.Fatalf("bootstrap integration actor: created=%v err=%v", created, err)
	}
	actor, err := metadata.UserByUsername(context.Background(), "nfs-integration")
	if err != nil {
		metadata.Close()
		t.Fatal(err)
	}
	artifacts, err := NewWithQuota(root, metadata, 1<<20)
	if err != nil {
		metadata.Close()
		t.Fatal(err)
	}
	content := []byte("LANReady NFS lifecycle " + time.Now().UTC().Format(time.RFC3339Nano))
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	artifactPath := artifacts.path(digest)
	defer os.Remove(artifactPath)
	stored, err := artifacts.Ingest(context.Background(), digest, int64(len(content)), "application/octet-stream", bytes.NewReader(content))
	if err != nil || stored.Digest != digest {
		metadata.Close()
		t.Fatalf("NFS ingest: stored=%#v err=%v", stored, err)
	}
	blob, err := artifacts.OpenVerified(context.Background(), digest)
	if err != nil {
		metadata.Close()
		t.Fatal(err)
	}
	rangeStart := int64(4)
	rangeBuffer := make([]byte, 9)
	if _, err = blob.File.ReadAt(rangeBuffer, rangeStart); err != nil {
		blob.File.Close()
		metadata.Close()
		t.Fatal(err)
	}
	if !bytes.Equal(rangeBuffer, content[rangeStart:rangeStart+int64(len(rangeBuffer))]) {
		blob.File.Close()
		metadata.Close()
		t.Fatalf("range read mismatch: %q", rangeBuffer)
	}
	if err = blob.File.Close(); err != nil {
		metadata.Close()
		t.Fatal(err)
	}
	if err = metadata.Close(); err != nil {
		t.Fatal(err)
	}

	metadata, err = lanstore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	artifacts, err = NewWithQuota(root, metadata, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = artifacts.Verify(context.Background(), digest, int64(len(content)), "application/octet-stream"); err != nil {
		t.Fatalf("verify after store restart: %v", err)
	}
	gc, err := artifacts.GarbageCollect(context.Background(), time.Now().UTC().Add(time.Hour), 100, actor.ID, "nfs-integration")
	if err != nil || gc.Removed != 1 || gc.RemovedBytes != int64(len(content)) {
		t.Fatalf("NFS garbage collection: result=%#v err=%v", gc, err)
	}
	if _, err = os.Stat(artifactPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("garbage-collected artifact remains: %v", err)
	}
	outside := t.TempDir()
	outsideInfo, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	var redirectedContent []byte
	var redirectedDigest string
	var prefixPath string
	for attempt := 0; attempt < 256; attempt++ {
		redirectedContent = []byte(fmt.Sprintf("NFS symlink boundary %d %d", time.Now().UnixNano(), attempt))
		redirectedBytes := sha256.Sum256(redirectedContent)
		redirectedDigest = hex.EncodeToString(redirectedBytes[:])
		prefixPath = filepath.Join(root, "sha256", redirectedDigest[:2])
		if _, statErr := os.Lstat(prefixPath); errors.Is(statErr, os.ErrNotExist) {
			break
		}
		prefixPath = ""
	}
	if prefixPath == "" {
		t.Fatal("could not find an unused digest prefix for NFS symlink test")
	}
	if err = os.Symlink(outside, prefixPath); err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.Ingest(context.Background(), redirectedDigest, int64(len(redirectedContent)), "application/octet-stream", bytes.NewReader(redirectedContent)); err == nil {
		os.Remove(prefixPath)
		t.Fatal("NFS digest-prefix symlink accepted")
	}
	if err = os.Remove(prefixPath); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(outside, redirectedDigest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NFS artifact escaped cache boundary: %v", err)
	}
	outsideAfter, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if outsideAfter.Mode().Perm() != outsideInfo.Mode().Perm() {
		t.Fatalf("NFS symlink target mode changed from %o to %o", outsideInfo.Mode().Perm(), outsideAfter.Mode().Perm())
	}
	rootAfter, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if rootAfter.Mode().Perm() != rootInfo.Mode().Perm() {
		t.Fatalf("NFS root mode changed from %o to %o", rootInfo.Mode().Perm(), rootAfter.Mode().Perm())
	}
}

func TestIngestAndOpenVerified(t *testing.T) {
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	root := t.TempDir()
	artifacts, err := New(root, metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("LANReady artifact payload")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	stored, err := artifacts.Ingest(context.Background(), digest, int64(len(content)), "application/zip", bytes.NewReader(content))
	if err != nil || stored.Digest != digest || stored.SizeBytes != int64(len(content)) {
		t.Fatalf("ingest: %#v %v", stored, err)
	}
	blob, err := artifacts.OpenVerified(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	defer blob.File.Close()
	got := make([]byte, len(content))
	if _, err = blob.File.Read(got); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("read: %q %v", got, err)
	}

	path := artifacts.path(digest)
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, bytes.Repeat([]byte("x"), len(content)), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.OpenVerified(context.Background(), digest); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt artifact accepted: %v", err)
	}
}

func TestIngestRejectsMismatchAndConflictingMetadata(t *testing.T) {
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	artifacts, err := New(t.TempDir(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("payload")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	if _, err = artifacts.Ingest(context.Background(), digest, int64(len(content)+1), "application/zip", bytes.NewReader(content)); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("size mismatch: %v", err)
	}
	if _, err = artifacts.Ingest(context.Background(), stringsOf("0", 64), int64(len(content)), "application/zip", bytes.NewReader(content)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digest mismatch: %v", err)
	}
	if _, err = artifacts.Ingest(context.Background(), digest, int64(len(content)), "application/zip", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.Ingest(context.Background(), digest, int64(len(content)), "application/octet-stream", bytes.NewReader(content)); err == nil {
		t.Fatal("conflicting content type was accepted")
	}
}

func TestIngestComputedDerivesIdentityAndEnforcesLimit(t *testing.T) {
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	artifacts, err := New(t.TempDir(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("computed payload")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	stored, err := artifacts.IngestComputed(context.Background(), int64(len(content)), "application/zip", bytes.NewReader(content))
	if err != nil || stored.Digest != digest || stored.SizeBytes != int64(len(content)) || stored.ContentType != "application/zip" {
		t.Fatalf("computed ingest: %#v %v", stored, err)
	}
	usage, err := metadata.ArtifactUsage(context.Background())
	if err != nil || usage != int64(len(content)) {
		t.Fatalf("artifact usage: %d %v", usage, err)
	}
	if _, err = artifacts.IngestComputed(context.Background(), int64(len(content)-1), "application/zip", bytes.NewReader(content)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized computed artifact accepted: %v", err)
	}
}

func TestQuotaIsSharedAcrossDeclaredAndComputedIngests(t *testing.T) {
	ctx := context.Background()
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	artifacts, err := NewWithQuota(t.TempDir(), metadata, 10)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("123456")
	firstDigest := sha256.Sum256(first)
	if _, err = artifacts.Ingest(ctx, hex.EncodeToString(firstDigest[:]), int64(len(first)), "application/zip", bytes.NewReader(first)); err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.IngestComputed(ctx, 5, "application/zip", bytes.NewReader([]byte("7890"))); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("computed ingest bypassed quota: %v", err)
	}
	second := []byte("abcde")
	secondDigest := sha256.Sum256(second)
	if _, err = artifacts.Ingest(ctx, hex.EncodeToString(secondDigest[:]), int64(len(second)), "application/zip", bytes.NewReader(second)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("declared ingest bypassed quota: %v", err)
	}
	usage, err := metadata.ArtifactUsage(ctx)
	if err != nil || usage != int64(len(first)) {
		t.Fatalf("unexpected usage after rejected writes: %d %v", usage, err)
	}
}

func TestQuotaReservationsPreventConcurrentOvercommit(t *testing.T) {
	ctx := context.Background()
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	artifacts, err := NewWithQuota(t.TempDir(), metadata, 10)
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("123456")
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		digest := sha256.Sum256(first)
		_, ingestErr := artifacts.Ingest(ctx, hex.EncodeToString(digest[:]), int64(len(first)), "application/zip", &gatedReader{reader: bytes.NewReader(first), entered: entered, release: release})
		firstDone <- ingestErr
	}()
	<-entered
	second := []byte("abcdef")
	secondDigest := sha256.Sum256(second)
	if _, err = artifacts.Ingest(ctx, hex.EncodeToString(secondDigest[:]), int64(len(second)), "application/zip", bytes.NewReader(second)); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("open reservation did not prevent overcommit: %v", err)
	}
	close(release)
	if err = <-firstDone; err != nil {
		t.Fatalf("reserved ingest failed: %v", err)
	}
}

type gatedReader struct {
	reader  *bytes.Reader
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *gatedReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	return r.reader.Read(buffer)
}

func stringsOf(value string, count int) string {
	var result bytes.Buffer
	for range count {
		result.WriteString(value)
	}
	return result.String()
}

func TestConcurrentIngestConflictNeverDeletesWinningBlob(t *testing.T) {
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	artifacts, err := New(t.TempDir(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("parallel-artifact"), 128)
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	types := []string{"application/zip", "application/octet-stream"}
	errorsByType := make([]error, len(types))
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for index, contentType := range types {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			_, errorsByType[index] = artifacts.Ingest(context.Background(), digest, int64(len(content)), contentType, bytes.NewReader(content))
		}()
	}
	start.Done()
	workers.Wait()
	successes := 0
	for _, ingestErr := range errorsByType {
		if ingestErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected one metadata winner, errors=%v", errorsByType)
	}
	blob, err := artifacts.OpenVerified(context.Background(), digest)
	if err != nil {
		t.Fatalf("winning blob was removed or corrupted: %v", err)
	}
	_ = blob.File.Close()
}

func TestGarbageCollectRemovesOnlyUnreferencedArtifactsAndRestoresOnTransactionFailure(t *testing.T) {
	ctx := context.Background()
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	if _, err = metadata.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	user, _ := metadata.UserByUsername(ctx, "admin")
	artifacts, err := New(t.TempDir(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("collect me")
	stored, err := artifacts.IngestComputed(ctx, 1024, "application/zip", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = artifacts.GarbageCollect(ctx, time.Now().Add(time.Hour), 100, 9999, "test"); err == nil {
		t.Fatal("garbage collection with invalid audit actor succeeded")
	}
	if err = artifacts.Verify(ctx, stored.Digest, stored.SizeBytes, stored.ContentType); err != nil {
		t.Fatalf("failed transaction did not restore blob: %v", err)
	}
	result, err := artifacts.GarbageCollect(ctx, time.Now().Add(time.Hour), 100, user.ID, "test")
	if err != nil || result.Removed != 1 || result.RemovedBytes != int64(len(content)) {
		t.Fatalf("garbage collection: %#v %v", result, err)
	}
	if _, err = artifacts.OpenVerified(ctx, stored.Digest); err == nil {
		t.Fatal("unreferenced artifact remains readable")
	}
}

func TestGarbageCollectRetainsCatalogAndReleaseReferences(t *testing.T) {
	ctx := context.Background()
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	if _, err = metadata.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	user, _ := metadata.UserByUsername(ctx, "admin")
	artifacts, err := New(t.TempDir(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("referenced")
	stored, err := artifacts.IngestComputed(ctx, 1024, "application/zip", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	source, err := metadata.SaveSourceAtomic(ctx, lanstore.Source{Name: "GC Source", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *lanstore.WebDAVConfig) (*lanstore.WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, _ := metadata.Launchers(ctx)
	gameID, err := metadata.SaveGameAtomic(ctx, lanstore.Game{Slug: "gc-game", Name: "GC Game", LauncherID: launchers[0].ID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = metadata.SaveGameVersionAtomic(ctx, lanstore.GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "game.zip", SHA256: stored.Digest, SizeBytes: stored.SizeBytes, Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	result, err := artifacts.GarbageCollect(ctx, time.Now().Add(time.Hour), 100, user.ID, "test")
	if err != nil || result.Removed != 0 {
		t.Fatalf("referenced artifact collected: %#v %v", result, err)
	}
	if err = artifacts.Verify(ctx, stored.Digest, stored.SizeBytes, stored.ContentType); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRenewsGarbageCollectionGraceForReusedArtifact(t *testing.T) {
	ctx := context.Background()
	metadata, err := lanstore.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	if _, err = metadata.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	user, err := metadata.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := New(t.TempDir(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("reused client update")
	stored, err := artifacts.IngestComputed(ctx, 1024, "application/vnd.microsoft.portable-executable", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err = metadata.RenewArtifactGCGrace(ctx, stored.Digest, old); err != nil {
		t.Fatal(err)
	}
	if err = artifacts.Verify(ctx, stored.Digest, stored.SizeBytes, stored.ContentType); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	result, err := artifacts.GarbageCollect(ctx, cutoff, 100, user.ID, "test")
	if err != nil || result.Removed != 0 {
		t.Fatalf("verified reused artifact lost its grace: %#v %v", result, err)
	}
	if err = metadata.RenewArtifactGCGrace(ctx, stored.Digest, old); err != nil {
		t.Fatal(err)
	}
	result, err = artifacts.GarbageCollect(ctx, cutoff, 100, user.ID, "test")
	if err != nil || result.Removed != 1 {
		t.Fatalf("expired unreferenced artifact was not collected: %#v %v", result, err)
	}
}
