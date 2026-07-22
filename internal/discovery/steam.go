package discovery

import (
	"bufio"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

var vdfPair = regexp.MustCompile(`^\s*"([^"]+)"\s+"([^"]*)"\s*$`)

type vdfValue struct{ Key, Value string }

func parseVDFValues(reader io.Reader) ([]vdfValue, error) {
	var values []vdfValue
	scanner := bufio.NewScanner(io.LimitReader(reader, 4<<20))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		match := vdfPair.FindStringSubmatch(scanner.Text())
		if len(match) == 3 {
			values = append(values, vdfValue{Key: strings.ToLower(match[1]), Value: strings.ReplaceAll(match[2], `\\`, `\`)})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("VDF contains no key/value pairs")
	}
	return values, nil
}

func parseVDFPairs(reader io.Reader) (map[string]string, error) {
	pairs, err := parseVDFValues(reader)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, pair := range pairs {
		values[pair.Key] = pair.Value
	}
	return values, nil
}

func steamInstallation(manifestPath string, values map[string]string) (Installation, error) {
	appid, name, directory := strings.TrimSpace(values["appid"]), strings.TrimSpace(values["name"]), strings.TrimSpace(values["installdir"])
	if appid == "" || name == "" || directory == "" {
		return Installation{}, errors.New("Steam manifest lacks appid, name or installdir")
	}
	var version *string
	versionSource := "steam-buildid-unavailable"
	if build := strings.TrimSpace(values["buildid"]); build != "" && build != "0" {
		version = &build
		versionSource = "steam-buildid"
	}
	return Installation{Launcher: "steam", ExternalGameID: appid, DisplayName: name, DetectedVersion: version, VersionSource: versionSource, InstallPath: filepath.Clean(filepath.Join(filepath.Dir(manifestPath), "common", directory))}, nil
}
