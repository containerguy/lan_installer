package deviceclient

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/containerguy/lan_installer/internal/protocol"
)

var ErrNoClientUpdate = errors.New("no newer client update is available")

type PreparedUpdate struct {
	Sequence                   int64  `json:"sequence"`
	Version                    string `json:"version"`
	Size                       int64  `json:"size"`
	SHA256                     string `json:"sha256"`
	PublisherCertificateSHA256 string `json:"publisherCertificateSHA256"`
	StagedPath                 string `json:"-"`
}

type AvailableUpdate struct {
	Sequence                   int64  `json:"sequence"`
	Version                    string `json:"version"`
	PublisherCertificateSHA256 string `json:"publisherCertificateSHA256"`
	metadata                   protocol.ClientUpdateMetadata
}

func (c *Client) CheckUpdate(ctx context.Context, runtimeVersion string, highestSequence int64, trusted map[string]ed25519.PublicKey) (AvailableUpdate, error) {
	metadata, err := c.fetchUpdateMetadata(ctx, trusted)
	if err != nil {
		return AvailableUpdate{}, err
	}
	comparison, err := protocol.CompareSemanticVersions(metadata.Version, runtimeVersion)
	if err != nil || comparison <= 0 || metadata.Sequence <= highestSequence {
		return AvailableUpdate{}, ErrNoClientUpdate
	}
	return AvailableUpdate{Sequence: metadata.Sequence, Version: metadata.Version, PublisherCertificateSHA256: metadata.PublisherCertificateSHA256, metadata: metadata}, nil
}

