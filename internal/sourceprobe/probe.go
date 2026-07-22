package sourceprobe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Policy struct {
	PrivateAllow     map[string][]netip.Prefix
	Resolver         Resolver
	TLSConfig        *tls.Config
	ConnectTimeout   time.Duration
	TLSHandshake     time.Duration
	ResponseHeader   time.Duration
	TotalTimeout     time.Duration
	DownloadTimeout  time.Duration
	MaxResponseBytes int64
	MaxRedirects     int
}

type Input struct {
	Kind, BaseURL, Username, Password string
}

type Result struct {
	TestedAt     time.Time
	Latency      time.Duration
	Capabilities []string
	StatusCode   int
}

type ProbeError struct {
	Code, Message string
}

func (e *ProbeError) Error() string { return e.Message }

func New(policy Policy) *Prober {
	if policy.Resolver == nil {
		policy.Resolver = net.DefaultResolver
	}
	if policy.ConnectTimeout <= 0 {
		policy.ConnectTimeout = 5 * time.Second
	}
	if policy.TLSHandshake <= 0 {
		policy.TLSHandshake = 5 * time.Second
	}
	if policy.ResponseHeader <= 0 {
		policy.ResponseHeader = 10 * time.Second
	}
	if policy.TotalTimeout <= 0 {
		policy.TotalTimeout = 30 * time.Second
	}
	if policy.DownloadTimeout <= 0 {
		policy.DownloadTimeout = 6 * time.Hour
	}
	if policy.MaxResponseBytes <= 0 {
		policy.MaxResponseBytes = 1 << 20
	}
	if policy.MaxRedirects <= 0 {
		policy.MaxRedirects = 3
	}
	if policy.PrivateAllow == nil {
		policy.PrivateAllow = map[string][]netip.Prefix{}
	}
	return &Prober{policy: policy}
}

type Prober struct{ policy Policy }

func ParsePrivateAllowlist(raw string) (map[string][]netip.Prefix, error) {
	out := map[string][]netip.Prefix{}
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host, values, ok := strings.Cut(entry, "=")
		host = strings.ToLower(strings.TrimSpace(host))
		if !ok || host == "" || strings.ContainsAny(host, "/:@") {
			return nil, fmt.Errorf("invalid source allowlist entry")
		}
		for _, value := range strings.Split(values, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR for %s", host)
			}
			out[host] = append(out[host], prefix.Masked())
		}
		if len(out[host]) == 0 {
			return nil, fmt.Errorf("missing CIDR for %s", host)
		}
	}
	return out, nil
}

func (p *Prober) Test(ctx context.Context, input Input) (Result, error) {
	started := time.Now()
	target, err := validateURL(input.BaseURL)
	if err != nil {
		return Result{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if kind != "https" && kind != "webdav" && kind != "nextcloud_webdav" {
		return Result{}, &ProbeError{Code: "unsupported_kind", Message: "Dieser Quellentyp wird nicht unterstützt."}
	}
	method := http.MethodHead
	if kind == "webdav" || kind == "nextcloud_webdav" {
		method = "PROPFIND"
	}
	ctx, cancel := context.WithTimeout(ctx, p.policy.TotalTimeout)
	defer cancel()

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if p.policy.TLSConfig != nil {
		tlsConfig = p.policy.TLSConfig.Clone()
		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   p.policy.TLSHandshake,
		ResponseHeaderTimeout: p.policy.ResponseHeader,
		DisableKeepAlives:     true,
		DialContext:           p.dialContext,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > p.policy.MaxRedirects {
			return &ProbeError{Code: "too_many_redirects", Message: "Die Quelle leitet zu häufig weiter."}
		}
		if _, err := validateURL(req.URL.String()); err != nil {
			return err
		}
		if method == "PROPFIND" && req.Method != "PROPFIND" {
			return &ProbeError{Code: "webdav_redirect_method", Message: "Die WebDAV-Weiterleitung erhält die PROPFIND-Methode nicht."}
		}
		if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Referer")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return Result{}, &ProbeError{Code: "invalid_url", Message: "Die Basis-URL ist ungültig."}
	}
	if method == "PROPFIND" {
		req.Header.Set("Depth", "0")
	}
	if input.Username != "" || input.Password != "" {
		req.SetBasicAuth(input.Username, input.Password)
	}
	response, err := client.Do(req)
	if err != nil {
		var probeErr *ProbeError
		if errors.As(err, &probeErr) {
			return Result{}, probeErr
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, &ProbeError{Code: "timeout", Message: "Zeitüberschreitung beim Verbindungsaufbau."}
		}
		return Result{}, &ProbeError{Code: "connection_failed", Message: "Die Quelle ist über HTTPS nicht erreichbar oder das TLS-Zertifikat ist ungültig."}
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.Method != method {
		return Result{}, &ProbeError{Code: "redirect_method_changed", Message: "Die Weiterleitung hat die Prüfmethode verändert."}
	}
	limited := io.LimitReader(response.Body, p.policy.MaxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Result{}, &ProbeError{Code: "read_failed", Message: "Die Testantwort konnte nicht gelesen werden."}
	}
	if int64(len(body)) > p.policy.MaxResponseBytes {
		return Result{}, &ProbeError{Code: "response_too_large", Message: "Die Testantwort überschreitet das erlaubte Limit."}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return Result{}, &ProbeError{Code: "authentication_failed", Message: "Benutzername oder App-Passwort wurde abgewiesen."}
	}
	validStatus := response.StatusCode >= 200 && response.StatusCode < 300
	if method == "PROPFIND" {
		validStatus = response.StatusCode == http.StatusMultiStatus || response.StatusCode == http.StatusOK
	}
	if !validStatus {
		return Result{}, &ProbeError{Code: "unexpected_status", Message: "Die Quelle antwortet mit HTTP " + strconv.Itoa(response.StatusCode) + "."}
	}
	capability := "https-head"
	if method == "PROPFIND" {
		capability = "webdav-propfind"
	}
	return Result{TestedAt: time.Now().UTC(), Latency: time.Since(started), Capabilities: []string{capability}, StatusCode: response.StatusCode}, nil
}

func validateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, &ProbeError{Code: "invalid_url", Message: "Nur HTTPS-URLs ohne eingebettete Zugangsdaten, Query oder Fragment sind erlaubt."}
	}
	if u.Port() != "" {
		if _, err := strconv.ParseUint(u.Port(), 10, 16); err != nil {
			return nil, &ProbeError{Code: "invalid_url", Message: "Der HTTPS-Port ist ungültig."}
		}
	}
	return u, nil
}

func (p *Prober) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &ProbeError{Code: "invalid_target", Message: "Das Ziel konnte nicht geprüft werden."}
	}
	addresses, err := p.policy.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, &ProbeError{Code: "dns_failed", Message: "Der Hostname konnte nicht aufgelöst werden."}
	}
	for _, address := range addresses {
		if !p.allowed(host, address.Unmap()) {
			return nil, &ProbeError{Code: "target_blocked", Message: "Die Zieladresse ist durch die Netzwerkpolicy gesperrt."}
		}
	}
	dialer := &net.Dialer{Timeout: p.policy.ConnectTimeout}
	var lastErr error
	for _, ip := range addresses {
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func (p *Prober) allowed(host string, address netip.Addr) bool {
	if !isPrivate(address) {
		return true
	}
	for _, prefix := range p.policy.PrivateAllow[strings.ToLower(host)] {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

var specialUsePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"), netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"), netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func isPrivate(address netip.Addr) bool {
	if !address.IsValid() || !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range specialUsePrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}
func effectivePort(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	return "443"
}
