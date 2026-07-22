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
	ea, warnings := discoverEA()
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

type eaRegistryCandidate struct {
	displayName      string
	version          string
	installPath      string
	gameRegistryHint bool
}

func discoverEA() ([]Installation, []string) {
	originIDs := make(map[string]string)
	for _, rootPath := range []string{`SOFTWARE\Origin Games`, `SOFTWARE\WOW6432Node\Origin Games`} {
		root, err := registry.OpenKey(registry.LOCAL_MACHINE, rootPath, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, _ := root.ReadSubKeyNames(-1)
		root.Close()
		for _, id := range names {
			key, openErr := registry.OpenKey(registry.LOCAL_MACHINE, rootPath+`\`+id, registry.QUERY_VALUE)
			if openErr != nil {
				continue
			}
			originIDs[id] = firstRegistryString(key, "DisplayName", "Display Name")
			key.Close()
		}
	}

	candidates := make([]eaRegistryCandidate, 0)
	for _, rootPath := range []string{`SOFTWARE\EA Games`, `SOFTWARE\WOW6432Node\EA Games`} {
		candidates = append(candidates, readEAGameRegistryCandidates(rootPath)...)
	}
	for _, rootPath := range []string{
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`,
		`SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`,
	} {
		candidates = append(candidates, readEAUninstallCandidates(rootPath)...)
	}

	candidates = mergeEARegistryCandidates(candidates)
	installations := make([]Installation, 0, len(candidates))
	warnings := make([]string, 0)
	for _, candidate := range candidates {
		if candidate.installPath == "" || isSteamInstallPath(candidate.installPath) {
			continue
		}
		if !validEAInstallPath(candidate.installPath) {
			if candidate.gameRegistryHint {
				warnings = append(warnings, "Ein EA-Spielpfad ist nicht für die lokale Inventarisierung geeignet.")
			}
			continue
		}
		info, err := os.Stat(candidate.installPath)
		if err != nil || !info.IsDir() {
			if candidate.gameRegistryHint && err != nil && !os.IsNotExist(err) {
				warnings = append(warnings, "Auf ein erkanntes EA-Spielverzeichnis konnte nicht zugegriffen werden.")
			}
			continue
		}
		version := strings.TrimSpace(candidate.version)
		versionSource := "ea_app-registry-version"
		manifestPath := filepath.Join(candidate.installPath, "__Installer", "installerdata.xml")
		manifest, openErr := os.Open(manifestPath)
		if openErr != nil {
			if candidate.gameRegistryHint {
				warnings = append(warnings, "Die stabile EA-Spiel-ID konnte nicht aus dem Installationsmanifest gelesen werden.")
			}
			continue
		}
		ids, manifestVersion, parseErr := parseEAInstallerManifest(manifest)
		manifest.Close()
		if parseErr != nil {
			warnings = append(warnings, "Ein EA-Installationsmanifest ist ungültig; das betroffene Spiel wurde ausgelassen.")
			continue
		}
		externalID, idOK := selectEAContentID(ids, originIDs)
		if !idOK || !validEAExternalID(externalID) {
			warnings = append(warnings, "Für ein EA-Spiel konnte keine eindeutige, zulässige Content-ID bestimmt werden.")
			continue
		}
		if manifestVersion != "" {
			version = manifestVersion
			versionSource = "ea_app-installer-manifest"
		}
		if !validEAVersion(version) {
			version = ""
			warnings = append(warnings, "Eine EA-Spielversion war zu lang und wurde als unbekannt behandelt.")
		}
		displayName := strings.TrimSpace(candidate.displayName)
		if displayName == "" {
			displayName = filepath.Base(filepath.Clean(candidate.installPath))
		}
		displayName = truncateUTF8Bytes(displayName, maxInventoryDisplayNameBytes)
		if displayName == "" {
			warnings = append(warnings, "Ein EA-Spiel ohne gültigen Anzeigenamen wurde ausgelassen.")
			continue
		}
		var detectedVersion *string
		if version != "" {
			detectedVersion = &version
		} else {
			versionSource = "ea_app-version-unavailable"
		}
		installations = append(installations, Installation{
			Launcher:        "ea_app",
			ExternalGameID:  externalID,
			DisplayName:     displayName,
			DetectedVersion: detectedVersion,
			VersionSource:   versionSource,
			InstallPath:     filepath.Clean(candidate.installPath),
		})
	}
	return installations, warnings
}

func mergeEARegistryCandidates(values []eaRegistryCandidate) []eaRegistryCandidate {
	out := make([]eaRegistryCandidate, 0, len(values))
	byPath := make(map[string]int)
	for _, value := range values {
		if strings.TrimSpace(value.installPath) == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(value.installPath))
		if index, ok := byPath[key]; ok {
			current := &out[index]
			if current.displayName == "" {
				current.displayName = value.displayName
			}
			if current.version == "" {
				current.version = value.version
			}
			current.gameRegistryHint = current.gameRegistryHint || value.gameRegistryHint
			continue
		}
		byPath[key] = len(out)
		out = append(out, value)
	}
	return out
}

func readEAGameRegistryCandidates(rootPath string) []eaRegistryCandidate {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, rootPath, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	names, _ := root.ReadSubKeyNames(-1)
	root.Close()
	out := make([]eaRegistryCandidate, 0, len(names))
	for _, name := range names {
		key, openErr := registry.OpenKey(registry.LOCAL_MACHINE, rootPath+`\`+name, registry.QUERY_VALUE)
		if openErr != nil {
			continue
		}
		out = append(out, eaRegistryCandidate{
			displayName:      firstRegistryString(key, "DisplayName", "Display Name"),
			installPath:      firstRegistryString(key, "InstallDir", "Install Dir", "InstallLocation"),
			gameRegistryHint: true,
		})
		key.Close()
	}
	return out
}

func readEAUninstallCandidates(rootPath string) []eaRegistryCandidate {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, rootPath, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	names, _ := root.ReadSubKeyNames(-1)
	root.Close()
	out := make([]eaRegistryCandidate, 0)
	for _, name := range names {
		key, openErr := registry.OpenKey(registry.LOCAL_MACHINE, rootPath+`\`+name, registry.QUERY_VALUE)
		if openErr != nil {
			continue
		}
		publisher := strings.ToLower(firstRegistryString(key, "Publisher"))
		displayName := firstRegistryString(key, "DisplayName", "Display Name")
		installPath := firstRegistryString(key, "InstallLocation", "Install Dir", "InstallDir")
		version := firstRegistryString(key, "DisplayVersion", "Version")
		key.Close()
		if !strings.Contains(publisher, "electronic arts") || strings.EqualFold(strings.TrimSpace(displayName), "EA app") || installPath == "" {
			continue
		}
		out = append(out, eaRegistryCandidate{displayName: displayName, version: version, installPath: installPath})
	}
	return out
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
