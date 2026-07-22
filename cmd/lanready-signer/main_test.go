package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
)

func TestSignerOnlySignsValidEventPayloads(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewReleaseValidator(filepath.Join("..", "..", "docs", "contracts", "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	handler := newSignerHandler(privateKey, validator, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey})
	now := time.Now().UTC()
	payload := []byte(strings.NewReplacer("ISSUED", now.Add(-time.Minute).Format(time.RFC3339), "VALID", now.Add(time.Hour).Format(time.RFC3339)).Replace(`{"formatVersion":2,"eventId":"lan-2026","releaseId":"01K0LANREADY00000000000002","sequence":1,"issuedAt":"ISSUED","validUntil":"VALID","minimumClientVersion":"0.2.0","artifacts":[],"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"detect"}]}],"games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","externalGameId":"730","version":"latest","required":true,"payloads":[]}]}`))

	request := httptest.NewRequest(http.MethodPost, "/v1/sign-event", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid event rejected: %d %s", response.Code, response.Body.String())
	}
	var envelope protocol.Envelope
	if err = json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.KeyID != protocol.KeyID(publicKey) {
		t.Fatalf("invalid envelope: %#v err=%v", envelope, err)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/sign-event", strings.NewReader(`{"formatVersion":1,"version":"9.9.9"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-event payload accepted: %d", response.Code)
	}
}

func TestSignerHealthExposesOnlyKeyID(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewReleaseValidator(filepath.Join("..", "..", "docs", "contracts", "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	newSignerHandler(privateKey, validator, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), protocol.KeyID(publicKey)) || strings.Contains(response.Body.String(), "private") {
		t.Fatalf("unexpected health response: %d %s", response.Code, response.Body.String())
	}
}
