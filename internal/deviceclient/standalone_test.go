package deviceclient

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneGamesUsesSignedCanonicalEndpoint(t *testing.T) {
	t.Parallel()
	requestSeen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen <- r.URL.Path + "|" + r.Header.Get("LANReady-Signature")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"games":[{"id":4,"slug":"open-ra","name":"OpenRA","externalGameId":"open-ra","versions":["2026.1"]}]}`))
	}))
	defer server.Close()
	client := &Client{Profile: deviceTestProfile(t, server.URL), HTTP: server.Client()}
	games, err := client.StandaloneGames(t.Context())
	if err != nil || len(games) != 1 || games[0].ExternalGameID != "open-ra" {
		t.Fatalf("standalone games: %#v %v", games, err)
	}
	if seen := <-requestSeen; !strings.HasPrefix(seen, "/v2/device/standalone-games|") || strings.HasSuffix(seen, "|") {
		t.Fatalf("unsigned or noncanonical request: %q", seen)
	}
}

func TestProfileProtectsManualGameRegistrations(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "device.json")
	profile := deviceTestProfile(t, "https://manager.example")
	profile.ManualGames = []ManualGame{{CatalogGameID: 4, ExternalGameID: "open-ra", DisplayName: "OpenRA", ExecutablePath: `D:\Games\OpenRA\OpenRA.exe`}}
	if err := SaveProfile(path, profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProfile(path)
	if err != nil || len(loaded.ManualGames) != 1 || loaded.ManualGames[0].ExecutablePath != profile.ManualGames[0].ExecutablePath {
		t.Fatalf("manual registrations: %#v %v", loaded.ManualGames, err)
	}
	profile.ManualGames[0].ExecutablePath = `\\server\games\OpenRA.exe`
	if err = SaveProfile(path, profile); err == nil {
		t.Fatal("UNC executable path was accepted")
	}
	profile.ManualGames[0].ExecutablePath = `D:\Games\OpenRA\OpenRA.exe:payload`
	if err = SaveProfile(path, profile); err == nil {
		t.Fatal("alternate data stream executable path was accepted")
	}
	profile.ManualGames = []ManualGame{{CatalogGameID: 4, ExternalGameID: "open-ra", DisplayName: "OpenRA", ExecutablePath: `D:\Games\OpenRA\OpenRA.exe`}, {CatalogGameID: 5, ExternalGameID: "open-ra", DisplayName: "Duplicate", ExecutablePath: `E:\Games\OpenRA.exe`}}
	if err = SaveProfile(path, profile); err == nil {
		t.Fatal("duplicate manual external id was accepted")
	}
}

func TestStandaloneGamesRejectsTrailingOversizedAndDuplicateData(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"trailing":  `{"games":[]} {}`,
		"duplicate": `{"games":[{"id":4,"slug":"open-ra","name":"OpenRA","externalGameId":"open-ra","versions":[]},{"id":5,"slug":"open-ra","name":"Other","externalGameId":"open-ra","versions":[]}]}`,
		"versions":  `{"games":[{"id":4,"slug":"open-ra","name":"OpenRA","externalGameId":"open-ra","versions":["1","1"]}]}`,
		"slug":      `{"games":[{"id":4,"slug":"open_ra","name":"OpenRA","externalGameId":"open_ra","versions":[]}]}`,
		"oversized": `{"games":[]}` + strings.Repeat(" ", (1<<20)+1),
	}
	for name, body := range cases {
		name, body := name, body
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := &Client{Profile: deviceTestProfile(t, server.URL), HTTP: server.Client()}
			if _, err := client.StandaloneGames(t.Context()); err == nil {
				t.Fatalf("invalid %s response was accepted", name)
			}
		})
	}
}
