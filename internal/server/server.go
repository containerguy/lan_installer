package server

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/containerguy/lan_installer/internal/model"
)

var eventIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type Server struct {
	dataDir     string
	clientToken string
	adminToken  string
	reportFile  string
	reportMu    sync.Mutex
	mux         *http.ServeMux
	admin       http.Handler
	device      http.Handler
}

func New(dataDir, clientToken, adminToken string) (*Server, error) {
	return NewWithAdmin(dataDir, clientToken, adminToken, nil)
}

func NewWithAdmin(dataDir, clientToken, adminToken string, admin http.Handler) (*Server, error) {
	return NewWithHandlers(dataDir, clientToken, adminToken, admin, nil)
}

func NewWithHandlers(dataDir, clientToken, adminToken string, admin, device http.Handler) (*Server, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if clientToken == "" || adminToken == "" {
		return nil, errors.New("client and admin API tokens are required")
	}
	if clientToken == adminToken {
		return nil, errors.New("client and admin API tokens must differ")
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "events"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "content"), 0o755); err != nil {
		return nil, err
	}
	s := &Server{
		dataDir:     dataDir,
		clientToken: clientToken,
		adminToken:  adminToken,
		reportFile:  filepath.Join(dataDir, "reports.ndjson"),
		mux:         http.NewServeMux(),
		admin:       admin,
		device:      device,
	}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return securityHeaders(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.index)
	s.mux.HandleFunc("GET /healthz", s.health)
	if s.admin != nil {
		s.mux.Handle("/admin/", s.admin)
	}
	if s.device != nil {
		s.mux.Handle("/v2/", s.device)
	}
	s.mux.Handle("GET /content/", requireToken(s.clientToken, http.StripPrefix("/content/", http.FileServer(http.Dir(filepath.Join(s.dataDir, "content"))))))
	s.mux.Handle("GET /v1/events/", requireToken(s.clientToken, http.HandlerFunc(s.eventManifest)))
	s.mux.Handle("POST /v1/reports", requireToken(s.clientToken, http.HandlerFunc(s.createReport)))
	s.mux.Handle("GET /v1/reports", requireToken(s.adminToken, http.HandlerFunc(s.listReports)))
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if s.admin != nil {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "LANReady Management Server",
		"status":  "ok",
		"health":  "/healthz",
		"api":     "/v1",
		"admin":   "/admin/",
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) eventManifest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/events/")
	eventID, suffix, found := strings.Cut(path, "/")
	if !found || suffix != "manifest" || !eventIDPattern.MatchString(eventID) {
		http.NotFound(w, r)
		return
	}
	manifestPath := filepath.Join(s.dataDir, "events", eventID, "envelope.json")
	content, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "cannot read manifest", http.StatusInternalServerError)
		return
	}
	if !json.Valid(content) {
		http.Error(w, "stored manifest is invalid", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(content)
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "report is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var report model.Report
	if err := json.Unmarshal(body, &report); err != nil {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	if report.EventID == "" || report.ClientID == "" || report.Hostname == "" {
		http.Error(w, "eventId, clientId and hostname are required", http.StatusBadRequest)
		return
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Now().UTC()
	}
	line, err := json.Marshal(report)
	if err != nil {
		http.Error(w, "cannot encode report", http.StatusInternalServerError)
		return
	}
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	file, err := os.OpenFile(s.reportFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		http.Error(w, "cannot store report", http.StatusInternalServerError)
		return
	}
	_, writeErr := file.Write(append(line, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		http.Error(w, "cannot store report", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	file, err := os.Open(s.reportFile)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusOK, []model.Report{})
		return
	}
	if err != nil {
		http.Error(w, "cannot read reports", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	filterEvent := r.URL.Query().Get("event")
	reports := make([]model.Report, 0)
	scanner := bufio.NewScanner(io.LimitReader(file, 16<<20))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var report model.Report
		if json.Unmarshal(scanner.Bytes(), &report) == nil && (filterEvent == "" || report.EventID == filterEvent) {
			reports = append(reports, report)
		}
	}
	if err := scanner.Err(); err != nil {
		http.Error(w, "cannot parse reports", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, reports)
}

func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(token) || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
	}
}