func (c *Client) PrepareUpdate(ctx context.Context, runtimeVersion string, highestSequence int64, trusted map[string]ed25519.PublicKey, stagingDirectory string) (PreparedUpdate, error) {
	var out PreparedUpdate
	available, err := c.CheckUpdate(ctx, runtimeVersion, highestSequence, trusted)
	if err != nil {
		return out, err
	}
	metadata := available.metadata
	if err = os.MkdirAll(stagingDirectory, 0o700); err != nil {
		return out, err
	}
	partPath := filepath.Join(stagingDirectory, metadata.SHA256+".part")
	finalPath := filepath.Join(stagingDirectory, metadata.SHA256+".exe")
	if verifyUpdateFile(finalPath, metadata.Size, metadata.SHA256) == nil {
		return PreparedUpdate{Sequence: metadata.Sequence, Version: metadata.Version, Size: metadata.Size, SHA256: metadata.SHA256, PublisherCertificateSHA256: metadata.PublisherCertificateSHA256, StagedPath: finalPath}, nil
	}
	if err = os.Remove(finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	if err = c.downloadUpdateArtifact(ctx, metadata, partPath); err != nil {
		return out, err
	}
	if err = os.Rename(partPath, finalPath); err != nil {
		return out, err
	}
	if err = os.Chmod(finalPath, 0o700); err != nil {
		return out, err
	}
	return PreparedUpdate{Sequence: metadata.Sequence, Version: metadata.Version, Size: metadata.Size, SHA256: metadata.SHA256, PublisherCertificateSHA256: metadata.PublisherCertificateSHA256, StagedPath: finalPath}, nil
}

func (c *Client) fetchUpdateMetadata(ctx context.Context, trusted map[string]ed25519.PublicKey) (protocol.ClientUpdateMetadata, error) {
	request, err := c.signedRequest(ctx, http.MethodGet, "/v2/client/releases/latest?channel=stable", nil)
	if err != nil {
		return protocol.ClientUpdateMetadata{}, err
	}
	response, err := artifactHTTPClient(c.HTTP).Do(request)
	if err != nil {
		return protocol.ClientUpdateMetadata{}, fmt.Errorf("Clientupdate-Metadaten laden: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.ClientUpdateMetadata{}, responseError(response)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<10+1))
	if err != nil || len(raw) > 32<<10 {
		return protocol.ClientUpdateMetadata{}, errors.New("Clientupdate-Envelope ist zu groß oder nicht lesbar")
	}
	_, _, metadata, err := protocol.ValidateClientUpdateEnvelopeRuntime(raw, trusted)
	if err != nil {
		return protocol.ClientUpdateMetadata{}, fmt.Errorf("Clientupdate-Signatur oder Vertrag ungültig: %w", err)
	}
	return metadata, nil
}

func (c *Client) downloadUpdateArtifact(ctx context.Context, metadata protocol.ClientUpdateMetadata, partPath string) error {
	return c.downloadVerifiedArtifact(ctx, metadata.ArtifactPath, metadata.Size, metadata.SHA256, partPath, "Clientupdate")
}

// downloadVerifiedArtifact resumably downloads one CAS artifact and verifies
// size and digest before returning. Shared by the self-update path and game
// installs so the range/resume handling exists exactly once.
func (c *Client) downloadVerifiedArtifact(ctx context.Context, artifactPath string, size int64, digest, partPath, subject string) error {
	metadata := struct {
		ArtifactPath string
		Size         int64
		SHA256       string
	}{artifactPath, size, digest}
	var offset int64
	if info, err := os.Stat(partPath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > metadata.Size {
			if removeErr := os.Remove(partPath); removeErr != nil {
				return removeErr
			}
		} else {
			offset = info.Size()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if offset == metadata.Size {
		if verifyErr := verifyUpdateFile(partPath, metadata.Size, metadata.SHA256); verifyErr == nil {
			return nil
		}
		if err := os.Remove(partPath); err != nil {
			return err
		}
		offset = 0
	}
	request, err := c.signedRequest(ctx, http.MethodGet, metadata.ArtifactPath, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		request.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(metadata.Size-1, 10))
		request.Header.Set("If-Range", `"sha256:`+metadata.SHA256+`"`)
	}
	response, err := artifactHTTPClient(c.HTTP).Do(request)
	if err != nil {
		return fmt.Errorf("%s herunterladen: %w", subject, err)
	}
	defer response.Body.Close()
	appendMode := offset > 0 && response.StatusCode == http.StatusPartialContent
	if response.StatusCode != http.StatusOK && !appendMode {
		return responseError(response)
	}
	if appendMode {
		expectedPrefix := "bytes " + strconv.FormatInt(offset, 10) + "-"
		if !strings.HasPrefix(response.Header.Get("Content-Range"), expectedPrefix) || !strings.HasSuffix(response.Header.Get("Content-Range"), "/"+strconv.FormatInt(metadata.Size, 10)) {
			return errors.New(subject + "-Resume-Antwort ist inkonsistent")
		}
	} else {
		offset = 0
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partPath, flags, 0o600)
	if err != nil {
		return err
	}
	remaining := metadata.Size - offset
	if c.OnDownloadProgress != nil {
		c.OnDownloadProgress(offset, metadata.Size)
	}
	writer := io.Writer(file)
	if c.OnDownloadProgress != nil {
		writer = &downloadProgressWriter{writer: file, downloaded: offset, total: metadata.Size, report: c.OnDownloadProgress}
	}
	written, copyErr := io.Copy(writer, io.LimitReader(response.Body, remaining+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != remaining {
		return fmt.Errorf("%s-Größe weicht ab: %d statt %d Byte", subject, offset+written, metadata.Size)
	}
	return verifyUpdateFile(partPath, metadata.Size, metadata.SHA256)
}

type downloadProgressWriter struct {
	writer     io.Writer
	downloaded int64
	total      int64
	report     func(int64, int64)
}

func (w *downloadProgressWriter) Write(content []byte) (int, error) {
	n, err := w.writer.Write(content)
	w.downloaded += int64(n)
	w.report(w.downloaded, w.total)
	return n, err
}

func verifyUpdateFile(path string, expectedSize int64, expectedDigest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expectedSize {
		return errors.New("heruntergeladenes Clientupdate besitzt eine falsche Größe")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return errors.New("SHA-256-Prüfung des Clientupdates fehlgeschlagen")
	}
	return nil
}
