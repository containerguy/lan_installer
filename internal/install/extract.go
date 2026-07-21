// Package install materialises signed LANReady payloads on a client machine.
//
// Extraction is a trust boundary: the archive bytes are verified against a
// signed digest, but the names inside the archive are attacker-controlled in
// exactly the same way as any downloaded ZIP. Every entry is therefore
// validated against the extraction root before anything is written, and the
// resolved path is checked again after joining so no symlink or normalisation
// quirk can move a write outside.
package install

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Limits bound what a single archive may materialise. They apply regardless of
// what the release claims, so a mis-declared or hostile archive cannot fill the
// disk.
type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits suits current game packages: a few tens of thousands of files
// and up to 64 GiB unpacked.
var DefaultLimits = Limits{MaxFiles: 200000, MaxFileBytes: 64 << 30, MaxTotalBytes: 64 << 30}

var errUnsafeEntry = errors.New("archive entry is unsafe")

// windowsReservedNames are refused on every platform. The archive is destined
// for Windows clients, and accepting them here would only move the failure to
// the machine that matters.
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// Extract unpacks archivePath below targetDir. targetDir is created if needed
// and must end up containing every written path.
func Extract(ctx context.Context, archivePath, targetDir string, limits Limits) error {
	root, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	// Resolve symlinks once so the containment check compares real paths: if
	// the root itself is a link, every later comparison must use its target.
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if len(reader.File) > limits.MaxFiles {
		return fmt.Errorf("%w: archive holds %d entries, limit is %d", errUnsafeEntry, len(reader.File), limits.MaxFiles)
	}
	var total int64
	for _, entry := range reader.File {
		if err = ctx.Err(); err != nil {
			return err
		}
		relative, err := safeRelativePath(entry.Name)
		if err != nil {
			return err
		}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q is a symlink", errUnsafeEntry, entry.Name)
		}
		if isDirEntry(entry.Name, mode) {
			if err = os.MkdirAll(filepath.Join(root, relative), 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("%w: %q is not a regular file", errUnsafeEntry, entry.Name)
		}
		declared := int64(entry.UncompressedSize64)
		if declared > limits.MaxFileBytes {
			return fmt.Errorf("%w: %q declares %d bytes, limit is %d", errUnsafeEntry, entry.Name, declared, limits.MaxFileBytes)
		}
		if total+declared > limits.MaxTotalBytes {
			return fmt.Errorf("%w: archive exceeds the %d byte budget", errUnsafeEntry, limits.MaxTotalBytes)
		}
		written, err := writeEntry(entry, root, relative, limits.MaxFileBytes)
		if err != nil {
			return err
		}
		total += written
		if total > limits.MaxTotalBytes {
			return fmt.Errorf("%w: archive exceeds the %d byte budget", errUnsafeEntry, limits.MaxTotalBytes)
		}
	}
	return nil
}

func writeEntry(entry *zip.File, root, relative string, maxFileBytes int64) (int64, error) {
	destination := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return 0, err
	}
	// Re-check after joining and resolving the parent: a directory created by
	// an earlier entry, or a pre-existing link on disk, must not redirect the
	// write outside the root.
	if err := assertInsideRoot(root, destination); err != nil {
		return 0, err
	}
	source, err := entry.Open()
	if err != nil {
		return 0, err
	}
	defer source.Close()
	// O_EXCL: never follow or overwrite something already at that path.
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(file, io.LimitReader(source, maxFileBytes+1))
	if err != nil {
		file.Close()
		os.Remove(destination)
		return 0, err
	}
	if err = file.Close(); err != nil {
		os.Remove(destination)
		return 0, err
	}
	if written > maxFileBytes {
		os.Remove(destination)
		return 0, fmt.Errorf("%w: %q exceeds %d bytes while unpacking", errUnsafeEntry, entry.Name, maxFileBytes)
	}
	return written, nil
}

// assertInsideRoot verifies that destination resolves below root even after the
// filesystem has had its say about links.
func assertInsideRoot(root, destination string) error {
	parent := filepath.Dir(destination)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	resolved := filepath.Join(resolvedParent, filepath.Base(destination))
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return fmt.Errorf("%w: %q escapes the extraction directory", errUnsafeEntry, destination)
	}
	return nil
}

// safeRelativePath converts an archive entry name into a relative path that is
// safe on both Unix and Windows, or refuses it.
func safeRelativePath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty entry name", errUnsafeEntry)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: %q contains a control character", errUnsafeEntry, name)
		}
	}
	// Windows treats both separators alike, so normalise before validating.
	// Without this, "..\\.." would survive a slash-only check.
	unified := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(unified, "/") {
		return "", fmt.Errorf("%w: %q is absolute", errUnsafeEntry, name)
	}
	if len(unified) >= 2 && unified[1] == ':' {
		return "", fmt.Errorf("%w: %q is drive-relative", errUnsafeEntry, name)
	}
	cleaned := path.Clean(unified)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q escapes the extraction directory", errUnsafeEntry, name)
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if err := checkSegment(segment, name); err != nil {
			return "", err
		}
	}
	return filepath.FromSlash(cleaned), nil
}

func checkSegment(segment, name string) error {
	if segment == "" || segment == "." || segment == ".." {
		return fmt.Errorf("%w: %q has an invalid path segment", errUnsafeEntry, name)
	}
	// Windows silently strips trailing dots and spaces, so "evil.txt." and
	// "evil.txt" would collide; refuse rather than guess.
	if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
		return fmt.Errorf("%w: %q has a trailing dot or space", errUnsafeEntry, name)
	}
	// A colon would create an NTFS alternate data stream instead of a file.
	if strings.ContainsAny(segment, `:*?"<>|`) {
		return fmt.Errorf("%w: %q contains a character Windows reserves", errUnsafeEntry, name)
	}
	base := strings.ToLower(segment)
	if index := strings.IndexByte(base, '.'); index >= 0 {
		base = base[:index]
	}
	if windowsReservedNames[base] {
		return fmt.Errorf("%w: %q uses the reserved device name %q", errUnsafeEntry, name, base)
	}
	return nil
}

func isDirEntry(name string, mode os.FileMode) bool {
	return mode.IsDir() || strings.HasSuffix(name, "/") || strings.HasSuffix(name, `\`)
}
