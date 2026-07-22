package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func cacheJobFixture(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "cache-jobs.db"))
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
	source, err := st.SaveSourceAtomic(context.Background(), Source{Name: "Cache Source", Kind: "https", BaseURL: "https://example.test/files", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	launchers, err := st.Launchers(context.Background())
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	gameID, err := st.SaveGameAtomic(context.Background(), Game{Slug: "cache-game", Name: "Cache Game", LauncherID: launchers[0].ID, Enabled: true}, nil)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	versionID, err := st.SaveGameVersionAtomic(context.Background(), GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "games/cache.zip", Enabled: true}, nil)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, user.ID, versionID
}

func TestCacheJobLifecyclePersistsArtifactMetadata(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil || job.Status != "queued" || job.TargetRevision != 1 {
		t.Fatalf("enqueue: %#v %v", job, err)
	}
	if _, err = st.EnqueueCacheJob(ctx, "game_version", versionID, userID); !errors.Is(err, ErrCacheJobAlreadyActive) {
		t.Fatalf("duplicate active job accepted: %v", err)
	}
	claimed, err := st.ClaimNextCacheJob(ctx)
	if err != nil || claimed.ID != job.ID || claimed.Status != "running" || claimed.Attempts != 1 {
		t.Fatalf("claim: %#v %v", claimed, err)
	}
	if err = st.UpdateCacheJobProgress(ctx, job.ID, 21); err != nil {
		t.Fatal(err)
	}
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	if err = st.CompleteCacheJob(ctx, job.ID, digest, 42, "application/zip"); err != nil {
		t.Fatal(err)
	}
	completed, err := st.CacheJob(ctx, job.ID)
	if err != nil || completed.Status != "succeeded" || completed.ProgressBytes != 42 || completed.ArtifactDigest != digest || completed.FinishedAt.IsZero() {
		t.Fatalf("completed job: %#v %v", completed, err)
	}
	versions, err := st.GameVersions(ctx)
	if err != nil || len(versions) != 1 || versions[0].SHA256 != digest || versions[0].SizeBytes != 42 || versions[0].Revision != 2 {
		t.Fatalf("derived catalog metadata: %#v %v", versions, err)
	}
}

