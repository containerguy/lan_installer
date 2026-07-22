package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/model"
	"github.com/containerguy/lan_installer/internal/signing"
	"github.com/containerguy/lan_installer/internal/windowsupdate"
)

type Config struct {
	ServerURL      string
	EventID        string
	Token          string
	PublicKey      ed25519.PublicKey
	TargetRoot     string
	Mode           string
	Yes            bool
	DryRun         bool
	SelectedGames  map[string]bool
	WindowsUpdates string
	HTTPClient     *http.Client
	Output         io.Writer
	Input          io.Reader
}

type Client struct {
	config Config
}

func New(config Config) (*Client, error) {
	if config.ServerURL == "" || config.EventID == "" || len(config.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("server URL, event ID and public key are required")
	}
	if config.TargetRoot == "" {
		return nil, errors.New("target root is required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		}}
	}
	if config.Output == nil {
		config.Output = os.Stdout
	}
	if config.Input == nil {
		config.Input = os.Stdin
	}
	if config.Mode == "" {
		config.Mode = "portable"
	}
	return &Client{config: config}, nil
}

func (c *Client) Run(ctx context.Context) (model.Report, error) {
	report := model.Report{
		EventID:      c.config.EventID,
		ClientID:     newClientID(),
		OperatingSys: runtime.GOOS,
		ClientMode:   c.config.Mode,
		CreatedAt:    time.Now().UTC(),
		Ready:        true,
	}
	report.Hostname, _ = os.Hostname()
	report.OSVersion = windowsupdate.OSVersion(ctx)

	manifest, err := c.fetchManifest(ctx)
	if err != nil {
		return report, err
	}
	if time.Now().After(manifest.ExpiresAt) {
		return report, fmt.Errorf("event manifest expired at %s", manifest.ExpiresAt.Format(time.RFC3339))
	}
	if manifest.ID != c.config.EventID {
		return report, fmt.Errorf("manifest event id %q does not match requested event", manifest.ID)
	}

	fmt.Fprintf(c.config.Output, "LANReady: %s (%s)\n", manifest.Name, manifest.ID)
	if err := c.validateSelection(manifest.Games); err != nil {
		return report, err
	}
	for _, game := range manifest.Games {
		if !c.gameSelected(game) {
			continue
		}
		result := c.syncGame(ctx, manifest.Mirrors, game)
		report.Games = append(report.Games, result)
		if !result.Ready {
			report.Ready = false
		}
	}

	if c.config.WindowsUpdates != "off" && c.config.WindowsUpdates != "" {
		winResult := windowsupdate.Run(ctx, c.config.WindowsUpdates)
		report.Windows = &winResult
		if winResult.ErrorText != "" || winResult.RebootRequired || (c.config.WindowsUpdates == "check" && winResult.MissingUpdates > 0) {
			report.Ready = false
		}
	}

	if err := c.sendReport(ctx, report); err != nil {
		fmt.Fprintf(c.config.Output, "Warnung: Status konnte nicht gemeldet werden: %v\n", err)
	}
	return report, nil
}

func (c *Client) validateSelection(games []model.Game) error {
	if len(c.config.SelectedGames) == 0 {
		return nil
	}
	missing := make(map[string]bool, len(c.config.SelectedGames))
	for id := range c.config.SelectedGames {
		missing[id] = true
	}
	for _, game := range games {
		delete(missing, game.ID)
	}
	if len(missing) == 0 {
		return nil
	}
	ids := make([]string, 0, len(missing))
	for id := range missing {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Errorf("unknown game id(s): %s", strings.Join(ids, ", "))
}

func (c *Client) fetchManifest(ctx context.Context) (model.EventManifest, error) {
	endpoint := strings.TrimRight(c.config.ServerURL, "/") + "/v1/events/" + url.PathEscape(c.config.EventID) + "/manifest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return model.EventManifest{}, err
	}
	c.authorize(req)
	response, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return model.EventManifest{}, fmt.Errorf("download manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return model.EventManifest{}, fmt.Errorf("download manifest: server returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return model.EventManifest{}, err
	}
	return signing.Verify(body, c.config.PublicKey)
}

func (c *Client) gameSelected(game model.Game) bool {
	if len(c.config.SelectedGames) > 0 {
		return c.config.SelectedGames[game.ID]
	}
	if c.config.Yes || c.config.Mode == "managed" {
		return true
	}
	fmt.Fprintf(c.config.Output, "%s %s synchronisieren? [j/N]: ", game.Name, game.Version)
	line, _ := bufio.NewReader(c.config.Input).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "j" || answer == "ja" || answer == "y" || answer == "yes"
}

func (c *Client) syncGame(ctx context.Context, mirrors []model.Mirror, game model.Game) model.GameReport {
	result := model.GameReport{ID: game.ID, Name: game.Name, Version: game.Version, Ready: true}
	gameRoot, err := safeJoin(c.config.TargetRoot, game.Target)
	if err != nil {
		result.Ready = false
		result.Failed++
		result.ErrorText = err.Error()
		return result
	}

	ordered := append([]model.Mirror(nil), mirrors...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	fmt.Fprintf(c.config.Output, "Prüfe %s %s ...\n", game.Name, game.Version)
	for _, file := range game.Files {
		destination, pathErr := safeJoin(gameRoot, file.Path)
		if pathErr != nil {
			result.Failed++
			result.Ready = false
			result.ErrorText = pathErr.Error()
			continue
		}
		valid, hashErr := fileValid(destination, file)
		if hashErr == nil && valid {
			result.Skipped++
			continue
		}
		if c.config.DryRun {
			result.Changed++
			result.Ready = false
			continue
		}
		var downloadErr error
		for _, mirror := range ordered {
			downloadErr = c.download(ctx, mirror.BaseURL, file, destination)
			if downloadErr == nil {
				break
			}
		}
		if downloadErr != nil {
			result.Failed++
			result.Ready = false
			result.ErrorText = downloadErr.Error()
			continue
		}
		result.Changed++
		result.Bytes += file.Size
	}
	fmt.Fprintf(c.config.Output, "%s: %d aktuell, %d geändert, %d Fehler\n", game.Name, result.Skipped, result.Changed, result.Failed)
	return result
}

func fileValid(path string, expected model.File) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != expected.Size {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected.SHA256), nil
}

func (c *Client) download(ctx context.Context, baseURL string, expected model.File, destination string) error {
	downloadURL, err := contentURL(baseURL, expected.Source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary := destination + ".lanready.part"
	offset := int64(0)
	if info, statErr := os.Stat(temporary); statErr == nil {
		offset = info.Size()
		if offset > expected.Size {
			_ = os.Remove(temporary)
			offset = 0
		} else if offset == expected.Size {
			valid, validateErr := fileValid(temporary, expected)
			if validateErr == nil && valid {
				return replaceFile(temporary, destination)
			}
			_ = os.Remove(temporary)
			offset = 0
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	if sameHost(downloadURL, c.config.ServerURL) {
		c.authorize(req)
	}
	response, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 && response.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else if response.StatusCode == http.StatusOK {
		flags |= os.O_TRUNC
		offset = 0
	} else {
		return fmt.Errorf("%s returned %s", downloadURL, response.Status)
	}
	out, err := os.OpenFile(temporary, flags, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, response.Body)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	valid, err := fileValid(temporary, expected)
	if err != nil || !valid {
		_ = os.Remove(temporary)
		return fmt.Errorf("downloaded file %q failed size or SHA-256 verification", expected.Path)
	}
	return replaceFile(temporary, destination)
}

func replaceFile(temporary, destination string) error {
	backup := destination + ".lanready.bak"
	_ = os.Remove(backup)
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func contentURL(baseURL, source string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid mirror URL %q", baseURL)
	}
	clean := strings.ReplaceAll(source, "\\", "/")
	if strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("unsafe content source %q", source)
	}
	segments := strings.Split(clean, "/")
	for i := range segments {
		if segments[i] == "" || segments[i] == "." || segments[i] == ".." {
			return "", fmt.Errorf("unsafe content source %q", source)
		}
		segments[i] = url.PathEscape(segments[i])
	}
	return strings.TrimRight(baseURL, "/") + "/content/" + strings.Join(segments, "/"), nil
}

func (c *Client) sendReport(ctx context.Context, report model.Report) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(c.config.ServerURL, "/") + "/v1/reports"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)
	response, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("report endpoint returned %s", response.Status)
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	if c.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.Token)
	}
}

func sameHost(first, second string) bool {
	a, errA := url.Parse(first)
	b, errB := url.Parse(second)
	return errA == nil && errB == nil && strings.EqualFold(a.Host, b.Host)
}

func newClientID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("client-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(random)
}
