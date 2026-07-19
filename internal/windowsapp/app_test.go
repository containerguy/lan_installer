package windowsapp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerguy/lan_installer/internal/deviceapi"
	"github.com/containerguy/lan_installer/internal/deviceclient"
	"github.com/containerguy/lan_installer/internal/discovery"
	"github.com/containerguy/lan_installer/internal/store"
)

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
