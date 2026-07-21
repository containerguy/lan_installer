package signing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type UnixEventSigner struct {
	client *http.Client
}

func (s *UnixEventSigner) KeyID(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://lanready-signer/healthz", nil)
	if err != nil {
		return "", err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("signer unavailable: %w", err)
	}
	defer response.Body.Close()
	var value struct {
		KeyID string `json:"keyId"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&value) != nil || value.KeyID == "" {
		return "", errors.New("signer health response is invalid")
	}
	return value.KeyID, nil
}

func NewUnixEventSigner(socketPath string) (*UnixEventSigner, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" || !strings.HasPrefix(socketPath, "/") {
		return nil, errors.New("signer socket path must be absolute")
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableCompression: true,
	}
	return &UnixEventSigner{client: &http.Client{Transport: transport, Timeout: 10 * time.Second}}, nil
}

func (s *UnixEventSigner) SignEvent(ctx context.Context, payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > 950<<10 {
		return nil, errors.New("event payload size is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://lanready-signer/v1/sign-event", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("signer unavailable: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 950<<10+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signer rejected event payload (HTTP %d)", response.StatusCode)
	}
	if len(raw) == 0 || len(raw) > 950<<10 {
		return nil, errors.New("signer response size is invalid")
	}
	return raw, nil
}