func TestCacheJobCancelRetryFailureAndRecovery(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := st.RequestCacheJobCancel(ctx, job.ID)
	if err != nil || cancelled.Status != "cancelled" || !cancelled.CancelRequested {
		t.Fatalf("queued cancel: %#v %v", cancelled, err)
	}
	retried, err := st.RetryCacheJob(ctx, job.ID)
	if err != nil || retried.Status != "queued" || retried.CancelRequested {
		t.Fatalf("retry: %#v %v", retried, err)
	}
	if _, err = st.ClaimNextCacheJob(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.FailCacheJob(ctx, job.ID, "source_timeout", "Die Quelle antwortete nicht.", false); err != nil {
		t.Fatal(err)
	}
	failed, err := st.CacheJob(ctx, job.ID)
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "source_timeout" {
		t.Fatalf("failure: %#v %v", failed, err)
	}
	if _, err = st.RetryCacheJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextCacheJob(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.RecoverCacheJobs(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.CacheJob(ctx, job.ID)
	if err != nil || recovered.Status != "queued" || recovered.Attempts != 2 || !recovered.StartedAt.IsZero() {
		t.Fatalf("recovered job: %#v %v", recovered, err)
	}
	var marker int
	if err = st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=17`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("migration marker: %d %v", marker, err)
	}
}

func TestCompleteCacheJobRejectsChangedTarget(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextCacheJob(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`UPDATE game_versions SET revision=revision+1 WHERE id=?`, versionID); err != nil {
		t.Fatal(err)
	}
	digest := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 12, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	if err = st.CompleteCacheJob(ctx, job.ID, digest, 12, "application/zip"); !errors.Is(err, ErrCacheTargetChanged) {
		t.Fatalf("changed target accepted: %v", err)
	}
}

func TestCompleteCacheJobRejectsChangedSource(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextCacheJob(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`UPDATE sources SET revision=revision+1 WHERE id=?`, job.SourceID); err != nil {
		t.Fatal(err)
	}
	digest := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 12, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	if err = st.CompleteCacheJob(ctx, job.ID, digest, 12, "application/zip"); !errors.Is(err, ErrCacheTargetChanged) {
		t.Fatalf("changed source accepted: %v", err)
	}
	if err = st.FailCacheJob(ctx, job.ID, "cache_target_changed", "changed", false); err != nil {
		t.Fatal(err)
	}
	retried, err := st.RetryCacheJob(ctx, job.ID)
	if err != nil || retried.SourceRevision != job.SourceRevision+1 || retried.Status != "queued" {
		t.Fatalf("retry did not refresh source snapshot: %#v %v", retried, err)
	}
}

func TestCompleteCacheJobHonorsAtomicCancellation(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextCacheJob(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = st.RequestCacheJobCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	digest := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err = st.RegisterArtifact(ctx, Artifact{Digest: digest, SizeBytes: 12, ContentType: "application/zip"}); err != nil {
		t.Fatal(err)
	}
	if err = st.CompleteCacheJob(ctx, job.ID, digest, 12, "application/zip"); !errors.Is(err, ErrCacheJobCancelled) {
		t.Fatalf("cancelled job completed: %v", err)
	}
}

func TestSourceDeletionKeepsTerminalCacheJobHistory(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	err = st.DeleteSource(ctx, job.SourceID, job.SourceRevision)
	var referenced *SourceReferencedError
	if !errors.As(err, &referenced) || referenced.GameVersions != 1 || referenced.CacheJobs != 1 {
		t.Fatalf("active job/source references not reported: %#v %v", referenced, err)
	}
	if _, err = st.RequestCacheJobCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteCatalogAtomic(ctx, "game-version", versionID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteSource(ctx, job.SourceID, job.SourceRevision); err != nil {
		t.Fatalf("terminal history blocked source deletion: %v", err)
	}
	retained, err := st.CacheJob(ctx, job.ID)
	if err != nil || retained.Status != "cancelled" || retained.SourceID != job.SourceID {
		t.Fatalf("terminal job history was not retained: %#v %v", retained, err)
	}
}

func TestActiveCacheJobBlocksVersionDeletionUntilCancelled(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	err = st.DeleteCatalogAtomic(ctx, "game-version", versionID, 1, nil)
	var referenced *CatalogReferencedError
	if !errors.As(err, &referenced) || referenced.References != 1 {
		t.Fatalf("active job did not protect its UI-visible version: %#v %v", referenced, err)
	}
	if _, err = st.RequestCacheJobCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.DeleteCatalogAtomic(ctx, "game-version", versionID, 1, nil); err != nil {
		t.Fatalf("terminal job still blocked version deletion: %v", err)
	}
}

func TestCacheJobMutationsRollBackWhenAuditFails(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	if _, err := st.db.Exec(`CREATE TRIGGER reject_cache_audit BEFORE INSERT ON audit_log WHEN NEW.action='reject_cache' BEGIN SELECT RAISE(ABORT,'audit rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	audit := &AuditEntry{ActorUserID: userID, Action: "reject_cache", ObjectType: "cache_job"}
	if _, err := st.EnqueueCacheJobWithAudit(ctx, "game_version", versionID, userID, audit); err == nil {
		t.Fatal("cache job survived failed enqueue audit")
	}
	jobs, err := st.CacheJobs(ctx, 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("failed enqueue persisted: %#v %v", jobs, err)
	}
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.RequestCacheJobCancelWithAudit(ctx, job.ID, audit); err == nil {
		t.Fatal("cache cancellation survived failed audit")
	}
	unchanged, err := st.CacheJob(ctx, job.ID)
	if err != nil || unchanged.Status != "queued" || unchanged.CancelRequested {
		t.Fatalf("failed cancel persisted: %#v %v", unchanged, err)
	}
}

func TestRecoverSingleRunningCacheJob(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	defer st.Close()
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ClaimNextCacheJob(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.RecoverCacheJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := st.CacheJob(ctx, job.ID)
	if err != nil || recovered.Status != "queued" || !recovered.StartedAt.IsZero() {
		t.Fatalf("single job was not recovered: %#v %v", recovered, err)
	}
}

func TestMigrationRemovesLegacyCacheJobSourceForeignKey(t *testing.T) {
	st, userID, versionID := cacheJobFixture(t)
	ctx := context.Background()
	job, err := st.EnqueueCacheJob(ctx, "game_version", versionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.RequestCacheJobCancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var name, databasePath string
	if err = st.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	if _, err = st.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	legacy := `CREATE TABLE cache_jobs_legacy(id TEXT PRIMARY KEY, target_type TEXT NOT NULL CHECK(target_type IN ('launcher_version','game_version')), target_id INTEGER NOT NULL, target_revision INTEGER NOT NULL CHECK(target_revision>0), source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE RESTRICT, source_revision INTEGER NOT NULL CHECK(source_revision>0), source_path TEXT NOT NULL, expected_sha256 TEXT NOT NULL DEFAULT '', expected_size INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed','cancelled')), progress_bytes INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, cancel_requested INTEGER NOT NULL DEFAULT 0, error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '', artifact_digest TEXT REFERENCES artifact_blobs(digest) ON DELETE RESTRICT, artifact_size INTEGER NOT NULL DEFAULT 0, content_type TEXT NOT NULL DEFAULT '', created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT, created_at INTEGER NOT NULL, started_at INTEGER, finished_at INTEGER);
		INSERT INTO cache_jobs_legacy SELECT * FROM cache_jobs;
		DROP TABLE cache_jobs;
		ALTER TABLE cache_jobs_legacy RENAME TO cache_jobs;
		CREATE UNIQUE INDEX one_active_cache_job_per_target ON cache_jobs(target_type,target_id) WHERE status IN ('queued','running');
		CREATE INDEX cache_jobs_status_created_idx ON cache_jobs(status,created_at);`
	if _, err = st.db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.db.Query(`PRAGMA foreign_key_list(cache_jobs)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err = rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "sources" && from == "source_id" {
			t.Fatal("legacy source foreign key survived v17 migration")
		}
	}
	retained, err := st.CacheJob(ctx, job.ID)
	if err != nil || retained.Status != "cancelled" {
		t.Fatalf("job history was not retained by migration: %#v %v", retained, err)
	}
}
