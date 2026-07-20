package store

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestDeviceAuthorizationAndInventory(t *testing.T) {
	st, err := Open(t.TempDir() + "/inventory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.db.Exec(`INSERT INTO users(id,username,password_hash,active) VALUES(1,'tester','unused',1)`); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err = st.CreateDevice(ctx, Device{ID: "device-1", Name: "Gaming-PC", PublicKey: key, WindowsVersion: "11", ClientVersion: "0.3.0"}); err != nil {
		t.Fatal(err)
	}
	auth, err := st.CreateDeviceAuthorization(ctx, "device-1", "https://manager.example", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if auth.UserCode == "" || auth.AuthorizationID == "" || auth.VerificationURL != "https://manager.example/admin/device" {
		t.Fatalf("unexpected authorization: %#v", auth)
	}
	if _, err = st.PollDeviceAuthorization(ctx, "device-1", auth.AuthorizationID, 15*time.Minute); !errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("expected pending, got %v", err)
	}
	if err = st.ApproveDeviceAuthorization(ctx, auth.UserCode, 1); err != nil {
		t.Fatal(err)
	}
	token, err := st.PollDeviceAuthorization(ctx, "device-1", auth.AuthorizationID, 15*time.Minute)
	if err != nil || token == "" {
		t.Fatalf("consume: token=%q err=%v", token, err)
	}
	if _, err = st.PollDeviceAuthorization(ctx, "device-1", auth.AuthorizationID, 15*time.Minute); !errors.Is(err, ErrAuthorizationUsed) {
		t.Fatalf("expected consumed, got %v", err)
	}
	version := "build-123"
	scan := InventoryScan{ID: "scan-1", ClientVersion: "0.4.0", ScannedAt: time.Now().UTC(), Installations: []InventoryInstallation{{Launcher: "steam", ExternalGameID: "730", DisplayName: "Counter-Strike 2", DetectedVersion: &version, VersionSource: "steam-buildid", InstallPath: `C:\\Steam\\steamapps\\common\\Counter-Strike Global Offensive`}, {Launcher: "ea_app", ExternalGameID: "OFB-EAST:109552154", DisplayName: "Beispiel", VersionSource: "unknown", InstallPath: `D:\\Games\\EA\\Beispiel`}}}
	invalidVersion := scan
	invalidVersion.ID = "scan-invalid-version"
	invalidVersion.ClientVersion = "not-semver"
	if err = st.SaveDeviceInventory(ctx, "device-1", token, invalidVersion); err == nil {
		t.Fatal("inventory accepted a non-SemVer runtime version")
	}
	if err = st.SaveDeviceInventory(ctx, "device-1", token, scan); err != nil {
		t.Fatal(err)
	}
	if err = st.SaveDeviceInventory(ctx, "device-1", token, scan); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	conflict := scan
	conflict.Installations = append([]InventoryInstallation(nil), scan.Installations...)
	conflict.Installations[0].DisplayName = "Manipulierter Name"
	if err = st.SaveDeviceInventory(ctx, "device-1", token, conflict); !errors.Is(err, ErrInventoryConflict) {
		t.Fatalf("different payload with same scan id accepted: %v", err)
	}
	got, err := st.DeviceInventory(ctx, "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ScanID != "scan-1" || got.ClientVersion != "0.4.0" || len(got.Installations) != 2 || got.Installations[0].DetectedVersion == nil || *got.Installations[0].DetectedVersion != version {
		t.Fatalf("unexpected inventory: %#v", got)
	}
	if got.Installations[1].DetectedVersion != nil {
		t.Fatalf("unknown version was invented: %#v", got.Installations[1])
	}
	launchers, err := st.Launchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var standaloneID int64
	for _, launcher := range launchers {
		if launcher.Adapter == "standalone" {
			standaloneID = launcher.ID
		}
	}
	if _, err = st.SaveGameAtomic(ctx, Game{Slug: "open-ra", Name: "OpenRA", LauncherID: standaloneID, Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	manualVersion := "2026.1"
	manual := InventoryScan{ID: "manual-valid", ClientVersion: "0.4.0", ScannedAt: time.Now().UTC(), Installations: []InventoryInstallation{{Launcher: "standalone", ExternalGameID: "open-ra", DisplayName: "OpenRA", DetectedVersion: &manualVersion, VersionSource: "windows-file-version", InstallPath: `D:\\Games\\OpenRA`}}}
	if err = st.SaveDeviceInventory(ctx, "device-1", token, manual); err != nil {
		t.Fatalf("catalog-bound standalone inventory: %v", err)
	}
	manual.ID = "manual-unknown"
	manual.Installations[0].ExternalGameID = "invented-game"
	if err = st.SaveDeviceInventory(ctx, "device-1", token, manual); err == nil {
		t.Fatal("free standalone inventory id was accepted")
	}
	manual.ID = "manual-source"
	manual.Installations[0].ExternalGameID = "open-ra"
	manual.Installations[0].VersionSource = "user-entered"
	if err = st.SaveDeviceInventory(ctx, "device-1", token, manual); err == nil {
		t.Fatal("unverified standalone version evidence was accepted")
	}
}

func TestDeviceRuntimeVersionAcknowledgementIsMonotonic(t *testing.T) {
	st, err := Open(t.TempDir() + "/device-version.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	if err = st.CreateDevice(context.Background(), Device{ID: "versioned", Name: "PC", PublicKey: key, WindowsVersion: "11", ClientVersion: "0.1.0"}); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateActiveDeviceClientVersion(context.Background(), "versioned", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateActiveDeviceClientVersion(context.Background(), "versioned", "0.1.9"); !errors.Is(err, ErrDeviceVersionRollback) {
		t.Fatalf("runtime version rollback: %v", err)
	}
	device, err := st.ActiveDevice(context.Background(), "versioned")
	if err != nil || device.ClientVersion != "0.1.9" || device.ClientVersionHighWatermark != "0.2.0" {
		t.Fatalf("stored runtime version: %#v %v", device, err)
	}
}

func TestDeviceAuthorizationOutstandingLimitIsAtomic(t *testing.T) {
	st, err := Open(t.TempDir() + "/authorization-limit.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	if err = st.CreateDevice(context.Background(), Device{ID: "limited", Name: "PC", PublicKey: key, WindowsVersion: "11", ClientVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			_, createErr := st.CreateDeviceAuthorization(context.Background(), "limited", "https://manager.example", time.Minute)
			results <- createErr
		}()
	}
	success := 0
	for i := 0; i < 12; i++ {
		if <-results == nil {
			success++
		}
	}
	if success != 5 {
		t.Fatalf("expected exactly 5 outstanding authorizations, got %d", success)
	}
}

func TestInventoryRejectsWrongDeviceTokenAndInvalidLauncher(t *testing.T) {
	st, err := Open(t.TempDir() + "/inventory.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err = st.SaveDeviceInventory(ctx, "missing", "invalid", InventoryScan{ID: "scan", ClientVersion: "1", ScannedAt: time.Now(), Installations: []InventoryInstallation{{Launcher: "epic", ExternalGameID: "1", DisplayName: "Game", VersionSource: "unknown", InstallPath: `C:\\Game`}}}); err == nil || errors.Is(err, ErrUserTokenInvalid) {
		t.Fatalf("invalid launcher should fail first, got %v", err)
	}
}
