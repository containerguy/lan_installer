package windowsapp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	contractschemas "github.com/containerguy/lan_installer/docs/contracts/schemas"
	"github.com/containerguy/lan_installer/internal/deviceclient"
	"github.com/containerguy/lan_installer/internal/discovery"
	"github.com/containerguy/lan_installer/internal/fileversion"
	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/selfupdate"
	"github.com/containerguy/lan_installer/internal/windowsupdate"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const DefaultServerURL = "https://game-manager.familie-keller.info"

type App struct {
	profilePath              string
	profilePathErr           error
	version                  string
	httpClient               *http.Client
	discover                 func() (discovery.Result, error)
	stat                     func(string) (os.FileInfo, error)
	fileVersion              func(string) (string, error)
	validateManualExecutable func(string) error

	opMu                    sync.Mutex
	installMu               sync.Mutex
	installStatus           InstallStatus
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
	trustedUpdateKeys       map[string]ed25519.PublicKey
	trustedEventKeys        map[string]ed25519.PublicKey
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
	releaseValidator        *protocol.ReleaseValidator
	releaseValidatorErr     error
	discoveryMu             sync.Mutex
	discoveryRun            *discoveryRun
	stateTimeout            time.Duration
	discoveryTimeout        time.Duration
	pollMu                  sync.Mutex
	pollAuthorizationID     string
	pollCancel              context.CancelFunc
}

type discoveryRun struct {
	done   chan struct{}
	result discovery.Result
	err    error
}

type State struct {
	Connected        bool           `json:"connected"`
	ServerURL        string         `json:"serverUrl"`
	DeviceID         string         `json:"deviceId,omitempty"`
	DeviceName       string         `json:"deviceName,omitempty"`
	ClientVersion    string         `json:"clientVersion"`
	ServerReachable  bool           `json:"serverReachable"`
	ServerWarning    string         `json:"serverWarning,omitempty"`
	UpdateRequired   bool           `json:"updateRequired"`
	UpdateAvailable  bool           `json:"updateAvailable"`
	UpdateVersion    string         `json:"updateVersion,omitempty"`
	AgentMode        bool           `json:"agentMode"`
	UpdateReleaseURL string         `json:"updateReleaseUrl,omitempty"`
	EventReadiness   EventReadiness `json:"eventReadiness"`
}

type EventReadiness struct {
	State         string                   `json:"state"`
	EventID       string                   `json:"eventId,omitempty"`
	ReleaseID     string                   `json:"releaseId,omitempty"`
	Sequence      int64                    `json:"sequence,omitempty"`
	RequiredGames int                      `json:"requiredGames"`
	ReadyGames    int                      `json:"readyGames"`
	Percentage    int                      `json:"percentage"`
	Message       string                   `json:"message"`
	Launchers     []EventLauncherReadiness `json:"launchers,omitempty"`
	Games         []EventGameReadiness     `json:"games,omitempty"`
}

type EventLauncherReadiness struct {
	Launcher        string `json:"launcher"`
	RequiredVersion string `json:"requiredVersion"`
	Required        bool   `json:"required"`
	Status          string `json:"status"`
	// DetectedVersion is only set when the launcher binary carried a readable
	// Windows file version.
	DetectedVersion string `json:"detectedVersion,omitempty"`
}

