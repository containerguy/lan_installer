package discovery

import (
	"strings"
	"testing"
)

func TestParseSteamManifest(t *testing.T) {
	values, err := parseVDFPairs(strings.NewReader(`"AppState"
{
  "appid" "730"
  "name" "Counter-Strike 2"
  "buildid" "12345678"
  "installdir" "Counter-Strike Global Offensive"
}`))
	if err != nil {
		t.Fatal(err)
	}
	installation, err := steamInstallation(`C:\Steam\steamapps\appmanifest_730.acf`, values)
	if err != nil {
		t.Fatal(err)
	}
	if installation.ExternalGameID != "730" || installation.DisplayName != "Counter-Strike 2" || installation.DetectedVersion == nil || *installation.DetectedVersion != "12345678" || installation.VersionSource != "steam-buildid" {
		t.Fatalf("unexpected installation: %#v", installation)
	}
}

func TestSteamManifestDoesNotInventVersion(t *testing.T) {
	values := map[string]string{"appid": "1", "name": "Game", "installdir": "Game"}
	installation, err := steamInstallation(`D:\Steam\steamapps\appmanifest_1.acf`, values)
	if err != nil {
		t.Fatal(err)
	}
	if installation.DetectedVersion != nil || installation.VersionSource != "steam-buildid-unavailable" {
		t.Fatalf("invented version: %#v", installation)
	}
}
