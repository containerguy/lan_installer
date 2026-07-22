package install

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stubFetcher stands in for the signed CAS download and can be told to deliver
// bytes other than the ones the release promised.
type stubFetcher struct {
	body     []byte
	calls    int
	failWith error
}

func (f *stubFetcher) FetchArtifact(_ context.Context, _ string, _ int64, destination string) error {
	f.calls++
	if f.failWith != nil {
		return f.failWith
	}
	return os.WriteFile(destination, f.body, 0o644)
}

func archiveBytes(t *testing.T) []byte {
	t.Helper()
	path := writeArchive(t, []zip.FileHeader{{Name: "game/"}, {Name: "game/start.exe"}}, []string{"", "binary"})
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestInstallArchiveExtractsVerifiedPayload(t *testing.T) {
	body := archiveBytes(t)
	root := isolatedTarget(t)
	fetcher := &stubFetcher{body: body}
	spec := ArchiveInstall{GameID: "flatout2", Digest: digestOf(body), Size: int64(len(body)), RelativePath: "flatout2"}
	dir, err := InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "game", "start.exe")); err != nil {
		t.Fatalf("extracted payload missing: %v", err)
	}
	// No staging or partial download may survive a successful install.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "flatout2" {
		t.Fatalf("install left debris: %v", entries)
	}
}

// The signed digest is the only thing tying the downloaded bytes to the
// release, so a mismatch must stop the install before extraction.
func TestInstallArchiveRejectsDigestMismatch(t *testing.T) {
	body := archiveBytes(t)
	root := isolatedTarget(t)
	fetcher := &stubFetcher{body: body}
	spec := ArchiveInstall{GameID: "flatout2", Digest: digestOf([]byte("different archive")), Size: int64(len(body)), RelativePath: "flatout2"}
	if _, err := InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, nil); err == nil {
		t.Fatal("substituted artifact was installed")
	}
	assertOnlyDebrisFree(t, root)
}

func TestInstallArchiveRejectsSizeMismatch(t *testing.T) {
	body := archiveBytes(t)
	root := isolatedTarget(t)
	fetcher := &stubFetcher{body: body}
	spec := ArchiveInstall{GameID: "flatout2", Digest: digestOf(body), Size: int64(len(body)) + 1, RelativePath: "flatout2"}
	if _, err := InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, nil); err == nil {
		t.Fatal("artifact with unexpected size was installed")
	}
	assertOnlyDebrisFree(t, root)
}

// The relative path comes from the release. The server constrains it, but the
// client must not rely on that.
func TestInstallArchiveRejectsEscapingRelativePath(t *testing.T) {
	body := archiveBytes(t)
	for _, relative := range []string{"../escaped", `..\escaped`, "/abs", `C:\Windows`, "game/../../escaped", "CON"} {
		root := isolatedTarget(t)
		fetcher := &stubFetcher{body: body}
		spec := ArchiveInstall{GameID: "x", Digest: digestOf(body), Size: int64(len(body)), RelativePath: relative}
		if _, err := InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, nil); err == nil {
			t.Fatalf("relative path %q was accepted", relative)
		}
		if fetcher.calls != 0 {
			t.Fatalf("relative path %q was rejected only after downloading", relative)
		}
		assertOnlyDebrisFree(t, root)
	}
}

// A malicious archive must not leave a directory that later looks like a
// successful install.
func TestInstallArchiveLeavesNothingWhenExtractionFails(t *testing.T) {
	hostile := writeArchive(t, []zip.FileHeader{{Name: "../escaped.txt"}}, []string{"payload"})
	body, err := os.ReadFile(hostile)
	if err != nil {
		t.Fatal(err)
	}
	root := isolatedTarget(t)
	fetcher := &stubFetcher{body: body}
	spec := ArchiveInstall{GameID: "x", Digest: digestOf(body), Size: int64(len(body)), RelativePath: "x"}
	if _, err = InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, nil); err == nil {
		t.Fatal("hostile archive was installed")
	}
	assertOnlyDebrisFree(t, root)
	assertNothingOutside(t, root)
}

func TestInstallArchiveRefusesExistingTarget(t *testing.T) {
	body := archiveBytes(t)
	root := isolatedTarget(t)
	if err := os.MkdirAll(filepath.Join(root, "flatout2"), 0o755); err != nil {
		t.Fatal(err)
	}
	fetcher := &stubFetcher{body: body}
	spec := ArchiveInstall{GameID: "flatout2", Digest: digestOf(body), Size: int64(len(body)), RelativePath: "flatout2"}
	if _, err := InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, nil); err == nil {
		t.Fatal("install overwrote an existing directory")
	}
	if fetcher.calls != 0 {
		t.Fatal("existing target was detected only after downloading")
	}
}

func TestInstallArchivePropagatesFetchFailure(t *testing.T) {
	root := isolatedTarget(t)
	failure := errors.New("network down")
	fetcher := &stubFetcher{failWith: failure}
	spec := ArchiveInstall{GameID: "x", Digest: digestOf([]byte("x")), Size: 1, RelativePath: "x"}
	if _, err := InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, nil); !errors.Is(err, failure) {
		t.Fatalf("fetch failure was not propagated: %v", err)
	}
	assertOnlyDebrisFree(t, root)
}

// assertOnlyDebrisFree verifies the games root holds no partial download or
// staging directory after a failed or finished install.
func assertOnlyDebrisFree(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) == ".part" || filepath.Ext(name) == ".partdir" {
			t.Fatalf("install left %q behind", name)
		}
	}
}

// A slow disk makes verification and extraction take longer than the download.
// Without stage reporting the UI cannot tell a working install from a hang.
func TestInstallArchiveReportsEveryStage(t *testing.T) {
	body := archiveBytes(t)
	root := isolatedTarget(t)
	fetcher := &stubFetcher{body: body}
	spec := ArchiveInstall{GameID: "g", Digest: digestOf(body), Size: int64(len(body)), RelativePath: "g"}
	var stages []string
	if _, err := InstallArchive(context.Background(), fetcher, spec, root, DefaultLimits, func(p Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range stages {
		seen[s] = true
	}
	for _, want := range []string{StageVerify, StageExtract} {
		if !seen[want] {
			t.Fatalf("stage %q was never reported, got %v", want, stages)
		}
	}
}
