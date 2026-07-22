package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/signing"
)

const maxPayloadBytes = 950 << 10

func main() {
	socketPath := strings.TrimSpace(os.Getenv("LANREADY_SIGNER_SOCKET"))
	privateKeyPath := strings.TrimSpace(os.Getenv("LANREADY_RELEASE_PRIVATE_KEY_FILE"))
	schemaDir := strings.TrimSpace(os.Getenv("LANREADY_RELEASE_SCHEMA_DIR"))
	if socketPath == "" || privateKeyPath == "" || schemaDir == "" {
		log.Fatal("signer socket, private key and schema directory are required")
	}
	privateKey, err := readPrivateKey(privateKeyPath)
	if err != nil {
		log.Fatalf("private key: %v", err)
	}
	validator, err := protocol.NewReleaseValidator(schemaDir)
	if err != nil {
		log.Fatalf("release schemas: %v", err)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	trusted := map[string]ed25519.PublicKey{protocol.KeyID(publicKey): publicKey}
	if err = os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		log.Fatal(err)
	}
	if info, statErr := os.Lstat(socketPath); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			log.Fatal("refusing to replace non-socket signer path")
		}
		if err = os.Remove(socketPath); err != nil {
			log.Fatal(err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		log.Fatal(statErr)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	defer os.Remove(socketPath)
	if err = os.Chmod(socketPath, 0o600); err != nil {
		log.Fatal(err)
	}
	mux := newSignerHandler(privateKey, validator, trusted)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	log.Printf("LANReady signer ready on Unix socket; key %s", protocol.KeyID(publicKey))
	log.Fatal(server.Serve(listener))
}

func newSignerHandler(privateKey ed25519.PrivateKey, validator *protocol.ReleaseValidator, trusted map[string]ed25519.PublicKey) http.Handler {
	publicKey := privateKey.Public().(ed25519.PublicKey)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]string{"keyId": protocol.KeyID(publicKey)})
	})
	mux.HandleFunc("POST /v1/sign-event", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		payload, readErr := io.ReadAll(io.LimitReader(r.Body, maxPayloadBytes+1))
		if readErr != nil || len(payload) == 0 || len(payload) > maxPayloadBytes {
			http.Error(w, "invalid payload size", http.StatusUnprocessableEntity)
			return
		}
		envelope, signErr := protocol.SignEnvelope(payload, privateKey)
		if signErr != nil {
			http.Error(w, "invalid payload", http.StatusUnprocessableEntity)
			return
		}
		raw, marshalErr := json.Marshal(envelope)
		if marshalErr != nil {
			http.Error(w, "signing failed", http.StatusInternalServerError)
			return
		}
		if _, _, _, validateErr := validator.ValidateEventEnvelope(raw, trusted); validateErr != nil {
			http.Error(w, "payload violates release contract", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(raw)
	})
	return mux
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("private key must be a regular file without group or other permissions")
	}
	return signing.ReadPrivateKey(path)
}
