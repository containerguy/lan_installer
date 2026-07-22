package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/signing"
)

const maxInputBytes = 950 << 10

var verifyClientUpdateArtifact = platformVerifyClientUpdateArtifact

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "sign-event":
		err = signRelease(os.Args[2:], true)
	case "sign-update":
		err = signRelease(os.Args[2:], false)
	case "verify-event":
		err = verifyRelease(os.Args[2:], true)
	case "verify-update":
		err = verifyRelease(os.Args[2:], false)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	privatePath := flags.String("private-key", "release-private.key", "Private-Key-Ausgabedatei")
	publicPath := flags.String("public-key", "release-public.key", "Public-Key-Ausgabedatei")
	if err := flags.Parse(args); err != nil {
		return err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err = writeKeyExclusive(*privatePath, privateKey, 0o600); err != nil {
		return err
	}
	if err = hardenPrivateKeyFile(*privatePath); err != nil {
		_ = os.Remove(*privatePath)
		return err
	}
	if err = writeKeyExclusive(*publicPath, publicKey, 0o644); err != nil {
		_ = os.Remove(*privatePath)
		return err
	}
	fmt.Printf("Release-Schlüsselpaar erzeugt. Key-ID: %s\n", protocol.KeyID(publicKey))
	fmt.Printf("Private Key: %s (niemals in den Webserver mounten)\n", *privatePath)
	fmt.Printf("Public Key:  %s\n", *publicPath)
	return nil
}

func signRelease(args []string, event bool) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	input := flags.String("in", "release.json", "Unsignierter Payload")
	output := flags.String("out", "release-envelope.json", "Signiertes Envelope")
	privatePath := flags.String("private-key", "release-private.key", "Ed25519 Private Key")
	schemaDir := flags.String("schema-dir", defaultSchemaDirectory(), "Verzeichnis der Release-Schemata")
	artifactPath := flags.String("artifact", "", "Authenticode-signierte portable Client-EXE (für sign-update zwingend)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	payload, err := readLimited(*input)
	if err != nil {
		return err
	}
	privateKey, err := readSecurePrivateKey(*privatePath)
	if err != nil {
		return err
	}
	envelope, err := protocol.SignEnvelope(payload, privateKey)
	if err != nil {
		return err
	}
	rawEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	validator, err := protocol.NewReleaseValidator(*schemaDir)
	if err != nil {
		return err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	trusted := map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}
	if event {
		_, _, _, err = validator.ValidateEventEnvelope(rawEnvelope, trusted)
	} else {
		var metadata protocol.ClientUpdateMetadata
		_, _, metadata, err = validator.ValidateClientUpdateEnvelope(rawEnvelope, trusted)
		if err == nil {
			if strings.TrimSpace(*artifactPath) == "" {
				err = errors.New("sign-update benötigt -artifact mit der fertig Authenticode-signierten portablen EXE")
			} else {
				err = verifyClientUpdateArtifact(*artifactPath, metadata)
			}
		}
	}
	if err != nil {
		return fmt.Errorf("Release vor Ausgabe abgewiesen: %w", err)
	}
	if err = writeExclusive(*output, append(rawEnvelope, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("Signiertes Release geschrieben: %s (%s)\n", *output, envelope.KeyID)
	return nil
}

func verifyRelease(args []string, event bool) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := flags.String("in", "release-envelope.json", "Signiertes Envelope")
	publicPath := flags.String("public-key", "release-public.key", "Ed25519 Public Key")
	schemaDir := flags.String("schema-dir", defaultSchemaDirectory(), "Verzeichnis der Release-Schemata")
	if err := flags.Parse(args); err != nil {
		return err
	}
	raw, err := readLimited(*input)
	if err != nil {
		return err
	}
	publicKey, err := signing.ReadPublicKey(*publicPath)
	if err != nil {
		return err
	}
	validator, err := protocol.NewReleaseValidator(*schemaDir)
	if err != nil {
		return err
	}
	trusted := map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}
	if event {
		_, _, metadata, validateErr := validator.ValidateEventEnvelope(raw, trusted)
		if validateErr != nil {
			return validateErr
		}
		fmt.Printf("Gültiges Event-Release: %s, Sequenz %d\n", metadata.EventID, metadata.Sequence)
	} else {
		_, _, metadata, validateErr := validator.ValidateClientUpdateEnvelope(raw, trusted)
		if validateErr != nil {
			return validateErr
		}
		fmt.Printf("Gültiges Clientupdate: %s %s, Sequenz %d\n", metadata.Channel, metadata.Version, metadata.Sequence)
	}
	return nil
}

func readSecurePrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err = securePrivateKeyFile(path, info); err != nil {
		return nil, err
	}
	return signing.ReadPrivateKey(path)
}

func readLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxInputBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > maxInputBytes {
		return nil, errors.New("Release-Datei ist leer oder zu groß")
	}
	return content, nil
}

func writeKeyExclusive(path string, key []byte, mode os.FileMode) error {
	return writeExclusive(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), mode)
}

func writeExclusive(path string, content []byte, mode os.FileMode) error {
	if directory := filepath.Dir(path); directory != "." {
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			return errors.New("Ausgabeverzeichnis existiert nicht")
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	name := file.Name()
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

func defaultSchemaDirectory() string {
	if value := strings.TrimSpace(os.Getenv("LANREADY_RELEASE_SCHEMA_DIR")); value != "" {
		return value
	}
	return "docs/contracts/schemas"
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: lanready-release <keygen|sign-event|sign-update|verify-event|verify-update> [Optionen]")
	os.Exit(2)
}
