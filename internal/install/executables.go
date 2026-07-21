package install

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExecutableCandidate is one .exe found in an installed game directory,
// together with why it might be the main program.
type ExecutableCandidate struct {
	// RelativePath is relative to the install directory, using the host
	// separator, and is what the UI shows.
	RelativePath string
	SizeBytes    int64
	// Score ranks likely main programs first. It is a presentation hint only:
	// the user always makes the final choice, because a wrong guess would
	// silently misreport the installed version.
	Score int
}

// scanLimits bound the walk so a hostile or simply huge archive cannot stall
// the UI. A game's main executable is never buried deeper than this.
const (
	maxScanDepth   = 6
	maxScanEntries = 20000
	maxCandidates  = 200
)

// helperNameHints mark executables that are almost never the game itself.
var helperNameHints = []string{
	"unins", "uninstall", "setup", "install", "redist", "vcredist", "directx",
	"dxsetup", "dotnet", "crashreport", "crashhandler", "report", "updater",
	"update", "launcher_helper", "config", "settings", "benchmark", "editor",
	"server", "dedicated", "tool", "cleanup", "repair",
}

// FindExecutables lists .exe files below dir, best guesses first, so the user
// can pick the main program without browsing the whole tree.
func FindExecutables(dir string) ([]ExecutableCandidate, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	// A missing or non-directory install path is a real failure the caller must
	// see; only errors *below* the root are tolerated so one unreadable folder
	// cannot hide every candidate.
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", root)
	}
	var candidates []ExecutableCandidate
	seen := 0
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory must not abort the whole scan; the
			// user can still pick from what was found.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		seen++
		if seen > maxScanEntries || len(candidates) >= maxCandidates {
			return fs.SkipAll
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		depth := len(strings.Split(filepath.ToSlash(relative), "/"))
		if entry.IsDir() {
			if depth > maxScanDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".exe") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		candidates = append(candidates, ExecutableCandidate{
			RelativePath: relative,
			SizeBytes:    info.Size(),
			Score:        scoreExecutable(relative, info.Size(), depth),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].SizeBytes != candidates[j].SizeBytes {
			return candidates[i].SizeBytes > candidates[j].SizeBytes
		}
		return candidates[i].RelativePath < candidates[j].RelativePath
	})
	return candidates, nil
}

// scoreExecutable is a deliberately simple heuristic: shallow, large files that
// do not look like helpers rank first.
func scoreExecutable(relative string, size int64, depth int) int {
	score := 0
	name := strings.ToLower(filepath.Base(relative))
	base := strings.TrimSuffix(name, filepath.Ext(name))
	for _, hint := range helperNameHints {
		if strings.Contains(base, hint) {
			score -= 50
			break
		}
	}
	switch depth {
	case 1:
		score += 30
	case 2:
		score += 20
	case 3:
		score += 10
	}
	switch {
	case size >= 8<<20:
		score += 20
	case size >= 1<<20:
		score += 10
	case size < 64<<10:
		score -= 10
	}
	return score
}
