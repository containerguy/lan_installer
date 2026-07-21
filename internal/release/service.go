package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/store"
)

type Service struct {
	store                   *store.Store
	validator               *protocol.ReleaseValidator
	trustedEvents           map[string]ed25519.PublicKey
	trustedUpdates          map[string]ed25519.PublicKey
	artifacts               ArtifactVerifier
	expectedPublisherSHA256 string
	eventSigner             EventSigner
	eventSignerKeyID        string
}

type EventSigner interface {
	SignEvent(context.Context, []byte) ([]byte, error)
}

var (
	ErrEventHasNoGames         = errors.New("event has no assigned game versions")
	ErrEventCatalogInvalid     = errors.New("event catalog contains inactive or incomplete entries")
	ErrEventPackageUnsupported = errors.New("LANReady package publishing needs package metadata that is not configured")
	ErrEventSignerUnavailable  = errors.New("event signer is unavailable")
	ErrEventActionsUnsupported = errors.New("event contains package actions the current Windows client cannot execute")
)

type GeneratedEventRelease struct {
	EventID, ReleaseID string
	Sequence           int64
	GameCount          int
	LauncherCount      int
}

type EventPreflightIssue struct {
	GameVersionID int64  `json:"gameVersionId,omitempty"`
	GameName      string `json:"gameName,omitempty"`
	Version       string `json:"version,omitempty"`
	Code          string `json:"code"`
	Message       string `json:"message"`
}

type EventPreflight struct {
	EventID       int64                 `json:"eventId"`
	EventSlug     string                `json:"eventSlug"`
	EventName     string                `json:"eventName"`
	GameCount     int                   `json:"gameCount"`
	LauncherCount int                   `json:"launcherCount"`
	Ready         bool                  `json:"ready"`
	Issues        []EventPreflightIssue `json:"issues"`
}

type EventPreflightError struct{ Preflight EventPreflight }

func (e *EventPreflightError) Error() string { return "event release preflight failed" }

type ClientCompatibilityError struct {
	MinimumVersion string
	Devices        []string
}

func (e *ClientCompatibilityError) Error() string {
	return "active clients do not satisfy the event minimum version"
}

type CompatibleClientUpdateError struct{ MinimumVersion string }

func (e *CompatibleClientUpdateError) Error() string {
	return "no compatible signed stable client update is available"
}

var publisherDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// BuildRollbackCandidate copies the immutable content of an older release into
// a new unsigned payload with the next sequence. The admin explicitly supplies
// the new end of validity; the candidate must still be signed offline and
// published through the normal verification path, making that extension a new
// trust decision.
func (s *Service) BuildRollbackCandidate(ctx context.Context, eventID string, sourceSequence int64, validUntil time.Time) ([]byte, protocol.EventReleaseMetadata, error) {
	var out protocol.EventReleaseMetadata
	source, err := s.store.EventReleaseSequence(ctx, eventID, sourceSequence)
	if err != nil {
		return nil, out, err
	}
	latest, err := s.store.EventRelease(ctx, eventID)
	if err != nil {
		return nil, out, err
	}
	if sourceSequence >= latest.Sequence {
		return nil, out, errors.New("rollback source must be older than the latest release")
	}
	_, payload, sourceMetadata, err := s.validator.ValidateEventEnvelope(source.EnvelopeJSON, s.trustedEvents)
	if err != nil {
		return nil, out, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	validUntil = validUntil.UTC().Truncate(time.Second)
	if !validUntil.After(now) {
		return nil, out, store.ErrReleaseValidity
	}
	var candidate map[string]any
	if err = json.Unmarshal(payload, &candidate); err != nil {
		return nil, out, err
	}
	randomID := make([]byte, 16)
	if _, err = rand.Read(randomID); err != nil {
		return nil, out, err
	}
	candidate["releaseId"] = "rollback-" + hex.EncodeToString(randomID)
	candidate["sequence"] = latest.Sequence + 1
	candidate["issuedAt"] = now.Format(time.RFC3339)
	candidate["validUntil"] = validUntil.Format(time.RFC3339)
	if minimum, _ := candidate["minimumClientVersion"].(string); minimum == "" {
		return nil, out, errors.New("rollback source has no minimum client version")
	} else if comparison, compareErr := protocol.CompareSemanticVersions(minimum, "0.2.0"); compareErr != nil || comparison < 0 {
		candidate["minimumClientVersion"] = "0.2.0"
	}
	encoded, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return nil, out, err
	}
	out = sourceMetadata
	out.ReleaseID = candidate["releaseId"].(string)
	out.Sequence = latest.Sequence + 1
	out.IssuedAt = now
	out.ValidUntil = validUntil
	out.MinimumClientVersion = candidate["minimumClientVersion"].(string)
	return append(encoded, '\n'), out, nil
}

