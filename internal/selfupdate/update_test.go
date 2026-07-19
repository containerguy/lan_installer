package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerguy/lan_installer/internal/deviceclient"
)

func TestHealthMarkerAdvancesProtectedUpdateSequence(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "device.json")
	profile := deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "device", DeviceName: "PC", ClientVersion: "0.1.0", PrivateKey: privateKey, UpdateSequence: 1}
	if err = deviceclient.SaveProfile(profilePath, profile); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(profilePath), "health.ok")
	request := HealthRequest{ProfilePath: profilePath, MarkerPath: marker, Version: "0.2.0", Sequence: 2}
	if err = SignalHealthy(request); err != nil {
		t.Fatal(err)
	}
	unchanged, err := deviceclient.LoadProfile(profilePath)
	if err != nil || unchanged.UpdateSequence != 1 || unchanged.ClientVersion != "0.1.0" {
		t.Fatalf("health signal changed protected profile before updater commit: %#v %v", unchanged, err)
	}
	if err = CommitHealthy(request); err != nil {
		t.Fatal(err)
	}
	updated, err := deviceclient.LoadProfile(profilePath)
	if err != nil || updated.ClientVersion != "0.2.0" || updated.UpdateSequence != 2 {
		t.Fatalf("updated profile: %#v %v", updated, err)
	}
	if content, readErr := os.ReadFile(marker); readErr != nil || string(content) != "2\n" {
		t.Fatalf("health marker: %q %v", content, readErr)
	}
	request.Sequence = 1
	if err = CommitHealthy(request); err == nil {
		t.Fatal("health marker accepted a sequence rollback")
	}
}

func TestInternalUpdateModesRequireCompleteAbsoluteArguments(t *testing.T) {
	request, mode, err := ParseApplyArgs([]string{"--lanready-apply-update", "--target", filepath.Join(t.TempDir(), "LANReady.exe"), "--profile", filepath.Join(t.TempDir(), "device.json"), "--marker", filepath.Join(t.TempDir(), "health.ok"), "--apply-ready", filepath.Join(t.TempDir(), "apply.ok"), "--version", "0.2.0", "--sequence", "2", "--parent-pid", "123", "--size", "1234", "--sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--publisher-sha256", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"})
	if err != nil || !mode || request.Sequence != 2 {
		t.Fatalf("apply args: %#v %v %v", request, mode, err)
	}
	if _, mode, err = ParseHealthArgs([]string{"--lanready-update-health", "--profile", "relative", "--marker", "relative", "--version", "0.2.0", "--sequence", "2"}); err == nil || !mode {
		t.Fatalf("unsafe health args accepted: %v %v", mode, err)
	}
}
