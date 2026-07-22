package windowsapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/deviceclient"
	"github.com/containerguy/lan_installer/internal/install"
	"github.com/containerguy/lan_installer/internal/protocol"
)

// PendingInstall describes one game the active event wants installed, in the
// form the UI shows before asking for confirmation. Downloads are large, so the
// user sees the size before anything starts.
type PendingInstall struct {
	GameID    string `json:"gameId"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"sizeBytes"`
	TargetDir string `json:"targetDir"`
	Installed bool   `json:"installed"`
}

// InstallCandidate is one executable the user may pick as the main program.
type InstallCandidate struct {
	RelativePath string `json:"relativePath"`
	AbsolutePath string `json:"absolutePath"`
	SizeBytes    int64  `json:"sizeBytes"`
	Recommended  bool   `json:"recommended"`
}

// InstallStatus is polled by the UI while an install runs. Multi-gigabyte
// downloads need a visible rate and percentage, not just a spinner.
type InstallStatus struct {
	GameID         string  `json:"gameId"`
	Running        bool    `json:"running"`
	Stage          string  `json:"stage"`
	Downloaded     int64   `json:"downloaded"`
	Total          int64   `json:"total"`
	Percent        float64 `json:"percent"`
	BytesPerSecond float64 `json:"bytesPerSecond"`
	Error          string  `json:"error"`
}

// InstallResult reports where a game landed and which executables the user can
// choose from. LANReady never picks the main program itself: a wrong guess
// would silently misreport the installed version.
type InstallResult struct {
	GameID     string             `json:"gameId"`
	TargetDir  string             `json:"targetDir"`
	Candidates []InstallCandidate `json:"candidates"`
}

// PendingInstalls lists the archives the active signed event asks this client
// to install, together with whether they are already present.
func (a *App) PendingInstalls() ([]PendingInstall, error) {
	actions, err := a.eventInstallActions()
	if err != nil {
		return nil, err
	}
	root, err := a.gamesRoot()
	if err != nil {
		return nil, err
	}
	pending := make([]PendingInstall, 0, len(actions))
	for _, action := range actions {
		target, dirErr := install.ResolveInstallDir(root, action.RelativePath)
		if dirErr != nil {
			return nil, dirErr
		}
		name := action.Name
		if name == "" {
			name = action.GameID
		}
		_, statErr := a.stat(target)
		pending = append(pending, PendingInstall{
			GameID: action.GameID, Name: name, SizeBytes: action.Size,
			TargetDir: target, Installed: statErr == nil,
		})
	}
	return pending, nil
}

// InstallGame downloads, verifies and extracts one game from the active event
// and returns the executables the user can register as its main program.
//
// The install is driven entirely by the signed release: the caller supplies
// only a game id, never a path or URL.
func (a *App) InstallGame(gameID string) (InstallResult, error) {
	var result InstallResult
	actions, err := a.eventInstallActions()
	if err != nil {
		return result, err
	}
	var selected *protocol.EventArchiveInstall
	for index := range actions {
		if actions[index].GameID == gameID {
			selected = &actions[index]
			break
		}
	}
	if selected == nil {
		return result, errors.New("dieses Spiel gehört nicht zum aktiven Event")
	}
	root, err := a.gamesRoot()
	if err != nil {
		return result, err
	}
	// The install holds no global lock: it runs for minutes to hours, and
	// blocking every other action for that long would freeze the UI.
	profile, err := a.connectedProfile()
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	a.setInstallStatus(InstallStatus{GameID: gameID, Running: true, Stage: "Download wird vorbereitet"})
	client := &deviceclient.Client{Profile: profile, HTTP: a.httpClient}
	dir, err := client.InstallGameArchive(ctx, install.ArchiveInstall{
		GameID: selected.GameID, Digest: selected.Digest,
		Size: selected.Size, RelativePath: selected.RelativePath,
	}, root, func(p deviceclient.InstallProgress) {
		percent := 0.0
		if p.Total > 0 {
			percent = float64(p.Downloaded) / float64(p.Total) * 100
		}
		// Verification and extraction move as many bytes as the download and,
		// on slow storage, take longer; naming the stage keeps a working
		// install from looking frozen at 100%.
		stage := "Wird heruntergeladen"
		switch p.Stage {
		case install.StageVerify:
			stage = "Wird geprüft"
		case install.StageExtract:
			stage = "Wird entpackt"
		}
		a.setInstallStatus(InstallStatus{
			GameID: gameID, Running: true, Stage: stage,
			Downloaded: p.Downloaded, Total: p.Total,
			Percent: percent, BytesPerSecond: p.BytesPerSecond,
		})
	})
	if err != nil {
		a.setInstallStatus(InstallStatus{GameID: gameID, Error: err.Error()})
		return result, fmt.Errorf("Installation von %s fehlgeschlagen: %w", gameID, err)
	}
	a.setInstallStatus(InstallStatus{GameID: gameID, Running: true, Stage: "Wird abgeschlossen", Percent: 100})
	candidates, err := install.FindExecutables(dir)
	if err != nil {
		return result, err
	}
	a.setInstallStatus(InstallStatus{GameID: gameID, Stage: "Fertig", Percent: 100})
	result = InstallResult{GameID: gameID, TargetDir: dir, Candidates: make([]InstallCandidate, 0, len(candidates))}
	for index, candidate := range candidates {
		result.Candidates = append(result.Candidates, InstallCandidate{
			RelativePath: candidate.RelativePath,
			AbsolutePath: joinPath(dir, candidate.RelativePath),
			SizeBytes:    candidate.SizeBytes,
			Recommended:  index == 0,
		})
	}
	return result, nil
}

