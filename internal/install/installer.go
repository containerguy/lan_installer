package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ArtifactFetcher downloads a CAS artifact to a local path. The device client
// supplies the signed, resumable implementation; keeping it an interface lets
// the install logic be tested without a server or Windows.
type ArtifactFetcher interface {
	FetchArtifact(ctx context.Context, digest string, size int64, destination string) error
}

// ArchiveInstall is one extract_archive action from a signed event release,
// already parsed. Digest is the bare lowercase hex digest without the
// "sha256:" prefix.
type ArchiveInstall struct {
	GameID       string
	Digest       string
	Size         int64
	RelativePath string
}

var errUnsafeInstall = errors.New("install target is unsafe")

// InstallArchive downloads, verifies and extracts one archive below gamesRoot.
//
// Order matters: the digest is verified over the complete downloaded file
// before a single entry is read, so a corrupted or substituted download never
// reaches the extractor. Extraction itself is contained by [Extract].
func InstallArchive(ctx context.Context, fetcher ArtifactFetcher, spec ArchiveInstall, gamesRoot string, limits Limits) (string, error) {
	target, err := resolveInstallDir(gamesRoot, spec.RelativePath)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	// A leftover directory from an aborted run must not silently merge with the
	// new contents; extraction opens files with O_EXCL and would fail midway.
	if _, statErr := os.Stat(target); statErr == nil {
		return "", fmt.Errorf("%w: %q already exists", errUnsafeInstall, target)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	download := target + ".part"
	defer os.Remove(download)
	if err = fetcher.FetchArtifact(ctx, spec.Digest, spec.Size, download); err != nil {
		return "", err
	}
	if err = verifyFile(download, spec.Digest, spec.Size); err != nil {
		return "", err
	}
	// Extract into a staging directory and only then move it into place, so an
	// interrupted install never leaves a half-populated game directory that
	// later looks installed.
	staging := target + ".partdir"
	os.RemoveAll(staging)
	if err = Extract(ctx, download, staging, limits); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	if err = os.Rename(staging, target); err != nil {
		os.RemoveAll(staging)
		return "", err
	}
	return target, nil
}

// verifyFile checks size first (cheap) and then the digest over the full file.
func verifyFile(path, digest string, size int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: downloaded artifact is not a regular file", errUnsafeInstall)
	}
	if info.Size() != size {
		return fmt.Errorf("%w: artifact is %d bytes, release declares %d", errUnsafeInstall, info.Size(), size)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, digest) {
		return fmt.Errorf("%w: artifact digest %s does not match the signed %s", errUnsafeInstall, actual, digest)
	}
	return nil
}

// resolveInstallDir applies the same containment rule to the release-supplied
// relative path as the extractor applies to archive entries. The server already
// restricts it, but the client is the last line of defence.
func resolveInstallDir(gamesRoot, relativePath string) (string, error) {
	root, err := filepath.Abs(gamesRoot)
	if err != nil {
		return "", err
	}
	relative, err := safeRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, relative)
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes the games root", errUnsafeInstall, relativePath)
	}
	return target, nil
}
