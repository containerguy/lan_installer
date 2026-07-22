package install

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeArchive builds a ZIP from raw entry names so tests can express exactly
// the byte-level names an attacker would craft, including ones the standard
// writer would normally sanitise.
func writeArchive(t *testing.T, entries []zip.FileHeader, bodies []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	for index, header := range entries {
		entry := header
		target, err := writer.CreateRaw(&zip.FileHeader{
			Name:               entry.Name,
			Method:             zip.Store,
			CreatorVersion:     entry.CreatorVersion,
			ExternalAttrs:      entry.ExternalAttrs,
			UncompressedSize64: uint64(len(bodies[index])),
			CompressedSize64:   uint64(len(bodies[index])),
			CRC32:              crc(bodies[index]),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = target.Write([]byte(bodies[index])); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func crc(body string) uint32 {
	table := uint32(0xFFFFFFFF)
	for _, b := range []byte(body) {
		table ^= uint32(b)
		for i := 0; i < 8; i++ {
			if table&1 != 0 {
				table = (table >> 1) ^ 0xEDB88320
			} else {
				table >>= 1
			}
		}
	}
	return table ^ 0xFFFFFFFF
}

// The whole point of this slice is that a signed release now makes the client
// write files. Every one of these archives must be refused before a single byte
// lands outside the extraction directory.
func TestExtractRefusesEscapingEntries(t *testing.T) {
	for name, entry := range map[string]string{
		"parent traversal":          "../escaped.txt",
		"nested traversal":          "game/../../escaped.txt",
		"absolute unix":             "/etc/cron.d/escaped",
		"windows drive":             `C:\Windows\System32\escaped.dll`,
		"windows unc":               `\\server\share\escaped`,
		"backslash traversal":       `..\..\escaped.txt`,
		"backslash nested":          `game\..\..\escaped.txt`,
		"leading slash":             "/escaped.txt",
		"current dir segment":       "./../escaped.txt",
		"trailing dot windows":      "escaped.txt.",
		"trailing space windows":    "escaped.txt ",
		"reserved device name":      "CON",
		"reserved device extension": "COM1.txt",
		"alternate data stream":     "game.exe:evil",
		"control character":         "game\x01.txt",
	} {
		t.Run(name, func(t *testing.T) {
			archive := writeArchive(t, []zip.FileHeader{{Name: entry}}, []string{"payload"})
			target := isolatedTarget(t)
			err := Extract(context.Background(), archive, target, DefaultLimits)
			if err == nil {
				t.Fatalf("archive entry %q was accepted", entry)
			}
			assertNothingOutside(t, target)
		})
	}
}

func TestExtractRefusesSymlinkEntries(t *testing.T) {
	// A symlink entry would let a later write follow the link outside the
	// extraction directory, so the entry itself must be refused.
	header := zip.FileHeader{Name: "link", CreatorVersion: 3 << 8}
	header.SetMode(os.ModeSymlink | 0o777)
	archive := writeArchive(t, []zip.FileHeader{header}, []string{"/etc/passwd"})
	target := isolatedTarget(t)
	if err := Extract(context.Background(), archive, target, DefaultLimits); err == nil {
		t.Fatal("symlink entry was accepted")
	}
	assertNothingOutside(t, target)
}

func TestExtractEnforcesLimits(t *testing.T) {
	archive := writeArchive(t, []zip.FileHeader{{Name: "big.bin"}}, []string{strings.Repeat("a", 512)})
	target := isolatedTarget(t)
	if err := Extract(context.Background(), archive, target, Limits{MaxFiles: 10, MaxFileBytes: 16, MaxTotalBytes: 1024}); err == nil {
		t.Fatal("oversized file was accepted")
	}
	if err := Extract(context.Background(), archive, target, Limits{MaxFiles: 10, MaxFileBytes: 1024, MaxTotalBytes: 64}); err == nil {
		t.Fatal("archive over the total budget was accepted")
	}
	if err := Extract(context.Background(), archive, target, Limits{MaxFiles: 0, MaxFileBytes: 1024, MaxTotalBytes: 1024}); err == nil {
		t.Fatal("archive over the file count budget was accepted")
	}
}

func TestExtractWritesContainedTree(t *testing.T) {
	entries := []zip.FileHeader{{Name: "game/"}, {Name: "game/start.exe"}, {Name: "game/data/list.txt"}}
	archive := writeArchive(t, entries, []string{"", "binary", "content"})
	target := isolatedTarget(t)
	if err := Extract(context.Background(), archive, target, DefaultLimits); err != nil {
		t.Fatalf("contained archive was refused: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(target, "game", "data", "list.txt"))
	if err != nil || string(body) != "content" {
		t.Fatalf("nested file: %q %v", body, err)
	}
	if _, err = os.Stat(filepath.Join(target, "game", "start.exe")); err != nil {
		t.Fatal(err)
	}
	assertNothingOutside(t, target)
}

// isolatedTarget returns an extraction directory whose parent contains nothing
// else, so any escape shows up as a stray sibling.
func isolatedTarget(t *testing.T) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "extract")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	return target
}

// assertNothingOutside guards against a refusal that still left debris behind:
// a partially written escaping file is just as bad as a fully written one.
func assertNothingOutside(t *testing.T, target string) {
	t.Helper()
	parent := filepath.Dir(target)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Join(parent, entry.Name()) != target {
			t.Fatalf("extraction created %q outside the target directory", entry.Name())
		}
	}
}
