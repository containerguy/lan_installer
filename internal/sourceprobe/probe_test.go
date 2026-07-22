package sourceprobe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type staticResolver []netip.Addr

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r, nil
}

func TestFetchStreamsExactBytesWithAuthentication(t *testing.T) {
	payload := []byte("LANReady archive")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/base/games/payload.zip" || r.Header.Get("Accept-Encoding") != "identity" || !ok || username != "user" || password != "app-password" {
			t.Errorf("unexpected fetch request: %s %s", r.Method, r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", "16")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	var received []byte
	result, err := New(localPolicy(server)).Fetch(context.Background(), Input{Kind: "webdav", BaseURL: server.URL + "/base", Username: "user", Password: "app-password"}, "games/payload.zip", 64, func(meta FetchResult, reader io.Reader) error {
		if meta.ContentType != "application/zip" || meta.ContentLength != int64(len(payload)) {
			t.Fatalf("metadata: %#v", meta)
		}
		var readErr error
		received, readErr = io.ReadAll(reader)
		return readErr
	})
	if err != nil || !bytes.Equal(received, payload) || result.ContentType != "application/zip" {
		t.Fatalf("fetch result=%#v bytes=%q err=%v", result, received, err)
	}
}

func TestFetchRejectsTraversalAndOversizedResponse(t *testing.T) {
	prober := New(Policy{})
	for _, invalid := range []string{"../secret", "/absolute", "a/../b", "a%2fb", "a?token=x"} {
		_, err := prober.Fetch(context.Background(), Input{Kind: "https", BaseURL: "https://example.test"}, invalid, 64, func(FetchResult, io.Reader) error { return nil })
		if probeCode(err) != "source_path_invalid" {
			t.Fatalf("path %q: %v", invalid, err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "65")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 65))
	}))
	defer server.Close()
	_, err := New(localPolicy(server)).Fetch(context.Background(), Input{Kind: "https", BaseURL: server.URL}, "large.bin", 64, func(FetchResult, io.Reader) error { return nil })
	if probeCode(err) != "artifact_too_large" {
		t.Fatalf("oversized response: %v", err)
	}
}

func TestFetchRedirectRechecksTargetAndDropsCredentials(t *testing.T) {
	var authorization, referer string
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		referer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("payload"))
	}))
	defer second.Close()
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/final", http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	_, err := New(localPolicy(first, second)).Fetch(context.Background(), Input{Kind: "webdav", BaseURL: first.URL, Username: "user", Password: "secret"}, "payload.bin", 64, func(_ FetchResult, reader io.Reader) error {
		_, readErr := io.Copy(io.Discard, reader)
		return readErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		t.Fatal("fetch credentials forwarded across origin")
	}
	if referer != "" {
		t.Fatalf("fetch referer forwarded across origin: %q", referer)
	}
	_, err = New(Policy{}).Fetch(context.Background(), Input{Kind: "https", BaseURL: "https://example.test/files?token=secret"}, "payload.bin", 64, func(FetchResult, io.Reader) error { return nil })
	if probeCode(err) != "invalid_url" {
		t.Fatalf("query-bearing source accepted: %v", err)
	}
}

func localPolicy(servers ...*httptest.Server) Policy {
	pool := x509.NewCertPool()
	for _, server := range servers {
		pool.AddCert(server.Certificate())
	}
	prefix := netip.MustParsePrefix("127.0.0.0/8")
	return Policy{PrivateAllow: map[string][]netip.Prefix{"127.0.0.1": {prefix}}, TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, TotalTimeout: time.Second}
}

type tlsConfigForTest struct{ RootCAs *x509.CertPool }

func (c *tlsConfigForTest) Config() *tls.Config {
	return &tls.Config{RootCAs: c.RootCAs, MinVersion: tls.VersionTLS12}
}

func probeCode(err error) string {
	if e, ok := err.(*ProbeError); ok {
		return e.Code
	}
	return ""
}

func TestDefaultPolicyBlocksPrivateTargets(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMultiStatus) }))
	defer server.Close()
	_, err := New(Policy{}).Test(context.Background(), Input{Kind: "webdav", BaseURL: server.URL})
	if probeCode(err) != "target_blocked" {
		t.Fatalf("expected target_blocked, got %v", err)
	}
}

