package cacheworker

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/sourceprobe"
	"github.com/containerguy/lan_installer/internal/store"
)

var errCancelled = errors.New("cache job cancelled")

type sourceFetcher interface {
	Fetch(context.Context, sourceprobe.Input, string, int64, func(sourceprobe.FetchResult, io.Reader) error) (sourceprobe.FetchResult, error)
}

type Worker struct {
	store     *store.Store
	artifacts *artifact.Store
	fetcher   sourceFetcher
	vault     *secretbox.Box
	quota     int64
	startOnce sync.Once
	startErr  error
}

func New(st *store.Store, artifacts *artifact.Store, fetcher sourceFetcher, vault *secretbox.Box, quota int64) (*Worker, error) {
	if st == nil || artifacts == nil || fetcher == nil || vault == nil || quota < 1 {
		return nil, errors.New("cache worker requires store, artifacts, fetcher, vault and a valid quota")
	}
	return &Worker{store: st, artifacts: artifacts, fetcher: fetcher, vault: vault, quota: quota}, nil
}

func (w *Worker) Start(ctx context.Context) error {
	w.startOnce.Do(func() {
		w.startErr = w.store.RecoverCacheJobs(ctx)
		if w.startErr == nil {
			go w.run(ctx)
		}
	})
	return w.startErr
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		processed := w.processNext(ctx)
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) processNext(ctx context.Context) bool {
	job, err := w.store.ClaimNextCacheJob(ctx)
	if errors.Is(err, store.ErrCacheJobNotFound) || ctx.Err() != nil {
		return false
	}
	if err != nil {
		return false
	}
	if err = w.process(ctx, job); err == nil {
		return true
	}
	if ctx.Err() != nil {
		_ = w.store.RecoverCacheJob(context.Background(), job.ID)
		return false
	}
	cancelled := errors.Is(err, errCancelled) || errors.Is(err, store.ErrCacheJobCancelled)
	code, message := publicFailure(err)
	if cancelled {
		code, message = "cancelled", "Vom Benutzer abgebrochen."
	}
	_ = w.store.FailCacheJob(context.Background(), job.ID, code, message, cancelled)
	return true
}

func (w *Worker) process(ctx context.Context, job store.CacheJob) error {
	requested, err := w.store.CacheJobCancelRequested(ctx, job.ID)
	if err != nil {
		return err
	}
	if requested {
		return errCancelled
	}
	jobContext, cancel := context.WithCancelCause(ctx)
	watchDone := make(chan struct{})
	defer func() {
		close(watchDone)
		cancel(nil)
	}()
	go w.watchCancellation(jobContext, cancel, watchDone, job.ID)
	source, err := w.store.Source(jobContext, job.SourceID)
	if err != nil {
		return err
	}
	if !source.Enabled || source.Revision != job.SourceRevision {
		return store.ErrCacheTargetChanged
	}
	if job.ExpectedSHA256 != "" && job.ExpectedSize > 0 {
		if verifyErr := w.artifacts.Verify(jobContext, job.ExpectedSHA256, job.ExpectedSize, ""); verifyErr == nil {
			metadata, metadataErr := w.store.Artifact(jobContext, job.ExpectedSHA256)
			if metadataErr != nil {
				return metadataErr
			}
			return w.store.CompleteCacheJob(jobContext, job.ID, metadata.Digest, metadata.SizeBytes, metadata.ContentType)
		}
	}
	input := sourceprobe.Input{Kind: source.Kind, BaseURL: source.BaseURL}
	configuration, configErr := w.store.WebDAVConfig(ctx, source.ID)
	if configErr == nil {
		if configuration.AuthType == "basic" {
			plaintext, openErr := w.vault.Open(configuration.SecretNonce, configuration.SecretCiphertext, secretbox.WebDAVAAD(source.ID))
			if openErr != nil {
				return errors.New("source credentials cannot be decrypted")
			}
			input.Username, input.Password = configuration.Username, string(plaintext)
			defer clearBytes(plaintext)
		}
	} else if !errors.Is(configErr, sql.ErrNoRows) {
		return configErr
	}
	usage, err := w.store.ArtifactUsage(jobContext)
	if err != nil {
		return err
	}
	available := w.quota - usage
	if available < 1 || (job.ExpectedSize > 0 && job.ExpectedSize > available) {
		return artifact.ErrTooLarge
	}
	maximum := min(available, store.MaxArtifactSize)
	var stored store.Artifact
	_, err = w.fetcher.Fetch(jobContext, input, job.SourcePath, maximum, func(result sourceprobe.FetchResult, reader io.Reader) error {
		progress := &progressReader{ctx: jobContext, source: reader, store: w.store, jobID: job.ID, lastUpdate: time.Now()}
		if job.ExpectedSHA256 != "" && job.ExpectedSize > 0 {
			stored, err = w.artifacts.Ingest(jobContext, job.ExpectedSHA256, job.ExpectedSize, result.ContentType, progress)
		} else {
			stored, err = w.artifacts.IngestComputed(jobContext, maximum, result.ContentType, progress)
		}
		return err
	})
	if err != nil {
		if cause := context.Cause(jobContext); cause != nil {
			return cause
		}
		return err
	}
	if job.ExpectedSHA256 != "" && stored.Digest != job.ExpectedSHA256 {
		return artifact.ErrDigestMismatch
	}
	if job.ExpectedSize > 0 && stored.SizeBytes != job.ExpectedSize {
		return artifact.ErrSizeMismatch
	}
	if err = w.store.CompleteCacheJob(jobContext, job.ID, stored.Digest, stored.SizeBytes, stored.ContentType); err != nil {
		return err
	}
	return nil
}

