package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestCatalogAtomicLifecycleAndReferences(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	source, err := s.SaveSourceAtomic(ctx, Source{Name: "Downloads", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, err := s.Launchers(ctx)
	if err != nil || len(launchers) == 0 {
		t.Fatalf("launchers=%#v err=%v", launchers, err)
	}
	audit := &AuditEntry{ActorUserID: 1, Action: "catalog_test"}
	gameID, err := s.SaveGameAtomic(ctx, Game{Slug: "test-game", Name: "Test Game", LauncherID: launchers[0].ID, ExternalGameID: "730", Enabled: true}, audit)
	if err != nil {
		t.Fatal(err)
	}
	launcherVersionID, err := s.SaveLauncherVersionAtomic(ctx, LauncherVersion{LauncherID: launchers[0].ID, Version: "1.0", SourceID: source.ID, SourcePath: "launcher/setup.exe", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 42, Enabled: true}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE launcher_versions SET silent_args_json='["/S"]',silent_args_verified=1 WHERE id=?`, launcherVersionID); err != nil {
		t.Fatal(err)
	}
	launcherUpdate := LauncherVersion{ID: launcherVersionID, Revision: 1, LauncherID: launchers[0].ID, Version: "1.1", SourceID: source.ID, SourcePath: "launcher/setup.exe", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 42, SilentArgs: []string{"malicious shell line"}, Enabled: true}
	if _, err = s.SaveLauncherVersionAtomic(ctx, launcherUpdate, audit); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveLauncherVersionAtomic(ctx, launcherUpdate, audit); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale launcher version accepted: %v", err)
	}
	gameVersionID, err := s.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "2026.1", SourceID: source.ID, SourcePath: "games/test.zip", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SizeBytes: 84, Enabled: true}, audit)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := s.SaveEventAtomic(ctx, Event{Slug: "keller-lan", Name: "Keller LAN", StartsAt: "2026-07-20T18:00:00+02:00", EndsAt: "2026-07-21T02:00:00+02:00", Status: "draft"}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveEventGameAtomic(ctx, EventGame{EventID: eventID, GameVersionID: gameVersionID, Required: true}, audit); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteCatalogAtomic(ctx, "game-version", gameVersionID, 1, audit); err == nil {
		t.Fatal("referenced game version deleted")
	} else {
		var referenced *CatalogReferencedError
		if !errors.As(err, &referenced) || referenced.References != 1 {
			t.Fatalf("unexpected reference error: %v", err)
		}
	}
	if err = s.DeleteCatalogAtomic(ctx, "launcher", launchers[0].ID, launchers[0].Revision, audit); err == nil {
		t.Fatal("referenced launcher deleted")
	}
	if err = s.SetCatalogEnabledAtomic(ctx, "launcher-version", launcherVersionID, 2, false, audit); err != nil {
		t.Fatal(err)
	}
	versions, err := s.LauncherVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundDisabled := false
	for _, value := range versions {
		if value.ID == launcherVersionID {
			foundDisabled = !value.Enabled && value.Revision == 3 && value.SilentArgsVerified && len(value.SilentArgs) == 1 && value.SilentArgs[0] == "/S"
		}
	}
	if !foundDisabled {
		t.Fatal("launcher version was not deactivated")
	}
	if err = s.DeleteEventGameAtomic(ctx, eventID, gameVersionID, 1, audit); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteCatalogAtomic(ctx, "game-version", gameVersionID, 1, audit); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRejectsDisabledParentBindings(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "disabled-parents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	activeSource, err := s.SaveSourceAtomic(ctx, Source{Name: "Active", Kind: "https", BaseURL: "https://active.example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	disabledSource, err := s.SaveSourceAtomic(ctx, Source{Name: "Disabled", Kind: "https", BaseURL: "https://disabled.example.test", Enabled: false}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, err := s.Launchers(ctx)
	if err != nil || len(launchers) < 3 {
		t.Fatalf("launchers=%#v err=%v", launchers, err)
	}
	if _, err = s.db.Exec(`UPDATE launchers SET enabled=0 WHERE id=?`, launchers[1].ID); err != nil {
		t.Fatal(err)
	}

	gameID, err := s.SaveGameAtomic(ctx, Game{Slug: "bound-game", Name: "Bound Game", LauncherID: launchers[0].ID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGameAtomic(ctx, Game{ID: gameID, Revision: 1, Slug: "bound-game", Name: "Changed Parent", LauncherID: launchers[1].ID, Enabled: true}, nil); err == nil {
		t.Fatal("game rebound to disabled launcher")
	}
	if _, err = s.db.Exec(`UPDATE launchers SET enabled=0 WHERE id=?`, launchers[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGameAtomic(ctx, Game{ID: gameID, Revision: 1, Slug: "bound-game", Name: "Unchanged Parent", LauncherID: launchers[0].ID, Enabled: true}, nil); err != nil {
		t.Fatalf("unchanged disabled launcher blocked: %v", err)
	}
	if _, err = s.SaveGameAtomic(ctx, Game{Slug: "new-disabled-game", Name: "New Disabled", LauncherID: launchers[0].ID, Enabled: true}, nil); err == nil {
		t.Fatal("game created below disabled launcher")
	}

	launcherVersionID, err := s.SaveLauncherVersionAtomic(ctx, LauncherVersion{LauncherID: launchers[2].ID, Version: "1", SourceID: activeSource.ID, SourcePath: "launcher/setup.exe", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveLauncherVersionAtomic(ctx, LauncherVersion{ID: launcherVersionID, Revision: 1, LauncherID: launchers[2].ID, Version: "2", SourceID: disabledSource.ID, SourcePath: "launcher/setup.exe", Enabled: true}, nil); err == nil {
		t.Fatal("launcher version rebound to disabled source")
	}
	if _, err = s.db.Exec(`UPDATE sources SET enabled=0 WHERE id=?`, activeSource.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveLauncherVersionAtomic(ctx, LauncherVersion{ID: launcherVersionID, Revision: 1, LauncherID: launchers[2].ID, Version: "2", SourceID: activeSource.ID, SourcePath: "launcher/setup.exe", Enabled: true}, nil); err != nil {
		t.Fatalf("unchanged disabled source blocked: %v", err)
	}
	if _, err = s.SaveLauncherVersionAtomic(ctx, LauncherVersion{LauncherID: launchers[2].ID, Version: "new", SourceID: activeSource.ID, SourcePath: "launcher/new.exe", Enabled: true}, nil); err == nil {
		t.Fatal("launcher version created below disabled source")
	}

	if _, err = s.db.Exec(`UPDATE sources SET enabled=1 WHERE id=?`, activeSource.ID); err != nil {
		t.Fatal(err)
	}
	parentGameID, err := s.SaveGameAtomic(ctx, Game{Slug: "version-parent", Name: "Version Parent", LauncherID: launchers[2].ID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	disabledGameID, err := s.SaveGameAtomic(ctx, Game{Slug: "disabled-parent", Name: "Disabled Parent", LauncherID: launchers[2].ID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gameVersionID, err := s.SaveGameVersionAtomic(ctx, GameVersion{GameID: parentGameID, Version: "1", SourceID: activeSource.ID, SourcePath: "games/one.zip", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE games SET enabled=0 WHERE id IN (?,?)`, parentGameID, disabledGameID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGameVersionAtomic(ctx, GameVersion{ID: gameVersionID, Revision: 1, GameID: disabledGameID, Version: "2", SourceID: activeSource.ID, SourcePath: "games/two.zip", Enabled: true}, nil); err == nil {
		t.Fatal("game version rebound to disabled game")
	}
	if _, err = s.SaveGameVersionAtomic(ctx, GameVersion{ID: gameVersionID, Revision: 1, GameID: parentGameID, Version: "2", SourceID: activeSource.ID, SourcePath: "games/two.zip", Enabled: true}, nil); err != nil {
		t.Fatalf("unchanged disabled game blocked: %v", err)
	}
	if _, err = s.SaveGameVersionAtomic(ctx, GameVersion{GameID: parentGameID, Version: "new", SourceID: activeSource.ID, SourcePath: "games/new.zip", Enabled: true}, nil); err == nil {
		t.Fatal("game version created below disabled game")
	}
}

func TestEventGameCreateRequiresActiveChain(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "event-chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := s.SaveSourceAtomic(ctx, Source{Name: "Chain", Kind: "https", BaseURL: "https://chain.example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, err := s.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gameID, err := s.SaveGameAtomic(ctx, Game{Slug: "chain-game", Name: "Chain Game", LauncherID: launchers[0].ID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := s.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "games/chain.zip", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := s.SaveEventAtomic(ctx, Event{Slug: "chain-event", Name: "Chain Event", Status: "draft"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []struct {
		table string
		id    int64
	}{{"game_versions", versionID}, {"games", gameID}, {"launchers", launchers[0].ID}, {"sources", source.ID}} {
		if _, err = s.db.Exec(`UPDATE `+parent.table+` SET enabled=0 WHERE id=?`, parent.id); err != nil {
			t.Fatal(err)
		}
		if err = s.SaveEventGameAtomic(ctx, EventGame{EventID: eventID, GameVersionID: versionID, Required: true}, nil); err == nil {
			t.Fatalf("event assignment accepted disabled %s", parent.table)
		}
		if _, err = s.db.Exec(`UPDATE `+parent.table+` SET enabled=1 WHERE id=?`, parent.id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEventRejectsTwoVersionsOfSameGame(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "event-version-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	launchers, _ := s.Launchers(ctx)
	var steamID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "steam" {
			steamID = launcher.ID
		}
	}
	gameID, err := s.SaveGameAtomic(ctx, Game{Slug: "cs2", Name: "Counter-Strike 2", LauncherID: steamID, ExternalGameID: "730", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "1", Enabled: true}, nil)
	second, _ := s.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "2", Enabled: true}, nil)
	eventID, _ := s.SaveEventAtomic(ctx, Event{Slug: "lan", Name: "LAN", Status: "draft"}, nil)
	if err = s.SaveEventGameAtomic(ctx, EventGame{EventID: eventID, GameVersionID: first, Required: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveEventGameAtomic(ctx, EventGame{EventID: eventID, GameVersionID: second, Required: true}, nil); !errors.Is(err, ErrEventGameConflict) {
		t.Fatalf("second version returned %v", err)
	}
}

func TestLauncherManagedGameVersionDoesNotRequirePackageSource(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "launcher-managed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	launchers, err := s.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var steamID, standaloneID int64
	for _, launcher := range launchers {
		switch launcher.Adapter {
		case "steam":
			steamID = launcher.ID
		case "standalone":
			standaloneID = launcher.ID
		}
	}
	steamGameID, err := s.SaveGameAtomic(ctx, Game{Slug: "age-of-empires-2-de", Name: "Age of Empires II: Definitive Edition", LauncherID: steamID, ExternalGameID: "813780", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := s.SaveGameVersionAtomic(ctx, GameVersion{GameID: steamGameID, Version: "101.103.25120.0", Enabled: true}, nil)
	if err != nil {
		t.Fatalf("Steam-managed version rejected: %v", err)
	}
	versions, err := s.GameVersions(ctx)
	if err != nil || len(versions) != 1 || versions[0].ID != versionID || versions[0].SourceID != 0 || versions[0].LauncherAdapter != "steam" || versions[0].LauncherName != "Steam" {
		t.Fatalf("launcher-managed version=%#v err=%v", versions, err)
	}
	eventID, err := s.SaveEventAtomic(ctx, Event{Slug: "lan", Name: "LAN", Status: "draft"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveEventGameAtomic(ctx, EventGame{EventID: eventID, GameVersionID: versionID, Required: true}, nil); err != nil {
		t.Fatalf("launcher-managed version could not be assigned: %v", err)
	}
	if _, err = s.SaveGameAtomic(ctx, Game{ID: steamGameID, Revision: 1, Slug: "age-of-empires-2-de", Name: "Age of Empires II: Definitive Edition", LauncherID: standaloneID, Enabled: true}, nil); err == nil {
		t.Fatal("game with package-less launcher version was switched to standalone")
	} else {
		var referenced *CatalogReferencedError
		if !errors.As(err, &referenced) || referenced.Entity != "package-less game version" || referenced.References != 1 {
			t.Fatalf("unexpected package-less reference error: %v", err)
		}
	}
	standaloneGameID, err := s.SaveGameAtomic(ctx, Game{Slug: "portable-game", Name: "Portable Game", LauncherID: standaloneID, ExternalGameID: "portable-game", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGameVersionAtomic(ctx, GameVersion{GameID: standaloneGameID, Version: "1", Enabled: true}, nil); err == nil {
		t.Fatal("standalone version without package source accepted")
	}
	if _, err = s.SaveGameVersionAtomic(ctx, GameVersion{GameID: steamGameID, Version: "invalid-package", SourcePath: "game.zip", Enabled: true}, nil); err == nil {
		t.Fatal("launcher-managed version accepted orphan package metadata")
	}
}

func TestPublishedEventLocksReferencedGraph(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "published-graph.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := s.SaveSourceAtomic(ctx, Source{Name: "Published", Kind: "https", BaseURL: "https://published.example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	launchers, err := s.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	launcher := launchers[0]
	gameID, err := s.SaveGameAtomic(ctx, Game{Slug: "published-game", Name: "Published Game", LauncherID: launcher.ID, Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionID, err := s.SaveGameVersionAtomic(ctx, GameVersion{GameID: gameID, Version: "1", SourceID: source.ID, SourcePath: "games/published.zip", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := s.SaveEventAtomic(ctx, Event{Slug: "published-event", Name: "Published Event", Status: "draft"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveEventGameAtomic(ctx, EventGame{EventID: eventID, GameVersionID: versionID, Required: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE events SET status="published" WHERE id=?`, eventID); err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		run  func() error
	}{
		{"event update", func() error {
			_, err := s.SaveEventAtomic(ctx, Event{ID: eventID, Revision: 1, Slug: "published-event", Name: "Changed", Status: "draft"}, nil)
			return err
		}},
		{"launcher update", func() error {
			_, err := s.SaveLauncherAtomic(ctx, Launcher{ID: launcher.ID, Revision: launcher.Revision, Slug: launcher.Slug, Name: "Changed", Adapter: launcher.Adapter, Enabled: true}, nil)
			return err
		}},
		{"game update", func() error {
			_, err := s.SaveGameAtomic(ctx, Game{ID: gameID, Revision: 1, Slug: "published-game", Name: "Changed", LauncherID: launcher.ID, Enabled: true}, nil)
			return err
		}},
		{"game version update", func() error {
			_, err := s.SaveGameVersionAtomic(ctx, GameVersion{ID: versionID, Revision: 1, GameID: gameID, Version: "2", SourceID: source.ID, SourcePath: "games/published.zip", Enabled: true}, nil)
			return err
		}},
		{"launcher deactivate", func() error {
			return s.SetCatalogEnabledAtomic(ctx, "launcher", launcher.ID, launcher.Revision, false, nil)
		}},
		{"game deactivate", func() error { return s.SetCatalogEnabledAtomic(ctx, "game", gameID, 1, false, nil) }},
		{"game version deactivate", func() error { return s.SetCatalogEnabledAtomic(ctx, "game-version", versionID, 1, false, nil) }},
		{"event delete", func() error { return s.DeleteCatalogAtomic(ctx, "event", eventID, 1, nil) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrPublishedEventLocked) {
				t.Fatalf("published graph mutation returned %v", err)
			}
		})
	}
	if err = s.SaveEventGameAtomic(ctx, EventGame{EventID: eventID, GameVersionID: versionID, Revision: 1, Required: false}, nil); err != nil {
		t.Fatalf("published event assignment update: %v", err)
	}
	if err = s.DeleteEventGameAtomic(ctx, eventID, versionID, 2, nil); err != nil {
		t.Fatalf("published event assignment delete: %v", err)
	}
}

func TestCatalogValidationAndAuditRollback(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "catalog-validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.BootstrapAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	launchers, _ := s.Launchers(ctx)
	source, _ := s.SaveSourceAtomic(ctx, Source{Name: "Downloads", Kind: "https", BaseURL: "https://example.test", Enabled: true}, 0, func(int64, *WebDAVConfig) (*WebDAVConfig, error) { return nil, nil })
	for _, unsafePath := range []string{"../escape.zip", "https://example.test/setup.exe", "C:/setup.exe", "games/%2e%2e/setup.exe", "games/\x00setup.exe", "games//setup.exe"} {
		if _, pathErr := normalizeArtifactPath(unsafePath); pathErr == nil {
			t.Fatalf("unsafe artifact path accepted: %q", unsafePath)
		}
	}
	if _, err = s.SaveLauncherVersionAtomic(ctx, LauncherVersion{LauncherID: launchers[0].ID, Version: "bad", SourceID: source.ID, SourcePath: "setup.exe", SHA256: "short", Enabled: true}, nil); err == nil {
		t.Fatal("invalid sha accepted")
	}
	if _, err = s.SaveEventAtomic(ctx, Event{Slug: "bad-time", Name: "Bad Time", StartsAt: "2026-07-21T02:00", EndsAt: "2026-07-20T18:00", Status: "draft"}, nil); err == nil {
		t.Fatal("inverted event time accepted")
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_catalog_audit BEFORE INSERT ON audit_log WHEN NEW.action="reject_catalog" BEGIN SELECT RAISE(ABORT,"audit rejected"); END;`); err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveGameAtomic(ctx, Game{Slug: "must-rollback", Name: "Must Roll Back", LauncherID: launchers[0].ID, Enabled: true}, &AuditEntry{ActorUserID: 1, Action: "reject_catalog"})
	if err == nil {
		t.Fatal("audit failure accepted")
	}
	games, listErr := s.Games(ctx)
	if listErr != nil || len(games) != 0 {
		t.Fatalf("game survived audit rollback: %#v %v", games, listErr)
	}
}