func (s *Service) GenerateAndPublishRollback(ctx context.Context, eventID string, sourceSequence int64, validUntil time.Time, activate bool, audit *store.AuditEntry) (protocol.EventReleaseMetadata, error) {
	var metadata protocol.EventReleaseMetadata
	if s.eventSigner == nil {
		return metadata, ErrEventSignerUnavailable
	}
	payload, _, err := s.BuildRollbackCandidate(ctx, eventID, sourceSequence, validUntil)
	if err != nil {
		return metadata, err
	}
	envelope, err := s.signEventPayload(ctx, payload)
	if err != nil {
		return metadata, err
	}
	return s.PublishEvent(ctx, envelope, activate, audit)
}

type ArtifactVerifier interface {
	Verify(context.Context, string, int64, string) error
}

func NewWithPurposeKeyrings(st *store.Store, validator *protocol.ReleaseValidator, trustedEvents, trustedUpdates map[string]ed25519.PublicKey, artifacts ArtifactVerifier, expectedPublisherSHA256 ...string) (*Service, error) {
	if st == nil || validator == nil || len(trustedEvents) == 0 || len(trustedUpdates) == 0 || artifacts == nil {
		return nil, errors.New("release store, validator, trusted keys and artifact verifier are required")
	}
	copyKeyring := func(source map[string]ed25519.PublicKey) (map[string]ed25519.PublicKey, error) {
		keyring := make(map[string]ed25519.PublicKey, len(source))
		for keyID, key := range source {
			if len(key) != ed25519.PublicKeySize || protocol.KeyID(key) != keyID {
				return nil, errors.New("release keyring contains an invalid key")
			}
			keyring[keyID] = append(ed25519.PublicKey(nil), key...)
		}
		return keyring, nil
	}
	eventKeyring, err := copyKeyring(trustedEvents)
	if err != nil {
		return nil, err
	}
	updateKeyring, err := copyKeyring(trustedUpdates)
	if err != nil {
		return nil, err
	}
	publisher := ""
	if len(expectedPublisherSHA256) > 0 {
		publisher = strings.ToLower(strings.TrimSpace(expectedPublisherSHA256[0]))
		if publisher != "" && !publisherDigestPattern.MatchString(publisher) {
			return nil, errors.New("expected Authenticode publisher certificate SHA-256 is invalid")
		}
	}
	return &Service{store: st, validator: validator, trustedEvents: eventKeyring, trustedUpdates: updateKeyring, artifacts: artifacts, expectedPublisherSHA256: publisher}, nil
}

func (s *Service) SetEventSigner(signer EventSigner, keyID string) error {
	if signer == nil || keyID == "" {
		return ErrEventSignerUnavailable
	}
	if _, trusted := s.trustedEvents[keyID]; !trusted {
		return errors.New("online event signer key is not trusted for events")
	}
	if _, updateKey := s.trustedUpdates[keyID]; updateKey {
		return errors.New("online event signer key must not be trusted for client updates")
	}
	s.eventSigner = signer
	s.eventSignerKeyID = keyID
	return nil
}

func (s *Service) signEventPayload(ctx context.Context, payload []byte) ([]byte, error) {
	if s.eventSigner == nil || s.eventSignerKeyID == "" {
		return nil, ErrEventSignerUnavailable
	}
	raw, err := s.eventSigner.SignEvent(ctx, payload)
	if err != nil {
		return nil, err
	}
	var envelope protocol.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil || envelope.KeyID != s.eventSignerKeyID {
		return nil, errors.New("event signer returned an envelope from an unexpected key")
	}
	return raw, nil
}

