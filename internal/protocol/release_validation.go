package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const maxReleaseEnvelopeBytes = 950 << 10

var (
	runtimeKeyIDPattern  = regexp.MustCompile(`^ed25519-[0-9a-f]{16}$`)
	runtimeDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type ReleaseValidator struct {
	eventEnvelope  *jsonschema.Schema
	eventPayload   *jsonschema.Schema
	updateEnvelope *jsonschema.Schema
	updatePayload  *jsonschema.Schema
}

type ReleaseArtifact struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	FileName  string `json:"fileName"`
}

type EventReleaseMetadata struct {
	EventID              string            `json:"eventId"`
	ReleaseID            string            `json:"releaseId"`
	Sequence             int64             `json:"sequence"`
	IssuedAt             time.Time         `json:"-"`
	ValidUntil           time.Time         `json:"-"`
	MinimumClientVersion string            `json:"minimumClientVersion"`
	Artifacts            []ReleaseArtifact `json:"artifacts"`
}

type ClientUpdateMetadata struct {
	Channel                    string    `json:"channel"`
	Sequence                   int64     `json:"sequence"`
	Version                    string    `json:"version"`
	MinimumVersion             string    `json:"minimumVersion"`
	ArtifactKind               string    `json:"artifactKind"`
	UpdaterProtocol            int       `json:"updaterProtocol"`
	PublisherCertificateSHA256 string    `json:"publisherCertificateSHA256"`
	ArtifactPath               string    `json:"artifactPath"`
	Size                       int64     `json:"size"`
	SHA256                     string    `json:"sha256"`
	PublishedAt                time.Time `json:"-"`
}

func NewReleaseValidator(schemaDirectory string) (*ReleaseValidator, error) {
	if strings.TrimSpace(schemaDirectory) == "" {
		return nil, errors.New("release schema directory is required")
	}
	eventPath, err := filepath.Abs(filepath.Join(schemaDirectory, "event-release-envelope.schema.json"))
	if err != nil {
		return nil, err
	}
	updatePath, err := filepath.Abs(filepath.Join(schemaDirectory, "client-update-envelope.schema.json"))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compile := func(path, fragment string) (*jsonschema.Schema, error) {
		return compiler.Compile("file://" + filepath.ToSlash(path) + fragment)
	}
	validator := &ReleaseValidator{}
	if validator.eventEnvelope, err = compile(eventPath, ""); err != nil {
		return nil, fmt.Errorf("compile event envelope schema: %w", err)
	}
	if validator.eventPayload, err = compile(eventPath, "#/$defs/eventRelease"); err != nil {
		return nil, fmt.Errorf("compile event release schema: %w", err)
	}
	if validator.updateEnvelope, err = compile(updatePath, ""); err != nil {
		return nil, fmt.Errorf("compile client update envelope schema: %w", err)
	}
	if validator.updatePayload, err = compile(updatePath, "#/$defs/clientUpdate"); err != nil {
		return nil, fmt.Errorf("compile client update schema: %w", err)
	}
	return validator, nil
}

// NewReleaseValidatorFromJSON compiles the canonical release schemas from
// embedded JSON documents. Portable clients use this path because they cannot
// rely on loose schema files beside the executable.
func NewReleaseValidatorFromJSON(eventSchema, updateSchema []byte) (*ReleaseValidator, error) {
	if len(eventSchema) == 0 || len(updateSchema) == 0 {
		return nil, errors.New("release schema documents are required")
	}
	eventDocument, err := decodeReleaseJSON(eventSchema)
	if err != nil {
		return nil, fmt.Errorf("decode embedded event schema: %w", err)
	}
	updateDocument, err := decodeReleaseJSON(updateSchema)
	if err != nil {
		return nil, fmt.Errorf("decode embedded client update schema: %w", err)
	}
	const eventURL = "https://game-manager.familie-keller.info/schemas/v2/event-release-envelope.schema.json"
	const updateURL = "https://game-manager.familie-keller.info/schemas/v2/client-update-envelope.schema.json"
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource(eventURL, eventDocument); err != nil {
		return nil, fmt.Errorf("add embedded event schema: %w", err)
	}
	if err = compiler.AddResource(updateURL, updateDocument); err != nil {
		return nil, fmt.Errorf("add embedded client update schema: %w", err)
	}
	validator := &ReleaseValidator{}
	if validator.eventEnvelope, err = compiler.Compile(eventURL); err != nil {
		return nil, fmt.Errorf("compile embedded event envelope schema: %w", err)
	}
	if validator.eventPayload, err = compiler.Compile(eventURL + "#/$defs/eventRelease"); err != nil {
		return nil, fmt.Errorf("compile embedded event release schema: %w", err)
	}
	if validator.updateEnvelope, err = compiler.Compile(updateURL); err != nil {
		return nil, fmt.Errorf("compile embedded client update envelope schema: %w", err)
	}
	if validator.updatePayload, err = compiler.Compile(updateURL + "#/$defs/clientUpdate"); err != nil {
		return nil, fmt.Errorf("compile embedded client update schema: %w", err)
	}
	return validator, nil
}