func (w *Worker) watchCancellation(ctx context.Context, cancel context.CancelCauseFunc, done <-chan struct{}, jobID string) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			requested, err := w.store.CacheJobCancelRequested(ctx, jobID)
			if errors.Is(err, store.ErrCacheJobState) {
				return
			}
			if err != nil {
				cancel(err)
				return
			}
			if requested {
				cancel(errCancelled)
				return
			}
		}
	}
}

type progressReader struct {
	ctx                 context.Context
	source              io.Reader
	store               *store.Store
	jobID               string
	total, lastReported int64
	lastUpdate          time.Time
}

func (r *progressReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, readErr := r.source.Read(buffer)
	r.total += int64(count)
	if r.total-r.lastReported >= 1<<20 || time.Since(r.lastUpdate) >= 500*time.Millisecond || readErr == io.EOF {
		if err := r.store.UpdateCacheJobProgress(r.ctx, r.jobID, r.total); err != nil {
			return count, err
		}
		r.lastReported, r.lastUpdate = r.total, time.Now()
	}
	return count, readErr
}

func publicFailure(err error) (string, string) {
	var probeErr *sourceprobe.ProbeError
	switch {
	case errors.As(err, &probeErr):
		return probeErr.Code, probeErr.Message
	case errors.Is(err, artifact.ErrTooLarge), errors.Is(err, artifact.ErrQuotaExceeded):
		return "cache_quota_exceeded", "Artefakt oder belegter Cache überschreitet die konfigurierte Cachegrenze."
	case errors.Is(err, artifact.ErrDigestMismatch):
		return "artifact_digest_mismatch", "Die Quelle liefert nicht die hinterlegte SHA-256-Prüfsumme."
	case errors.Is(err, artifact.ErrSizeMismatch):
		return "artifact_size_mismatch", "Die Quelle liefert nicht die hinterlegte Bytegröße."
	case errors.Is(err, store.ErrCacheTargetChanged):
		return "cache_target_changed", "Version oder Quelle wurde während des Auftrags geändert. Bitte neu einplanen."
	default:
		return "cache_internal", "Der Cache-Auftrag konnte nicht abgeschlossen werden."
	}
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