func (s *Service) PreflightEvent(ctx context.Context, eventID int64) (EventPreflight, error) {
	result := EventPreflight{EventID: eventID, Issues: []EventPreflightIssue{}}
	event, err := s.store.EventByID(ctx, eventID)
	if err != nil {
		return result, err
	}
	result.EventSlug, result.EventName = event.Slug, event.Name
	if event.Status == "archived" {
		result.Issues = append(result.Issues, EventPreflightIssue{Code: "event_archived", Message: "Das Event ist archiviert und kann nicht veröffentlicht werden."})
	}
	games, err := s.store.EventReleaseGames(ctx, eventID)
	if err != nil {
		return result, err
	}
	result.GameCount = len(games)
	if len(games) == 0 {
		result.Issues = append(result.Issues, EventPreflightIssue{Code: "event_empty", Message: "Dem Event ist noch keine Spielversion zugeordnet."})
	}
	launchers := map[string]struct{}{}
	gameSlugs := map[string]struct{}{}
	for _, game := range games {
		issue := func(code, message string) {
			result.Issues = append(result.Issues, EventPreflightIssue{GameVersionID: game.GameVersionID, GameName: game.GameName, Version: game.Version, Code: code, Message: message})
		}
		if _, duplicate := gameSlugs[game.GameSlug]; duplicate {
			issue("duplicate_game", "Mehrere Versionen desselben Spiels sind zugeordnet. Entferne zuerst die alte Version.")
		}
		gameSlugs[game.GameSlug] = struct{}{}
		switch {
		case !game.GameEnabled:
			issue("game_disabled", "Das Spiel ist deaktiviert.")
		case !game.VersionEnabled:
			issue("version_disabled", "Die Spielversion ist deaktiviert.")
		case !game.LauncherEnabled:
			issue("launcher_disabled", "Der Launcher ist deaktiviert.")
		case strings.TrimSpace(game.ExternalGameID) == "":
			issue("external_id_missing", "Die externe Spiel-ID fehlt.")
		case game.SourceID != 0 || game.LauncherAdapter == "standalone":
			issue("client_installation_missing", "LANReady kann dieses Paket im Windows-Client noch nicht sicher installieren. Entferne die Zuordnung vorläufig oder veröffentliche sie erst nach dem Installations-Slice.")
		default:
			launcherID, _ := releaseLauncher(game.LauncherAdapter)
			if launcherID == "" {
				issue("launcher_unsupported", "Der Launcher wird für Browser-Releases nicht unterstützt.")
			} else {
				launchers[launcherID] = struct{}{}
			}
		}
	}
	result.LauncherCount = len(launchers)
	result.Ready = len(result.Issues) == 0
	return result, nil
}