func SignEnvelope(payload []byte, privateKey ed25519.PrivateKey) (Envelope, error) {
	if len(privateKey) != ed25519.PrivateKeySize || len(payload) == 0 || !json.Valid(payload) {
		return Envelope{}, errors.New("release payload or private key is invalid")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return Envelope{
		FormatVersion: 1,
		KeyID:         KeyID(publicKey),
		Algorithm:     "Ed25519",
		Payload:       base64.RawURLEncoding.EncodeToString(payload),
		Signature:     base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	}, nil
}

func (v *ReleaseValidator) ValidateEventEnvelope(raw []byte, trusted map[string]ed25519.PublicKey) (Envelope, []byte, EventReleaseMetadata, error) {
	var metadata EventReleaseMetadata
	envelope, payload, err := v.validateEnvelope(raw, trusted, v.eventEnvelope, v.eventPayload)
	if err != nil {
		return Envelope{}, nil, metadata, err
	}
	if err = ValidateEventReleaseSemantics(payload); err != nil {
		return Envelope{}, nil, metadata, err
	}
	var value struct {
		EventID              string            `json:"eventId"`
		ReleaseID            string            `json:"releaseId"`
		Sequence             int64             `json:"sequence"`
		IssuedAt             string            `json:"issuedAt"`
		ValidUntil           string            `json:"validUntil"`
		MinimumClientVersion string            `json:"minimumClientVersion"`
		Artifacts            []ReleaseArtifact `json:"artifacts"`
	}
	if err = json.Unmarshal(payload, &value); err != nil {
		return Envelope{}, nil, metadata, err
	}
	issuedAt, err := time.Parse(time.RFC3339, value.IssuedAt)
	if err != nil {
		return Envelope{}, nil, metadata, errors.New("event release issuedAt is invalid")
	}
	validUntil, err := time.Parse(time.RFC3339, value.ValidUntil)
	if err != nil || !validUntil.After(issuedAt) {
		return Envelope{}, nil, metadata, errors.New("event release validity window is invalid")
	}
	if _, err = CompareSemanticVersions(value.MinimumClientVersion, "0.0.0"); err != nil {
		return Envelope{}, nil, metadata, errors.New("event release minimumClientVersion is invalid")
	}
	metadata = EventReleaseMetadata{EventID: value.EventID, ReleaseID: value.ReleaseID, Sequence: value.Sequence, IssuedAt: issuedAt.UTC(), ValidUntil: validUntil.UTC(), MinimumClientVersion: value.MinimumClientVersion, Artifacts: value.Artifacts}
	return envelope, payload, metadata, nil
}

func (v *ReleaseValidator) ValidateClientUpdateEnvelope(raw []byte, trusted map[string]ed25519.PublicKey) (Envelope, []byte, ClientUpdateMetadata, error) {
	var metadata ClientUpdateMetadata
	envelope, payload, err := v.validateEnvelope(raw, trusted, v.updateEnvelope, v.updatePayload)
	if err != nil {
		return Envelope{}, nil, metadata, err
	}
	if err = ValidateClientUpdateSemantics(payload); err != nil {
		return Envelope{}, nil, metadata, err
	}
	var value struct {
		Channel                    string `json:"channel"`
		Sequence                   int64  `json:"sequence"`
		Version                    string `json:"version"`
		MinimumVersion             string `json:"minimumVersion"`
		ArtifactKind               string `json:"artifactKind"`
		UpdaterProtocol            int    `json:"updaterProtocol"`
		PublisherCertificateSHA256 string `json:"publisherCertificateSHA256"`
		ArtifactPath               string `json:"artifactPath"`
		Size                       int64  `json:"size"`
		SHA256                     string `json:"sha256"`
		PublishedAt                string `json:"publishedAt"`
	}
	if err = json.Unmarshal(payload, &value); err != nil {
		return Envelope{}, nil, metadata, err
	}
	publishedAt, err := time.Parse(time.RFC3339, value.PublishedAt)
	if err != nil {
		return Envelope{}, nil, metadata, errors.New("client update publishedAt is invalid")
	}
	metadata = ClientUpdateMetadata{Channel: value.Channel, Sequence: value.Sequence, Version: value.Version, MinimumVersion: value.MinimumVersion, ArtifactKind: value.ArtifactKind, UpdaterProtocol: value.UpdaterProtocol, PublisherCertificateSHA256: value.PublisherCertificateSHA256, ArtifactPath: value.ArtifactPath, Size: value.Size, SHA256: value.SHA256, PublishedAt: publishedAt.UTC()}
	return envelope, payload, metadata, nil
}

// ValidateClientUpdateEnvelopeRuntime applies the complete client-update
// contract without filesystem-backed JSON schemas. It is used by the portable
// Windows binary, which must be able to verify an update from a single EXE.
func ValidateClientUpdateEnvelopeRuntime(raw []byte, trusted map[string]ed25519.PublicKey) (Envelope, []byte, ClientUpdateMetadata, error) {
	var metadata ClientUpdateMetadata
	if len(raw) == 0 || len(raw) > 32<<10 {
		return Envelope{}, nil, metadata, errors.New("client update envelope size is invalid")
	}
	value, err := decodeReleaseJSON(raw)
	if err != nil {
		return Envelope{}, nil, metadata, err
	}
	object, ok := value.(map[string]any)
	if !ok || !hasExactFields(object, "formatVersion", "keyId", "algorithm", "payload", "signature") {
		return Envelope{}, nil, metadata, errors.New("client update envelope fields are invalid")
	}
	var envelope Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil || envelope.FormatVersion != 1 || envelope.Algorithm != "Ed25519" || !runtimeKeyIDPattern.MatchString(envelope.KeyID) || len(envelope.Payload) > 16384 || len(envelope.Signature) != 86 {
		return Envelope{}, nil, metadata, errors.New("client update envelope is invalid")
	}
	payload, err := VerifyEnvelope(envelope, trusted)
	if err != nil {
		return Envelope{}, nil, metadata, err
	}
	payloadValue, err := decodeReleaseJSON(payload)
	if err != nil {
		return Envelope{}, nil, metadata, err
	}
	payloadObject, ok := payloadValue.(map[string]any)
	if !ok || !hasExactFields(payloadObject, "formatVersion", "channel", "sequence", "version", "minimumVersion", "artifactKind", "updaterProtocol", "publisherCertificateSHA256", "artifactPath", "size", "sha256", "publishedAt") {
		return Envelope{}, nil, metadata, errors.New("client update payload fields are invalid")
	}
	var update struct {
		FormatVersion              int    `json:"formatVersion"`
		Channel                    string `json:"channel"`
		Sequence                   int64  `json:"sequence"`
		Version                    string `json:"version"`
		MinimumVersion             string `json:"minimumVersion"`
		ArtifactKind               string `json:"artifactKind"`
		UpdaterProtocol            int    `json:"updaterProtocol"`
		PublisherCertificateSHA256 string `json:"publisherCertificateSHA256"`
		ArtifactPath               string `json:"artifactPath"`
		Size                       int64  `json:"size"`
		SHA256                     string `json:"sha256"`
		PublishedAt                string `json:"publishedAt"`
	}
	if err = json.Unmarshal(payload, &update); err != nil || update.FormatVersion != 1 || update.Channel != "stable" || update.Sequence < 1 || update.ArtifactKind != "portable_exe" || update.UpdaterProtocol != 1 || !runtimeDigestPattern.MatchString(update.PublisherCertificateSHA256) || update.Size < 1 || update.Size > 1<<30 || !runtimeDigestPattern.MatchString(update.SHA256) {
		return Envelope{}, nil, metadata, errors.New("client update payload is invalid")
	}
	if err = ValidateClientUpdateSemantics(payload); err != nil {
		return Envelope{}, nil, metadata, err
	}
	publishedAt, err := time.Parse(time.RFC3339, update.PublishedAt)
	if err != nil || publishedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return Envelope{}, nil, metadata, errors.New("client update publishedAt is invalid")
	}
	metadata = ClientUpdateMetadata{Channel: update.Channel, Sequence: update.Sequence, Version: update.Version, MinimumVersion: update.MinimumVersion, ArtifactKind: update.ArtifactKind, UpdaterProtocol: update.UpdaterProtocol, PublisherCertificateSHA256: update.PublisherCertificateSHA256, ArtifactPath: update.ArtifactPath, Size: update.Size, SHA256: update.SHA256, PublishedAt: publishedAt.UTC()}
	return envelope, payload, metadata, nil
}

