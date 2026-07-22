package discovery

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestParseModernEAInstallerManifest(t *testing.T) {
	ids, version, err := parseEAInstallerManifest(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<DiPManifest version="4.0">
  <buildMetaData><gameVersion version="1.0.434.61343" /></buildMetaData>
  <contentIDs><contentID>16426154</contentID></contentIDs>
</DiPManifest>`))
	if err != nil {
		t.Fatal(err)
	}
	if version != "1.0.434.61343" || len(ids) != 1 || ids[0] != "16426154" {
		t.Fatalf("unexpected manifest result: ids=%v version=%q", ids, version)
	}
}

func TestParseLegacyEAInstallerManifest(t *testing.T) {
	ids, version, err := parseEAInstallerManifest(strings.NewReader(`<?xml version="1.0"?>
<game gameVersion="1.6.0.0" manifestVersion="2.1">
  <contentIDs><contentID>70619</contentID><contentID>71067</contentID></contentIDs>
</game>`))
	if err != nil {
		t.Fatal(err)
	}
	if version != "1.6.0.0" || len(ids) != 2 {
		t.Fatalf("unexpected manifest result: ids=%v version=%q", ids, version)
	}
}

func TestParseUTF16LegacyEAInstallerManifest(t *testing.T) {
	text := `<?xml version="1.0" encoding="utf-16"?><game gameVersion="1.6.0.0"><contentIDs><contentID>70619</contentID><contentID>71067</contentID></contentIDs></game>`
	data := []byte{0xff, 0xfe}
	for _, unit := range utf16.Encode([]rune(text)) {
		data = append(data, byte(unit), byte(unit>>8))
	}
	ids, version, err := parseEAInstallerManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if version != "1.6.0.0" || len(ids) != 2 || ids[1] != "71067" {
		t.Fatalf("unexpected UTF-16 manifest result: ids=%v version=%q", ids, version)
	}
}

func TestSelectEAContentIDPrefersKnownOriginID(t *testing.T) {
	got, ok := selectEAContentID([]string{"1001303", "1001304"}, map[string]string{"1001304": "Command & Conquer"})
	if !ok || got != "1001304" {
		t.Fatalf("got %q", got)
	}
}

func TestSelectEAContentIDRejectsAmbiguousOrUnprovenIDs(t *testing.T) {
	if got, ok := selectEAContentID([]string{"one", "two"}, nil); ok || got != "" {
		t.Fatalf("accepted unproven ID %q", got)
	}
	if got, ok := selectEAContentID([]string{"one", "two"}, map[string]string{"one": "A", "two": "B"}); ok || got != "" {
		t.Fatalf("accepted ambiguous ID %q", got)
	}
	if got, ok := selectEAContentID([]string{"only"}, nil); !ok || got != "only" {
		t.Fatalf("rejected single canonical ID %q", got)
	}
}

func TestSteamInstallPathIsExcludedFromEAApp(t *testing.T) {
	if !isSteamInstallPath(`E:\SteamLibrary\steamapps\common\Titanfall2`) {
		t.Fatal("Steam library path was not detected")
	}
	if isSteamInstallPath(`E:\OriginGames\Titanfall2`) {
		t.Fatal("EA library path was misclassified")
	}
}

func TestEAInventoryFieldBoundaries(t *testing.T) {
	if !validEAExternalID(strings.Repeat("i", maxInventoryExternalIDBytes)) || validEAExternalID(strings.Repeat("i", maxInventoryExternalIDBytes+1)) {
		t.Fatal("external ID byte boundary is not enforced")
	}
	if !validEAVersion(strings.Repeat("v", maxInventoryVersionBytes)) || validEAVersion(strings.Repeat("v", maxInventoryVersionBytes+1)) {
		t.Fatal("version byte boundary is not enforced")
	}
	if !validEAInstallPath(strings.Repeat("p", maxInventoryInstallPathBytes)) || validEAInstallPath(strings.Repeat("p", maxInventoryInstallPathBytes+1)) {
		t.Fatal("install path byte boundary is not enforced")
	}
	if got := truncateUTF8Bytes(strings.Repeat("a", maxInventoryDisplayNameBytes), maxInventoryDisplayNameBytes); len(got) != maxInventoryDisplayNameBytes {
		t.Fatalf("boundary display name changed: %d", len(got))
	}
	got := truncateUTF8Bytes(strings.Repeat("a", maxInventoryDisplayNameBytes-1)+"ä", maxInventoryDisplayNameBytes)
	if len(got) > maxInventoryDisplayNameBytes || !strings.HasSuffix(got, "a") {
		t.Fatalf("invalid UTF-8 truncation: %q (%d bytes)", got, len(got))
	}
	for _, path := range []string{`\\server\share\game`, `\\?\C:\Games\Game`, `\??\C:\Games\Game`} {
		if !isUnsafeWindowsInstallPath(path) {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	if isUnsafeWindowsInstallPath(`D:\Games\Game`) {
		t.Fatal("local drive path rejected")
	}
}

func TestEAInstallerManifestSizeBoundary(t *testing.T) {
	manifest := `<DiPManifest><contentIDs><contentID>game</contentID></contentIDs></DiPManifest>`
	exact := strings.Repeat(" ", maxEAInstallerManifestSize-len(manifest)) + manifest
	ids, _, err := parseEAInstallerManifest(strings.NewReader(exact))
	if err != nil || len(ids) != 1 || ids[0] != "game" {
		t.Fatalf("exact-size manifest rejected: ids=%v err=%v", ids, err)
	}
	if _, _, err = parseEAInstallerManifest(strings.NewReader(exact + " ")); err == nil {
		t.Fatal("oversize manifest accepted")
	}
}

func TestDecodeUTF16BEAndRejectOddLength(t *testing.T) {
	text := `<DiPManifest><contentIDs><contentID>be-game</contentID></contentIDs></DiPManifest>`
	data := []byte{0xfe, 0xff}
	for _, unit := range utf16.Encode([]rune(text)) {
		data = append(data, byte(unit>>8), byte(unit))
	}
	ids, _, err := parseEAInstallerManifest(bytes.NewReader(data))
	if err != nil || len(ids) != 1 || ids[0] != "be-game" {
		t.Fatalf("UTF-16BE manifest rejected: ids=%v err=%v", ids, err)
	}
	if _, _, err = parseEAInstallerManifest(bytes.NewReader([]byte{0xff, 0xfe, 0x01})); err == nil {
		t.Fatal("odd-length UTF-16 manifest accepted")
	}
	if _, _, err = parseEAInstallerManifest(strings.NewReader(`<broken>`)); err == nil {
		t.Fatal("malformed manifest accepted")
	}
}
