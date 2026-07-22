//go:build windows

package discovery

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/containerguy/lan_installer/internal/fileversion"
)

// launcherProbe describes how to find one launcher. Registry first because it
// survives a non-default install location; the fixed paths are the fallback for
// launchers that do not register a usable key.
type launcherProbe struct {
	adapter string
	// registryPaths are (root, key, value) triples pointing at an install dir.
	registryPaths []registryProbe
	// relativeExecutables are tried below each install dir, in order.
	relativeExecutables []string
	// absoluteFallbacks are full paths under the program files roots.
	absoluteFallbacks []string
}

type registryProbe struct {
	root  registry.Key
	path  string
	value string
}

var launcherProbes = []launcherProbe{
	{
		adapter: "steam",
		registryPaths: []registryProbe{
			{registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath"},
			{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"},
			{registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath"},
		},
		relativeExecutables: []string{"steam.exe"},
		absoluteFallbacks:   []string{`Steam\steam.exe`},
	},
	{
		adapter: "ea_app",
		registryPaths: []registryProbe{
			{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Electronic Arts\EA Desktop`, "DesktopAppPath"},
			{registry.LOCAL_MACHINE, `SOFTWARE\Electronic Arts\EA Desktop`, "DesktopAppPath"},
			{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Electronic Arts\EA Desktop`, "InstallLocation"},
		},
		relativeExecutables: []string{"EALauncher.exe", "EADesktop.exe"},
		absoluteFallbacks: []string{
			`Electronic Arts\EA Desktop\EA Desktop\EALauncher.exe`,
			`Electronic Arts\EA Desktop\EA Desktop\EADesktop.exe`,
			`Electronic Arts\EA Desktop\EALauncher.exe`,
		},
	},
	{
		adapter: "ubisoft_connect",
		registryPaths: []registryProbe{
			{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Ubisoft\Launcher`, "InstallDir"},
			{registry.LOCAL_MACHINE, `SOFTWARE\Ubisoft\Launcher`, "InstallDir"},
		},
		relativeExecutables: []string{"upc.exe", "UbisoftConnect.exe"},
		absoluteFallbacks:   []string{`Ubisoft\Ubisoft Game Launcher\upc.exe`},
	},
	{
		adapter: "battle_net",
		registryPaths: []registryProbe{
			{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Blizzard Entertainment\Battle.net`, "InstallPath"},
			{registry.LOCAL_MACHINE, `SOFTWARE\Blizzard Entertainment\Battle.net`, "InstallPath"},
		},
		relativeExecutables: []string{"Battle.net.exe", "Battle.net Launcher.exe"},
		absoluteFallbacks:   []string{`Battle.net\Battle.net.exe`, `Battle.net\Battle.net Launcher.exe`},
	},
}

// DiscoverLaunchers reports which launcher applications are installed,
// independently of whether any of their games are.
func DiscoverLaunchers() []InstalledLauncher {
	roots := programFilesRoots()
	found := make([]InstalledLauncher, 0, len(launcherProbes))
	for _, probe := range launcherProbes {
		if path := probe.locate(roots); path != "" {
			found = append(found, InstalledLauncher{
				Adapter:        probe.adapter,
				ExecutablePath: path,
				Version:        launcherVersion(path),
			})
		}
	}
	return found
}

func (p launcherProbe) locate(roots []string) string {
	for _, entry := range p.registryPaths {
		dir := registryString(entry.root, entry.path, entry.value)
		if dir == "" {
			continue
		}
		for _, relative := range p.relativeExecutables {
			// The registry value sometimes already points at the executable.
			if strings.EqualFold(filepath.Ext(dir), ".exe") {
				if regularFile(dir) {
					return dir
				}
				break
			}
			if candidate := filepath.Join(dir, relative); regularFile(candidate) {
				return candidate
			}
		}
	}
	for _, root := range roots {
		for _, relative := range p.absoluteFallbacks {
			if candidate := filepath.Join(root, relative); regularFile(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// launcherVersion returns the Windows file version when present. An absent
// version is not an error: the launcher is installed either way, and guessing
// a version would be worse than reporting none.
func launcherVersion(path string) string {
	version, err := fileversion.Read(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(version)
}

func registryString(root registry.Key, path, value string) string {
	for _, access := range []uint32{registry.QUERY_VALUE, registry.QUERY_VALUE | registry.WOW64_32KEY} {
		key, err := registry.OpenKey(root, path, access)
		if err != nil {
			continue
		}
		text, _, err := key.GetStringValue(value)
		key.Close()
		if err == nil && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func programFilesRoots() []string {
	roots := make([]string, 0, 3)
	for _, name := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			roots = append(roots, value)
		}
	}
	return roots
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
