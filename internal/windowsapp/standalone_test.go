package windowsapp

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerguy/lan_installer/internal/deviceclient"
	"github.com/containerguy/lan_installer/internal/discovery"
)

func TestManualGameIsCatalogBoundPersistedAndMerged(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/device/standalone-games" || r.Header.Get("LANReady-Signature") == "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"games":[{"id":4,"slug":"open-ra","name":"OpenRA","externalGameId":"open-ra","versions":["2026.1"]}]}`))
	}))
	defer server.Close()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "device.json")
	if err = deviceclient.SaveProfile(profilePath, deviceclient.Profile{ServerURL: server.URL, DeviceID: "device", DeviceName: "PC", ClientVersion: "1.0.0", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	testExecutable := filepath.Join(t.TempDir(), "game.exe")
	if err = os.WriteFile(testExecutable, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	app := New(profilePath, "1.0.0")
	app.httpClient = server.Client()
	app.serverReady = true
	app.stat = func(string) (os.FileInfo, error) { return fileInfo, nil }
	app.fileVersion = func(string) (string, error) { return "2026.1", nil }
	if err = app.SaveManualGame(ManualGameInput{CatalogGameID: 4, ExecutablePath: `D:\Games\OpenRA\OpenRA.exe`}); err != nil {
		t.Fatal(err)
	}
	profile, err := deviceclient.LoadProfile(profilePath)
	if err != nil || len(profile.ManualGames) != 1 || profile.ManualGames[0].ExternalGameID != "open-ra" {
		t.Fatalf("stored manual game: %#v %v", profile.ManualGames, err)
	}
	result := app.mergeManualGames(discovery.Result{})
	if len(result.Installations) != 1 || result.Installations[0].Launcher != "standalone" || result.Installations[0].DetectedVersion == nil || *result.Installations[0].DetectedVersion != "2026.1" || result.Installations[0].VersionSource != "windows-file-version" {
		t.Fatalf("merged manual game: %#v", result)
	}
	statCalls, versionCalls := 0, 0
	app.validateManualExecutable = func(string) error { return os.ErrPermission }
	app.stat = func(string) (os.FileInfo, error) { statCalls++; return fileInfo, nil }
	app.fileVersion = func(string) (string, error) { versionCalls++; return "2026.1", nil }
	remote := app.mergeManualGames(discovery.Result{})
	if len(remote.Installations) != 0 || len(remote.Warnings) != 1 || statCalls != 0 || versionCalls != 0 {
		t.Fatalf("remote drive reached file I/O: result=%#v stat=%d version=%d", remote, statCalls, versionCalls)
	}
	if err = app.RemoveManualGame("open-ra"); err != nil {
		t.Fatal(err)
	}
	profile, err = deviceclient.LoadProfile(profilePath)
	if err != nil || len(profile.ManualGames) != 0 {
		t.Fatalf("manual game not removed: %#v %v", profile.ManualGames, err)
	}
}

func TestSaveManualGameRejectsNetworkAndADSPathsBeforeIO(t *testing.T) {
	app := New(filepath.Join(t.TempDir(), "device.json"), "1.0.0")
	statCalls := 0
	app.stat = func(string) (os.FileInfo, error) {
		statCalls++
		return nil, os.ErrNotExist
	}
	for _, unsafePath := range []string{`\\server\games\game.exe`, `C:\Games\game.exe:payload`, `\\?\C:\Games\game.exe`, `C:\Games\..\game.exe`, `C:\Games\NUL.exe`} {
		if err := app.SaveManualGame(ManualGameInput{CatalogGameID: 1, ExecutablePath: unsafePath}); err == nil {
			t.Fatalf("unsafe path was accepted: %q", unsafePath)
		}
	}
	if statCalls != 0 {
		t.Fatalf("unsafe paths reached filesystem I/O %d time(s)", statCalls)
	}
	app.validateManualExecutable = func(string) error { return os.ErrPermission }
	if err := app.SaveManualGame(ManualGameInput{CatalogGameID: 1, ExecutablePath: `Z:\Games\game.exe`}); err == nil {
		t.Fatal("mapped network drive was accepted")
	}
	if statCalls != 0 {
		t.Fatal("rejected mapped drive reached filesystem I/O")
	}
}
