package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerguy/lan_installer/internal/model"
)

func TestSyncGameDownloadsAndVerifiesFile(t *testing.T) {
	content := []byte("verified content")
	hash := sha256.Sum256(content)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content/games/demo.bin" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(content)
	}))
	defer mirror.Close()

	root := t.TempDir()
	c := &Client{config: Config{
		TargetRoot: root,
		HTTPClient: mirror.Client(),
		Output:     io.Discard,
	}}
	game := model.Game{
		ID:      "demo",
		Name:    "Demo",
		Version: "1.0",
		Target:  "Demo",
		Files: []model.File{{
			Path:   "demo.bin",
			Source: "games/demo.bin",
			Size:   int64(len(content)),
			SHA256: hex.EncodeToString(hash[:]),
		}},
	}
	result := c.syncGame(context.Background(), []model.Mirror{{Name: "test", BaseURL: mirror.URL, Priority: 1}}, game)
	if !result.Ready || result.Changed != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	actual, err := os.ReadFile(filepath.Join(root, "Demo", "demo.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(content) {
		t.Fatalf("unexpected content %q", actual)
	}
}

func TestContentURLRejectsTraversal(t *testing.T) {
	for _, source := range []string{"../secret", "games/../secret", "/absolute", "games//file"} {
		if _, err := contentURL("https://example.invalid", source); err == nil {
			t.Fatalf("unsafe source %q was accepted", source)
		}
	}
}
