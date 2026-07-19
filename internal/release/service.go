package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	trusted                 map[string]ed25519.PublicKey
	artifacts               ArtifactVerifier
	expectedPublisherSHA256 string
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
	_, payload, sourceMetadata, err := s.validator.ValidateEventEnvelope(source.EnvelopeJSON, s.trusted)
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
	encoded, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return nil, out, err
	}
	out = sourceMetadata
	out.ReleaseID = candidate["releaseId"].(string)
	out.Sequence = latest.Sequence + 1
	out.IssuedAt = now
	out.ValidUntil = validUntil
	return append(encoded, '\n'), out, nil
}

type ArtifactVerifier interface {
	Verify(context.Context, string, int64, string) error
}

func New(st *store.Store, validator *protocol.ReleaseValidator, trusted map[string]ed25519.PublicKey, artifacts ArtifactVerifier, expectedPublisherSHA256 ...string) (*Service, error) {
	if st == nil || validator == nil || len(trusted) == 0 || artifacts == nil {
		return nil, errors.New("release store, validator, trusted keys and artifact verifier are required")
	}
	keyring := make(map[string]ed25519.PublicKey, len(trusted))
	for keyID, key := range trusted {
		if len(key) != ed25519.PublicKeySize || protocol.KeyID(key) != keyID {
			return nil, errors.New("release keyring contains an invalid key")
		}
		keyring[keyID] = append(ed25519.PublicKey(nil), key...)
	}
	publisher := ""
	if len(expectedPublisherSHA256) > 0 {
		publisher = strings.ToLower(strings.TrimSpace(expectedPublisherSHA256[0]))
		if publisher != "" && !publisherDigestPattern.MatchString(publisher) {
			return nil, errors.New("expected Authenticode publisher certificate SHA-256 is invalid")
		}
	}
	return &Service{store: st, validator: validator, trusted: keyring, artifacts: artifacts, expectedPublisherSHA256: publisher}, nil
}

func (s *Service) PublishEvent(ctx context.Context, rawEnvelope []byte, activate bool, audit *store.AuditEntry) (protocol.EventReleaseMetadata, error) {
	envelope, payload, metadata, err := s.validator.ValidateEventEnvelope(rawEnvelope, s.trusted)
	if err != nil {
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
	err = s.store.PublishEventRelease(ctx, store.EventReleaseRecord{
		EventID: metadata.EventID, ReleaseID: metadata.ReleaseID, KeyID: envelope.KeyID,
		Sequence: metadata.Sequence, EnvelopeJSON: canonicalEnvelope, PayloadJSON: payload,
		IssuedAt: metadata.IssuedAt, ValidUntil: metadata.ValidUntil,
		MinimumClientVersion: metadata.MinimumClientVersion, Artifacts: artifacts,
	}, activate, audit)
	return metadata, err
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
	_, _, metadata, err = s.validator.ValidateEventEnvelope(stored.EnvelopeJSON, s.trusted)
	if err != nil {
		return metadata, err
	}
	if metadata.EventID != eventID || metadata.Sequence != sequence || metadata.ReleaseID != stored.ReleaseID {
		return metadata, errors.New("stored event release metadata is inconsistent")
	}
	if _, err = s.verifyEventArtifacts(ctx, metadata); err != nil {
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
	envelope, payload, metadata, err := s.validator.ValidateClientUpdateEnvelope(rawEnvelope, s.trusted)
	if err != nil {
		return metadata, err
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
