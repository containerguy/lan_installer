package signing

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixEventSignerHealthAndSigning(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "signer.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/healthz":
			response.Write([]byte(`{"keyId":"ed25519-0011223344556677"}`))
		case "/v1/sign-event":
			response.Write([]byte(`{"formatVersion":1}`))
		default:
			http.NotFound(response, request)
		}
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		listener.Close()
		os.Remove(socketPath)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Unix signer server did not stop")
		}
	})

	client, err := NewUnixEventSigner(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := client.KeyID(context.Background())
	if err != nil || keyID != "ed25519-0011223344556677" {
		t.Fatalf("key ID=%q err=%v", keyID, err)
	}
	raw, err := client.SignEvent(context.Background(), []byte(`{"formatVersion":2}`))
	if err != nil || string(raw) != `{"formatVersion":1}` {
		t.Fatalf("sign response=%s err=%v", raw, err)
	}
}

func TestUnixEventSignerRejectsInvalidSocketAndPayload(t *testing.T) {
	if _, err := NewUnixEventSigner("relative.sock"); err == nil {
		t.Fatal("relative socket accepted")
	}
	client, err := NewUnixEventSigner(filepath.Join(t.TempDir(), "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.SignEvent(context.Background(), nil); err == nil {
		t.Fatal("empty payload accepted")
	}
}
