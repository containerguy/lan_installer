package deviceclient

import (
	"net/http"
	"testing"
	"time"
)

// http.Client.Timeout covers reading the body, so the 30s default that suits
// API calls silently kills any multi-gigabyte artifact download. Downloads must
// rely on the caller's context for the overall bound instead.
func TestArtifactClientHasNoOverallTimeout(t *testing.T) {
	if got := httpClient(nil).Timeout; got == 0 {
		t.Fatal("API client must keep a request timeout")
	}
	if got := artifactHTTPClient(nil).Timeout; got != 0 {
		t.Fatalf("artifact client must not cap total transfer time, got %s", got)
	}
}

// A caller-supplied timeout must not leak into artifact downloads either.
func TestArtifactClientDropsCallerTimeout(t *testing.T) {
	supplied := &http.Client{Timeout: 30 * time.Second}
	if got := artifactHTTPClient(supplied).Timeout; got != 0 {
		t.Fatalf("caller timeout leaked into artifact download: %s", got)
	}
	if supplied.Timeout != 30*time.Second {
		t.Fatal("caller's client was mutated")
	}
}

// Dropping the overall timeout must not mean waiting forever on a dead peer.
func TestArtifactClientStillBoundsHeaderWait(t *testing.T) {
	transport, ok := artifactHTTPClient(nil).Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected a *http.Transport so stalled connections stay bounded")
	}
	if transport.ResponseHeaderTimeout == 0 {
		t.Fatal("a connection that never sends headers would hang forever")
	}
}
