package windowsapp

import (
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
