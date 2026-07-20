package windowsapp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/deviceapi"
	"github.com/containerguy/lan_installer/internal/deviceclient"
	"github.com/containerguy/lan_installer/internal/discovery"
	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/store"
)

func signedEventReadinessEnvelope(t *testing.T, privateKey ed25519.PrivateKey, sequence int64) []byte {
	t.Helper()
	now := time.Now().UTC()
	digest := "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	payload := []byte(fmt.Sprintf(`{"formatVersion":2,"eventId":"lan-2026","releaseId":"01K0LANREADY00000000000002","sequence":%d,"issuedAt":%q,"validUntil":%q,"minimumClientVersion":"1.0.0","artifacts":[{"digest":%q,"size":42,"mediaType":"application/zip","fileName":"cs2.zip"}],"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"verify","artifactDigest":%q}]}],"games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","externalGameId":"730","version":"200","required":true,"payloads":[{"type":"archive","artifacts":[%q],"actions":[{"adapter":"lanready_archive","operation":"verify","artifactDigest":%q}]}]}]}`,
		sequence, now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), digest, digest, digest, digest))
	envelope, err := protocol.SignEnvelope(payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type blockingTransport struct{ started chan struct{} }

func (transport *blockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	close(transport.started)
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestNormalizeServerURL(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "origin", value: "https://game-manager.familie-keller.info/", want: DefaultServerURL},
		{name: "http rejected", value: "http://game-manager.example", wantErr: true},
		{name: "credentials rejected", value: "https://admin:secret@example.test", wantErr: true},
		{name: "path rejected", value: "https://example.test/lanready", wantErr: true},
		{name: "query rejected", value: "https://example.test/?token=x", wantErr: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeServerURL(test.value)
			if test.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !test.wantErr && (err != nil || got != test.want) {
				t.Fatalf("normalizeServerURL() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestVerificationURLMustRemainOnEnrolledHTTPSOrigin(t *testing.T) {
	t.Parallel()
	got, err := verificationURL("https://manager.example", "https://manager.example/admin/device?from=client", "AB CD")
	if err != nil || got != "https://manager.example/admin/device?code=AB+CD&from=client" {
		t.Fatalf("verificationURL() = %q, %v", got, err)
	}
	for _, value := range []string{
		"http://manager.example/admin/device",
		"https://evil.example/admin/device",
		"file:///C:/Windows/System32/calc.exe",
		"https://user:pass@manager.example/admin/device",
		"https://manager.example/admin/device#fragment",
	} {
		if _, err = verificationURL("https://manager.example", value, "CODE"); err == nil {
			t.Fatalf("unsafe verification URL %q was accepted", value)
		}
	}
}

func TestAuthorizedSelectionIsDeepCopied(t *testing.T) {
	t.Parallel()
	version := "100"
	result := discovery.Result{Installations: []discovery.Installation{{DisplayName: "Original", DetectedVersion: &version}}}
	selected, err := selectedInstallations(result, []int{0})
	if err != nil {
		t.Fatal(err)
	}
	result.Installations[0].DisplayName = "Changed"
	*result.Installations[0].DetectedVersion = "200"
	if selected[0].DisplayName != "Original" || selected[0].DetectedVersion == nil || *selected[0].DetectedVersion != "100" {
		t.Fatalf("selection changed with source: %#v", selected)
	}
}

func TestSelectedInstallationsRequiresFreshUniqueSelection(t *testing.T) {
	t.Parallel()
	result := discovery.Result{Installations: []discovery.Installation{{DisplayName: "One"}, {DisplayName: "Two"}}}
	selected, err := selectedInstallations(result, []int{1, 0})
	if err != nil || len(selected) != 2 || selected[0].DisplayName != "Two" {
		t.Fatalf("unexpected selection: %#v, %v", selected, err)
	}
	for _, indices := range [][]int{nil, {-1}, {2}, {0, 0}} {
		if _, err = selectedInstallations(result, indices); err == nil {
			t.Fatalf("selection %v should fail", indices)
		}
	}
}

func TestStateWithoutProfileIsDisconnected(t *testing.T) {
	t.Parallel()
	app := New(filepath.Join(t.TempDir(), "missing.json"), "1.2.3")
	state, err := app.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Connected || state.ServerURL != DefaultServerURL || state.ClientVersion != "1.2.3" {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestProfilePathIsStableAcrossPortableExecutableLocations(t *testing.T) {
	configRoot := t.TempDir()
	first, err := profilePathFromConfigRoot(configRoot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := profilePathFromConfigRoot(configRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(configRoot, "LANReady", "device.json")
	if first != want || second != want {
		t.Fatalf("profile path changed between portable versions: first=%q second=%q want=%q", first, second, want)
	}
	for _, invalid := range []string{"", ".", "portable-build-folder"} {
		if path, pathErr := profilePathFromConfigRoot(invalid); pathErr == nil || path != "" {
			t.Fatalf("relative fallback accepted for %q: path=%q err=%v", invalid, path, pathErr)
		}
	}
}

func TestLegacyPortableProfileMigratesOnceAndSurvivesExecutableMove(t *testing.T) {
	configRoot := t.TempDir()
	firstExecutableDir := filepath.Join(t.TempDir(), "LANReady-Portable-Old")
	secondExecutableDir := filepath.Join(t.TempDir(), "LANReady-Portable-New")
	if err := os.MkdirAll(firstExecutableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(firstExecutableDir, "lanready-device.json")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	want := deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "stable-device", DeviceName: "Gaming-PC", ClientVersion: "0.1.0", PrivateKey: privateKey}
	if err = deviceclient.SaveProfile(legacyPath, want); err != nil {
		t.Fatal(err)
	}
	firstPath, err := resolveDefaultProfilePath(configRoot, []string{firstExecutableDir})
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := resolveDefaultProfilePath(configRoot, []string{secondExecutableDir})
	if err != nil {
		t.Fatal(err)
	}
	if firstPath != secondPath || firstPath != filepath.Join(configRoot, "LANReady", "device.json") {
		t.Fatalf("profile moved with executable: first=%q second=%q", firstPath, secondPath)
	}
	got, err := deviceclient.LoadProfile(secondPath)
	if err != nil || got.DeviceID != want.DeviceID || !got.PrivateKey.Equal(want.PrivateKey) {
		t.Fatalf("migrated profile=%#v err=%v", got, err)
	}
	if _, err = os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy profile remained after migration: %v", err)
	}
}

func TestLegacyProfileMigratesWhenUserConfigDirFails(t *testing.T) {
	homeRoot := t.TempDir()
	legacyDir := filepath.Join(t.TempDir(), "LANReady-Portable-Legacy")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "lanready-device.json")
	if err = deviceclient.SaveProfile(legacyPath, deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "legacy-device", DeviceName: "PC", ClientVersion: "0.1.0", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	stable, err := resolveDefaultProfilePathFromUserDirs("", errors.New("APPDATA fehlt"), homeRoot, nil, []string{legacyDir})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeRoot, "AppData", "Roaming", "LANReady", "device.json")
	profile, err := deviceclient.LoadProfile(stable)
	if err != nil || stable != want || profile.DeviceID != "legacy-device" || !profile.PrivateKey.Equal(privateKey) {
		t.Fatalf("fallback migration path=%q profile=%#v err=%v", stable, profile, err)
	}
}

func TestNewPortableFindsLegacyProfileInPreviousDownloadsFolder(t *testing.T) {
	homeRoot := t.TempDir()
	oldDir := filepath.Join(homeRoot, "Downloads", "LANReady-Portable-Old")
	newDir := filepath.Join(homeRoot, "Downloads", "LANReady-Portable-New")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = deviceclient.SaveProfile(filepath.Join(oldDir, "lanready-device.json"), deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "old-portable-device", DeviceName: "PC", ClientVersion: "0.1.0", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	searchDirs := append([]string{newDir}, commonLegacyProfileDirs(homeRoot, 2, 10000)...)
	stable, err := resolveDefaultProfilePathFromUserDirs(filepath.Join(homeRoot, "AppData", "Roaming"), nil, homeRoot, nil, searchDirs)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := deviceclient.LoadProfile(stable)
	if err != nil || profile.DeviceID != "old-portable-device" || !profile.PrivateKey.Equal(privateKey) {
		t.Fatalf("new portable did not recover old identity: %#v err=%v", profile, err)
	}
}

func TestLegacyMigrationRejectsCopiesWithDifferentAntiRollbackState(t *testing.T) {
	homeRoot := t.TempDir()
	firstDir := filepath.Join(homeRoot, "Downloads", "LANReady-Old-A")
	secondDir := filepath.Join(homeRoot, "Downloads", "LANReady-Old-B")
	for _, dir := range []string{firstDir, secondDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	base := deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "same-device", DeviceName: "PC", ClientVersion: "0.1.0", PrivateKey: privateKey, UpdateSequence: 4, EventSequences: map[string]int64{"lan": 7}}
	if err = deviceclient.SaveProfile(filepath.Join(firstDir, "lanready-device.json"), base); err != nil {
		t.Fatal(err)
	}
	older := base
	older.UpdateSequence = 3
	older.EventSequences = map[string]int64{"lan": 6}
	if err = deviceclient.SaveProfile(filepath.Join(secondDir, "lanready-device.json"), older); err != nil {
		t.Fatal(err)
	}
	stable, err := resolveDefaultProfilePath(filepath.Join(homeRoot, "AppData", "Roaming"), commonLegacyProfileDirs(homeRoot, 2, 10000))
	if err == nil || !strings.Contains(err.Error(), "mehrere unterschiedliche") {
		t.Fatalf("ambiguous anti-rollback profiles accepted: path=%q err=%v", stable, err)
	}
	if _, statErr := os.Stat(filepath.Join(homeRoot, "AppData", "Roaming", "LANReady", "device.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stable profile was written despite ambiguity: %v", statErr)
	}
}

func TestLegacyMigrationRejectsSymlinkCandidate(t *testing.T) {
	configRoot := t.TempDir()
	legacyDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "device.json")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = deviceclient.SaveProfile(target, deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "device", DeviceName: "PC", ClientVersion: "0.1.0", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(legacyDir, "lanready-device.json")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, err = resolveDefaultProfilePath(configRoot, []string{legacyDir}); err == nil || !strings.Contains(err.Error(), "keine reguläre Datei") {
		t.Fatalf("symlinked legacy profile accepted: %v", err)
	}
}

func TestStateVerifiesActiveEventAndRejectsRollback(t *testing.T) {
	publicKey, releasePrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var releaseMu sync.RWMutex
	releaseEnvelope := signedEventReadinessEnvelope(t, releasePrivateKey, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/device/bootstrap":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"apiVersion":2,"activeEvent":{"eventId":"lan-2026","releaseUrl":"/v2/events/lan-2026/release"},"clientUpdate":{"required":false,"releaseUrl":"/v2/client/releases/latest?channel=stable"}}`))
		case r.URL.Path == "/v2/events/lan-2026/release":
			releaseMu.RLock()
			current := append([]byte(nil), releaseEnvelope...)
			releaseMu.RUnlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(current)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","message":"not found"}`))
		}
	}))
	defer server.Close()
	_, devicePrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "device.json")
	if err = deviceclient.SaveProfile(profilePath, deviceclient.Profile{ServerURL: server.URL, DeviceID: "device", DeviceName: "PC", ClientVersion: "1.0.0", PrivateKey: devicePrivateKey}); err != nil {
		t.Fatal(err)
	}
	version := "200"
	app := New(profilePath, "1.0.0", publicKey)
	app.httpClient = server.Client()
	app.discover = func() (discovery.Result, error) {
		return discovery.Result{Installations: []discovery.Installation{{Launcher: "steam", ExternalGameID: "730", DisplayName: "Counter-Strike 2", DetectedVersion: &version}}}, nil
	}
	state, err := app.State()
	if err != nil {
		t.Fatal(err)
	}
	readiness := state.EventReadiness
	if readiness.State != "warning" || readiness.Percentage != 100 || readiness.ReadyGames != 1 || len(readiness.Games) != 1 || readiness.Games[0].Status != "ready" || len(readiness.Launchers) != 1 || readiness.Launchers[0].Status != "detected_version_unverified" {
		t.Fatalf("readiness: %#v", readiness)
	}
	stored, err := deviceclient.LoadProfile(profilePath)
	if err != nil || stored.EventSequences["lan-2026"] != 2 {
		t.Fatalf("event watermark: %#v %v", stored.EventSequences, err)
	}
	releaseMu.Lock()
	releaseEnvelope = signedEventReadinessEnvelope(t, releasePrivateKey, 1)
	releaseMu.Unlock()
	state, err = app.State()
	if err != nil || state.EventReadiness.State != "security_error" || !strings.Contains(state.EventReadiness.Message, "älterer signierter Eventstand") {
		t.Fatalf("rollback state: %#v %v", state.EventReadiness, err)
	}

	releaseMu.Lock()
	releaseEnvelope = signedEventReadinessEnvelope(t, releasePrivateKey, 3)
	releaseMu.Unlock()
	blocked := make(chan struct{})
	app.discover = func() (discovery.Result, error) {
		<-blocked
		return discovery.Result{}, nil
	}
	app.stateTimeout = 50 * time.Millisecond
	app.discoveryTimeout = 50 * time.Millisecond
	started := time.Now()
	state, err = app.State()
	if err != nil || time.Since(started) > 500*time.Millisecond || state.EventReadiness.State != "error" || !strings.Contains(state.EventReadiness.Message, "Zeitlimit") {
		t.Fatalf("bounded readiness discovery: %#v elapsed=%s err=%v", state.EventReadiness, time.Since(started), err)
	}
	started = time.Now()
	if _, err = app.Discover(); err == nil || time.Since(started) > 500*time.Millisecond || !strings.Contains(err.Error(), "Zeitlimit") {
		t.Fatalf("bounded manual discovery: elapsed=%s err=%v", time.Since(started), err)
	}
	close(blocked)
	if _, err = app.Discover(); err != nil {
		t.Fatalf("discovery mutex remained blocked after timeout: %v", err)
	}
}

