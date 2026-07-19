package windowsapp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/containerguy/lan_installer/internal/deviceclient"
	"github.com/containerguy/lan_installer/internal/discovery"
	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/selfupdate"
	"github.com/containerguy/lan_installer/internal/windowsupdate"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const DefaultServerURL = "https://game-manager.familie-keller.info"

type App struct {
	profilePath string
	version     string
	httpClient  *http.Client
	discover    func() (discovery.Result, error)

	opMu                    sync.Mutex
	discoveryGeneration     uint64
	lastDiscovery           *discovery.Result
	authorizationID         string
	authorizationGeneration uint64
	authorizedSelection     []discovery.Installation
	authorizedWarnings      []string
	accessToken             string
	serverReady             bool
	updateRequired          bool
	updateAvailable         bool
	trustedReleaseKeys      map[string]ed25519.PublicKey
	expectedPublisherSHA256 string
	runtimeContext          context.Context
	healthRequest           *selfupdate.HealthRequest
	healthReady             chan struct{}
	healthReadyOnce         sync.Once
	agentMode               bool
	allowQuit               bool
	updateOpMu              sync.Mutex
	updateCancel            context.CancelFunc
	updateProgress          UpdateProgress
	pollMu                  sync.Mutex
	pollAuthorizationID     string
	pollCancel              context.CancelFunc
}

type State struct {
	Connected        bool   `json:"connected"`
	ServerURL        string `json:"serverUrl"`
	DeviceID         string `json:"deviceId,omitempty"`
	DeviceName       string `json:"deviceName,omitempty"`
	ClientVersion    string `json:"clientVersion"`
	ServerReachable  bool   `json:"serverReachable"`
	ServerWarning    string `json:"serverWarning,omitempty"`
	UpdateRequired   bool   `json:"updateRequired"`
	UpdateAvailable  bool   `json:"updateAvailable"`
	UpdateVersion    string `json:"updateVersion,omitempty"`
	AgentMode        bool   `json:"agentMode"`
	UpdateReleaseURL string `json:"updateReleaseUrl,omitempty"`
}

type EnrollmentInput struct {
	ServerURL  string `json:"serverUrl"`
	Code       string `json:"code"`
	DeviceName string `json:"deviceName"`
}

type AuthorizationView struct {
	AuthorizationID     string `json:"authorizationId"`
	UserCode            string `json:"userCode"`
	VerificationURL     string `json:"verificationUrl"`
	ExpiresAt           string `json:"expiresAt"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
	BrowserWarning      string `json:"browserWarning,omitempty"`
}

type PollResult struct {
	Status string `json:"status"`
}

type SyncResult struct {
	Uploaded int `json:"uploaded"`
}

type UpdateProgress struct {
	Running    bool   `json:"running"`
	Stage      string `json:"stage"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}

func New(profilePath, version string, trustedKeys ...ed25519.PublicKey) *App {
	if strings.TrimSpace(profilePath) == "" {
		profilePath = DefaultProfilePath()
	}
	if strings.TrimSpace(version) == "" {
		version = "0.0.0-dev"
	}
	keyring := make(map[string]ed25519.PublicKey, len(trustedKeys))
	for _, key := range trustedKeys {
		if len(key) == ed25519.PublicKeySize {
			keyring[protocol.KeyID(key)] = append(ed25519.PublicKey(nil), key...)
		}
	}
	return &App{profilePath: profilePath, version: version, discover: discovery.Discover, trustedReleaseKeys: keyring, healthReady: make(chan struct{})}
}

func (a *App) SetHealthRequest(request selfupdate.HealthRequest) { a.healthRequest = &request }
func (a *App) SetExpectedPublisherSHA256(value string) {
	a.expectedPublisherSHA256 = strings.ToLower(strings.TrimSpace(value))
}
func (a *App) SetAgentMode(value bool) { a.agentMode = value }

func (a *App) OnStartup(ctx context.Context) {
	a.opMu.Lock()
	a.runtimeContext = ctx
	health := a.healthRequest
	a.opMu.Unlock()
	if health != nil {
		go func() {
			select {
			case <-a.healthReady:
				_ = selfupdate.SignalHealthy(*health)
			case <-time.After(45 * time.Second):
			case <-ctx.Done():
			}
		}()
	}
	if a.agentMode {
		a.startAgent(ctx)
	}
}

