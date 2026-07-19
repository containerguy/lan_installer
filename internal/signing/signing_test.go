package signing

import (
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/model"
)

func TestSignAndVerify(t *testing.T) {
	publicKey, privateKey, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"id":"demo","name":"Demo","expiresAt":"2030-01-01T00:00:00Z","mirrors":[{"name":"main","baseUrl":"https://example.invalid","priority":1}],"games":[]}`)
	envelope, err := Sign(raw, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Verify(envelope, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "demo" {
		t.Fatalf("unexpected manifest id %q", manifest.ID)
	}

	envelope[len(envelope)-2] ^= 1
	if _, err := Verify(envelope, publicKey); err == nil {
		t.Fatal("tampered envelope was accepted")
	}
}

func TestValidateRejectsDuplicateGames(t *testing.T) {
	m := model.EventManifest{
		Version:   1,
		ID:        "demo",
		Name:      "Demo",
		ExpiresAt: time.Now().Add(time.Hour),
		Mirrors:   []model.Mirror{{Name: "main", BaseURL: "https://example.invalid"}},
		Games: []model.Game{
			{ID: "same", Name: "A", Target: "A"},
			{ID: "same", Name: "B", Target: "B"},
		},
	}
	if err := Validate(m); err == nil {
		t.Fatal("duplicate game id was accepted")
	}
}
