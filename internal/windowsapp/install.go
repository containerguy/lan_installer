package windowsapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	a.opMu.Lock()
	defer a.opMu.Unlock()
	profile, err := a.connectedProfile()
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	client := &deviceclient.Client{Profile: profile, HTTP: a.httpClient}
	dir, err := client.InstallGameArchive(ctx, install.ArchiveInstall{
		GameID: selected.GameID, Digest: selected.Digest,
		Size: selected.Size, RelativePath: selected.RelativePath,
	}, root)
	if err != nil {
		return result, fmt.Errorf("Installation von %s fehlgeschlagen: %w", gameID, err)
	}
	candidates, err := install.FindExecutables(dir)
	if err != nil {
		return result, err
	}
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
