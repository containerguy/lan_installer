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

type Bootstrap struct {
	APIVersion   int          `json:"apiVersion"`
	ActiveEvent  *ActiveEvent `json:"activeEvent"`
	ClientUpdate struct {
		Required   bool   `json:"required"`
		ReleaseURL string `json:"releaseUrl"`
	} `json:"clientUpdate"`
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
