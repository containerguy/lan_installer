package webadmin

import (
	"context"
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
	lanrelease "github.com/containerguy/lan_installer/internal/release"
	"github.com/containerguy/lan_installer/internal/store"
)

type releaseAPITestArtifactVerifier struct{}

func (releaseAPITestArtifactVerifier) Verify(context.Context, string, int64, string) error {
	return nil
}

func TestReleasePublishingAPIValidatesAndReplaysAtomically(t *testing.T) {
	st, admin, cookie, csrf := sourceAPITestAdmin(t, nil, nil)
	defer st.Close()
	ctx := context.Background()
	if _, err := st.SaveEventAtomic(ctx, store.Event{Slug: "kellerlan-2026", Name: "Keller-LAN 2026", Status: "draft"}, nil); err != nil {
		t.Fatal(err)
	}
	eventDigest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	updateDigest := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := st.RegisterArtifact(ctx, store.Artifact{Digest: eventDigest, SizeBytes: 1024, ContentType: "application/octet-stream"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterArtifact(ctx, store.Artifact{Digest: updateDigest, SizeBytes: 42, ContentType: "application/vnd.microsoft.portable-executable"}); err != nil {
		t.Fatal(err)
	}
	schemaDirectory, err := filepath.Abs("../../docs/contracts/schemas")
	if err != nil {
		t.Fatal(err)
	}
	validator, err := protocol.NewReleaseValidator(schemaDirectory)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service, err := lanrelease.New(st, validator, map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}, releaseAPITestArtifactVerifier{}, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if err != nil {
		t.Fatal(err)
	}
	admin.releases = service

	eventPayload := []byte(`{"formatVersion":2,"eventId":"kellerlan-2026","releaseId":"01K0LANREADY00000000000020","sequence":1,"issuedAt":"2026-07-18T08:00:00Z","validUntil":"2026-07-20T08:00:00Z","minimumClientVersion":"0.1.0","artifacts":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1024,"mediaType":"application/octet-stream","fileName":"payload.zip"}],"launchers":[{"launcherId":"steam","version":"1.0","required":true,"actions":[{"adapter":"steam","operation":"install_launcher","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}],"games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","version":"1","required":true,"payloads":[{"type":"archive","artifacts":["sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetRoot":"steam_library","relativePath":"steamapps/common/cs2"}]}]}]}`)
	eventEnvelope, err := protocol.SignEnvelope(eventPayload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	eventRequest := sourceJSONRequest(http.MethodPost, "/admin/api/v1/releases/events", map[string]any{"envelope": eventEnvelope, "activate": false}, cookie, csrf)
	eventRequest.Header.Set("Idempotency-Key", "018f6b43-3f4f-7bb2-9b73-2fbb4f99f111")
	eventResponse := httptest.NewRecorder()
	admin.ServeHTTP(eventResponse, eventRequest)
	if eventResponse.Code != http.StatusCreated {
		t.Fatalf("publish event: %d %s", eventResponse.Code, eventResponse.Body.String())
	}
	replayRequest := sourceJSONRequest(http.MethodPost, "/admin/api/v1/releases/events", map[string]any{"envelope": eventEnvelope, "activate": false}, cookie, csrf)
	replayRequest.Header.Set("Idempotency-Key", "018f6b43-3f4f-7bb2-9b73-2fbb4f99f111")
	replayResponse := httptest.NewRecorder()
	admin.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusCreated || replayResponse.Body.String() != eventResponse.Body.String() {
		t.Fatalf("event replay: %d %s", replayResponse.Code, replayResponse.Body.String())
	}
	activateRequest := sourceJSONRequest(http.MethodPost, "/admin/api/v1/releases/events/kellerlan-2026/1/activate", map[string]any{}, cookie, csrf)
	activateRequest.Header.Set("Idempotency-Key", "018f6b43-3f4f-7bb2-9b73-2fbb4f99f112")
	activateResponse := httptest.NewRecorder()
	admin.ServeHTTP(activateResponse, activateRequest)
	if activateResponse.Code != http.StatusOK {
		t.Fatalf("activate staged event: %d %s", activateResponse.Code, activateResponse.Body.String())
	}
	statusRequest := sourceJSONRequest(http.MethodGet, "/admin/api/v1/releases/status", nil, cookie, csrf)
	statusResponse := httptest.NewRecorder()
	admin.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"deliveryState":"active"`) || !strings.Contains(statusResponse.Body.String(), `"delivering":true`) {
		t.Fatalf("active release status: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	var nextEventPayload map[string]any
	if err = json.Unmarshal(eventPayload, &nextEventPayload); err != nil {
		t.Fatal(err)
	}
	nextEventPayload["releaseId"] = "01K0LANREADY00000000000021"
	nextEventPayload["sequence"] = float64(2)
	nextEventRaw, _ := json.Marshal(nextEventPayload)
	nextEventEnvelope, err := protocol.SignEnvelope(nextEventRaw, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	nextEventRequest := sourceJSONRequest(http.MethodPost, "/admin/api/v1/releases/events", map[string]any{"envelope": nextEventEnvelope, "activate": false}, cookie, csrf)
	nextEventRequest.Header.Set("Idempotency-Key", "018f6b43-3f4f-7bb2-9b73-2fbb4f99f113")
	nextEventResponse := httptest.NewRecorder()
	admin.ServeHTTP(nextEventResponse, nextEventRequest)
	if nextEventResponse.Code != http.StatusCreated {
		t.Fatalf("publish second event sequence: %d %s", nextEventResponse.Code, nextEventResponse.Body.String())
	}
	rollbackRequest := sourceJSONRequest(http.MethodPost, "/admin/api/v1/releases/events/kellerlan-2026/1/rollback-candidate", map[string]any{"validUntil": time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)}, cookie, csrf)
	rollbackResponse := httptest.NewRecorder()
	admin.ServeHTTP(rollbackResponse, rollbackRequest)
	if rollbackResponse.Code != http.StatusOK || !strings.Contains(rollbackResponse.Body.String(), `"sequence":3`) || !strings.Contains(rollbackResponse.Body.String(), `"sourceSequence":1`) {
		t.Fatalf("rollback candidate API: %d %s", rollbackResponse.Code, rollbackResponse.Body.String())
	}

	updatePayload := []byte(`{"formatVersion":1,"channel":"stable","sequence":1,"version":"0.2.0","minimumVersion":"0.1.0","artifactKind":"portable_exe","updaterProtocol":1,"publisherCertificateSHA256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","artifactPath":"/v2/client/artifacts/sha256/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","size":42,"sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","publishedAt":"2026-07-18T12:00:00Z"}`)
	updateEnvelope, err := protocol.SignEnvelope(updatePayload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	updateRequest := sourceJSONRequest(http.MethodPost, "/admin/api/v1/releases/client-updates", map[string]any{"envelope": updateEnvelope}, cookie, csrf)
	updateResponse := httptest.NewRecorder()
	admin.ServeHTTP(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusCreated {
		t.Fatalf("publish update: %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	var response map[string]any
	if err = json.Unmarshal(updateResponse.Body.Bytes(), &response); err != nil || response["version"] != "0.2.0" {
		t.Fatalf("update response: %#v %v", response, err)
	}
}

func TestReleasePublishingAPIRejectsMissingConfiguration(t *testing.T) {
	st, admin, cookie, csrf := sourceAPITestAdmin(t, nil, nil)
	defer st.Close()
	request := sourceJSONRequest(http.MethodPost, "/admin/api/v1/releases/events", map[string]any{"envelope": map[string]any{}}, cookie, csrf)
	response := httptest.NewRecorder()
	admin.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured publisher: %d %s", response.Code, response.Body.String())
	}
}
