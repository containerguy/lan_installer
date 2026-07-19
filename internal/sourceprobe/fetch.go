package sourceprobe

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
)

type FetchResult struct {
	ContentType   string
	ContentLength int64
	FinalURL      string
}

func (p *Prober) Fetch(ctx context.Context, input Input, relativePath string, maxBytes int64, consume func(FetchResult, io.Reader) error) (FetchResult, error) {
	base, err := validateURL(input.BaseURL)
	if err != nil {
		return FetchResult{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if kind != "https" && kind != "webdav" && kind != "nextcloud_webdav" {
		return FetchResult{}, &ProbeError{Code: "unsupported_kind", Message: "Dieser Quellentyp wird nicht unterstützt."}
	}
	relativePath, err = validateRelativePath(relativePath)
	if err != nil {
		return FetchResult{}, err
	}
	if maxBytes < 1 || consume == nil {
		return FetchResult{}, &ProbeError{Code: "download_limit_invalid", Message: "Für den Download ist kein gültiges Größenlimit konfiguriert."}
	}
	targetRaw, err := url.JoinPath(base.String(), relativePath)
	if err != nil {
		return FetchResult{}, &ProbeError{Code: "source_path_invalid", Message: "Der relative Quellpfad ist ungültig."}
	}
	target, err := validateURL(targetRaw)
	if err != nil {
		return FetchResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.policy.DownloadTimeout)
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
		DisableCompression:    true,
		DialContext:           p.dialContext,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > p.policy.MaxRedirects {
			return &ProbeError{Code: "too_many_redirects", Message: "Die Quelle leitet zu häufig weiter."}
		}
		if _, redirectErr := validateURL(req.URL.String()); redirectErr != nil {
			return redirectErr
		}
		if req.Method != http.MethodGet {
			return &ProbeError{Code: "redirect_method_changed", Message: "Die Weiterleitung hat die Downloadmethode verändert."}
		}
		if len(via) > 0 && !sameOrigin(via[0].URL, req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Referer")
		}
		return nil
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return FetchResult{}, &ProbeError{Code: "source_path_invalid", Message: "Der relative Quellpfad ist ungültig."}
	}
	request.Header.Set("Accept-Encoding", "identity")
	if input.Username != "" || input.Password != "" {
		request.SetBasicAuth(input.Username, input.Password)
	}
	response, err := client.Do(request)
	if err != nil {
		var probeErr *ProbeError
		if errors.As(err, &probeErr) {
			return FetchResult{}, probeErr
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return FetchResult{}, &ProbeError{Code: "download_timeout", Message: "Der Download hat das erlaubte Zeitlimit überschritten."}
		}
		return FetchResult{}, &ProbeError{Code: "download_failed", Message: "Die Quelle ist über HTTPS nicht erreichbar oder das TLS-Zertifikat ist ungültig."}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return FetchResult{}, &ProbeError{Code: "authentication_failed", Message: "Benutzername oder App-Passwort wurde abgewiesen."}
	}
	if response.StatusCode != http.StatusOK {
		return FetchResult{}, &ProbeError{Code: "unexpected_status", Message: "Die Quelle antwortet mit HTTP " + strconv.Itoa(response.StatusCode) + "."}
	}
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return FetchResult{}, &ProbeError{Code: "content_encoding_invalid", Message: "Die Quelle liefert keine bytegenau prüfbare Repräsentation."}
	}
	if response.ContentLength > maxBytes {
		return FetchResult{}, &ProbeError{Code: "artifact_too_large", Message: "Das Artefakt überschreitet die konfigurierte Cachegrenze."}
	}
	contentType := "application/octet-stream"
	if value := strings.TrimSpace(response.Header.Get("Content-Type")); value != "" {
		mediaType, _, parseErr := mime.ParseMediaType(value)
		if parseErr != nil || strings.TrimSpace(mediaType) == "" {
			return FetchResult{}, &ProbeError{Code: "content_type_invalid", Message: "Die Quelle liefert einen ungültigen Content-Type."}
		}
		contentType = strings.ToLower(mediaType)
	}
	result := FetchResult{ContentType: contentType, ContentLength: response.ContentLength, FinalURL: response.Request.URL.Redacted()}
	if err = consume(result, io.LimitReader(response.Body, maxBytes+1)); err != nil {
		return FetchResult{}, err
	}
	return result, nil
}

func validateRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\\:?#%") || path.IsAbs(value) {
		return "", &ProbeError{Code: "source_path_invalid", Message: "Der relative Quellpfad ist ungültig."}
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", &ProbeError{Code: "source_path_invalid", Message: "Der relative Quellpfad ist ungültig."}
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != value {
		return "", &ProbeError{Code: "source_path_invalid", Message: "Der relative Quellpfad ist ungültig."}
	}
	return cleaned, nil
}