// GenerateAndPublishEvent turns the current event assignments into a signed,
// immutable release. Provider-managed launcher games deliberately contain no
// LANReady payload: Steam, EA App or Ubisoft Connect remains the source of
// installation and updates and LANReady only declares the required version.
func (s *Service) GenerateAndPublishEvent(ctx context.Context, eventID int64, minimumClientVersion string, validUntil time.Time, activate bool, audit *store.AuditEntry) (GeneratedEventRelease, error) {
	var result GeneratedEventRelease
	if s.eventSigner == nil || s.eventSignerKeyID == "" {
		return result, ErrEventSignerUnavailable
	}
	if comparison, err := protocol.CompareSemanticVersions(minimumClientVersion, "0.2.0"); err != nil || comparison < 0 {
		return result, errors.New("provider-managed releases require minimum client version 0.2.0")
	}
	now := time.Now().UTC().Truncate(time.Second)
	validUntil = validUntil.UTC().Truncate(time.Second)
	if !validUntil.After(now) {
		return result, store.ErrReleaseValidity
	}
	preflight, err := s.PreflightEvent(ctx, eventID)
	if err != nil {
		return result, err
	}
	if !preflight.Ready {
		return result, &EventPreflightError{Preflight: preflight}
	}
	games, err := s.store.EventReleaseGames(ctx, eventID)
	if err != nil {
		return result, err
	}
	event := games[0]
	if event.EventStatus == "archived" {
		return result, store.ErrReleaseEventArchived
	}
	sequence, err := s.store.NextEventReleaseSequence(ctx, event.EventSlug)
	if err != nil {
		return result, err
	}
	type action struct {
		Adapter   string `json:"adapter"`
		Operation string `json:"operation"`
	}
	type launcherRelease struct {
		LauncherID string   `json:"launcherId"`
		Version    string   `json:"version"`
		Required   bool     `json:"required"`
		Actions    []action `json:"actions"`
	}
	type gameRelease struct {
		GameID         string `json:"gameId"`
		Name           string `json:"name"`
		LauncherID     string `json:"launcherId"`
		ExternalGameID string `json:"externalGameId"`
		Version        string `json:"version"`
		Required       bool   `json:"required"`
		Payloads       []any  `json:"payloads"`
	}
	launcherByID := map[string]*launcherRelease{}
	releaseGames := make([]gameRelease, 0, len(games))
	for _, game := range games {
		if game.EventID != eventID || game.EventSlug != event.EventSlug || !game.GameEnabled || !game.VersionEnabled || !game.LauncherEnabled || strings.TrimSpace(game.ExternalGameID) == "" {
			return result, ErrEventCatalogInvalid
		}
		launcherID, adapter := releaseLauncher(game.LauncherAdapter)
		if game.SourceID != 0 || launcherID == "" {
			return result, ErrEventPackageUnsupported
		}
		launcher := launcherByID[launcherID]
		if launcher == nil {
			launcher = &launcherRelease{LauncherID: launcherID, Version: "current", Actions: []action{{Adapter: adapter, Operation: "detect"}}}
			launcherByID[launcherID] = launcher
		}
		launcher.Required = launcher.Required || game.Required
		releaseGames = append(releaseGames, gameRelease{GameID: game.GameSlug, Name: game.GameName, LauncherID: launcherID, ExternalGameID: game.ExternalGameID, Version: game.Version, Required: game.Required, Payloads: []any{}})
	}
	launcherOrder := []string{"steam", "ea-app", "ubisoft-connect"}
	launchers := make([]launcherRelease, 0, len(launcherByID))
	for _, id := range launcherOrder {
		if launcher := launcherByID[id]; launcher != nil {
			launchers = append(launchers, *launcher)
		}
	}
	randomID := make([]byte, 16)
	if _, err = rand.Read(randomID); err != nil {
		return result, err
	}
	payload := struct {
		FormatVersion        int               `json:"formatVersion"`
		EventID              string            `json:"eventId"`
		ReleaseID            string            `json:"releaseId"`
		Sequence             int64             `json:"sequence"`
		IssuedAt             string            `json:"issuedAt"`
		ValidUntil           string            `json:"validUntil"`
		MinimumClientVersion string            `json:"minimumClientVersion"`
		Artifacts            []any             `json:"artifacts"`
		Launchers            []launcherRelease `json:"launchers"`
		Games                []gameRelease     `json:"games"`
	}{2, event.EventSlug, "release-" + hex.EncodeToString(randomID), sequence, now.Format(time.RFC3339), validUntil.Format(time.RFC3339), minimumClientVersion, []any{}, launchers, releaseGames}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	rawEnvelope, err := s.signEventPayload(ctx, rawPayload)
	if err != nil {
		return result, err
	}
	metadata, err := s.PublishEvent(ctx, rawEnvelope, activate, audit)
	if err != nil {
		return result, err
	}
	return GeneratedEventRelease{EventID: metadata.EventID, ReleaseID: metadata.ReleaseID, Sequence: metadata.Sequence, GameCount: len(releaseGames), LauncherCount: len(launchers)}, nil
}

func releaseLauncher(adapter string) (string, string) {
	switch adapter {
	case "steam":
		return "steam", "steam"
	case "ea_app":
		return "ea-app", "ea_app"
	case "ubisoft_connect":
		return "ubisoft-connect", "ubisoft_connect"
	default:
		return "", ""
	}
}

func validateSupportedEventPayload(payload []byte) error {
	var release struct {
		Artifacts []any `json:"artifacts"`
		Launchers []struct {
			Actions []struct {
				Operation      string `json:"operation"`
				ArtifactDigest string `json:"artifactDigest"`
			} `json:"actions"`
		} `json:"launchers"`
		Games []struct {
			LauncherID string `json:"launcherId"`
			Payloads   []any  `json:"payloads"`
		} `json:"games"`
	}
	if err := json.Unmarshal(payload, &release); err != nil {
		return err
	}
	if len(release.Artifacts) > 0 {
		return ErrEventActionsUnsupported
	}
	for _, launcher := range release.Launchers {
		for _, action := range launcher.Actions {
			if action.Operation != "detect" || action.ArtifactDigest != "" {
				return ErrEventActionsUnsupported
			}
		}
	}
	for _, game := range release.Games {
		if game.LauncherID == "standalone" || len(game.Payloads) > 0 {
			return ErrEventActionsUnsupported
		}
	}
	return nil
}