func hasExactFields(object map[string]any, fields ...string) bool {
	if len(object) != len(fields) {
		return false
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return false
		}
	}
	return true
}

func (v *ReleaseValidator) validateEnvelope(raw []byte, trusted map[string]ed25519.PublicKey, envelopeSchema, payloadSchema *jsonschema.Schema) (Envelope, []byte, error) {
	if len(raw) == 0 || len(raw) > maxReleaseEnvelopeBytes {
		return Envelope{}, nil, errors.New("release envelope size is invalid")
	}
	value, err := decodeReleaseJSON(raw)
	if err != nil {
		return Envelope{}, nil, err
	}
	if err = envelopeSchema.Validate(value); err != nil {
		return Envelope{}, nil, fmt.Errorf("release envelope schema: %w", err)
	}
	var envelope Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return Envelope{}, nil, err
	}
	payload, err := VerifyEnvelope(envelope, trusted)
	if err != nil {
		return Envelope{}, nil, err
	}
	payloadValue, err := decodeReleaseJSON(payload)
	if err != nil {
		return Envelope{}, nil, err
	}
	if err = payloadSchema.Validate(payloadValue); err != nil {
		return Envelope{}, nil, fmt.Errorf("release payload schema: %w", err)
	}
	return envelope, payload, nil
}

func decodeReleaseJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(decoder); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("release JSON has trailing content")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("release JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("release JSON contains duplicate field %q", key)
			}
			seen[key] = struct{}{}
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return errors.New("release JSON object is incomplete")
		}
	case '[':
		for decoder.More() {
			if err = scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return errors.New("release JSON array is incomplete")
		}
	default:
		return errors.New("release JSON delimiter is invalid")
	}
	return nil
}
