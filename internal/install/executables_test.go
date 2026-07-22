package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, relative string, size int) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindExecutablesRanksLikelyMainProgramFirst(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "FlatOut2.exe", 12<<20)
	writeFile(t, root, "unins000.exe", 20<<20)
	writeFile(t, root, "redist/vcredist_x86.exe", 30<<20)
	writeFile(t, root, "tools/editor.exe", 5<<20)
	writeFile(t, root, "data/readme.txt", 100)
	candidates, err := FindExecutables(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 4 {
		t.Fatalf("expected the four executables, got %d: %#v", len(candidates), candidates)
	}
	// The uninstaller and the redistributable are larger, so size alone must
	// not win: the game executable has to rank first.
	if candidates[0].RelativePath != "FlatOut2.exe" {
		t.Fatalf("main program did not rank first: %#v", candidates)
	}
	for _, candidate := range candidates {
		if strings.HasSuffix(candidate.RelativePath, ".txt") {
			t.Fatal("non-executable was listed")
		}
	}
}

func TestFindExecutablesStopsAtDepthLimit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a/b/c/d/e/f/g/deep.exe", 1<<20)
	writeFile(t, root, "shallow.exe", 1<<20)
	candidates, err := FindExecutables(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate.RelativePath, "deep.exe") {
			t.Fatalf("scan went past the depth limit: %#v", candidates)
		}
	}
	if len(candidates) != 1 || candidates[0].RelativePath != "shallow.exe" {
		t.Fatalf("candidates: %#v", candidates)
	}
}

func TestFindExecutablesHandlesMissingDirectory(t *testing.T) {
	if _, err := FindExecutables(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing directory was accepted")
	}
}
