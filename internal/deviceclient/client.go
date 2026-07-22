package deviceclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/discovery"
	"github.com/containerguy/lan_installer/internal/protocol"
)

var (
	ErrAuthorizationPending  = errors.New("authorization pending")
	ErrAuthorizationSlowDown = errors.New("authorization polling too fast")
	eventIDPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	standaloneSlugPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type Client struct {
	Profile            Profile
	HTTP               *http.Client
	OnDownloadProgress func(downloaded, total int64)
}

type Authorization struct {
	AuthorizationID     string    `json:"authorizationId"`
	UserCode            string    `json:"userCode"`
	VerificationURL     string    `json:"verificationUrl"`
	ExpiresAt           time.Time `json:"expiresAt"`
	PollIntervalSeconds int       `json:"pollIntervalSeconds"`
}

type ActiveEvent struct {
	EventID    string `json:"eventId"`
	ReleaseURL string `json:"releaseUrl"`
}

type StandaloneGame struct {
	ID             int64    `json:"id"`
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	ExternalGameID string   `json:"externalGameId"`
	Versions       []string `json:"versions"`
}

type Bootstrap struct {
	APIVersion   int          `json:"apiVersion"`
	ActiveEvent  *ActiveEvent `json:"activeEvent"`
	ClientUpdate struct {
		Required   bool   `json:"required"`
		ReleaseURL string `json:"releaseUrl"`
	} `json:"clientUpdate"`
}

// artifactHTTPClient returns a client suitable for multi-gigabyte downloads.
//
// http.Client.Timeout covers reading the response body, so the 30s default used
// for API calls aborts any sizeable artifact mid-transfer. Here the caller's
// context bounds the overall duration instead, while the transport still guards
// against a connection that stalls before sending headers.
func artifactHTTPClient(value *http.Client) *http.Client {
	client := httpClient(value)
	clone := *client
	clone.Timeout = 0
	transport := http.DefaultTransport
	if clone.Transport != nil {
		transport = clone.Transport
	}
	if base, ok := transport.(*http.Transport); ok {
		tuned := base.Clone()
		tuned.ResponseHeaderTimeout = 60 * time.Second
		clone.Transport = tuned
	}
	return &clone
}

func httpClient(value *http.Client) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if value != nil {
		clone := *value
		client = &clone
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	// Signed device requests and enrollment bodies must never be forwarded to a
	// different location. Legitimate deployments expose canonical v2 paths and
	// therefore do not require HTTP redirects.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}

func Enroll(ctx context.Context, serverURL, code, deviceName, windowsVersion, clientVersion string, client *http.Client) (Profile, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Profile{}, err
	}
	requestBody, _ := json.Marshal(map[string]string{"code": code, "publicKey": base64.RawURLEncoding.EncodeToString(publicKey), "deviceName": deviceName, "windowsVersion": windowsVersion, "clientVersion": clientVersion})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/v2/devices/enroll", bytes.NewReader(requestBody))
	if err != nil {
		return Profile{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient(client).Do(request)
	if err != nil {
		return Profile{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return Profile{}, responseError(response)
	}
	var decoded struct {
		DeviceID string `json:"deviceId"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil || decoded.DeviceID == "" {
		return Profile{}, errors.New("enrollment response is invalid")
	}
	return Profile{ServerURL: strings.TrimRight(serverURL, "/"), DeviceID: decoded.DeviceID, DeviceName: deviceName, ClientVersion: clientVersion, PrivateKey: privateKey}, nil
}

func (c *Client) signedRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	if len(c.Profile.PrivateKey) != ed25519.PrivateKeySize || c.Profile.DeviceID == "" {
		return nil, errors.New("device profile is incomplete")
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Unix()
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Profile.ServerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	canonical, err := protocol.CanonicalRequest(method, request.URL.EscapedPath(), request.URL.RawQuery, body, timestamp, nonce)
	if err != nil {
		return nil, err
	}
	request.Header.Set("LANReady-Device-ID", c.Profile.DeviceID)
	request.Header.Set("LANReady-Timestamp", fmt.Sprint(timestamp))
	request.Header.Set("LANReady-Nonce", base64.RawURLEncoding.EncodeToString(nonce))
	request.Header.Set("LANReady-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(c.Profile.PrivateKey, canonical)))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

// Bootstrap reports the version of the currently running binary in the signed
// query string. This deliberately does not trust the enrollment-time profile
// version, which would otherwise leave an updated client in a permanent 426
// loop until it uploaded a new inventory scan.
func (c *Client) Bootstrap(ctx context.Context, runtimeVersion string) (Bootstrap, error) {
	var out Bootstrap
	if _, err := protocol.CompareSemanticVersions(runtimeVersion, "0.0.0"); err != nil {
		return out, errors.New("running client version is invalid")
	}
	path := "/v2/device/bootstrap?clientVersion=" + url.QueryEscape(runtimeVersion)
	request, err := c.signedRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return out, err
	}
	response, err := httpClient(c.HTTP).Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusUpgradeRequired {
		return out, responseError(response)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out); err != nil || out.ClientUpdate.ReleaseURL == "" {
		return Bootstrap{}, errors.New("bootstrap response is invalid")
	}
	if response.StatusCode == http.StatusUpgradeRequired && !out.ClientUpdate.Required {
		return Bootstrap{}, errors.New("bootstrap update policy is inconsistent")
	}
	c.Profile.ClientVersion = runtimeVersion
	return out, nil
}

// EventRelease downloads the signed envelope advertised by bootstrap. The
// path is reconstructed from the event ID instead of accepting an arbitrary
// server-provided URL, and signed device requests are never redirected.
func (c *Client) EventRelease(ctx context.Context, eventID, releaseURL string) ([]byte, error) {
	if !eventIDPattern.MatchString(eventID) {
		return nil, errors.New("active event ID is invalid")
	}
	expectedPath := "/v2/events/" + eventID + "/release"
	if releaseURL != expectedPath {
		return nil, errors.New("active event release path is invalid")
	}
	request, err := c.signedRequest(ctx, http.MethodGet, expectedPath, nil)
	if err != nil {
		return nil, err
	}
	response, err := httpClient(c.HTTP).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("event release content type is invalid")
	}
	const maximum = 950 << 10
	content, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > maximum {
		return nil, errors.New("event release size is invalid")
	}
	return content, nil
}

func (c *Client) StandaloneGames(ctx context.Context) ([]StandaloneGame, error) {
	request, err := c.signedRequest(ctx, http.MethodGet, "/v2/device/standalone-games", nil)
	if err != nil {
		return nil, err
	}
	response, err := httpClient(c.HTTP).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("standalone catalog content type is invalid")
	}
	var decoded struct {
		Games []StandaloneGame `json:"games"`
	}
	const maximum = 1 << 20
	content, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || len(content) == 0 || len(content) > maximum {
		return nil, errors.New("standalone catalog response size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&decoded); err != nil {
		return nil, errors.New("standalone catalog response is invalid")
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("standalone catalog response has trailing data")
	}
	if len(decoded.Games) > 10000 {
		return nil, errors.New("standalone catalog has too many entries")
	}
	seenIDs := make(map[int64]struct{}, len(decoded.Games))
	seenSlugs := make(map[string]struct{}, len(decoded.Games))
	for _, game := range decoded.Games {
		if game.ID < 1 || len(game.Slug) > 64 || !standaloneSlugPattern.MatchString(game.Slug) || game.ExternalGameID != game.Slug || strings.TrimSpace(game.Name) == "" || len(game.Name) > 200 {
			return nil, errors.New("standalone catalog entry is invalid")
		}
		if _, duplicate := seenIDs[game.ID]; duplicate {
			return nil, errors.New("standalone catalog id is duplicated")
		}
		if _, duplicate := seenSlugs[game.Slug]; duplicate {
			return nil, errors.New("standalone catalog slug is duplicated")
		}
		seenIDs[game.ID] = struct{}{}
		seenSlugs[game.Slug] = struct{}{}
		if len(game.Versions) > 10000 {
			return nil, errors.New("standalone catalog has too many versions")
		}
		seenVersions := make(map[string]struct{}, len(game.Versions))
		for _, version := range game.Versions {
			if strings.TrimSpace(version) != version || version == "" || len(version) > 256 {
				return nil, errors.New("standalone catalog version is invalid")
			}
			if _, duplicate := seenVersions[version]; duplicate {
				return nil, errors.New("standalone catalog version is duplicated")
			}
			seenVersions[version] = struct{}{}
		}
	}
	return decoded.Games, nil
}

func (c *Client) StartAuthorization(ctx context.Context) (Authorization, error) {
	body := []byte(`{}`)
	request, err := c.signedRequest(ctx, http.MethodPost, "/v2/user-device-authorizations", body)
	if err != nil {
		return Authorization{}, err
	}
	response, err := httpClient(c.HTTP).Do(request)
	if err != nil {
		return Authorization{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return Authorization{}, responseError(response)
	}
	var out Authorization
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out); err != nil || out.AuthorizationID == "" {
		return Authorization{}, errors.New("authorization response is invalid")
	}
	return out, nil
}

func (c *Client) PollAuthorization(ctx context.Context, id string) (string, error) {
	request, err := c.signedRequest(ctx, http.MethodGet, "/v2/user-device-authorizations/"+id, nil)
	if err != nil {
		return "", err
	}
	response, err := httpClient(c.HTTP).Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusAccepted {
		return "", ErrAuthorizationPending
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return "", ErrAuthorizationSlowDown
	}
	if response.StatusCode != http.StatusOK {
		return "", responseError(response)
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out); err != nil || out.AccessToken == "" {
		return "", errors.New("authorization token response is invalid")
	}
	return out.AccessToken, nil
}

func (c *Client) UploadInventory(ctx context.Context, accessToken, scanID string, result discovery.Result) error {
	body, err := json.Marshal(map[string]any{"scanId": scanID, "scannedAt": time.Now().UTC(), "clientVersion": c.Profile.ClientVersion, "installations": result.Installations})
	if err != nil {
		return err
	}
	request, err := c.signedRequest(ctx, http.MethodPost, "/v2/device/inventory-scans", body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := httpClient(c.HTTP).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return responseError(response)
	}
	return nil
}

func responseError(response *http.Response) error {
	content, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	var value struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(content, &value) == nil && value.Message != "" {
		return errors.New(value.Message)
	}
	return fmt.Errorf("server returned %s", response.Status)
}
