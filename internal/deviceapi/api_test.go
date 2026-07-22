package deviceapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/store"
)

func signedRequest(t *testing.T, method, target string, body []byte, deviceID string, privateKey ed25519.PrivateKey, nonce []byte) *http.Request {
	t.Helper()
	timestamp := time.Now().UTC().Unix()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	canonical, err := protocol.CanonicalRequest(method, req.URL.EscapedPath(), req.URL.RawQuery, body, timestamp, nonce)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("LANReady-Device-ID", deviceID)
	req.Header.Set("LANReady-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("LANReady-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	req.Header.Set("LANReady-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, canonical)))
	return req
}

func TestAuthorizationAndInventoryFlow(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/api.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const deviceID = "device-api-test"
	if err = st.CreateDevice(ctx, store.Device{ID: deviceID, Name: "Gaming-PC", PublicKey: publicKey, WindowsVersion: "11", ClientVersion: "0.4.0"}); err != nil {
		t.Fatal(err)
	}
	api, err := New(st, "https://manager.example")
	if err != nil {
		t.Fatal(err)
	}

	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	start := signedRequest(t, http.MethodPost, "/v2/user-device-authorizations", []byte(`{}`), deviceID, privateKey, nonce)
	startResponse := httptest.NewRecorder()
	api.ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusCreated {
		t.Fatalf("start: %d %s", startResponse.Code, startResponse.Body.String())
	}
	var authorization struct {
		AuthorizationID, UserCode, VerificationURL string
	}
	if err = json.Unmarshal(startResponse.Body.Bytes(), &authorization); err != nil {
		t.Fatal(err)
	}
	if authorization.AuthorizationID == "" || authorization.UserCode == "" || authorization.VerificationURL != "https://manager.example/admin/device" {
		t.Fatalf("unexpected authorization: %#v", authorization)
	}

	_, _ = rand.Read(nonce)
	poll := signedRequest(t, http.MethodGet, "/v2/user-device-authorizations/"+authorization.AuthorizationID, nil, deviceID, privateKey, nonce)
	pollResponse := httptest.NewRecorder()
	api.ServeHTTP(pollResponse, poll)
	if pollResponse.Code != http.StatusAccepted {
		t.Fatalf("pending: %d %s", pollResponse.Code, pollResponse.Body.String())
	}
	if err = st.ApproveDeviceAuthorization(ctx, authorization.UserCode, 1); err != nil {
		t.Fatal(err)
	}
	_, _ = rand.Read(nonce)
	poll = signedRequest(t, http.MethodGet, "/v2/user-device-authorizations/"+authorization.AuthorizationID, nil, deviceID, privateKey, nonce)
	pollResponse = httptest.NewRecorder()
	api.ServeHTTP(pollResponse, poll)
	if pollResponse.Code != http.StatusOK {
		t.Fatalf("approved: %d %s", pollResponse.Code, pollResponse.Body.String())
	}
	var tokenResponse struct {
		AccessToken string `json:"accessToken"`
	}
	if err = json.Unmarshal(pollResponse.Body.Bytes(), &tokenResponse); err != nil || tokenResponse.AccessToken == "" {
		t.Fatalf("token: %#v %v", tokenResponse, err)
	}

	body := []byte(`{"scanId":"scan-api-1","scannedAt":"2026-07-18T08:00:00Z","clientVersion":"0.4.0","installations":[{"launcher":"steam","externalGameId":"730","displayName":"Counter-Strike 2","detectedVersion":"build-123","versionSource":"steam-buildid","installPath":"C:\\Steam\\steamapps\\common\\Counter-Strike Global Offensive"}]}`)
	_, _ = rand.Read(nonce)
	upload := signedRequest(t, http.MethodPost, "/v2/device/inventory-scans", body, deviceID, privateKey, append([]byte(nil), nonce...))
	upload.Header.Set("Authorization", "Bearer "+tokenResponse.AccessToken)
	uploadResponse := httptest.NewRecorder()
	api.ServeHTTP(uploadResponse, upload)
	if uploadResponse.Code != http.StatusAccepted {
		t.Fatalf("upload: %d %s", uploadResponse.Code, uploadResponse.Body.String())
	}
	got, err := st.DeviceInventory(ctx, deviceID)
	if err != nil || got.ScanID != "scan-api-1" || len(got.Installations) != 1 || got.Installations[0].ExternalGameID != "730" {
		t.Fatalf("stored inventory: %#v %v", got, err)
	}

	replayResponse := httptest.NewRecorder()
	replay := signedRequest(t, http.MethodPost, "/v2/device/inventory-scans", body, deviceID, privateKey, nonce)
	replay.Header.Set("Authorization", "Bearer "+tokenResponse.AccessToken)
	api.ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusConflict {
		t.Fatalf("replay accepted: %d %s", replayResponse.Code, replayResponse.Body.String())
	}
}

func TestEnrollmentCodeIsConsumedExactlyOnce(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/enroll.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	code, err := st.CreateEnrollmentCode(ctx, 1, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(st, "https://manager.example")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": code.Code, "publicKey": base64.RawURLEncoding.EncodeToString(publicKey), "deviceName": "Gaming-PC", "windowsVersion": "11.0.26100", "clientVersion": "0.4.0"})
	for attempt, expected := range []int{http.StatusCreated, http.StatusConflict} {
		request := httptest.NewRequest(http.MethodPost, "/v2/devices/enroll", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != expected {
			t.Fatalf("attempt %d: %d %s", attempt+1, response.Code, response.Body.String())
		}
	}
}

func TestConfigurationAndContentTypeAreStrict(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/strict.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, invalid := range []string{"http://manager.example", "https://user:secret@manager.example", "https://manager.example/base", "https://manager.example?query=1"} {
		if _, err = New(st, invalid); err == nil {
			t.Fatalf("accepted public URL %q", invalid)
		}
	}
	api, err := New(st, "https://manager.example", "10.20.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v2/devices/enroll", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("Content-Type", "application/jsonp")
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("accepted JSONP content type: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.20.0.5:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.66, 203.0.113.9")
	if got := api.clientIP(request); got != "203.0.113.9" {
		t.Fatalf("spoofed leftmost address was trusted: %q", got)
	}
	request.RemoteAddr = "198.51.100.7:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.10")
	if got := api.clientIP(request); got != "198.51.100.7" {
		t.Fatalf("untrusted proxy spoof accepted: %q", got)
	}
}

func TestAuthenticatedArtifactDownloads(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/artifacts.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const deviceID = "artifact-device"
	if err = st.CreateDevice(context.Background(), store.Device{ID: deviceID, Name: "Gaming-PC", PublicKey: publicKey, WindowsVersion: "11", ClientVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifact.New(t.TempDir(), st)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("0123456789abcdef")
	digestBytes := sha256.Sum256(content)
	digest := hex.EncodeToString(digestBytes[:])
	if _, err = artifactStore.Ingest(context.Background(), digest, int64(len(content)), "application/octet-stream", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	api, err := NewWithArtifactStore(st, "https://manager.example", artifactStore)
	if err != nil {
		t.Fatal(err)
	}
	target := "/v2/artifacts/sha256/" + digest
	request := func(method string) *http.Request {
		nonce := make([]byte, 12)
		_, _ = rand.Read(nonce)
		return signedRequest(t, method, target, nil, deviceID, privateKey, nonce)
	}

	unauthenticated := httptest.NewRecorder()
	api.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, target, nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated download: %d", unauthenticated.Code)
	}

	headResponse := httptest.NewRecorder()
	api.ServeHTTP(headResponse, request(http.MethodHead))
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") != strconv.Itoa(len(content)) || headResponse.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("head: %d headers=%v body=%q", headResponse.Code, headResponse.Header(), headResponse.Body.String())
	}
	etag := `"sha256:` + digest + `"`
	if headResponse.Header().Get("ETag") != etag {
		t.Fatalf("etag: %q", headResponse.Header().Get("ETag"))
	}

	fullResponse := httptest.NewRecorder()
	api.ServeHTTP(fullResponse, request(http.MethodGet))
	if fullResponse.Code != http.StatusOK || !bytes.Equal(fullResponse.Body.Bytes(), content) {
		t.Fatalf("full: %d %q", fullResponse.Code, fullResponse.Body.Bytes())
	}

	rangeRequest := request(http.MethodGet)
	rangeRequest.Header.Set("Range", "bytes=4-9")
	rangeResponse := httptest.NewRecorder()
	api.ServeHTTP(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "456789" || rangeResponse.Header().Get("Content-Range") != "bytes 4-9/16" {
		t.Fatalf("range: %d headers=%v body=%q", rangeResponse.Code, rangeResponse.Header(), rangeResponse.Body.String())
	}

	ifRangeRequest := request(http.MethodGet)
	ifRangeRequest.Header.Set("Range", "bytes=4-9")
	ifRangeRequest.Header.Set("If-Range", `"sha256:wrong"`)
	ifRangeResponse := httptest.NewRecorder()
	api.ServeHTTP(ifRangeResponse, ifRangeRequest)
	if ifRangeResponse.Code != http.StatusOK || !bytes.Equal(ifRangeResponse.Body.Bytes(), content) {
		t.Fatalf("if-range fallback: %d %q", ifRangeResponse.Code, ifRangeResponse.Body.Bytes())
	}

	invalidRequest := request(http.MethodGet)
	invalidRequest.Header.Set("Range", "bytes=-4")
	invalidResponse := httptest.NewRecorder()
	api.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusRequestedRangeNotSatisfiable || invalidResponse.Header().Get("Content-Range") != "bytes */16" {
		t.Fatalf("invalid range: %d headers=%v", invalidResponse.Code, invalidResponse.Header())
	}
}

func TestBootstrapAndSignedReleaseDelivery(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/releases.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SaveEventAtomic(ctx, store.Event{Slug: "lan-2026", Name: "LAN 2026", Status: "draft"}, nil); err != nil {
		t.Fatal(err)
	}
	digest := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err = st.RegisterArtifact(ctx, store.Artifact{Digest: digest, SizeBytes: 42, ContentType: "application/vnd.microsoft.portable-executable"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	audit := func(action string) *store.AuditEntry { return &store.AuditEntry{ActorUserID: 1, Action: action} }
	if err = st.PublishEventRelease(ctx, store.EventReleaseRecord{EventID: "lan-2026", ReleaseID: "01K0LANREADY00000000000010", KeyID: "ed25519-aaaaaaaaaaaaaaaa", Sequence: 1, EnvelopeJSON: []byte(`{"kind":"event"}`), PayloadJSON: []byte(`{}`), IssuedAt: now, ValidUntil: now.Add(time.Hour), MinimumClientVersion: "0.1.0"}, true, audit("publish_event_release")); err != nil {
		t.Fatal(err)
	}
	if err = st.PublishEventRelease(ctx, store.EventReleaseRecord{EventID: "lan-2026", ReleaseID: "01K0LANREADY00000000000011", KeyID: "ed25519-aaaaaaaaaaaaaaaa", Sequence: 2, EnvelopeJSON: []byte(`{"kind":"inactive-newer-event"}`), PayloadJSON: []byte(`{}`), IssuedAt: now, ValidUntil: now.Add(time.Hour), MinimumClientVersion: "0.1.0"}, false, audit("publish_event_release")); err != nil {
		t.Fatal(err)
	}
	if err = st.PublishClientUpdateRelease(ctx, store.ClientUpdateReleaseRecord{Channel: "stable", Sequence: 1, Version: "0.2.0", MinimumVersion: "0.1.0", ArtifactDigest: digest, SizeBytes: 42, KeyID: "ed25519-aaaaaaaaaaaaaaaa", EnvelopeJSON: []byte(`{"kind":"update"}`), PayloadJSON: []byte(`{}`), PublishedAt: now}, audit("publish_client_update")); err != nil {
		t.Fatal(err)
	}
	artifactStore, err := artifact.New(t.TempDir(), st)
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewWithArtifactStore(st, "https://manager.example", artifactStore)
	if err != nil {
		t.Fatal(err)
	}
	newDeviceID, newPrivateKey := createAPIReleaseDevice(t, st, "new-device", "0.1.0")
	signed := func(method, target, deviceID string, key ed25519.PrivateKey) *http.Request {
		nonce := make([]byte, 12)
		_, _ = rand.Read(nonce)
		return signedRequest(t, method, target, nil, deviceID, key, nonce)
	}

	bootstrapResponse := httptest.NewRecorder()
	api.ServeHTTP(bootstrapResponse, signed(http.MethodGet, "/v2/device/bootstrap?clientVersion=0.1.0", newDeviceID, newPrivateKey))
	if bootstrapResponse.Code != http.StatusOK || !bytes.Contains(bootstrapResponse.Body.Bytes(), []byte(`"eventId":"lan-2026"`)) {
		t.Fatalf("bootstrap: %d %s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	versionResponse := httptest.NewRecorder()
	api.ServeHTTP(versionResponse, signed(http.MethodGet, "/v2/device/bootstrap?clientVersion=0.1.1", newDeviceID, newPrivateKey))
	if versionResponse.Code != http.StatusOK {
		t.Fatalf("signed runtime version acknowledgement: %d %s", versionResponse.Code, versionResponse.Body.String())
	}
	updatedDevice, err := st.ActiveDevice(ctx, newDeviceID)
	if err != nil || updatedDevice.ClientVersion != "0.1.1" {
		t.Fatalf("runtime version was not persisted: %#v %v", updatedDevice, err)
	}
	downgradeResponse := httptest.NewRecorder()
	api.ServeHTTP(downgradeResponse, signed(http.MethodGet, "/v2/device/bootstrap?clientVersion=0.0.9", newDeviceID, newPrivateKey))
	if downgradeResponse.Code != http.StatusUpgradeRequired {
		t.Fatalf("downgraded running binary was not blocked: %d %s", downgradeResponse.Code, downgradeResponse.Body.String())
	}
	updatedDevice, err = st.ActiveDevice(ctx, newDeviceID)
	if err != nil || updatedDevice.ClientVersion != "0.0.9" || updatedDevice.ClientVersionHighWatermark != "0.1.1" {
		t.Fatalf("runtime/high-watermark separation failed: %#v %v", updatedDevice, err)
	}
	blockedAuthorization := httptest.NewRecorder()
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	api.ServeHTTP(blockedAuthorization, signedRequest(t, http.MethodPost, "/v2/user-device-authorizations", []byte(`{}`), newDeviceID, newPrivateKey, nonce))
	if blockedAuthorization.Code != http.StatusUpgradeRequired {
		t.Fatalf("downgraded client started a personal authorization: %d %s", blockedAuthorization.Code, blockedAuthorization.Body.String())
	}
	eventResponse := httptest.NewRecorder()
	api.ServeHTTP(eventResponse, signed(http.MethodGet, "/v2/events/lan-2026/release", newDeviceID, newPrivateKey))
	if eventResponse.Code != http.StatusUpgradeRequired {
		t.Fatalf("downgraded client bypassed bootstrap and fetched event: %d %s", eventResponse.Code, eventResponse.Body.String())
	}
	recoveryResponse := httptest.NewRecorder()
	api.ServeHTTP(recoveryResponse, signed(http.MethodGet, "/v2/device/bootstrap?clientVersion=0.1.1", newDeviceID, newPrivateKey))
	if recoveryResponse.Code != http.StatusOK {
		t.Fatalf("runtime version recovery: %d %s", recoveryResponse.Code, recoveryResponse.Body.String())
	}
	eventResponse = httptest.NewRecorder()
	api.ServeHTTP(eventResponse, signed(http.MethodGet, "/v2/events/lan-2026/release", newDeviceID, newPrivateKey))
	if eventResponse.Code != http.StatusOK || eventResponse.Body.String() != `{"kind":"event"}` || eventResponse.Header().Get("ETag") != `"1"` {
		t.Fatalf("event release: %d headers=%v body=%q", eventResponse.Code, eventResponse.Header(), eventResponse.Body.String())
	}
	updateResponse := httptest.NewRecorder()
	api.ServeHTTP(updateResponse, signed(http.MethodGet, "/v2/client/releases/latest?channel=stable", newDeviceID, newPrivateKey))
	if updateResponse.Code != http.StatusOK || updateResponse.Body.String() != `{"kind":"update"}` {
		t.Fatalf("update release: %d %q", updateResponse.Code, updateResponse.Body.String())
	}

	oldDeviceID, oldPrivateKey := createAPIReleaseDevice(t, st, "old-device", "0.0.9")
	upgradeResponse := httptest.NewRecorder()
	api.ServeHTTP(upgradeResponse, signed(http.MethodGet, "/v2/device/bootstrap?clientVersion=0.0.9", oldDeviceID, oldPrivateKey))
	if upgradeResponse.Code != http.StatusUpgradeRequired || bytes.Contains(upgradeResponse.Body.Bytes(), []byte("activeEvent")) || !bytes.Contains(upgradeResponse.Body.Bytes(), []byte(`"releaseUrl":"/v2/client/releases/latest?channel=stable"`)) {
		t.Fatalf("upgrade bootstrap: %d %s", upgradeResponse.Code, upgradeResponse.Body.String())
	}

	duplicateChannel := httptest.NewRecorder()
	api.ServeHTTP(duplicateChannel, signed(http.MethodGet, "/v2/client/releases/latest?channel=stable&channel=stable", newDeviceID, newPrivateKey))
	if duplicateChannel.Code != http.StatusBadRequest {
		t.Fatalf("duplicate update channel accepted: %d", duplicateChannel.Code)
	}
}

func createAPIReleaseDevice(t *testing.T, st *store.Store, deviceID, version string) (string, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.CreateDevice(context.Background(), store.Device{ID: deviceID, Name: deviceID, PublicKey: publicKey, WindowsVersion: "11", ClientVersion: version}); err != nil {
		t.Fatal(err)
	}
	return deviceID, privateKey
}