func TestMatchEventGamePrefersExactVersionDeterministically(t *testing.T) {
	t.Parallel()
	unknown, old, exact := "", "100", "200"
	installations := []discovery.Installation{
		{Launcher: "steam", ExternalGameID: "730", DetectedVersion: &unknown},
		{Launcher: "steam", ExternalGameID: "730", DetectedVersion: &old},
		{Launcher: "steam", ExternalGameID: "730", DetectedVersion: &exact},
	}
	status, detected := matchEventGame(installations, "steam", "730", "200")
	if status != "ready" || detected != "200" {
		t.Fatalf("match: %s %q", status, detected)
	}
	status, detected = matchEventGame(installations[:2], "steam", "730", "200")
	if status != "version_mismatch" || detected != "100" {
		t.Fatalf("fallback match: %s %q", status, detected)
	}
}

func TestEventWatermarksNeverEvictOlderEventIDs(t *testing.T) {
	t.Parallel()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sequences := make(map[string]int64, 128)
	for index := 0; index < 128; index++ {
		sequences[fmt.Sprintf("event-%03d", index)] = 10
	}
	profilePath := filepath.Join(t.TempDir(), "device.json")
	if err = deviceclient.SaveProfile(profilePath, deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "device", DeviceName: "PC", ClientVersion: "1.0.0", PrivateKey: privateKey, EventSequences: sequences}); err != nil {
		t.Fatal(err)
	}
	app := New(profilePath, "1.0.0")
	if err = app.persistEventSequence("event-128", 1); err != nil {
		t.Fatal(err)
	}
	stored, err := deviceclient.LoadProfile(profilePath)
	if err != nil || len(stored.EventSequences) != 129 || stored.EventSequences["event-000"] != 10 {
		t.Fatalf("watermarks after event 129: count=%d oldest=%d err=%v", len(stored.EventSequences), stored.EventSequences["event-000"], err)
	}
	if err = app.persistEventSequence("event-000", 9); err == nil || !strings.Contains(err.Error(), "älterer signierter Eventstand") {
		t.Fatalf("rollback after event 129 was not rejected: %v", err)
	}
}

