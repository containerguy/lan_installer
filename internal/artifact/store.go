package artifact

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	lanstore "github.com/containerguy/lan_installer/internal/store"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var (
	ErrInvalidMetadata = errors.New("artifact metadata is invalid")
	ErrSizeMismatch    = errors.New("artifact size does not match declaration")
	ErrDigestMismatch  = errors.New("artifact digest does not match declaration")
	ErrCorrupt         = errors.New("stored artifact is corrupt")
	ErrTooLarge        = errors.New("artifact exceeds configured limit")
	ErrQuotaExceeded   = errors.New("artifact store quota exceeded")
)

type Store struct {
	root     string
	metadata *lanstore.Store
	locks    sync.Map
	quotaMu  sync.Mutex
	quota    int64
	reserved int64
}

type Blob struct {
	File *os.File
	lanstore.Artifact
}

type GCResult struct {
	Examined, Removed int
	RemovedBytes      int64
}

func New(root string, metadata *lanstore.Store) (*Store, error) {
	return NewWithQuota(root, metadata, int64(^uint64(0)>>1))
}

func NewWithQuota(root string, metadata *lanstore.Store, quota int64) (*Store, error) {
	if strings.TrimSpace(root) == "" || metadata == nil || quota < 1 {
		return nil, errors.New("artifact root and metadata store are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = ensureRootDirectory(absolute); err != nil {
		return nil, err
	}
	for _, directory := range []string{filepath.Join(absolute, ".tmp"), filepath.Join(absolute, "sha256")} {
		if err = ensurePrivateDirectory(directory); err != nil {
			return nil, err
		}
	}
	s := &Store{root: absolute, metadata: metadata, quota: quota}
	if err = s.recoverGarbageCollection(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func ensureRootDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return validateDirectory(path, false)
}

func ensurePrivateDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return validateDirectory(path, true)
}

func validateDirectory(path string, private bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("artifact path must be a directory and not a symlink")
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil {
		return err
	}
	if !opened.IsDir() || !os.SameFile(info, opened) {
		return errors.New("artifact directory changed while it was validated")
	}
	if private {
		if err = directory.Chmod(0o700); err != nil {
			return err
		}
	}
	return nil
}

func ensureDigestDirectory(root, digest string) error {
	base := filepath.Join(root, "sha256")
	if err := validateDirectory(base, true); err != nil {
		return err
	}
	return ensurePrivateDirectory(filepath.Join(base, digest[:2]))
}

func (s *Store) Ingest(ctx context.Context, digest string, size int64, contentType string, source io.Reader) (lanstore.Artifact, error) {
	contentType, err := normalizeContentType(contentType)
	if !digestPattern.MatchString(digest) || size < 0 || size > lanstore.MaxArtifactSize || err != nil || source == nil {
		return lanstore.Artifact{}, ErrInvalidMetadata
	}
	releaseReservation, err := s.reserve(ctx, size)
	if err != nil {
		return lanstore.Artifact{}, err
	}
	defer releaseReservation()
	digestLock, _ := s.locks.LoadOrStore(digest, &sync.Mutex{})
	mutex := digestLock.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
	temporary, err := os.CreateTemp(filepath.Join(s.root, ".tmp"), "ingest-*")
	if err != nil {
		return lanstore.Artifact{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return lanstore.Artifact{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, size+1))
	if copyErr != nil {
		temporary.Close()
		return lanstore.Artifact{}, copyErr
	}
	if written != size {
		temporary.Close()
		return lanstore.Artifact{}, ErrSizeMismatch
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != digest {
		temporary.Close()
		return lanstore.Artifact{}, ErrDigestMismatch
	}
	if err = temporary.Sync(); err != nil {
		temporary.Close()
		return lanstore.Artifact{}, err
	}
	if err = temporary.Chmod(0o400); err != nil {
		temporary.Close()
		return lanstore.Artifact{}, err
	}
	if err = temporary.Close(); err != nil {
		return lanstore.Artifact{}, err
	}

	destination := s.path(digest)
	if err = ensureDigestDirectory(s.root, digest); err != nil {
		return lanstore.Artifact{}, err
	}
	installed := false
	if _, statErr := os.Lstat(destination); errors.Is(statErr, os.ErrNotExist) {
		if err = os.Rename(temporaryName, destination); err != nil {
			return lanstore.Artifact{}, err
		}
		installed = true
	} else if statErr != nil {
		return lanstore.Artifact{}, statErr
	}
	artifact := lanstore.Artifact{Digest: digest, SizeBytes: size, ContentType: contentType}
	verified, verifyErr := s.openVerifiedFile(destination, artifact)
	if verifyErr != nil {
		if installed {
			_ = os.Remove(destination)
		}
		return lanstore.Artifact{}, verifyErr
	}
	if err = verified.Close(); err != nil {
		return lanstore.Artifact{}, err
	}
	if err = s.metadata.RegisterArtifact(ctx, artifact); err != nil {
		if installed {
			_ = os.Remove(destination)
		}
		return lanstore.Artifact{}, err
	}
	return s.metadata.Artifact(ctx, digest)
}

func (s *Store) IngestComputed(ctx context.Context, maxSize int64, contentType string, source io.Reader) (lanstore.Artifact, error) {
	contentType, err := normalizeContentType(contentType)
	if maxSize < 1 || maxSize > lanstore.MaxArtifactSize || err != nil || source == nil {
		return lanstore.Artifact{}, ErrInvalidMetadata
	}
	releaseReservation, err := s.reserve(ctx, maxSize)
	if err != nil {
		return lanstore.Artifact{}, err
	}
	defer releaseReservation()
	temporary, err := os.CreateTemp(filepath.Join(s.root, ".tmp"), "ingest-computed-*")
	if err != nil {
		return lanstore.Artifact{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return lanstore.Artifact{}, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, maxSize+1))
	if copyErr != nil {
		temporary.Close()
		return lanstore.Artifact{}, copyErr
	}
	if written > maxSize {
		temporary.Close()
		return lanstore.Artifact{}, ErrTooLarge
	}
	if err = temporary.Sync(); err != nil {
		temporary.Close()
		return lanstore.Artifact{}, err
	}
	if err = temporary.Chmod(0o400); err != nil {
		temporary.Close()
		return lanstore.Artifact{}, err
	}
	if err = temporary.Close(); err != nil {
		return lanstore.Artifact{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	digestLock, _ := s.locks.LoadOrStore(digest, &sync.Mutex{})
	mutex := digestLock.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
	destination := s.path(digest)
	if err = ensureDigestDirectory(s.root, digest); err != nil {
		return lanstore.Artifact{}, err
	}
	installed := false
	if _, statErr := os.Lstat(destination); errors.Is(statErr, os.ErrNotExist) {
		if err = os.Rename(temporaryName, destination); err != nil {
			return lanstore.Artifact{}, err
		}
		installed = true
	} else if statErr != nil {
		return lanstore.Artifact{}, statErr
	}
	metadata := lanstore.Artifact{Digest: digest, SizeBytes: written, ContentType: contentType}
	verified, verifyErr := s.openVerifiedFile(destination, metadata)
	if verifyErr != nil {
		if installed {
			_ = os.Remove(destination)
		}
		return lanstore.Artifact{}, verifyErr
	}
	if err = verified.Close(); err != nil {
		return lanstore.Artifact{}, err
	}
	if err = s.metadata.RegisterArtifact(ctx, metadata); err != nil {
		if installed {
			_ = os.Remove(destination)
		}
		return lanstore.Artifact{}, err
	}
	return s.metadata.Artifact(ctx, digest)
}

func (s *Store) reserve(ctx context.Context, size int64) (func(), error) {
	if size < 0 {
		return nil, ErrInvalidMetadata
	}
	s.quotaMu.Lock()
	usage, err := s.metadata.ArtifactUsage(ctx)
	if err != nil {
		s.quotaMu.Unlock()
		return nil, err
	}
	if usage > s.quota || s.reserved > s.quota-usage || size > s.quota-usage-s.reserved {
		s.quotaMu.Unlock()
		return nil, ErrQuotaExceeded
	}
	s.reserved += size
	s.quotaMu.Unlock()
	return func() {
		s.quotaMu.Lock()
		s.reserved -= size
		s.quotaMu.Unlock()
	}, nil
}

func (s *Store) OpenVerified(ctx context.Context, digest string) (Blob, error) {
	metadata, err := s.metadata.Artifact(ctx, digest)
	if err != nil {
		return Blob{}, err
	}
	path := s.path(digest)
	file, err := s.openVerifiedFile(path, metadata)
	if err != nil {
		return Blob{}, err
	}
	return Blob{File: file, Artifact: metadata}, nil
}

func (s *Store) Verify(ctx context.Context, digest string, size int64, contentType string) error {
	blob, err := s.OpenVerified(ctx, digest)
	if err != nil {
		return err
	}
	defer blob.File.Close()
	if blob.SizeBytes != size || (contentType != "" && blob.ContentType != contentType) {
		return ErrCorrupt
	}
	return nil
}

func (s *Store) GarbageCollect(ctx context.Context, before time.Time, limit int, actorID int64, remoteAddr string) (GCResult, error) {
	if actorID < 1 || before.IsZero() {
		return GCResult{}, errors.New("garbage collection actor and cutoff are required")
	}
	candidates, err := s.metadata.ArtifactGCCandidates(ctx, before, limit)
	if err != nil {
		return GCResult{}, err
	}
	result := GCResult{Examined: len(candidates)}
	for _, candidate := range candidates {
		removed, removeErr := s.deleteUnreferenced(ctx, candidate, actorID, remoteAddr)
		if removeErr != nil {
			return result, removeErr
		}
		if removed {
			result.Removed++
			result.RemovedBytes += candidate.SizeBytes
		}
	}
	return result, nil
}

func (s *Store) deleteUnreferenced(ctx context.Context, candidate lanstore.Artifact, actorID int64, remoteAddr string) (bool, error) {
	digestLock, _ := s.locks.LoadOrStore(candidate.Digest, &sync.Mutex{})
	mutex := digestLock.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
	destination := s.path(candidate.Digest)
	trash := filepath.Join(s.root, ".tmp", "gc-"+candidate.Digest)
	renamed := false
	prepare := func() error {
		if _, err := os.Lstat(trash); err == nil {
			return errors.New("garbage collection recovery file already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if err := os.Rename(destination, trash); err != nil {
			return err
		}
		renamed = true
		return nil
	}
	removed, err := s.metadata.DeleteArtifactIfUnreferenced(ctx, candidate.Digest, prepare, &lanstore.AuditEntry{ActorUserID: actorID, Action: "garbage_collect_artifact", RemoteAddr: remoteAddr})
	if err != nil {
		if renamed {
			_ = os.Rename(trash, destination)
		}
		return false, err
	}
	if !removed {
		return false, nil
	}
	if renamed {
		if err = os.Remove(trash); err != nil && !errors.Is(err, os.ErrNotExist) {
			return true, err
		}
	}
	return true, nil
}

func (s *Store) recoverGarbageCollection(ctx context.Context) error {
	entries, err := filepath.Glob(filepath.Join(s.root, ".tmp", "gc-*"))
	if err != nil {
		return err
	}
	for _, trash := range entries {
		digest := strings.TrimPrefix(filepath.Base(trash), "gc-")
		if !digestPattern.MatchString(digest) {
			return errors.New("invalid garbage collection recovery file")
		}
		_, metadataErr := s.metadata.Artifact(ctx, digest)
		if metadataErr == nil {
			destination := s.path(digest)
			if err = ensureDigestDirectory(s.root, digest); err != nil {
				return err
			}
			if _, destinationErr := os.Lstat(destination); errors.Is(destinationErr, os.ErrNotExist) {
				if err = os.Rename(trash, destination); err != nil {
					return err
				}
			} else if destinationErr != nil {
				return destinationErr
			} else if err = os.Remove(trash); err != nil {
				return err
			}
		} else if errors.Is(metadataErr, sql.ErrNoRows) {
			if err = os.Remove(trash); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			return metadataErr
		}
	}
	return nil
}

func (s *Store) openVerifiedFile(path string, artifact lanstore.Artifact) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.SizeBytes {
		return nil, ErrCorrupt
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	closeOnError := func(failure error) (*os.File, error) { _ = file.Close(); return nil, failure }
	openedInfo, err := file.Stat()
	if err != nil {
		return closeOnError(err)
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() != artifact.SizeBytes || !os.SameFile(info, openedInfo) {
		return closeOnError(ErrCorrupt)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(file, artifact.SizeBytes+1))
	if copyErr != nil {
		return closeOnError(copyErr)
	}
	if written != artifact.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != artifact.Digest {
		return closeOnError(ErrCorrupt)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return closeOnError(err)
	}
	return file, nil
}

func (s *Store) path(digest string) string {
	return filepath.Join(s.root, "sha256", digest[:2], digest)
}

func normalizeContentType(value string) (string, error) {
	mediaType, parameters, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil || mediaType == "" || len(parameters) != 0 {
		return "", fmt.Errorf("content type is invalid")
	}
	return strings.ToLower(mediaType), nil
}
