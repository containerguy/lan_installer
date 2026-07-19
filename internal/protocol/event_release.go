package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type releaseAction struct {
	Adapter        string `json:"adapter"`
	Operation      string `json:"operation"`
	ArtifactDigest string `json:"artifactDigest"`
	RelativePath   string `json:"relativePath"`
	TargetRoot     string `json:"targetRoot"`
}

type eventReleasePayload struct {
	Artifacts []struct {
		Digest string `json:"digest"`
	} `json:"artifacts"`
	Launchers []struct {
		LauncherID string          `json:"launcherId"`
		Actions    []releaseAction `json:"actions"`
	} `json:"launchers"`
	Games []struct {
		GameID     string `json:"gameId"`
		LauncherID string `json:"launcherId"`
		Payloads   []struct {
			Type      string          `json:"type"`
			Artifacts []string        `json:"artifacts"`
			Actions   []releaseAction `json:"actions"`
		} `json:"payloads"`
	} `json:"games"`
}

// ValidateEventReleaseSemantics enforces references and adapter dependencies that
// JSON Schema cannot express. It must run after schema validation and before signing.
func ValidateEventReleaseSemantics(payload []byte) error {
	var release eventReleasePayload
	if err := json.Unmarshal(payload, &release); err != nil {
		return fmt.Errorf("decode event release: %w", err)
	}
	artifacts := make(map[string]struct{}, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		if _, exists := artifacts[artifact.Digest]; exists {
			return fmt.Errorf("duplicate artifact %s", artifact.Digest)
		}
		artifacts[artifact.Digest] = struct{}{}
	}
	launcherAdapters := map[string]string{
		"steam": "steam", "ea-app": "ea_app", "ubisoft-connect": "ubisoft_connect",
	}
	launcherTargets := map[string]string{
		"steam": "steam_library", "ea-app": "ea_library", "ubisoft-connect": "ubisoft_library",
	}
	launchers := make(map[string]struct{}, len(release.Launchers))
	for _, launcher := range release.Launchers {
		adapter, known := launcherAdapters[launcher.LauncherID]
		if !known {
			return fmt.Errorf("unsupported launcher %s", launcher.LauncherID)
		}
		if _, exists := launchers[launcher.LauncherID]; exists {
			return fmt.Errorf("duplicate launcher %s", launcher.LauncherID)
		}
		launchers[launcher.LauncherID] = struct{}{}
		for _, action := range launcher.Actions {
			if action.Adapter != adapter {
				return fmt.Errorf("launcher %s uses adapter %s", launcher.LauncherID, action.Adapter)
			}
			if err := requireKnownArtifact(action.ArtifactDigest, artifacts); err != nil {
				return fmt.Errorf("launcher %s: %w", launcher.LauncherID, err)
			}
			if action.RelativePath != "" && !validRelativePath(action.RelativePath) {
				return fmt.Errorf("launcher %s has unsafe relative path", launcher.LauncherID)
			}
			if action.TargetRoot != "" && action.TargetRoot != launcherTargets[launcher.LauncherID] {
				return fmt.Errorf("launcher %s targets %s instead of %s", launcher.LauncherID, action.TargetRoot, launcherTargets[launcher.LauncherID])
			}
		}
	}
	games := make(map[string]struct{}, len(release.Games))
	allowedOperations := map[string]map[string]bool{
		"launcher_library": {"import_library": true, "verify": true},
		"archive":          {"extract_archive": true, "verify": true},
		"directory_tree":   {"materialize_tree": true, "verify": true},
		"installer":        {"install_launcher": true, "verify": true},
	}
	for _, game := range release.Games {
		if _, exists := games[game.GameID]; exists {
			return fmt.Errorf("duplicate game %s", game.GameID)
		}
		games[game.GameID] = struct{}{}
		if _, exists := launchers[game.LauncherID]; !exists {
			return fmt.Errorf("game %s references missing launcher %s", game.GameID, game.LauncherID)
		}
		expectedAdapter := launcherAdapters[game.LauncherID]
		expectedTarget := launcherTargets[game.LauncherID]
		for _, payload := range game.Payloads {
			allowed, known := allowedOperations[payload.Type]
			if !known {
				return fmt.Errorf("game %s has unsupported payload %s", game.GameID, payload.Type)
			}
			payloadArtifacts := make(map[string]struct{}, len(payload.Artifacts))
			for _, digest := range payload.Artifacts {
				if err := requireKnownArtifact(digest, artifacts); err != nil {
					return fmt.Errorf("game %s: %w", game.GameID, err)
				}
				payloadArtifacts[digest] = struct{}{}
			}
			for _, action := range payload.Actions {
				if !allowed[action.Operation] {
					return fmt.Errorf("operation %s is invalid for payload %s", action.Operation, payload.Type)
				}
				if isLauncherAdapter(action.Adapter) && action.Adapter != expectedAdapter {
					return fmt.Errorf("game %s uses adapter %s instead of %s", game.GameID, action.Adapter, expectedAdapter)
				}
				if action.TargetRoot != "" && action.TargetRoot != expectedTarget {
					return fmt.Errorf("game %s targets %s instead of %s", game.GameID, action.TargetRoot, expectedTarget)
				}
				if err := requireKnownArtifact(action.ArtifactDigest, artifacts); err != nil {
					return fmt.Errorf("game %s action: %w", game.GameID, err)
				}
				if action.ArtifactDigest != "" {
					if _, exists := payloadArtifacts[action.ArtifactDigest]; !exists {
						return fmt.Errorf("game %s action references artifact outside its payload", game.GameID)
					}
				}
				if action.RelativePath != "" && !validRelativePath(action.RelativePath) {
					return fmt.Errorf("game %s has unsafe relative path", game.GameID)
				}
			}
		}
	}
	return nil
}

func requireKnownArtifact(digest string, artifacts map[string]struct{}) error {
	if digest == "" {
		return nil
	}
	if _, exists := artifacts[digest]; !exists {
		return errors.New("action references missing artifact " + digest)
	}
	return nil
}

// ValidateClientUpdateSemantics binds the signed digest to its only legal
// same-origin download path. Clients must not follow redirects.
func ValidateClientUpdateSemantics(payload []byte) error {
	var update struct {
		ArtifactPath   string `json:"artifactPath"`
		SHA256         string `json:"sha256"`
		Version        string `json:"version"`
		MinimumVersion string `json:"minimumVersion"`
	}
	if err := json.Unmarshal(payload, &update); err != nil {
		return fmt.Errorf("decode client update: %w", err)
	}
	expected := "/v2/client/artifacts/sha256/" + update.SHA256
	if update.ArtifactPath != expected || strings.Contains(update.ArtifactPath, "://") {
		return errors.New("client update artifact path does not match signed digest")
	}
	comparison, err := CompareSemanticVersions(update.MinimumVersion, update.Version)
	if err != nil || comparison > 0 {
		return errors.New("client update semantic versions are invalid")
	}
	return nil
}

func validRelativePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") || (len(value) >= 2 && value[1] == ':') {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(strings.ReplaceAll(value, "\\", "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func isLauncherAdapter(value string) bool {
	return value == "steam" || value == "ea_app" || value == "ubisoft_connect"
}
