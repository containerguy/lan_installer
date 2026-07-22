package discovery

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

const maxEAInstallerManifestSize = 2 << 20

const (
	maxInventoryExternalIDBytes  = 256
	maxInventoryDisplayNameBytes = 500
	maxInventoryVersionBytes     = 256
	maxInventoryInstallPathBytes = 4096
)

type eaInstallerManifest struct {
	GameVersion struct {
		Version string `xml:"version,attr"`
	} `xml:"buildMetaData>gameVersion"`
	LegacyGameVersion string   `xml:"gameVersion,attr"`
	ContentIDs        []string `xml:"contentIDs>contentID"`
}

func parseEAInstallerManifest(reader io.Reader) ([]string, string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxEAInstallerManifestSize+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxEAInstallerManifestSize {
		return nil, "", errors.New("EA installer manifest exceeds size limit")
	}
	data, wasUTF16, err := decodeUTF16BOM(data)
	if err != nil {
		return nil, "", err
	}
	var manifest eaInstallerManifest
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if wasUTF16 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(label)), "utf-16") {
			return input, nil
		}
		return charset.NewReaderLabel(label, input)
	}
	if err := decoder.Decode(&manifest); err != nil {
		return nil, "", err
	}
	ids := make([]string, 0, len(manifest.ContentIDs))
	seen := make(map[string]bool)
	for _, value := range manifest.ContentIDs {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			ids = append(ids, value)
		}
	}
	version := strings.TrimSpace(manifest.GameVersion.Version)
	if version == "" {
		version = strings.TrimSpace(manifest.LegacyGameVersion)
	}
	return ids, version, nil
}

func decodeUTF16BOM(data []byte) ([]byte, bool, error) {
	if len(data) < 2 || !((data[0] == 0xff && data[1] == 0xfe) || (data[0] == 0xfe && data[1] == 0xff)) {
		return data, false, nil
	}
	if (len(data)-2)%2 != 0 {
		return nil, true, errors.New("EA installer manifest has invalid UTF-16 length")
	}
	littleEndian := data[0] == 0xff
	units := make([]uint16, 0, (len(data)-2)/2)
	for index := 2; index < len(data); index += 2 {
		if littleEndian {
			units = append(units, uint16(data[index])|uint16(data[index+1])<<8)
		} else {
			units = append(units, uint16(data[index])<<8|uint16(data[index+1]))
		}
	}
	return []byte(string(utf16.Decode(units))), true, nil
}

func selectEAContentID(manifestIDs []string, knownOriginIDs map[string]string) (string, bool) {
	if len(manifestIDs) == 1 {
		return manifestIDs[0], true
	}
	var selected string
	for _, id := range manifestIDs {
		if _, ok := knownOriginIDs[id]; ok {
			if selected != "" {
				return "", false
			}
			selected = id
		}
	}
	return selected, selected != ""
}

func isSteamInstallPath(value string) bool {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "/", `\`))
	return strings.Contains(value, `\steamapps\common\`)
}

func isUnsafeWindowsInstallPath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "/", `\`))
	return strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, `\??\`)
}

func validEAExternalID(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxInventoryExternalIDBytes
}

func validEAVersion(value string) bool {
	return len(value) <= maxInventoryVersionBytes
}

func validEAInstallPath(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxInventoryInstallPathBytes && !isUnsafeWindowsInstallPath(value)
}

func truncateUTF8Bytes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}
