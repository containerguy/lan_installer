package deviceclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/deviceapi"
	"github.com/containerguy/lan_installer/internal/discovery"
	"github.com/containerguy/lan_installer/internal/store"
)

func deviceTestProfile(t *testing.T, serverURL string) Profile {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Profile{ServerURL: serverURL, DeviceID: "device", DeviceName: "PC", ClientVersion: "1.0.0", PrivateKey: privateKey}
}

func TestEventReleaseUsesCanonicalSignedPathAndLimitsResponse(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"formatVersion":1}`)
	requestSeen := make(chan [3]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen <- [3]string{r.Method, r.URL.Path, r.Header.Get("LANReady-Signature")}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	client := &Client{Profile: deviceTestProfile(t, server.URL), HTTP: server.Client()}
	got, err := client.EventRelease(t.Context(), "lan-2026", "/v2/events/lan-2026/release")
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("event release: %q %v", got, err)
	}
	seen := <-requestSeen
	if seen[0] != http.MethodGet || seen[1] != "/v2/events/lan-2026/release" || seen[2] == "" {
		t.Fatalf("unexpected event request: %#v", seen)
	}
	for _, test := range []struct{ eventID, releaseURL string }{
		{eventID: "../admin", releaseURL: "/v2/events/../admin/release"},
		{eventID: "lan-2026", releaseURL: "https://evil.example/release"},
		{eventID: "lan-2026", releaseURL: "/v2/events/other/release"},
	} {
		if _, err = client.EventRelease(t.Context(), test.eventID, test.releaseURL); err == nil {
			t.Fatalf("unsafe release location accepted: %#v", test)
		}
	}
}

func TestEventReleaseRejectsRedirectMediaTypeAndOversize(t *testing.T) {
	t.Parallel()
	targetHits := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()
	responses := []func(http.ResponseWriter){
		func(w http.ResponseWriter) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(http.StatusTemporaryRedirect)
		},
		func(w http.ResponseWriter) { w.Header().Set("Content-Type", "text/html"); _, _ = w.Write([]byte(`{}`)) },
		func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(bytes.Repeat([]byte("x"), (950<<10)+1))
		},
	}
	for index, response := range responses {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { response(w) }))
		client := &Client{Profile: deviceTestProfile(t, server.URL), HTTP: server.Client()}
		if _, err := client.EventRelease(t.Context(), "lan-2026", "/v2/events/lan-2026/release"); err == nil {
			server.Close()
			t.Fatalf("invalid response %d accepted", index)
		}
		server.Close()
	}
	select {
	case <-targetHits:
		t.Fatal("signed event request followed a redirect")
	default:
	}
}

func TestSignedClientNeverFollowsRedirects(t *testing.T) {
	t.Parallel()
	forwarded := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded <- struct{}{} }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/capture")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := Enroll(context.Background(), redirect.URL, "one-time-code", "PC", "11", "test", redirect.Client())
	if err == nil {
		t.Fatal("redirect response should not be accepted as enrollment")
	}
	select {
	case <-forwarded:
		t.Fatal("enrollment body was forwarded across a redirect")
	default:
	}
}

func TestPreflightProfileLeavesNoCredentialFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "LANReady", "device.json")
	if err := PreflightProfile(path); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".lanready-profile-preflight-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("preflight files remain: %v, %v", entries, err)
	}
}

func TestProfileProtectsMetadataAndRejectsInvalidEnvelope(t *testing.T) {
	t.Parallel()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "device.json")
	profile := Profile{ServerURL: "https://manager.example", DeviceID: "device-secret-id", DeviceName: "Private PC", ClientVersion: "1.2.3", PrivateKey: privateKey}
	if err = SaveProfile(path, profile); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), profile.ServerURL) || strings.Contains(string(content), profile.DeviceID) || strings.Contains(string(content), profile.DeviceName) {
		t.Fatalf("profile metadata is visible in cleartext: %s", content)
	}
	content = []byte(`{"formatVersion":2,"protectedPayload":"not!base64"}`)
	if err = os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadProfile(path); err == nil {
		t.Fatal("invalid protected profile was accepted")
	}
}

func TestProfileProtectsEventSequenceWatermarks(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "device.json")
	profile := deviceTestProfile(t, "https://manager.example")
	profile.EventSequences = map[string]int64{"lan-2026": 7}
	if err := SaveProfile(path, profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProfile(path)
	if err != nil || loaded.EventSequences["lan-2026"] != 7 {
		t.Fatalf("watermarks: %#v %v", loaded.EventSequences, err)
	}
	profile.EventSequences = map[string]int64{"../invalid": 1}
	if err = SaveProfile(path, profile); err == nil {
		t.Fatal("invalid event watermark was accepted")
	}
}

func TestProfileDurablyReplacesWatermarksAcrossRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "device.json")
	profile := deviceTestProfile(t, "https://manager.example")
	profile.EventSequences = map[string]int64{"lan-2026": 7}
	if err := SaveProfile(path, profile); err != nil {
		t.Fatal(err)
	}
	first, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	first.EventSequences["lan-2026"] = 8
	if err = SaveProfile(path, first); err != nil {
		t.Fatal(err)
	}
	restarted, err := LoadProfile(path)
	if err != nil || restarted.EventSequences["lan-2026"] != 8 {
		t.Fatalf("restarted profile: %#v %v", restarted, err)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".lanready-device-*.tmp"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary profiles remain: %v %v", temporary, err)
	}
}

func TestOversizedWatermarksNeverReplaceReadableProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device.json")
	profile := deviceTestProfile(t, "https://manager.example")
	profile.EventSequences = map[string]int64{"existing-event": 7}
	if err := SaveProfile(path, profile); err != nil {
		t.Fatal(err)
	}
	tooMany := make(map[string]int64, 100000)
	for index := 0; index < 100000; index++ {
		tooMany[fmt.Sprintf("event-%06d", index)] = 1
	}
	profile.EventSequences = tooMany
	if err := SaveProfile(path, profile); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized profile was not rejected before replace: %v", err)
	}
	loaded, err := LoadProfile(path)
	if err != nil || loaded.EventSequences["existing-event"] != 7 || len(loaded.EventSequences) != 1 {
		t.Fatalf("existing profile changed after rejected save: %#v %v", loaded.EventSequences, err)
	}
}

func TestEnrollAuthorizeAndUploadInventory(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	code, err := st.CreateEnrollmentCode(ctx, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	api, err := deviceapi.New(st, "https://manager.example")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(api)
	defer server.Close()
	profile, err := Enroll(ctx, server.URL, code.Code, "Gaming-PC", "11", "0.5.0", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "device.json")
	if err = SaveProfile(profilePath, profile); err != nil {
		t.Fatal(err)
	}
	profile, err = LoadProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{Profile: profile, HTTP: server.Client()}
	bootstrap, err := client.Bootstrap(ctx, "0.5.1")
	if err != nil || bootstrap.APIVersion != 2 {
		t.Fatalf("bootstrap runtime version: %#v %v", bootstrap, err)
	}
	deviceState, err := st.ActiveDevice(ctx, profile.DeviceID)
	if err != nil || deviceState.ClientVersion != "0.5.1" {
		t.Fatalf("acknowledged runtime version: %#v %v", deviceState, err)
	}
	authorization, err := client.StartAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ApproveDeviceAuthorization(ctx, authorization.UserCode, 1); err != nil {
		t.Fatal(err)
	}
	accessToken, err := client.PollAuthorization(ctx, authorization.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	version := "123456"
	result := discovery.Result{Installations: []discovery.Installation{{Launcher: "steam", ExternalGameID: "730", DisplayName: "Counter-Strike 2", DetectedVersion: &version, VersionSource: "steam-buildid", InstallPath: `C:\Steam\steamapps\common\Counter-Strike Global Offensive`}}}
	if err = client.UploadInventory(ctx, accessToken, "scan-client-test", result); err != nil {
		t.Fatal(err)
	}
	inventory, err := st.DeviceInventory(ctx, profile.DeviceID)
	if err != nil || len(inventory.Installations) != 1 || inventory.Installations[0].ExternalGameID != "730" {
		t.Fatalf("inventory=%#v err=%v", inventory, err)
	}
}