func (s *Service) PublishEvent(ctx context.Context, rawEnvelope []byte, activate bool, audit *store.AuditEntry) (protocol.EventReleaseMetadata, error) {
	envelope, payload, metadata, err := s.validator.ValidateEventEnvelope(rawEnvelope, s.trustedEvents)
	if err != nil {
		return metadata, err
	}
	if err = validateSupportedEventPayload(payload); err != nil {
		return metadata, err
	}
	canonicalEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return metadata, err
	}
	artifacts, err := s.verifyEventArtifacts(ctx, metadata)
	if err != nil {
		return metadata, err
	}
	if activate {
		if err = s.requireCompatibleStableUpdate(ctx, metadata.MinimumClientVersion); err != nil {
			return metadata, err
		}
		if err = s.requireCompatibleClients(ctx, metadata.MinimumClientVersion); err != nil {
			return metadata, err
		}
	}
	err = s.store.PublishEventRelease(ctx, store.EventReleaseRecord{
		EventID: metadata.EventID, ReleaseID: metadata.ReleaseID, KeyID: envelope.KeyID,
		Sequence: metadata.Sequence, EnvelopeJSON: canonicalEnvelope, PayloadJSON: payload,
		IssuedAt: metadata.IssuedAt, ValidUntil: metadata.ValidUntil,
		MinimumClientVersion: metadata.MinimumClientVersion, Artifacts: artifacts,
	}, activate, audit)
	return metadata, err
}

// incompatibleClients lists active devices whose reported runtime version is
// below minimumVersion, formatted for operator-facing errors.
func (s *Service) incompatibleClients(ctx context.Context, minimumVersion string) ([]string, error) {
	devices, err := s.store.ActiveDevices(ctx)
	if err != nil {
		return nil, err
	}
	incompatible := []string{}
	for _, device := range devices {
		comparison, compareErr := protocol.CompareSemanticVersions(device.ClientVersion, minimumVersion)
		if compareErr != nil || comparison < 0 {
			incompatible = append(incompatible, device.Name+" ("+device.ClientVersion+")")
		}
	}
	return incompatible, nil
}

func (s *Service) requireCompatibleClients(ctx context.Context, minimumVersion string) error {
	incompatible, err := s.incompatibleClients(ctx, minimumVersion)
	if err != nil {
		return err
	}
	if len(incompatible) > 0 {
		return &ClientCompatibilityError{MinimumVersion: minimumVersion, Devices: incompatible}
	}
	return nil
}

func (s *Service) requireCompatibleStableUpdate(ctx context.Context, minimumVersion string) error {
	if comparison, err := protocol.CompareSemanticVersions(minimumVersion, "0.2.0"); err == nil && comparison < 0 {
		return nil
	}
	// A distributable stable update only has to exist while some active device
	// still needs upgrading. Once every active device already reports the
	// minimum version there is nobody left to serve, so a manually distributed
	// client may activate without a self-update artifact in the CAS. Devices
	// enrolling later are unaffected: the device endpoints keep refusing to
	// hand out a release below its minimum client version.
	incompatible, err := s.incompatibleClients(ctx, minimumVersion)
	if err != nil {
		return err
	}
	if len(incompatible) == 0 {
		return nil
	}
	update, err := s.store.LatestClientUpdateRelease(ctx, "stable")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &CompatibleClientUpdateError{MinimumVersion: minimumVersion}
		}
		return err
	}
	_, _, metadata, err := s.validator.ValidateClientUpdateEnvelope(update.EnvelopeJSON, s.trustedUpdates)
	if err != nil {
		return &CompatibleClientUpdateError{MinimumVersion: minimumVersion}
	}
	comparison, compareErr := protocol.CompareSemanticVersions(metadata.Version, minimumVersion)
	if compareErr != nil || comparison < 0 {
		return &CompatibleClientUpdateError{MinimumVersion: minimumVersion}
	}
	if err = s.artifacts.Verify(ctx, metadata.SHA256, metadata.Size, "application/vnd.microsoft.portable-executable"); err != nil {
		return &CompatibleClientUpdateError{MinimumVersion: minimumVersion}
	}
	return nil
}

