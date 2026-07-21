//go:build windows

package deviceclient

import (
	"errors"
	"os"
	"path/filepath"
)

// DefaultGamesRoot returns the per-user directory LANReady extracts game
// archives into. The client never runs as a service, so games belong under the
// user's own profile where no elevation is required.
//
// %USERPROFILE% is used rather than %LOCALAPPDATA% because game installs are
// large and users expect to find and back them up, not to have them hidden in
// an application data folder.
func DefaultGamesRoot() (string, error) {
	profile := os.Getenv("USERPROFILE")
	if profile == "" {
		return "", errors.New("USERPROFILE ist nicht gesetzt")
	}
	if !filepath.IsAbs(profile) {
		return "", errors.New("USERPROFILE ist kein absoluter Pfad")
	}
	return filepath.Join(profile, "LANReady Games"), nil
}
