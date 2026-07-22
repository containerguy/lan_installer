package deviceclient

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/containerguy/lan_installer/internal/install"
)

var artifactDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// artifactFetcher adapts the signed, resumable client download to the install
// package, which stays free of transport and signing concerns.
type artifactFetcher struct{ client *Client }

// InstallProgress reports how far a game install has got. Downloads here run
// for minutes to hours, so the UI needs more than a spinner.
type InstallProgress struct {
	Downloaded int64
	Total      int64
	// BytesPerSecond is averaged over the transfer so far.
	BytesPerSecond float64
}

// FetchArtifact downloads one CAS artifact by digest. The path is built here
// rather than taken from the release so a signed payload cannot redirect the
// client to an arbitrary endpoint.
func (f artifactFetcher) FetchArtifact(ctx context.Context, digest string, size int64, destination string) error {
	if !artifactDigestPattern.MatchString(digest) {
		return errors.New("Artefakt-Digest ist ungültig")
	}
	if size < 1 {
		return errors.New("Artefaktgröße ist ungültig")
	}
	return f.client.downloadVerifiedArtifact(ctx, "/v2/client/artifacts/sha256/"+digest, size, digest, destination, "Spielpaket")
}

// InstallGameArchive downloads, verifies and extracts one signed game archive
// below gamesRoot and returns the directory it was installed into.
//
// It deliberately does not register the game: the main executable cannot be
// derived reliably from an archive, and guessing it would silently misreport
// the installed version. The caller presents [install.FindExecutables] and
// registers the user's choice.
func (c *Client) InstallGameArchive(ctx context.Context, spec install.ArchiveInstall, gamesRoot string, progress func(InstallProgress)) (string, error) {
	client := *c
	if progress != nil {
		started := time.Now()
		client.OnDownloadProgress = func(downloaded, total int64) {
			elapsed := time.Since(started).Seconds()
			var rate float64
			if elapsed > 0 {
				rate = float64(downloaded) / elapsed
			}
			progress(InstallProgress{Downloaded: downloaded, Total: total, BytesPerSecond: rate})
		}
	}
	return install.InstallArchive(ctx, artifactFetcher{client: &client}, spec, gamesRoot, install.DefaultLimits)
}