// ActivateEvent activates a previously staged immutable release. Only the
// newest sequence may be activated; rollback must always be published as a new
// higher signed sequence. Signature, schema and CAS bytes are rechecked here so
// staging never bypasses the activation trust boundary.
func (s *Service) ActivateEvent(ctx context.Context, eventID string, sequence int64, audit *store.AuditEntry) (protocol.EventReleaseMetadata, error) {
	var metadata protocol.EventReleaseMetadata
	stored, err := s.store.EventReleaseSequence(ctx, eventID, sequence)
	if err != nil {
		return metadata, err
	}
	_, payload, metadata, err := s.validator.ValidateEventEnvelope(stored.EnvelopeJSON, s.trustedEvents)
	if err != nil {
		return metadata, err
	}
	if err = validateSupportedEventPayload(payload); err != nil {
		return metadata, err
	}
	if metadata.EventID != eventID || metadata.Sequence != sequence || metadata.ReleaseID != stored.ReleaseID {
		return metadata, errors.New("stored event release metadata is inconsistent")
	}
	if _, err = s.verifyEventArtifacts(ctx, metadata); err != nil {
		return metadata, err
	}
	if err = s.requireCompatibleStableUpdate(ctx, metadata.MinimumClientVersion); err != nil {
		return metadata, err
	}
	if err = s.requireCompatibleClients(ctx, metadata.MinimumClientVersion); err != nil {
		return metadata, err
	}
	return metadata, s.store.ActivateEventRelease(ctx, eventID, sequence, audit)
}

func (s *Service) verifyEventArtifacts(ctx context.Context, metadata protocol.EventReleaseMetadata) ([]store.ReleaseArtifactReference, error) {
	artifacts := make([]store.ReleaseArtifactReference, 0, len(metadata.Artifacts))
	for _, artifact := range metadata.Artifacts {
		digest, found := strings.CutPrefix(artifact.Digest, "sha256:")
		if !found {
			return nil, errors.New("event release artifact digest is invalid")
		}
		if err := s.artifacts.Verify(ctx, digest, artifact.Size, artifact.MediaType); err != nil {
			return nil, store.ErrReleaseArtifact
		}
		artifacts = append(artifacts, store.ReleaseArtifactReference{Digest: digest, SizeBytes: artifact.Size, ContentType: artifact.MediaType})
	}
	return artifacts, nil
}

func (s *Service) PublishClientUpdate(ctx context.Context, rawEnvelope []byte, audit *store.AuditEntry) (protocol.ClientUpdateMetadata, error) {
	envelope, payload, metadata, err := s.validator.ValidateClientUpdateEnvelope(rawEnvelope, s.trustedUpdates)
	if err != nil {
		return metadata, err
	}
	if s.eventSignerKeyID != "" && envelope.KeyID == s.eventSignerKeyID {
		return metadata, errors.New("online event signing key is not authorized for client updates")
	}
	if s.expectedPublisherSHA256 == "" || metadata.PublisherCertificateSHA256 != s.expectedPublisherSHA256 || metadata.ArtifactKind != "portable_exe" || metadata.UpdaterProtocol != 1 {
		return metadata, errors.New("client update publisher or updater protocol does not match server release policy")
	}
	canonicalEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return metadata, err
	}
	if err = s.artifacts.Verify(ctx, metadata.SHA256, metadata.Size, "application/vnd.microsoft.portable-executable"); err != nil {
		return metadata, store.ErrReleaseArtifact
	}
	err = s.store.PublishClientUpdateRelease(ctx, store.ClientUpdateReleaseRecord{
		Channel: metadata.Channel, Sequence: metadata.Sequence, Version: metadata.Version,
		MinimumVersion: metadata.MinimumVersion, ArtifactDigest: metadata.SHA256,
		SizeBytes: metadata.Size, KeyID: envelope.KeyID, EnvelopeJSON: canonicalEnvelope,
		PayloadJSON: payload, PublishedAt: metadata.PublishedAt,
	}, audit)
	return metadata, err
}