func TestAuthorizationBindsDeepCopiedSelectionAndRejectsStaleScan(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err = st.BootstrapAdmin(ctx, "admin", "unused"); err != nil {
		t.Fatal(err)
	}
	var api *deviceapi.API
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(w, r) }))
	defer server.Close()
	api, err = deviceapi.New(st, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	code, err := st.CreateEnrollmentCode(ctx, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	firstVersion, secondVersion := "100", "200"
	source := discovery.Result{Installations: []discovery.Installation{
		{Launcher: "steam", ExternalGameID: "one", DisplayName: "One", DetectedVersion: &firstVersion, VersionSource: "test", InstallPath: `C:\One`},
		{Launcher: "steam", ExternalGameID: "two", DisplayName: "Two", DetectedVersion: &secondVersion, VersionSource: "test", InstallPath: `C:\Two`},
	}}
	app := New(filepath.Join(t.TempDir(), "device.json"), "0.0.0-test")
	app.httpClient = server.Client()
	app.discover = func() (discovery.Result, error) { return source, nil }
	if _, err = app.Enroll(EnrollmentInput{ServerURL: server.URL, Code: code.Code, DeviceName: "Test-PC"}); err != nil {
		t.Fatal(err)
	}
	if state, stateErr := app.State(); stateErr != nil || !state.ServerReachable || state.UpdateRequired {
		t.Fatalf("initial server policy: %#v %v", state, stateErr)
	}
	if _, err = app.Discover(); err != nil {
		t.Fatal(err)
	}
	// Mutating the discoverer's backing slice must not change the server-bound
	// scan snapshot held by the app.
	source.Installations[0].ExternalGameID = "mutated"
	*source.Installations[0].DetectedVersion = "999"
	authorization, err := app.StartAuthorization([]int{0})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ApproveDeviceAuthorization(ctx, authorization.UserCode, 1); err != nil {
		t.Fatal(err)
	}
	if result, pollErr := app.PollAuthorization(authorization.AuthorizationID); pollErr != nil || result.Status != "authorized" {
		t.Fatalf("poll = %#v, %v", result, pollErr)
	}
	if _, err = app.SyncInventory(); err != nil {
		t.Fatal(err)
	}
	state, err := app.State()
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := st.DeviceInventory(ctx, state.DeviceID)
	if err != nil || len(inventory.Installations) != 1 || inventory.Installations[0].ExternalGameID != "one" || inventory.Installations[0].DetectedVersion == nil || *inventory.Installations[0].DetectedVersion != "100" {
		t.Fatalf("inventory = %#v, %v", inventory, err)
	}

	stale, err := app.StartAuthorization([]int{1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.Discover(); err != nil {
		t.Fatal(err)
	}
	if _, err = app.PollAuthorization(stale.AuthorizationID); err == nil {
		t.Fatal("authorization from an older discovery generation was accepted")
	}
}

func TestCancelAuthorizationIsIDBoundAndCancelsActivePoll(t *testing.T) {
	t.Parallel()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "device.json")
	if err = deviceclient.SaveProfile(profilePath, deviceclient.Profile{ServerURL: "https://manager.example", DeviceID: "device", DeviceName: "PC", ClientVersion: "0.0.0-test", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	transport := &blockingTransport{started: make(chan struct{})}
	app := New(profilePath, "0.0.0-test")
	app.httpClient = &http.Client{Transport: transport}
	app.discoveryGeneration = 1
	app.authorizationGeneration = 1
	app.authorizationID = "current-authorization"
	app.lastDiscovery = &discovery.Result{Installations: []discovery.Installation{{ExternalGameID: "one"}}}
	if app.CancelAuthorization("different-authorization") {
		t.Fatal("mismatched cancellation was accepted")
	}
	if app.authorizationID != "current-authorization" {
		t.Fatal("mismatched cancellation cleared the active authorization")
	}
	pollDone := make(chan error, 1)
	go func() {
		_, pollErr := app.PollAuthorization("current-authorization")
		pollDone <- pollErr
	}()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("poll did not start")
	}
	cancelDone := make(chan bool, 1)
	go func() { cancelDone <- app.CancelAuthorization("current-authorization") }()
	select {
	case canceled := <-cancelDone:
		if !canceled {
			t.Fatal("matching cancellation was rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the active poll")
	}
	select {
	case err = <-pollDone:
		if err == nil {
			t.Fatal("canceled poll succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled poll did not return")
	}
}