func (a *App) ShowWindow() {
	a.opMu.Lock()
	ctx := a.runtimeContext
	a.opMu.Unlock()
	if ctx != nil {
		wailsruntime.WindowShow(ctx)
		wailsruntime.WindowUnminimise(ctx)
	}
}

func (a *App) OnBeforeClose(ctx context.Context) bool {
	a.opMu.Lock()
	keepRunning := a.agentMode && !a.allowQuit
	a.opMu.Unlock()
	if keepRunning {
		wailsruntime.WindowHide(ctx)
	}
	return keepRunning
}

// ConfirmUIReady is called by the embedded frontend only after it rendered and
// completed an initial profile/state check. Reaching OnStartup alone is not a
// sufficient post-update health signal.
func (a *App) ConfirmUIReady() {
	if a.healthRequest != nil {
		a.healthReadyOnce.Do(func() { close(a.healthReady) })
	}
}

func DefaultProfilePath() string {
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		return "lanready-device.json"
	}
	return filepath.Join(root, "LANReady", "device.json")
}

func (a *App) State() (State, error) {
	a.opMu.Lock()
	state, err := a.state()
	a.opMu.Unlock()
	if err != nil || !state.Connected {
		return state, err
	}
	profile, err := deviceclient.LoadProfile(a.profilePath)
	if err != nil {
		return State{}, fmt.Errorf("Geräteprofil für Serverstatus laden: %w", err)
	}
	profile.ClientVersion = a.version
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	bootstrap, bootstrapErr := (&deviceclient.Client{Profile: profile, HTTP: a.httpClient}).Bootstrap(ctx, a.version)
	if bootstrapErr != nil {
		a.opMu.Lock()
		a.serverReady = false
		a.updateRequired = false
		a.opMu.Unlock()
		state.ServerWarning = "Managementserver derzeit nicht erreichbar oder Statusprüfung fehlgeschlagen: " + bootstrapErr.Error()
		return state, nil
	}
	state.ServerReachable = true
	state.UpdateRequired = bootstrap.ClientUpdate.Required
	state.UpdateAvailable = bootstrap.ClientUpdate.Required
	state.UpdateReleaseURL = bootstrap.ClientUpdate.ReleaseURL
	if len(a.trustedReleaseKeys) > 0 {
		available, updateErr := (&deviceclient.Client{Profile: profile, HTTP: a.httpClient}).CheckUpdate(ctx, a.version, profile.UpdateSequence, a.trustedReleaseKeys)
		if updateErr == nil {
			if available.PublisherCertificateSHA256 == a.expectedPublisherSHA256 && len(a.expectedPublisherSHA256) == 64 {
				state.UpdateAvailable = true
				state.UpdateVersion = available.Version
			} else if state.UpdateRequired {
				state.UpdateAvailable = false
				state.ServerWarning = "Das erforderliche Clientupdate stammt nicht vom für diesen Build erwarteten Authenticode-Herausgeber."
			}
		} else if state.UpdateRequired {
			if errors.Is(updateErr, deviceclient.ErrNoClientUpdate) {
				state.UpdateAvailable = false
				state.ServerWarning = "Der geschützte Update-Höchststand dieses Benutzerprofils liegt bereits auf oder über dem angebotenen Release. Bitte den aktuell signierten Installer manuell ausführen; ein automatischer Rollback wird aus Sicherheitsgründen abgelehnt."
			} else {
				state.ServerWarning = "Das erforderliche Clientupdate konnte nicht sicher geprüft werden: " + updateErr.Error()
			}
		}
	}
	a.opMu.Lock()
	a.serverReady = true
	a.updateRequired = bootstrap.ClientUpdate.Required
	a.updateAvailable = state.UpdateAvailable
	a.opMu.Unlock()
	if state.UpdateRequired {
		state.ServerWarning = "Für LANReady ist ein signiertes Clientupdate erforderlich. Installationen und Eventinhalte bleiben bis zur Aktualisierung gesperrt."
	}
	return state, nil
}

