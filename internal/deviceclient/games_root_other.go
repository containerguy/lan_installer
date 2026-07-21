//go:build !windows

package deviceclient

import (
	"errors"
	"os"
	"path/filepath"
)

// DefaultGamesRoot mirrors the Windows layout for development and tests. The
// shipped client is Windows-only.
func DefaultGamesRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("Benutzerverzeichnis ist kein absoluter Pfad")
	}
	return filepath.Join(home, "LANReady Games"), nil
}
