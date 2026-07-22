package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var artifactDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// EventArchiveInstall is one game archive a signed event release asks the
// client to install, already reduced to what the client needs.
type EventArchiveInstall struct {
	GameID string
	Name   string
	// Digest is bare lowercase hex without the "sha256:" prefix.
	Digest       string
	Size         int64
	MediaType    string
	RelativePath string
}

// installableArchiveMediaTypes mirrors the server-side allow list. The client
// repeats it because it, not the server, is the party executing the result.
var installableArchiveMediaTypes = map[string]bool{
	"application/zip":              true,
	"application/x-zip-compressed": true,
}

// EventInstallActions extracts the install work from a validated event release
// payload.
//
// This is the point where a signed payload stops being a description and starts
// driving writes on the machine, so it is deliberately fail-closed: anything
// other than a single archive extraction below the user games root is an error
// rather than a silently skipped entry. A release that asks for something this
// client cannot execute safely must stop the install, not proceed partially.
//
// Callers must validate schema, signature and semantics first; this function
// assumes the payload already passed [ValidateEventReleaseSemantics].
func EventInstallActions(payload []byte) ([]EventArchiveInstall, error) {
	var release eventReleasePayload
	if err := json.Unmarshal(payload, &release); err != nil {
		return nil, err
	}
	artifacts := make(map[string]struct {
		size      int64
		mediaType string
	}, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		artifacts[artifact.Digest] = struct {
			size      int64
			mediaType string
		}{artifact.Size, artifact.MediaType}
	}
	for _, launcher := range release.Launchers {
		for _, action := range launcher.Actions {
			if action.Operation != "detect" {
				return nil, fmt.Errorf("Launcheraktion %q wird von dieser Clientversion nicht unterstützt", action.Operation)
			}
		}
	}
	installs := make([]EventArchiveInstall, 0, len(release.Games))
	for _, game := range release.Games {
		if len(game.Payloads) == 0 {
			continue
		}
		if game.LauncherID != "standalone" {
			return nil, fmt.Errorf("Spiel %q liefert ein Paket, obwohl es vom Launcher verwaltet wird", game.GameID)
		}
		if len(game.Payloads) != 1 {
			return nil, fmt.Errorf("Spiel %q hat %d Pakete, unterstützt wird genau eines", game.GameID, len(game.Payloads))
		}
		archive := game.Payloads[0]
		if archive.Type != "archive" {
			return nil, fmt.Errorf("Pakettyp %q wird von dieser Clientversion nicht unterstützt", archive.Type)
		}
		if len(archive.Actions) != 1 {
			return nil, fmt.Errorf("Spiel %q hat %d Aktionen, unterstützt wird genau eine", game.GameID, len(archive.Actions))
		}
		action := archive.Actions[0]
		if action.Adapter != "lanready_archive" || action.Operation != "extract_archive" {
			return nil, fmt.Errorf("Aktion %q/%q wird von dieser Clientversion nicht unterstützt", action.Adapter, action.Operation)
		}
		if action.TargetRoot != "user_games" {
			return nil, fmt.Errorf("Zielverzeichnis %q wird von dieser Clientversion nicht unterstützt", action.TargetRoot)
		}
		if !validRelativePath(action.RelativePath) {
			return nil, fmt.Errorf("Zielpfad von Spiel %q ist unzulässig", game.GameID)
		}
		digest, found := strings.CutPrefix(action.ArtifactDigest, "sha256:")
		if !found || !artifactDigestPattern.MatchString(digest) {
			return nil, fmt.Errorf("Artefaktverweis von Spiel %q ist unzulässig", game.GameID)
		}
		if len(archive.Artifacts) != 1 || archive.Artifacts[0] != action.ArtifactDigest {
			return nil, fmt.Errorf("Paket von Spiel %q verweist auf ein anderes Artefakt als seine Aktion", game.GameID)
		}
		declared, known := artifacts[action.ArtifactDigest]
		if !known {
			return nil, fmt.Errorf("Artefakt von Spiel %q fehlt in der Releaseliste", game.GameID)
		}
		if declared.size < 1 {
			return nil, fmt.Errorf("Artefaktgröße von Spiel %q ist unzulässig", game.GameID)
		}
		if !installableArchiveMediaTypes[declared.mediaType] {
			return nil, fmt.Errorf("Paketformat %q von Spiel %q kann nicht installiert werden", declared.mediaType, game.GameID)
		}
		installs = append(installs, EventArchiveInstall{
			GameID:       game.GameID,
			Name:         game.Name,
			Digest:       digest,
			Size:         declared.size,
			MediaType:    declared.mediaType,
			RelativePath: action.RelativePath,
		})
	}
	return installs, nil
}