func (a *App) state() (State, error) {
	state := State{ServerURL: DefaultServerURL, ClientVersion: a.version, AgentMode: a.agentMode}
	profile, err := deviceclient.LoadProfile(a.profilePath)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("das lokale Geräteprofil ist beschädigt oder für diesen Windows-Benutzer nicht lesbar: %w", err)
	}
	state.Connected = true
	state.ServerURL = profile.ServerURL
	state.DeviceID = profile.DeviceID
	state.DeviceName = profile.DeviceName
	return state, nil
}

func (a *App) QuitApplication() {
	a.opMu.Lock()
	a.allowQuit = true
	ctx := a.runtimeContext
	a.opMu.Unlock()
	if ctx != nil {
		wailsruntime.Quit(ctx)
	}
}

func (a *App) Enroll(input EnrollmentInput) (State, error) {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	serverURL, err := normalizeServerURL(input.ServerURL)
	if err != nil {
		return State{}, err
	}
	code := strings.ToUpper(strings.TrimSpace(input.Code))
	if code == "" || len(code) > 64 {
		return State{}, errors.New("bitte einen gültigen Enrollment-Code eingeben")
	}
	if _, err = os.Stat(a.profilePath); err == nil {
		return State{}, errors.New("dieser Benutzer ist bereits mit einem Gerät verbunden; vor einem Wechsel zuerst die Verbindung trennen")
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, fmt.Errorf("lokales Geräteprofil prüfen: %w", err)
	}
	if err = deviceclient.PreflightProfile(a.profilePath); err != nil {
		return State{}, fmt.Errorf("lokalen DPAPI- und Speicherzugriff prüfen: %w", err)
	}
	deviceName := strings.TrimSpace(input.DeviceName)
	if deviceName == "" {
		deviceName, _ = os.Hostname()
	}
	if deviceName == "" || len(deviceName) > 128 {
		return State{}, errors.New("der Gerätename muss zwischen 1 und 128 Zeichen lang sein")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	profile, err := deviceclient.Enroll(ctx, serverURL, code, deviceName, windowsupdate.OSVersion(ctx), a.version, a.httpClient)
	if err != nil {
		return State{}, fmt.Errorf("Gerät verbinden: %w", err)
	}
	if err = deviceclient.SaveProfile(a.profilePath, profile); err != nil {
		return State{}, fmt.Errorf("Geräteprofil sicher speichern: %w", err)
	}
	a.clearSessionLocked()
	return a.state()
}

func (a *App) Disconnect() error {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if err := os.Remove(a.profilePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Geräteprofil löschen: %w", err)
	}
	a.clearSessionLocked()
	a.serverReady = false
	a.updateRequired = false
	a.updateAvailable = false
	return nil
}

func (a *App) InstallUpdate() (returnErr error) {
	a.opMu.Lock()
	if !a.updateRequired && !a.updateAvailable {
		a.opMu.Unlock()
		return errors.New("der Managementserver bietet derzeit kein neueres Clientupdate an")
	}
	if len(a.trustedReleaseKeys) == 0 {
		a.opMu.Unlock()
		return errors.New("dieser Build enthält keinen vertrauenswürdigen Release-Public-Key")
	}
	if a.runtimeContext == nil {
		a.opMu.Unlock()
		return errors.New("die Windows-Laufzeit ist noch nicht bereit")
	}
	runtimeContext := a.runtimeContext
	expectedPublisher := a.expectedPublisherSHA256
	trustedKeys := a.trustedReleaseKeys
	a.opMu.Unlock()
	profile, err := a.connectedProfile()
	if err != nil {
		return err
	}
	a.updateOpMu.Lock()
	if a.updateProgress.Running {
		a.updateOpMu.Unlock()
		return errors.New("ein Clientupdate wird bereits heruntergeladen")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	a.updateCancel = cancel
	a.updateProgress = UpdateProgress{Running: true, Stage: "Metadaten werden geprüft"}
	a.updateOpMu.Unlock()
	defer func() {
		cancel()
		a.updateOpMu.Lock()
		a.updateCancel = nil
		a.updateProgress.Running = false
		if returnErr != nil {
			a.updateProgress.Error = returnErr.Error()
		}
		a.updateOpMu.Unlock()
	}()
	staging := filepath.Join(filepath.Dir(a.profilePath), "updates")
	client := &deviceclient.Client{Profile: profile, HTTP: a.httpClient, OnDownloadProgress: func(downloaded, total int64) {
		a.updateOpMu.Lock()
		a.updateProgress.Stage = "Update wird heruntergeladen"
		a.updateProgress.Downloaded = downloaded
		a.updateProgress.Total = total
		a.updateOpMu.Unlock()
	}}
	prepared, err := client.PrepareUpdate(ctx, a.version, profile.UpdateSequence, trustedKeys, staging)
	if err != nil {
		return fmt.Errorf("signiertes Clientupdate vorbereiten: %w", err)
	}
	a.updateOpMu.Lock()
	a.updateProgress.Stage = "Signatur wird geprüft und Neustart vorbereitet"
	a.updateProgress.Downloaded = prepared.Size
	a.updateProgress.Total = prepared.Size
	a.updateOpMu.Unlock()
	target, err := os.Executable()
	if err != nil {
		return err
	}
	if err = selfupdate.Launch(prepared, target, a.profilePath, expectedPublisher); err != nil {
		return fmt.Errorf("atomare Aktualisierung starten: %w", err)
	}
	a.opMu.Lock()
	a.allowQuit = true
	a.opMu.Unlock()
	wailsruntime.Quit(runtimeContext)
	return nil
}

func (a *App) GetUpdateProgress() UpdateProgress {
	a.updateOpMu.Lock()
	defer a.updateOpMu.Unlock()
	return a.updateProgress
}

func (a *App) CancelUpdate() bool {
	a.updateOpMu.Lock()
	defer a.updateOpMu.Unlock()
	if !a.updateProgress.Running || a.updateCancel == nil {
		return false
	}
	a.updateProgress.Stage = "Download wird abgebrochen"
	a.updateCancel()
	return true
}

func (a *App) Discover() (discovery.Result, error) {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if _, err := a.connectedProfile(); err != nil {
		return discovery.Result{}, err
	}
	if err := a.requireReadyLocked(); err != nil {
		return discovery.Result{}, err
	}
	result, err := a.discover()
	if err != nil {
		return discovery.Result{}, fmt.Errorf("installierte Spiele erkennen: %w", err)
	}
	sort.SliceStable(result.Installations, func(i, j int) bool {
		left, right := result.Installations[i], result.Installations[j]
		if left.Launcher != right.Launcher {
			return left.Launcher < right.Launcher
		}
		if left.DisplayName != right.DisplayName {
			return strings.ToLower(left.DisplayName) < strings.ToLower(right.DisplayName)
		}
		return left.InstallPath < right.InstallPath
	})
	copyResult := cloneResult(result)
	a.discoveryGeneration++
	a.lastDiscovery = &copyResult
	a.resetAuthorizationLocked()
	return result, nil
}

func (a *App) StartAuthorization(selectedIndices []int) (AuthorizationView, error) {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	profile, err := a.connectedProfile()
	if err != nil {
		return AuthorizationView{}, err
	}
	if err = a.requireReadyLocked(); err != nil {
		return AuthorizationView{}, err
	}
	if a.lastDiscovery == nil {
		return AuthorizationView{}, errors.New("vor der Anmeldung zuerst installierte Spiele suchen")
	}
	selected, err := selectedInstallations(*a.lastDiscovery, selectedIndices)
	if err != nil {
		return AuthorizationView{}, err
	}
	generation := a.discoveryGeneration
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	authorization, err := (&deviceclient.Client{Profile: profile, HTTP: a.httpClient}).StartAuthorization(ctx)
	if err != nil {
		return AuthorizationView{}, fmt.Errorf("persönliche Anmeldung starten: %w", err)
	}
	verificationURL, err := verificationURL(profile.ServerURL, authorization.VerificationURL, authorization.UserCode)
	if err != nil {
		return AuthorizationView{}, err
	}
	view := AuthorizationView{
		AuthorizationID:     authorization.AuthorizationID,
		UserCode:            authorization.UserCode,
		VerificationURL:     verificationURL,
		ExpiresAt:           authorization.ExpiresAt.UTC().Format(time.RFC3339),
		PollIntervalSeconds: authorization.PollIntervalSeconds,
	}
	if err = deviceclient.OpenBrowser(verificationURL); err != nil {
		view.BrowserWarning = "Der Browser konnte nicht automatisch geöffnet werden. Bitte den angezeigten Link manuell öffnen."
	}
	a.authorizationID = authorization.AuthorizationID
	a.authorizationGeneration = generation
	a.authorizedSelection = cloneInstallations(selected)
	a.authorizedWarnings = append([]string(nil), a.lastDiscovery.Warnings...)
	a.accessToken = ""
	return view, nil
}

func (a *App) PollAuthorization(authorizationID string) (PollResult, error) {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	profile, err := a.connectedProfile()
	if err != nil {
		return PollResult{}, err
	}
	expected := a.authorizationID
	expectedGeneration := a.authorizationGeneration
	if authorizationID == "" || authorizationID != expected || expectedGeneration != a.discoveryGeneration {
		return PollResult{}, errors.New("die Anmeldeanforderung gehört nicht zu dieser laufenden Sitzung")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	a.setActivePoll(authorizationID, cancel)
	defer func() {
		cancel()
		a.clearActivePoll(authorizationID)
	}()
	token, err := (&deviceclient.Client{Profile: profile, HTTP: a.httpClient}).PollAuthorization(ctx, authorizationID)
	if errors.Is(err, deviceclient.ErrAuthorizationPending) {
		return PollResult{Status: "pending"}, nil
	}
	if errors.Is(err, deviceclient.ErrAuthorizationSlowDown) {
		return PollResult{Status: "slow_down"}, nil
	}
	if err != nil {
		return PollResult{}, fmt.Errorf("Anmeldung abschließen: %w", err)
	}
	if authorizationID != a.authorizationID || expectedGeneration != a.authorizationGeneration || expectedGeneration != a.discoveryGeneration {
		return PollResult{}, errors.New("die freigegebene Spielauswahl ist nicht mehr aktuell; bitte die Anmeldung neu starten")
	}
	a.accessToken = token
	return PollResult{Status: "authorized"}, nil
}

func (a *App) SyncInventory() (SyncResult, error) {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	profile, err := a.connectedProfile()
	if err != nil {
		return SyncResult{}, err
	}
	if err = a.requireReadyLocked(); err != nil {
		return SyncResult{}, err
	}
	token := a.accessToken
	if a.lastDiscovery == nil {
		return SyncResult{}, errors.New("es liegt kein aktuelles Suchergebnis vor")
	}
	if token == "" || a.authorizationID == "" || a.authorizationGeneration != a.discoveryGeneration || len(a.authorizedSelection) == 0 {
		return SyncResult{}, errors.New("vor der Synchronisation ist eine persönliche Anmeldung erforderlich")
	}
	selected := cloneInstallations(a.authorizedSelection)
	warnings := append([]string(nil), a.authorizedWarnings...)
	// The personal token is locally one-shot, including failed uploads. A retry
	// therefore always requires a new explicit browser authorization.
	a.resetAuthorizationLocked()
	randomID := make([]byte, 16)
	if _, err = rand.Read(randomID); err != nil {
		return SyncResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := &deviceclient.Client{Profile: profile, HTTP: a.httpClient}
	if err = client.UploadInventory(ctx, token, base64.RawURLEncoding.EncodeToString(randomID), discovery.Result{Installations: selected, Warnings: warnings}); err != nil {
		return SyncResult{}, fmt.Errorf("Inventar synchronisieren: %w", err)
	}
	return SyncResult{Uploaded: len(selected)}, nil
}

func (a *App) CancelAuthorization(expectedAuthorizationID string) bool {
	if expectedAuthorizationID == "" {
		return false
	}
	a.cancelActivePoll(expectedAuthorizationID)
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if a.authorizationID != expectedAuthorizationID {
		return false
	}
	a.resetAuthorizationLocked()
	return true
}

func (a *App) connectedProfile() (deviceclient.Profile, error) {
	profile, err := deviceclient.LoadProfile(a.profilePath)
	if errors.Is(err, os.ErrNotExist) {
		return deviceclient.Profile{}, errors.New("dieser Windows-Benutzer ist noch nicht mit dem Managementserver verbunden")
	}
	if err != nil {
		return deviceclient.Profile{}, fmt.Errorf("Geräteprofil laden: %w", err)
	}
	profile.ClientVersion = a.version
	return profile, nil
}

func (a *App) requireReadyLocked() error {
	if a.updateRequired {
		return errors.New("ein signiertes LANReady-Clientupdate ist erforderlich; diese Funktion bleibt bis zur Aktualisierung gesperrt")
	}
	if !a.serverReady {
		return errors.New("der Managementserver-Status wurde noch nicht erfolgreich geprüft; bitte den Verbindungsstatus erneut laden")
	}
	return nil
}

func (a *App) clearSessionLocked() {
	a.discoveryGeneration++
	a.lastDiscovery = nil
	a.resetAuthorizationLocked()
}

func (a *App) resetAuthorizationLocked() {
	a.authorizationID = ""
	a.authorizationGeneration = 0
	a.authorizedSelection = nil
	a.authorizedWarnings = nil
	a.accessToken = ""
}

func (a *App) setActivePoll(authorizationID string, cancel context.CancelFunc) {
	a.pollMu.Lock()
	defer a.pollMu.Unlock()
	a.pollAuthorizationID = authorizationID
	a.pollCancel = cancel
}

func (a *App) clearActivePoll(authorizationID string) {
	a.pollMu.Lock()
	defer a.pollMu.Unlock()
	if a.pollAuthorizationID == authorizationID {
		a.pollAuthorizationID = ""
		a.pollCancel = nil
	}
}

func (a *App) cancelActivePoll(expectedAuthorizationID string) {
	a.pollMu.Lock()
	defer a.pollMu.Unlock()
	if a.pollAuthorizationID == expectedAuthorizationID && a.pollCancel != nil {
		a.pollCancel()
	}
}

func normalizeServerURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("die Serveradresse muss eine vollständige HTTPS-Adresse ohne Zugangsdaten, Query oder Fragment sein")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("die Serveradresse darf keinen Unterpfad enthalten")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func selectedInstallations(result discovery.Result, indices []int) ([]discovery.Installation, error) {
	if len(indices) == 0 {
		return nil, errors.New("mindestens ein Spiel muss für die Synchronisation ausgewählt sein")
	}
	seen := make(map[int]struct{}, len(indices))
	out := make([]discovery.Installation, 0, len(indices))
	for _, index := range indices {
		if index < 0 || index >= len(result.Installations) {
			return nil, errors.New("die Spielauswahl ist nicht mehr aktuell; bitte erneut suchen")
		}
		if _, exists := seen[index]; exists {
			return nil, errors.New("die Spielauswahl enthält einen doppelten Eintrag")
		}
		seen[index] = struct{}{}
		out = append(out, cloneInstallation(result.Installations[index]))
	}
	return out, nil
}

func verificationURL(serverURL, rawVerificationURL, code string) (string, error) {
	server, err := url.Parse(serverURL)
	if err != nil {
		return "", errors.New("gespeicherte Serveradresse ist ungültig")
	}
	target, err := url.Parse(rawVerificationURL)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.Fragment != "" || !strings.EqualFold(target.Scheme, server.Scheme) || !strings.EqualFold(target.Host, server.Host) {
		return "", errors.New("der Managementserver hat eine unsichere oder fremde Browser-Anmeldeadresse geliefert")
	}
	query := target.Query()
	query.Set("code", code)
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func cloneResult(result discovery.Result) discovery.Result {
	return discovery.Result{Installations: cloneInstallations(result.Installations), Warnings: append([]string(nil), result.Warnings...)}
}

func cloneInstallations(values []discovery.Installation) []discovery.Installation {
	out := make([]discovery.Installation, len(values))
	for index := range values {
		out[index] = cloneInstallation(values[index])
	}
	return out
}

func cloneInstallation(value discovery.Installation) discovery.Installation {
	if value.DetectedVersion != nil {
		version := *value.DetectedVersion
		value.DetectedVersion = &version
	}
	return value
}