func TestAllowedWebDAVAndBasicAuth(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if r.Method != "PROPFIND" || r.Header.Get("Depth") != "0" || !ok || username != "user" || password != "app-password" {
			t.Fatalf("unexpected request")
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte("<multistatus/>"))
	}))
	defer server.Close()
	result, err := New(localPolicy(server)).Test(context.Background(), Input{Kind: "nextcloud_webdav", BaseURL: server.URL, Username: "user", Password: "app-password"})
	if err != nil || result.StatusCode != http.StatusMultiStatus || len(result.Capabilities) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCrossOriginRedirectDropsCredentials(t *testing.T) {
	var authorization, referer string
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		referer = r.Header.Get("Referer")
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer second.Close()
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	_, err := New(localPolicy(first, second)).Test(context.Background(), Input{Kind: "webdav", BaseURL: first.URL, Username: "user", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		t.Fatal("credentials forwarded across origin")
	}
	if referer != "" {
		t.Fatalf("probe referer forwarded across origin: %q", referer)
	}
}

func TestProbeRejectsQueryBearingSourceURL(t *testing.T) {
	_, err := New(Policy{}).Test(context.Background(), Input{Kind: "https", BaseURL: "https://example.test/files?token=secret"})
	if probeCode(err) != "invalid_url" {
		t.Fatalf("query-bearing source accepted: %v", err)
	}
}

func TestOversizeAndTLSFailureAreRedacted(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(strings.Repeat("x", 65)))
	}))
	defer server.Close()
	policy := localPolicy(server)
	policy.MaxResponseBytes = 64
	_, err := New(policy).Test(context.Background(), Input{Kind: "webdav", BaseURL: server.URL})
	if probeCode(err) != "response_too_large" {
		t.Fatalf("oversize: %v", err)
	}
	policy.TLSConfig = nil
	_, err = New(policy).Test(context.Background(), Input{Kind: "webdav", BaseURL: server.URL})
	if probeCode(err) != "connection_failed" || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("TLS error not redacted: %v", err)
	}
}

func TestMixedDNSAndMetadataTargetsAreBlocked(t *testing.T) {
	for name, addresses := range map[string]staticResolver{
		"rebinding": {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
		"metadata":  {netip.MustParseAddr("169.254.169.254")},
		"cgnat":     {netip.MustParseAddr("100.64.0.1")},
		"benchmark": {netip.MustParseAddr("198.18.0.1")},
		"testnet":   {netip.MustParseAddr("203.0.113.10")},
		"ipv6-doc":  {netip.MustParseAddr("2001:db8::1")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(Policy{Resolver: addresses, TotalTimeout: time.Second}).Test(context.Background(), Input{Kind: "https", BaseURL: "https://example.test/path"})
			if probeCode(err) != "target_blocked" {
				t.Fatalf("expected block, got %v", err)
			}
		})
	}
}

func TestParsePrivateAllowlist(t *testing.T) {
	allowed, err := ParsePrivateAllowlist("cloud.internal=10.20.0.0/16,fd00::/8;nas.local=192.168.2.5/32")
	if err != nil || len(allowed["cloud.internal"]) != 2 {
		t.Fatalf("allowlist=%#v err=%v", allowed, err)
	}
	if _, err = ParsePrivateAllowlist("bad-entry"); err == nil {
		t.Fatal("invalid allowlist accepted")
	}
}

type hostResolver map[string][]netip.Addr

func (r hostResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func TestWebDAVRedirectMustPreserveMethod(t *testing.T) {
	final := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer final.Close()
	for _, tc := range []struct {
		name string
		code int
		want string
	}{
		{"found_changes_method", http.StatusFound, "webdav_redirect_method"},
		{"temporary_preserves_method", http.StatusTemporaryRedirect, ""},
		{"permanent_preserves_method", http.StatusPermanentRedirect, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, final.URL, tc.code)
			}))
			defer first.Close()
			_, err := New(localPolicy(first, final)).Test(context.Background(), Input{Kind: "webdav", BaseURL: first.URL})
			if probeCode(err) != tc.want {
				t.Fatalf("code=%q err=%v", probeCode(err), err)
			}
		})
	}
}

func TestRedirectToBlockedTargetAndTimeout(t *testing.T) {
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	policy := localPolicy(redirect)
	policy.Resolver = hostResolver{
		"127.0.0.1":       {netip.MustParseAddr("127.0.0.1")},
		"169.254.169.254": {netip.MustParseAddr("169.254.169.254")},
	}
	_, err := New(policy).Test(context.Background(), Input{Kind: "webdav", BaseURL: redirect.URL})
	if probeCode(err) != "target_blocked" {
		t.Fatalf("blocked redirect: %v", err)
	}

	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusMultiStatus)
	}))
	defer slow.Close()
	policy = localPolicy(slow)
	policy.TotalTimeout = 20 * time.Millisecond
	_, err = New(policy).Test(context.Background(), Input{Kind: "webdav", BaseURL: slow.URL})
	if probeCode(err) != "timeout" {
		t.Fatalf("timeout: %v", err)
	}
}
