package deviceapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/containerguy/lan_installer/internal/store"
)

func TestStandaloneCatalogRequiresSignedActiveDevice(t *testing.T) {
	t.Parallel()
	st, err := store.Open(filepath.Join(t.TempDir(), "standalone-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	launchers, err := st.Launchers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var standaloneID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "standalone" {
			standaloneID = launcher.ID
		}
	}
	if _, err = st.SaveGameAtomic(context.Background(), store.Game{Slug: "open-ra", Name: "OpenRA", LauncherID: standaloneID, Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.CreateDevice(context.Background(), store.Device{ID: "device", Name: "PC", PublicKey: publicKey, WindowsVersion: "11", ClientVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	api, err := New(st, "https://manager.example")
	if err != nil {
		t.Fatal(err)
	}
	unsigned := httptest.NewRecorder()
	api.ServeHTTP(unsigned, httptest.NewRequest(http.MethodGet, "/v2/device/standalone-games", nil))
	if unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned status: %d", unsigned.Code)
	}
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	request := signedRequest(t, http.MethodGet, "/v2/device/standalone-games", nil, "device", privateKey, nonce)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("signed status: %d %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Games []store.StandaloneGame `json:"games"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || len(decoded.Games) != 1 || decoded.Games[0].ExternalGameID != "open-ra" {
		t.Fatalf("response: %#v %v", decoded, err)
	}
}