type EventGameReadiness struct {
	GameID          string `json:"gameId"`
	Name            string `json:"name"`
	Launcher        string `json:"launcher"`
	ExternalGameID  string `json:"externalGameId,omitempty"`
	RequiredVersion string `json:"requiredVersion"`
	DetectedVersion string `json:"detectedVersion,omitempty"`
	Required        bool   `json:"required"`
	Status          string `json:"status"`
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

type ManualGameInput struct {
	CatalogGameID  int64  `json:"catalogGameId"`
	ExecutablePath string `json:"executablePath"`
}

type UpdateProgress struct {
	Running    bool   `json:"running"`
	Stage      string `json:"stage"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}

func New(profilePath, version string) *App {
	return NewWithPurposeKeys(profilePath, version, nil, nil)
}

func NewWithPurposeKeys(profilePath, version string, updateKeys, eventKeys []ed25519.PublicKey) *App {
	var profilePathErr error
	if strings.TrimSpace(profilePath) == "" {
		profilePath, profilePathErr = defaultProfilePath()
	}
	if strings.TrimSpace(version) == "" {
		version = "0.0.0-dev"
	}
	keyring := func(keys []ed25519.PublicKey) map[string]ed25519.PublicKey {
		result := make(map[string]ed25519.PublicKey, len(keys))
		for _, key := range keys {
			if len(key) == ed25519.PublicKeySize {
				result[protocol.KeyID(key)] = append(ed25519.PublicKey(nil), key...)
			}
		}
		return result
	}
	validator, validatorErr := protocol.NewReleaseValidatorFromJSON(contractschemas.EventReleaseEnvelope, contractschemas.ClientUpdateEnvelope)
	return &App{profilePath: profilePath, profilePathErr: profilePathErr, version: version, discover: discovery.Discover, stat: os.Stat, fileVersion: fileversion.Read, validateManualExecutable: deviceclient.ValidateManualExecutableLocation, trustedUpdateKeys: keyring(updateKeys), trustedEventKeys: keyring(eventKeys), releaseValidator: validator, releaseValidatorErr: validatorErr, healthReady: make(chan struct{}), stateTimeout: 15 * time.Second, discoveryTimeout: 30 * time.Second}
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
	path, _ := defaultProfilePath()
	return path
}

func defaultProfilePath() (string, error) {
	configRoot, configErr := os.UserConfigDir()
	homeRoot, homeErr := os.UserHomeDir()
	legacyDirs := make([]string, 0, 2)
	if workingDir, workingErr := os.Getwd(); workingErr == nil {
		legacyDirs = append(legacyDirs, workingDir)
	}
	if executable, executableErr := os.Executable(); executableErr == nil {
		legacyDirs = append(legacyDirs, filepath.Dir(executable))
	}
	legacyDirs = append(legacyDirs, commonLegacyProfileDirs(homeRoot, 2, 10000)...)
	return resolveDefaultProfilePathFromUserDirs(configRoot, configErr, homeRoot, homeErr, legacyDirs)
}

func commonLegacyProfileDirs(homeRoot string, maxDepth, maxDirectories int) []string {
	homeRoot = strings.TrimSpace(homeRoot)
	if homeRoot == "" || !filepath.IsAbs(homeRoot) || maxDepth < 0 || maxDirectories < 1 {
		return nil
	}
	result := make([]string, 0, 32)
	var visit func(string, int)
	visit = func(dir string, depth int) {
		if len(result) >= maxDirectories {
			return
		}
		result = append(result, dir)
		if depth >= maxDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if len(result) >= maxDirectories {
				return
			}
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				visit(filepath.Join(dir, entry.Name()), depth+1)
			}
		}
	}
	for _, name := range []string{"Downloads", "Desktop", "Documents"} {
		visit(filepath.Join(homeRoot, name), 0)
	}
	return result
}

func resolveDefaultProfilePathFromUserDirs(configRoot string, configErr error, homeRoot string, homeErr error, legacyDirs []string) (string, error) {
	if configErr != nil || strings.TrimSpace(configRoot) == "" || !filepath.IsAbs(configRoot) {
		homeRoot = strings.TrimSpace(homeRoot)
		if homeErr != nil || homeRoot == "" || !filepath.IsAbs(homeRoot) {
			if configErr != nil {
				return "", fmt.Errorf("Windows-Benutzerprofil konnte weder über AppData noch das Benutzerverzeichnis bestimmt werden: %w", configErr)
			}
			return "", errors.New("Windows-Benutzerprofil hat keinen absoluten AppData- oder Benutzerpfad")
		}
		configRoot = filepath.Join(homeRoot, "AppData", "Roaming")
	}
	return resolveDefaultProfilePath(configRoot, legacyDirs)
}

func profilePathFromConfigRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("Windows-Benutzerprofil hat keinen absoluten Konfigurationspfad")
	}
	return filepath.Join(root, "LANReady", "device.json"), nil
}

func resolveDefaultProfilePath(configRoot string, legacyDirs []string) (string, error) {
	stable, err := profilePathFromConfigRoot(configRoot)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(stable); statErr == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("LANReady-Geräteprofil ist keine reguläre Datei")
		}
		return stable, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("stabiles LANReady-Geräteprofil prüfen: %w", statErr)
	}
	seen := make(map[string]struct{}, len(legacyDirs))
	var legacyPath string
	var legacyProfile deviceclient.Profile
	for _, dir := range legacyDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		legacy := filepath.Join(dir, "lanready-device.json")
		if _, duplicate := seen[legacy]; duplicate || legacy == stable {
			continue
		}
		seen[legacy] = struct{}{}
		info, statErr := os.Lstat(legacy)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return "", fmt.Errorf("altes LANReady-Geräteprofil prüfen: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("altes LANReady-Geräteprofil ist keine reguläre Datei")
		}
		profile, loadErr := deviceclient.LoadProfile(legacy)
		if loadErr != nil {
			return "", fmt.Errorf("altes LANReady-Geräteprofil laden: %w", loadErr)
		}
		if legacyPath != "" && !reflect.DeepEqual(legacyProfile, profile) {
			return "", errors.New("mehrere unterschiedliche alte LANReady-Geräteprofile gefunden; automatische Migration wurde abgebrochen")
		}
		legacyPath, legacyProfile = legacy, profile
	}
	if legacyPath != "" {
		if saveErr := deviceclient.SaveProfile(stable, legacyProfile); saveErr != nil {
			return "", fmt.Errorf("altes LANReady-Geräteprofil migrieren: %w", saveErr)
		}
		_ = os.Remove(legacyPath)
	}
	return stable, nil
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
	ctx, cancel := context.WithTimeout(context.Background(), a.stateTimeout)
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
	if len(a.trustedUpdateKeys) > 0 {
		available, updateErr := (&deviceclient.Client{Profile: profile, HTTP: a.httpClient}).CheckUpdate(ctx, a.version, profile.UpdateSequence, a.trustedUpdateKeys)
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
	if !state.UpdateRequired {
		state.EventReadiness = a.loadEventReadiness(ctx, profile, bootstrap.ActiveEvent)
		if state.EventReadiness.State == "security_error" || state.EventReadiness.State == "error" {
			state.ServerWarning = state.EventReadiness.Message
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
	state := State{ServerURL: DefaultServerURL, ClientVersion: a.version, AgentMode: a.agentMode, EventReadiness: noActiveEventReadiness()}
	if a.profilePathErr != nil {
		return state, a.profilePathErr
	}
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
	if a.profilePathErr != nil {
		return State{}, a.profilePathErr
	}
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
	if len(a.trustedUpdateKeys) == 0 {
		a.opMu.Unlock()
		return errors.New("dieser Build enthält keinen vertrauenswürdigen Release-Public-Key")
	}
	if a.runtimeContext == nil {
		a.opMu.Unlock()
		return errors.New("die Windows-Laufzeit ist noch nicht bereit")
	}
	runtimeContext := a.runtimeContext
	expectedPublisher := a.expectedPublisherSHA256
	trustedKeys := a.trustedUpdateKeys
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
	if _, err := a.connectedProfile(); err != nil {
		a.opMu.Unlock()
		return discovery.Result{}, err
	}
	if err := a.requireReadyLocked(); err != nil {
		a.opMu.Unlock()
		return discovery.Result{}, err
	}
	generation := a.discoveryGeneration
	a.opMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), a.discoveryTimeout)
	defer cancel()
	result, err := a.runDiscovery(ctx)
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
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if _, err = a.connectedProfile(); err != nil {
		return discovery.Result{}, err
	}
	if err = a.requireReadyLocked(); err != nil {
		return discovery.Result{}, err
	}
	if generation != a.discoveryGeneration {
		return discovery.Result{}, errors.New("der Verbindungs- oder Inventarstatus hat sich während der Erkennung geändert; bitte erneut suchen")
	}
	copyResult := cloneResult(result)
	a.discoveryGeneration++
	a.lastDiscovery = &copyResult
	a.resetAuthorizationLocked()
	return result, nil
}

func (a *App) StandaloneCatalog() ([]deviceclient.StandaloneGame, error) {
	a.opMu.Lock()
	profile, err := a.connectedProfile()
	if err == nil {
		err = a.requireReadyLocked()
	}
	a.opMu.Unlock()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	games, err := (&deviceclient.Client{Profile: profile, HTTP: a.httpClient}).StandaloneGames(ctx)
	if err != nil {
		return nil, fmt.Errorf("Katalog für Spiele ohne Launcher laden: %w", err)
	}
	return games, nil
}

func (a *App) ChooseManualExecutable() (string, error) {
	a.opMu.Lock()
	ctx := a.runtimeContext
	a.opMu.Unlock()
	if ctx == nil {
		return "", errors.New("die Dateiauswahl ist erst nach dem Start der Windows-Oberfläche verfügbar")
	}
	return wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:   "Hauptprogramm des Spiels auswählen",
		Filters: []wailsruntime.FileFilter{{DisplayName: "Windows-Programme (*.exe)", Pattern: "*.exe"}},
	})
}

func (a *App) SaveManualGame(input ManualGameInput) error {
	input.ExecutablePath = strings.TrimSpace(input.ExecutablePath)
	if input.CatalogGameID < 1 || input.ExecutablePath == "" {
		return errors.New("Katalogspiel und Hauptprogramm sind erforderlich")
	}
	if err := a.validateManualExecutable(input.ExecutablePath); err != nil {
		return errors.New("das Hauptprogramm muss eine lokale, absolute Windows-EXE ohne Netzwerk-, Geräte- oder Alternativdatenstrom-Pfad sein")
	}
	info, err := a.stat(input.ExecutablePath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("das ausgewählte Hauptprogramm ist nicht als reguläre Datei erreichbar")
	}
	catalog, err := a.StandaloneCatalog()
	if err != nil {
		return err
	}
	var selected *deviceclient.StandaloneGame
	for index := range catalog {
		if catalog[index].ID == input.CatalogGameID {
			selected = &catalog[index]
			break
		}
	}
	if selected == nil {
		return errors.New("das ausgewählte Spiel ist im aktuellen Standalone-Katalog nicht mehr verfügbar")
	}
	a.opMu.Lock()
	defer a.opMu.Unlock()
	profile, err := a.connectedProfile()
	if err != nil {
		return err
	}
	games := append([]deviceclient.ManualGame(nil), profile.ManualGames...)
	registration := deviceclient.ManualGame{CatalogGameID: selected.ID, ExternalGameID: selected.ExternalGameID, DisplayName: selected.Name, ExecutablePath: input.ExecutablePath}
	replaced := false
	for index := range games {
		if games[index].CatalogGameID == selected.ID {
			games[index] = registration
			replaced = true
			break
		}
	}
	if !replaced {
		games = append(games, registration)
	}
	sort.Slice(games, func(i, j int) bool { return games[i].CatalogGameID < games[j].CatalogGameID })
	profile.ManualGames = games
	if err = deviceclient.SaveProfile(a.profilePath, profile); err != nil {
		return fmt.Errorf("manuell hinzugefügtes Spiel geschützt speichern: %w", err)
	}
	a.clearSessionLocked()
	return nil
}

func (a *App) RemoveManualGame(externalGameID string) error {
	externalGameID = strings.TrimSpace(externalGameID)
	if externalGameID == "" {
		return errors.New("ungültiges Katalogspiel")
	}
	a.opMu.Lock()
	defer a.opMu.Unlock()
	profile, err := a.connectedProfile()
	if err != nil {
		return err
	}
	games := make([]deviceclient.ManualGame, 0, len(profile.ManualGames))
	removed := false
	for _, game := range profile.ManualGames {
		if game.ExternalGameID == externalGameID {
			removed = true
			continue
		}
		games = append(games, game)
	}
	if !removed {
		return errors.New("die manuelle Spielregistrierung wurde nicht gefunden")
	}
	profile.ManualGames = games
	if err = deviceclient.SaveProfile(a.profilePath, profile); err != nil {
		return fmt.Errorf("manuelle Spielregistrierung entfernen: %w", err)
	}
	a.clearSessionLocked()
	return nil
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

func noActiveEventReadiness() EventReadiness {
	return EventReadiness{State: "none", Message: "Kein aktives Event. Ein Katalogeintrag allein verändert die Event-Bereitschaft nicht."}
}

func eventReadinessError(state, message string) EventReadiness {
	return EventReadiness{State: state, Message: message}
}

func (a *App) loadEventReadiness(ctx context.Context, profile deviceclient.Profile, active *deviceclient.ActiveEvent) EventReadiness {
	if active == nil {
		return noActiveEventReadiness()
	}
	if a.releaseValidatorErr != nil || a.releaseValidator == nil {
		return eventReadinessError("security_error", "Die eingebetteten Event-Prüfregeln sind ungültig. Event-Bereitschaft bleibt aus Sicherheitsgründen blockiert.")
	}
	if len(a.trustedEventKeys) == 0 {
		return eventReadinessError("security_error", "Dieser Client enthält keinen vertrauenswürdigen Event-Signaturschlüssel.")
	}
	raw, err := (&deviceclient.Client{Profile: profile, HTTP: a.httpClient}).EventRelease(ctx, active.EventID, active.ReleaseURL)
	if err != nil {
		return eventReadinessError("error", "Das aktive Event konnte nicht sicher geladen werden: "+err.Error())
	}
	_, payload, metadata, err := a.releaseValidator.ValidateEventEnvelope(raw, a.trustedEventKeys)
	if err != nil {
		return eventReadinessError("security_error", "Das aktive Event hat die Signatur- oder Vertragsprüfung nicht bestanden.")
	}
	if metadata.EventID != active.EventID {
		return eventReadinessError("security_error", "Event-ID und signiertes Release stimmen nicht überein.")
	}
	now := time.Now().UTC()
	if now.Before(metadata.IssuedAt) || !now.Before(metadata.ValidUntil) {
		return eventReadinessError("security_error", "Das signierte Event ist noch nicht gültig oder bereits abgelaufen.")
	}
	comparison, versionErr := protocol.CompareSemanticVersions(a.version, metadata.MinimumClientVersion)
	if versionErr != nil || comparison < 0 {
		return eventReadinessError("security_error", "Für dieses Event ist eine neuere signierte LANReady-Version erforderlich.")
	}
	if err = a.persistEventSequence(active.EventID, metadata.Sequence); err != nil {
		return eventReadinessError("security_error", err.Error())
	}
	var release struct {
		Games []struct {
			GameID, Name, LauncherID, ExternalGameID, Version string
			Required                                          bool
		} `json:"games"`
		Launchers []struct {
			LauncherID, Version string
			Required            bool
		} `json:"launchers"`
	}
	if err = json.Unmarshal(payload, &release); err != nil {
		return eventReadinessError("security_error", "Das signierte Eventpayload konnte nicht ausgewertet werden.")
	}
	local, err := a.runDiscovery(ctx)
	if err != nil {
		return eventReadinessError("error", "Installierte Spiele konnten für die Event-Bereitschaft nicht geprüft werden: "+err.Error())
	}
	readiness := EventReadiness{State: "action_required", EventID: metadata.EventID, ReleaseID: metadata.ReleaseID, Sequence: metadata.Sequence}
	readyRequiredGames := 0
	for _, game := range release.Games {
		item := EventGameReadiness{GameID: game.GameID, Name: game.Name, Launcher: game.LauncherID, ExternalGameID: game.ExternalGameID, RequiredVersion: game.Version, Required: game.Required, Status: "missing"}
		if game.ExternalGameID == "" {
			item.Status = "unidentifiable"
		} else {
			item.Status, item.DetectedVersion = matchEventGame(local.Installations, releaseLauncherAdapter(game.LauncherID), game.ExternalGameID, game.Version)
		}
		if game.Required {
			readiness.RequiredGames++
			if item.Status == "ready" {
				readiness.ReadyGames++
				readyRequiredGames++
			}
		}
		readiness.Games = append(readiness.Games, item)
	}
	installedLaunchers := discovery.DiscoverLaunchers()
	requiredLaunchers, presentRequiredLaunchers := 0, 0
	for _, launcher := range release.Launchers {
		adapter := releaseLauncherAdapter(launcher.LauncherID)
		item := EventLauncherReadiness{Launcher: launcher.LauncherID, RequiredVersion: launcher.Version, Required: launcher.Required, Status: "not_detected"}
		// A launcher can be installed with no games in it, so check for the
		// application itself first; inferring presence only from game finds
		// reports a working installation as missing.
		for _, installed := range installedLaunchers {
			if installed.Adapter == adapter {
				item.Status = "detected"
				item.DetectedVersion = installed.Version
				break
			}
		}
		if item.Status == "not_detected" {
			for _, installation := range local.Installations {
				if installation.Launcher == adapter {
					item.Status = "detected_version_unverified"
					break
				}
			}
		}
		if launcher.Required {
			requiredLaunchers++
			if item.Status == "detected" || item.Status == "detected_version_unverified" {
				presentRequiredLaunchers++
			}
		}
		readiness.Launchers = append(readiness.Launchers, item)
	}
	requiredComponents := readiness.RequiredGames + requiredLaunchers
	readyComponents := readyRequiredGames + presentRequiredLaunchers
	if requiredComponents == 0 {
		readiness.Percentage = 100
	} else {
		readiness.Percentage = readyComponents * 100 / requiredComponents
	}
	switch {
	case readyRequiredGames < readiness.RequiredGames || presentRequiredLaunchers < requiredLaunchers:
		readiness.State = "action_required"
		readiness.Message = fmt.Sprintf("%d von %d erforderlichen Komponenten sind lokal vorhanden. Spielversionen müssen exakt passen.", readyComponents, requiredComponents)
	case requiredLaunchers > 0:
		readiness.State = "warning"
		readiness.Message = "Alle erforderlichen Spiele passen. Die installierten Launcher wurden erkannt; ihre genaue Version ist noch nicht unabhängig verifiziert."
	default:
		readiness.State = "ready"
		readiness.Message = "Alle erforderlichen Spiele sind in der signierten Eventversion vorhanden."
	}
	return readiness
}

func matchEventGame(installations []discovery.Installation, launcher, externalGameID, requiredVersion string) (string, string) {
	status, detectedVersion, rank := "missing", "", 0
	for _, installation := range installations {
		if installation.Launcher != launcher || installation.ExternalGameID != externalGameID {
			continue
		}
		candidateVersion := ""
		if installation.DetectedVersion != nil {
			candidateVersion = *installation.DetectedVersion
		}
		candidateStatus, candidateRank := "version_unknown", 1
		if candidateVersion != "" {
			candidateStatus, candidateRank = "version_mismatch", 2
		}
		if candidateVersion == requiredVersion {
			return "ready", candidateVersion
		}
		if candidateRank > rank {
			status, detectedVersion, rank = candidateStatus, candidateVersion, candidateRank
		}
	}
	return status, detectedVersion
}

func releaseLauncherAdapter(value string) string {
	return map[string]string{"steam": "steam", "ea-app": "ea_app", "ubisoft-connect": "ubisoft_connect", "standalone": "standalone"}[value]
}

func (a *App) persistEventSequence(eventID string, sequence int64) error {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	profile, err := deviceclient.LoadProfile(a.profilePath)
	if err != nil {
		return errors.New("Der geschützte Event-Sequenzstand konnte nicht geladen werden.")
	}
	current := profile.EventSequences[eventID]
	if sequence < current {
		return errors.New("Ein älterer signierter Eventstand wurde aus Sicherheitsgründen abgelehnt.")
	}
	if sequence == current {
		return nil
	}
	sequences := make(map[string]int64, len(profile.EventSequences)+1)
	for key, value := range profile.EventSequences {
		sequences[key] = value
	}
	sequences[eventID] = sequence
	profile.EventSequences = sequences
	if err = deviceclient.SaveProfile(a.profilePath, profile); err != nil {
		return errors.New("Der geschützte Event-Sequenzstand konnte nicht gespeichert werden.")
	}
	return nil
}

func (a *App) runDiscovery(ctx context.Context) (discovery.Result, error) {
	a.discoveryMu.Lock()
	run := a.discoveryRun
	if run == nil {
		run = &discoveryRun{done: make(chan struct{})}
		a.discoveryRun = run
		go func(current *discoveryRun) {
			current.result, current.err = a.discover()
			if current.err == nil {
				current.result = a.mergeManualGames(current.result)
			}
			a.discoveryMu.Lock()
			if a.discoveryRun == current {
				a.discoveryRun = nil
			}
			close(current.done)
			a.discoveryMu.Unlock()
		}(run)
	}
	a.discoveryMu.Unlock()

	select {
	case <-run.done:
		return cloneResult(run.result), run.err
	case <-ctx.Done():
		return discovery.Result{}, fmt.Errorf("Erkennung hat das Zeitlimit überschritten: %w", ctx.Err())
	}
}

func (a *App) mergeManualGames(result discovery.Result) discovery.Result {
	profile, err := deviceclient.LoadProfile(a.profilePath)
	if err != nil {
		result.Warnings = append(result.Warnings, "Manuell hinzugefügte Spiele konnten nicht aus dem geschützten Profil geladen werden.")
		return result
	}
	for _, game := range profile.ManualGames {
		if locationErr := a.validateManualExecutable(game.ExecutablePath); locationErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s liegt nicht mehr auf einem verfügbaren lokalen Laufwerk; wähle das Hauptprogramm erneut aus.", game.DisplayName))
			continue
		}
		info, statErr := a.stat(game.ExecutablePath)
		if statErr != nil || !info.Mode().IsRegular() {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s wurde nicht gefunden; wähle das Hauptprogramm erneut aus.", game.DisplayName))
			continue
		}
		version, versionErr := a.fileVersion(game.ExecutablePath)
		var detected *string
		source := "manual-registration-unverified"
		if versionErr == nil && strings.TrimSpace(version) != "" {
			version = strings.TrimSpace(version)
			detected = &version
			source = "windows-file-version"
		}
		result.Installations = append(result.Installations, discovery.Installation{Launcher: "standalone", ExternalGameID: game.ExternalGameID, DisplayName: game.DisplayName, DetectedVersion: detected, VersionSource: source, InstallPath: filepath.Dir(game.ExecutablePath)})
	}
	return result
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
