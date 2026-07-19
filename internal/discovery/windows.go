//go:build windows

package discovery

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func Discover() (Result, error) {
	result := Result{Installations: make([]Installation, 0)}
	steam, warnings := discoverSteam()
	result.Installations = append(result.Installations, steam...)
	result.Warnings = append(result.Warnings, warnings...)
	ea, warnings := discoverRegistryGames("ea_app", []string{`SOFTWARE\Origin Games`, `SOFTWARE\WOW6432Node\Origin Games`})
	result.Installations = append(result.Installations, ea...)
	result.Warnings = append(result.Warnings, warnings...)
	ubisoft, warnings := discoverRegistryGames("ubisoft_connect", []string{`SOFTWARE\Ubisoft\Launcher\Installs`, `SOFTWARE\WOW6432Node\Ubisoft\Launcher\Installs`})
	result.Installations = append(result.Installations, ubisoft...)
	result.Warnings = append(result.Warnings, warnings...)
	result.Installations = deduplicate(result.Installations)
	sort.Slice(result.Installations, func(i, j int) bool {
		if result.Installations[i].Launcher == result.Installations[j].Launcher {
			return result.Installations[i].DisplayName < result.Installations[j].DisplayName
		}
		return result.Installations[i].Launcher < result.Installations[j].Launcher
	})
	return result, nil
}

func discoverSteam() ([]Installation, []string) {
	roots := make(map[string]bool)
	for _, candidate := range []struct {
		root        registry.Key
		path, value string
	}{{registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath"}, {registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath"}, {registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"}} {
		key, err := registry.OpenKey(candidate.root, candidate.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, readErr := key.GetStringValue(candidate.value)
		key.Close()
		if readErr == nil && value != "" {
			roots[filepath.Clean(filepath.FromSlash(value))] = true
		}
	}
	var warnings []string
	for root := range roots {
		libraryFile := filepath.Join(root, "steamapps", "libraryfolders.vdf")
		file, err := os.Open(libraryFile)
		if err == nil {
			values, parseErr := parseVDFValues(file)
			file.Close()
			if parseErr == nil {
				for _, pair := range values {
					if pair.Key == "path" || allDigits(pair.Key) {
						roots[filepath.Clean(pair.Value)] = true
					}
				}
			} else {
				warnings = append(warnings, "Steam-Bibliotheken konnten nicht vollständig gelesen werden.")
			}
		}
	}
	var out []Installation
	for root := range roots {
		manifests, _ := filepath.Glob(filepath.Join(root, "steamapps", "appmanifest_*.acf"))
		for _, manifest := range manifests {
			file, err := os.Open(manifest)
			if err != nil {
				continue
			}
			values, parseErr := parseVDFPairs(file)
			file.Close()
			if parseErr != nil {
				warnings = append(warnings, "Ein Steam-Manifest ist ungültig: "+filepath.Base(manifest))
				continue
			}
			installation, parseErr := steamInstallation(manifest, values)
			if parseErr == nil {
				out = append(out, installation)
			}
		}
	}
	return out, warnings
}

func discoverRegistryGames(launcher string, roots []string) ([]Installation, []string) {
	var out []Installation
	for _, rootPath := range roots {
		root, err := registry.OpenKey(registry.LOCAL_MACHINE, rootPath, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, _ := root.ReadSubKeyNames(-1)
		root.Close()
		for _, externalID := range names {
			key, openErr := registry.OpenKey(registry.LOCAL_MACHINE, rootPath+`\`+externalID, registry.QUERY_VALUE)
			if openErr != nil {
				continue
			}
			installPath := firstRegistryString(key, "InstallDir", "Install Dir", "InstallLocation")
			displayName := firstRegistryString(key, "DisplayName", "Display Name")
			versionValue := firstRegistryString(key, "DisplayVersion", "Version")
			key.Close()
			if installPath == "" {
				continue
			}
			if displayName == "" {
				displayName = filepath.Base(filepath.Clean(installPath))
			}
			var version *string
			versionSource := launcher + "-registry-version-unavailable"
			if versionValue != "" {
				version = &versionValue
				versionSource = launcher + "-registry-version"
			}
			out = append(out, Installation{Launcher: launcher, ExternalGameID: externalID, DisplayName: displayName, DetectedVersion: version, VersionSource: versionSource, InstallPath: filepath.Clean(installPath)})
		}
	}
	return out, nil
}

func firstRegistryString(key registry.Key, names ...string) string {
	for _, name := range names {
		if value, _, err := key.GetStringValue(name); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func deduplicate(values []Installation) []Installation {
	seen := make(map[string]bool)
	out := make([]Installation, 0, len(values))
	for _, value := range values {
		key := value.Launcher + "\x00" + value.ExternalGameID + "\x00" + strings.ToLower(value.InstallPath)
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}
