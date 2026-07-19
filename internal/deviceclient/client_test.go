package deviceclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