// setInstallStatus stores the latest progress for InstallStatusFor to read.
func (a *App) setInstallStatus(status InstallStatus) {
	a.installMu.Lock()
	a.installStatus = status
	a.installMu.Unlock()
}

// InstallStatusFor returns the current progress; the UI polls this while an
// install runs.
func (a *App) InstallStatusFor(gameID string) InstallStatus {
	a.installMu.Lock()
	defer a.installMu.Unlock()
	if a.installStatus.GameID != gameID {
		return InstallStatus{GameID: gameID}
	}
	return a.installStatus
}

// installTimeout bounds a single game install. Packages are multi-gigabyte, so
// this is generous, but it must not be unbounded.
const installTimeout = 6 * time.Hour

// eventInstallActions loads the active event and reduces it to install work,
// refusing anything this client cannot execute safely.
func (a *App) eventInstallActions() ([]protocol.EventArchiveInstall, error) {
	payload, err := a.activeEventPayload()
	if err != nil {
		return nil, err
	}
	return protocol.EventInstallActions(payload)
}

// activeEventPayload fetches the active event release and returns its payload
// only after signature, schema and semantic validation have all passed.
func (a *App) activeEventPayload() ([]byte, error) {
	if a.releaseValidatorErr != nil || a.releaseValidator == nil {
		return nil, errors.New("die eingebetteten Event-Prüfregeln sind ungültig")
	}
	if len(a.trustedEventKeys) == 0 {
		return nil, errors.New("dieser Client enthält keinen vertrauenswürdigen Event-Signaturschlüssel")
	}
	profile, err := a.connectedProfile()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.stateTimeout)
	defer cancel()
	client := &deviceclient.Client{Profile: profile, HTTP: a.httpClient}
	bootstrap, err := client.Bootstrap(ctx, a.version)
	if err != nil {
		return nil, err
	}
	if bootstrap.ActiveEvent == nil {
		return nil, errors.New("es ist kein Event aktiv")
	}
	raw, err := client.EventRelease(ctx, bootstrap.ActiveEvent.EventID, bootstrap.ActiveEvent.ReleaseURL)
	if err != nil {
		return nil, err
	}
	_, payload, metadata, err := a.releaseValidator.ValidateEventEnvelope(raw, a.trustedEventKeys)
	if err != nil {
		return nil, errors.New("das aktive Event hat die Signatur- oder Vertragsprüfung nicht bestanden")
	}
	if metadata.EventID != bootstrap.ActiveEvent.EventID {
		return nil, errors.New("das geladene Release gehört zu einem anderen Event")
	}
	return payload, nil
}

// gamesRoot is the per-user directory game archives are extracted into.
func (a *App) gamesRoot() (string, error) {
	return deviceclient.DefaultGamesRoot()
}

func joinPath(dir, relative string) string {
	return filepath.Join(dir, relative)
}

// launcherDownloadPages are the official pages a user gets sent to when a
// required launcher is missing.
//
// These URLs are compiled in on purpose. Taking them from the signed event
// payload would turn a release into a way to point users at an arbitrary site,
// and that is a far worse trade than keeping a short list in the client.
var launcherDownloadPages = map[string]string{
	"steam":           "https://store.steampowered.com/about/",
	"ea-app":          "https://www.ea.com/ea-app",
	"ubisoft-connect": "https://ubisoftconnect.com/",
}

// LauncherDownloadPage returns the official download page for a launcher, or an
// empty string if the client knows none. The UI only offers a link when this
// returns something.
func (a *App) LauncherDownloadPage(launcherID string) string {
	return launcherDownloadPages[strings.TrimSpace(launcherID)]
}

// OpenLauncherDownload opens the official download page in the user's browser.
// LANReady does not install launchers itself; it only points the way.
func (a *App) OpenLauncherDownload(launcherID string) error {
	page := a.LauncherDownloadPage(launcherID)
	if page == "" {
		return fmt.Errorf("für %q ist keine offizielle Downloadseite hinterlegt", launcherID)
	}
	return deviceclient.OpenBrowser(page)
}
