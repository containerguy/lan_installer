package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSaveSourceAtomicRollsBackSourceAndCredentials(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "sources.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	failed := errors.New("encryption failed")
	_, err = s.SaveSourceAtomic(ctx, Source{Name: "Cloud", Kind: "webdav", BaseURL: "https://cloud.example.test/dav", Enabled: true, LastTestState: "succeeded"}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, failed })
	if !errors.Is(err, failed) {
		t.Fatalf("expected builder error, got %v", err)
	}
	if sources, listErr := s.Sources(ctx); listErr != nil || len(sources) != 0 {
		t.Fatalf("partial source persisted: %#v %v", sources, listErr)
	}

	created, err := s.SaveSourceAtomic(ctx, Source{Name: "Cloud", Kind: "webdav", BaseURL: "https://cloud.example.test/dav", Enabled: true, LastTestState: "succeeded", LastTestedAt: "2026-07-16T08:00:00Z", LastTestLatencyMS: 42}, 0, func(id int64, existing *WebDAVConfig) (*WebDAVConfig, error) {
		if id < 1 || existing != nil {
			t.Fatal("unexpected builder input")
		}
		return &WebDAVConfig{AuthType: "basic", Username: "user", SecretNonce: []byte{1}, SecretCiphertext: []byte{2}}, nil
	})
	if err != nil || created.Revision != 1 || !created.AuthConfigured {
		t.Fatalf("created=%#v err=%v", created, err)
	}

	_, err = s.SaveSourceAtomic(ctx, Source{ID: created.ID, Name: "Changed", Kind: "webdav", BaseURL: "https://changed.example.test/dav", Enabled: true, LastTestState: "succeeded"}, created.Revision, func(id int64, existing *WebDAVConfig) (*WebDAVConfig, error) {
		if existing == nil || existing.Username != "user" {
			t.Fatal("existing secret unavailable in transaction")
		}
		return nil, failed
	})
	if !errors.Is(err, failed) {
		t.Fatalf("update: %v", err)
	}
	unchanged, err := s.Source(ctx, created.ID)
	if err != nil || unchanged.Name != "Cloud" || unchanged.Revision != 1 {
		t.Fatalf("source update was not rolled back: %#v %v", unchanged, err)
	}
	config, err := s.WebDAVConfig(ctx, created.ID)
	if err != nil || config.Username != "user" {
		t.Fatalf("config changed: %#v %v", config, err)
	}
}

func TestSourceRevisionDeactivateAndReferencedDelete(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "revision.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created, err := s.SaveSourceAtomic(ctx, Source{Name: "Downloads", Kind: "https", BaseURL: "https://files.example.test", Enabled: true, LastTestState: "succeeded"}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, err := s.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveLauncherVersionAtomic(ctx, LauncherVersion{LauncherID: launchers[0].ID, Version: "1", SourceID: created.ID, SourcePath: "setup.exe", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeactivateSource(ctx, created.ID, 99); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	revision, err := s.DeactivateSource(ctx, created.ID, created.Revision)
	if err != nil || revision != 2 {
		t.Fatalf("deactivate=%d %v", revision, err)
	}
	err = s.DeleteSource(ctx, created.ID, revision)
	var referenced *SourceReferencedError
	if !errors.As(err, &referenced) || referenced.LauncherVersions != 1 {
		t.Fatalf("referenced delete not blocked: %#v %v", referenced, err)
	}
}

func TestSourceMutationAuditIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "audit-atomic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_audit BEFORE INSERT ON audit_log WHEN NEW.action="reject" BEGIN SELECT RAISE(ABORT,"audit rejected"); END;`); err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveSourceAtomicWithAudit(ctx, Source{Name: "Must Roll Back", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil }, &AuditEntry{ActorUserID: 1, Action: "reject", ObjectType: "source"})
	if err == nil {
		t.Fatal("audit failure accepted")
	}
	values, listErr := s.Sources(ctx)
	if listErr != nil || len(values) != 0 {
		t.Fatalf("source survived audit rollback: %#v %v", values, listErr)
	}
}
