package webadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/store"
)

var idempotencyKeyPattern = regexp.MustCompile(`(?i)^(?:[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}|[0-9A-HJKMNP-TV-Z]{26})$`)

func (a *Admin) idempotentFormPost(route string, next http.HandlerFunc) http.HandlerFunc {
	protected := a.idempotentPost(route, next)
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "Formulardaten überschreiten das erlaubte Limit.", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "Formulardaten sind ungültig", http.StatusBadRequest)
			return
		}
		key := r.PostForm.Get("idempotency_key")
		if key == "" {
			http.Error(w, "Wiederholungsschutz fehlt; lade die Clientseite neu.", http.StatusBadRequest)
			return
		}
		encoded := r.PostForm.Encode()
		r.Header.Set("Idempotency-Key", key)
		r.Body = io.NopCloser(strings.NewReader(encoded))
		r.ContentLength = int64(len(encoded))
		r.Header.Set("Content-Length", strconv.Itoa(len(encoded)))
		r.Form, r.PostForm = nil, nil
		protected(w, r)
	}
}

func (a *Admin) idempotentPost(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next(w, r)
			return
		}
		if !idempotencyKeyPattern.MatchString(key) {
			a.apiError(w, http.StatusBadRequest, "idempotency_key_invalid", "Idempotency-Key muss eine UUID oder ULID sein.")
			return
		}
		session, _, err := a.currentSession(r)
		if err != nil {
			a.apiError(w, http.StatusUnauthorized, "unauthorized", "Anmeldung erforderlich.")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			a.apiError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Anfrage überschreitet das erlaubte Limit.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		digestInput := bytes.Join([][]byte{[]byte(r.Method), []byte(r.URL.Path), []byte(r.Header.Get("If-Match")), body}, []byte{0})
		digest := sha256.Sum256(digestInput)
		record, owner, err := a.store.ClaimIdempotency(r.Context(), session.User.ID, route, key, digest[:], time.Now().UTC().Add(24*time.Hour))
		if errors.Is(err, store.ErrIdempotencyConflict) {
			a.apiError(w, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key wurde bereits für eine andere Anfrage verwendet.")
			return
		}
		if err != nil {
			a.apiError(w, http.StatusInternalServerError, "idempotency_unavailable", "Anfrage konnte nicht gegen Wiederholung abgesichert werden.")
			return
		}
		if !owner {
			if record.State != "complete" {
				record, err = a.waitForIdempotency(r, session.User.ID, route, key)
				if err != nil {
					a.apiError(w, http.StatusServiceUnavailable, "idempotency_pending", "Eine identische Anfrage wird noch verarbeitet.")
					return
				}
			}
			replayIdempotency(w, record)
			return
		}

		capture := &bufferedResponse{header: w.Header().Clone()}
		next(capture, r)
		if capture.status >= 200 && capture.status < 300 {
			persistContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			err = a.store.CompleteIdempotency(persistContext, session.User.ID, route, key, capture.status, capture.header.Get("Content-Type"), capture.header.Get("ETag"), capture.body.Bytes())
			cancel()
			if err != nil {
				a.apiError(w, http.StatusInternalServerError, "idempotency_save_failed", "Ergebnis konnte nicht wiederholungssicher gespeichert werden.")
				return
			}
		} else {
			_ = a.store.ReleaseIdempotency(r.Context(), session.User.ID, route, key)
		}
		copyHeaders(w.Header(), capture.header)
		w.WriteHeader(capture.status)
		_, _ = w.Write(capture.body.Bytes())
	}
}

func (a *Admin) waitForIdempotency(r *http.Request, actorID int64, route, key string) (store.IdempotencyRecord, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-r.Context().Done():
			return store.IdempotencyRecord{}, r.Context().Err()
		case <-timer.C:
			return store.IdempotencyRecord{}, errors.New("idempotency wait timeout")
		case <-ticker.C:
			record, err := a.store.Idempotency(r.Context(), actorID, route, key)
			if err != nil {
				return store.IdempotencyRecord{}, err
			}
			if record.State == "complete" {
				return record, nil
			}
		}
	}
}

type bufferedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *bufferedResponse) Header() http.Header { return w.header }
func (w *bufferedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *bufferedResponse) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(value)
}

func replayIdempotency(w http.ResponseWriter, record store.IdempotencyRecord) {
	if record.ContentType != "" {
		w.Header().Set("Content-Type", record.ContentType)
	}
	if record.ETag != "" {
		w.Header().Set("ETag", record.ETag)
	}
	w.Header().Set("LANReady-API-Version", "2")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(record.Status)
	_, _ = w.Write(record.ResponseBody)
}

func copyHeaders(target, source http.Header) {
	for key := range target {
		if key != "X-Request-ID" {
			target.Del(key)
		}
	}
	for key, values := range source {
		if key == "X-Request-ID" {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}
