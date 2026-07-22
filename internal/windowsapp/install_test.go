package windowsapp

import (
	"net/url"
	"strings"
	"testing"
)

// A game id that is not part of the active signed event must never start an
// install, even before any network access happens.
func TestInstallGameRejectsUnknownGame(t *testing.T) {
	app := &App{}
	if _, err := app.InstallGame("not-in-event"); err == nil {
		t.Fatal("install started for a game outside the active event")
	}
}

// Without embedded event keys the client cannot establish that a release is
// genuine, so it must refuse to derive install work at all.
func TestInstallRefusesWithoutTrustedEventKeys(t *testing.T) {
	app := &App{}
	_, err := app.activeEventPayload()
	if err == nil {
		t.Fatal("payload was accepted without a trusted event key")
	}
	if !strings.Contains(err.Error(), "Prüfregeln") && !strings.Contains(err.Error(), "Signaturschlüssel") {
		t.Fatalf("unexpected refusal reason: %v", err)
	}
}

// The download page must come from the client, never from a signed payload:
// otherwise a release could send users to any site it likes.
func TestLauncherDownloadPagesAreKnownAndOfficial(t *testing.T) {
	app := &App{}
	for launcher, wantHost := range map[string]string{
		"steam":           "store.steampowered.com",
		"ea-app":          "www.ea.com",
		"ubisoft-connect": "ubisoftconnect.com",
	} {
		page := app.LauncherDownloadPage(launcher)
		if page == "" {
			t.Fatalf("%s has no download page", launcher)
		}
		parsed, err := url.Parse(page)
		if err != nil || parsed.Scheme != "https" {
			t.Fatalf("%s: %q must be an https URL (%v)", launcher, page, err)
		}
		if parsed.Host != wantHost {
			t.Fatalf("%s points at %q, expected %q", launcher, parsed.Host, wantHost)
		}
	}
}

func TestUnknownLauncherHasNoDownloadPage(t *testing.T) {
	app := &App{}
	for _, launcher := range []string{"", "standalone", "evil", "https://attacker.test"} {
		if page := app.LauncherDownloadPage(launcher); page != "" {
			t.Fatalf("%q unexpectedly resolved to %q", launcher, page)
		}
		if err := app.OpenLauncherDownload(launcher); err == nil {
			t.Fatalf("%q was accepted for opening", launcher)
		}
	}
}
