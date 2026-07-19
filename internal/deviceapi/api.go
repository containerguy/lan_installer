package deviceapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/store"
)

const maxBody = 1 << 20

var releaseEventIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type API struct {
	store          *store.Store
	publicURL      string
	mux            *http.ServeMux
	now            func() time.Time
	trustedProxies []*net.IPNet
	artifacts      *artifact.Store
}

func New(st *store.Store, publicURL string, trustedProxyCIDRs ...string) (*API, error) {
	return newAPI(st, publicURL, nil, trustedProxyCIDRs...)
}

func NewWithArtifactStore(st *store.Store, publicURL string, artifacts *artifact.Store, trustedProxyCIDRs ...string) (*API, error) {
	if artifacts == nil {
		return nil, errors.New("artifact store is required")
	}
	return newAPI(st, publicURL, artifacts, trustedProxyCIDRs...)
}

func newAPI(st *store.Store, publicURL string, artifacts *artifact.Store, trustedProxyCIDRs ...string) (*API, error) {
	parsed, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("public URL must be a credential-free HTTPS origin")
	}
	a := &API{store: st, publicURL: strings.TrimRight(publicURL, "/"), mux: http.NewServeMux(), now: func() time.Time { return time.Now().UTC() }, artifacts: artifacts}
	for _, list := range trustedProxyCIDRs {
		for _, value := range strings.Split(list, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			_, network, parseErr := net.ParseCIDR(value)
			if parseErr != nil {
				return nil, errors.New("trusted proxy CIDR is invalid")
			}
			a.trustedProxies = append(a.trustedProxies, network)
		}
	}
	a.mux.HandleFunc("POST /v2/devices/enroll", a.enrollDevice)
	a.mux.HandleFunc("GET /v2/device/bootstrap", a.bootstrap)
	a.mux.HandleFunc("POST /v2/user-device-authorizations", a.createAuthorization)
	a.mux.HandleFunc("GET /v2/user-device-authorizations/{id}", a.pollAuthorization)
	a.mux.HandleFunc("POST /v2/device/inventory-scans", a.saveInventory)
	a.mux.HandleFunc("GET /v2/events/{eventId}/release", a.eventRelease)
	a.mux.HandleFunc("GET /v2/client/releases/latest", a.clientUpdateRelease)
	if a.artifacts != nil {
		a.mux.HandleFunc("HEAD /v2/artifacts/sha256/{digest}", a.downloadArtifact)
		a.mux.HandleFunc("GET /v2/artifacts/sha256/{digest}", a.downloadArtifact)
		a.mux.HandleFunc("HEAD /v2/client/artifacts/sha256/{digest}", a.downloadArtifact)
		a.mux.HandleFunc("GET /v2/client/artifacts/sha256/{digest}", a.downloadArtifact)
	}
	return a, nil
}

type enrollRequest struct {
	Code           string `json:"code"`
	PublicKey      string `json:"publicKey"`
	DeviceName     string `json:"deviceName"`
	WindowsVersion string `json:"windowsVersion"`
	ClientVersion  string `json:"clientVersion"`
}

func (a *API) enrollDevice(w http.ResponseWriter, r *http.Request) {
	if !isJSON(r.Header.Get("Content-Type")) {
		a.error(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type muss application/json sein.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		a.error(w, http.StatusRequestEntityTooLarge, "request_too_large", "Anfrage überschreitet das erlaubte Limit.")
		return
	}
	var request enrollRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		a.error(w, http.StatusBadRequest, "invalid_json", "Enrollment-Daten sind ungültig.")
		return
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(request.PublicKey)
	if err != nil || base64.RawURLEncoding.EncodeToString(publicKey) != request.PublicKey || len(publicKey) != ed25519.PublicKeySize {
		a.error(w, http.StatusUnprocessableEntity, "public_key_invalid", "Öffentlicher Geräteschlüssel ist ungültig.")
		return
	}
	allowed, err := a.store.AllowEnrollmentAttempt(r.Context(), a.clientIP(r), request.Code, a.now())
	if err != nil {
		a.error(w, http.StatusInternalServerError, "enrollment_rate_limit_failed", "Enrollment konnte nicht geprüft werden.")
		return
	}
	if !allowed {
		w.Header().Set("Retry-After", "300")
		a.error(w, http.StatusTooManyRequests, "enrollment_rate_limited", "Zu viele Enrollment-Versuche. Bitte später erneut versuchen.")
		return
	}
	device, err := a.store.EnrollDevice(r.Context(), request.Code, publicKey, request.DeviceName, request.WindowsVersion, request.ClientVersion)
	if errors.Is(err, store.ErrEnrollmentCodeInvalid) {
		a.error(w, http.StatusConflict, "enrollment_code_invalid", "Enrollment-Code ist ungültig, abgelaufen oder bereits verwendet.")
		return
	}
	if err != nil {
		a.error(w, http.StatusUnprocessableEntity, "enrollment_invalid", "Gerät konnte nicht registriert werden.")
		return
	}
	a.writeJSON(w, http.StatusCreated, map[string]any{"deviceId": device.ID, "serverTime": a.now().Format(time.RFC3339), "apiVersion": 2, "capabilities": []string{"range-download", "status-v1", "client-update-v1", "inventory-v1"}, "bootstrapUrl": "/v2/device/bootstrap"})
}

func (a *API) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(strings.TrimSpace(host))
	if remote == nil {
		return "unknown"
	}
	current := remote
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for index := len(parts) - 1; index >= 0 && a.isTrustedProxy(current); index-- {
		candidate := net.ParseIP(strings.TrimSpace(parts[index]))
		if candidate == nil {
			break
		}
		current = candidate
	}
	return current.String()
}

func (a *API) isTrustedProxy(ip net.IP) bool {
	for _, network := range a.trustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (a *API) bootstrap(w http.ResponseWriter, r *http.Request) {
	deviceID, _, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	device, err := a.store.ActiveDevice(r.Context(), deviceID)
	if err != nil {
		a.error(w, http.StatusInternalServerError, "device_read_failed", "Gerätestatus konnte nicht gelesen werden.")
		return
	}
	reportedVersion := device.ClientVersion
	query := r.URL.Query()
	versions, present := query["clientVersion"]
	if !present || len(query) != 1 || len(versions) != 1 {
		a.error(w, http.StatusUnprocessableEntity, "client_version_invalid", "Bootstrap erfordert ausschließlich eine eindeutige clientVersion.")
		return
	}
	reportedVersion = strings.TrimSpace(versions[0])
	if _, compareErr := protocol.CompareSemanticVersions(reportedVersion, "0.0.0"); compareErr != nil {
		a.error(w, http.StatusUnprocessableEntity, "client_version_invalid", "Die gemeldete Clientversion ist kein gültiger semantischer Versionsstand.")
		return
	}
	if err = a.store.UpdateActiveDeviceClientVersion(r.Context(), deviceID, reportedVersion); errors.Is(err, store.ErrDeviceVersionRollback) {
		// Keep the stored anti-rollback watermark, but evaluate policy against
		// the actually running binary so a downgraded client is still blocked.
	} else if err != nil {
		a.error(w, http.StatusInternalServerError, "client_version_update_failed", "Die laufende Clientversion konnte nicht bestätigt werden.")
		return
	}
	update, updateErr := a.store.LatestClientUpdateRelease(r.Context(), "stable")
	updateAvailable := updateErr == nil
	if updateErr != nil && !errors.Is(updateErr, sql.ErrNoRows) {
		a.error(w, http.StatusInternalServerError, "client_update_read_failed", "Clientupdate konnte nicht gelesen werden.")
		return
	}
	updateRequired := false
	clientVersionValid := true
	if updateAvailable {
		comparison, compareErr := protocol.CompareSemanticVersions(reportedVersion, update.MinimumVersion)
		clientVersionValid = compareErr == nil
		updateRequired = !clientVersionValid || comparison < 0
	}
	var activeEvent any
	active, activeErr := a.store.ActiveEventRelease(r.Context())
	if activeErr == nil {
		now := a.now()
		if !now.Before(active.IssuedAt) && now.Before(active.ValidUntil) {
			comparison, compareErr := protocol.CompareSemanticVersions(reportedVersion, active.MinimumVersion)
			eventUpdateRequired := compareErr != nil || comparison < 0
			updateRequired = updateRequired || eventUpdateRequired
			if eventUpdateRequired && (!updateAvailable || semanticVersionLess(update.Version, active.MinimumVersion)) {
				a.error(w, http.StatusServiceUnavailable, "compatible_client_update_unavailable", "Für dieses Event ist noch kein kompatibles signiertes Clientupdate verfügbar.")
				return
			}
			activeEvent = map[string]string{"eventId": active.EventID, "releaseUrl": "/v2/events/" + active.EventID + "/release"}
		}
	} else if !errors.Is(activeErr, sql.ErrNoRows) {
		a.error(w, http.StatusInternalServerError, "active_event_read_failed", "Aktives Event konnte nicht gelesen werden.")
		return
	}
	clientUpdate := map[string]any{"required": updateRequired, "releaseUrl": "/v2/client/releases/latest?channel=stable"}
	if updateRequired {
		if !updateAvailable {
			a.error(w, http.StatusServiceUnavailable, "client_update_unavailable", "Ein erforderliches signiertes Clientupdate ist noch nicht verfügbar.")
			return
		}
		a.writeJSON(w, http.StatusUpgradeRequired, map[string]any{"code": "client_update_required", "message": "Ein signiertes LANReady-Clientupdate ist erforderlich.", "requestId": w.Header().Get("X-Request-ID"), "clientUpdate": clientUpdate})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"serverTime": a.now().Format(time.RFC3339), "apiVersion": 2, "capabilities": []string{"range-download", "status-v1", "client-update-v1", "inventory-v1"}, "activeEvent": activeEvent, "clientUpdate": clientUpdate})
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Request-ID", requestID())
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("LANReady-API-Version", "2")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	a.mux.ServeHTTP(w, r)
}

func (a *API) authenticate(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	if r.Method == http.MethodPost && !isJSON(r.Header.Get("Content-Type")) {
		a.error(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type muss application/json sein.")
		return "", nil, false
	}
	deviceID := strings.TrimSpace(r.Header.Get("LANReady-Device-ID"))
	timestamp, err := strconv.ParseInt(r.Header.Get("LANReady-Timestamp"), 10, 64)
	if err != nil || deviceID == "" {
		a.error(w, http.StatusUnauthorized, "device_auth_invalid", "Geräteauthentisierung ist ungültig.")
		return "", nil, false
	}
	now := a.now()
	requestTime := time.Unix(timestamp, 0).UTC()
	if requestTime.Before(now.Add(-5*time.Minute)) || requestTime.After(now.Add(5*time.Minute)) {
		a.error(w, http.StatusUnauthorized, "device_time_invalid", "Gerätezeit liegt außerhalb des erlaubten Fensters.")
		return "", nil, false
	}
	nonceText := r.Header.Get("LANReady-Nonce")
	nonce, err := base64.RawURLEncoding.DecodeString(nonceText)
	if err != nil || base64.RawURLEncoding.EncodeToString(nonce) != nonceText || len(nonce) < 12 || len(nonce) > 32 {
		a.error(w, http.StatusUnauthorized, "device_nonce_invalid", "Geräte-Nonce ist ungültig.")
		return "", nil, false
	}
	signatureText := r.Header.Get("LANReady-Signature")
	signature, err := base64.RawURLEncoding.DecodeString(signatureText)
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != signatureText || len(signature) != ed25519.SignatureSize {
		a.error(w, http.StatusUnauthorized, "device_signature_invalid", "Gerätesignatur ist ungültig.")
		return "", nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		a.error(w, http.StatusRequestEntityTooLarge, "request_too_large", "Anfrage überschreitet das erlaubte Limit.")
		return "", nil, false
	}
	publicKey, err := a.store.ActiveDevicePublicKey(r.Context(), deviceID)
	if err != nil {
		a.error(w, http.StatusUnauthorized, "device_unknown", "Gerät ist nicht registriert oder gesperrt.")
		return "", nil, false
	}
	canonical, err := protocol.CanonicalRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, body, timestamp, nonce)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), canonical, signature) {
		a.error(w, http.StatusUnauthorized, "device_signature_invalid", "Gerätesignatur ist ungültig.")
		return "", nil, false
	}
	if err = a.store.ConsumeDeviceNonce(r.Context(), deviceID, nonce, requestTime.Add(5*time.Minute)); err != nil {
		if errors.Is(err, store.ErrNonceReplay) {
			a.error(w, http.StatusConflict, "device_replay", "Diese Geräteanfrage wurde bereits verarbeitet.")
		} else {
			a.error(w, http.StatusInternalServerError, "device_nonce_failed", "Geräteanfrage konnte nicht abgesichert werden.")
		}
		return "", nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return deviceID, body, true
}

func (a *API) createAuthorization(w http.ResponseWriter, r *http.Request) {
	deviceID, _, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	if !a.requireCompatibleClient(w, r, deviceID) {
		return
	}
	value, err := a.store.CreateDeviceAuthorization(r.Context(), deviceID, a.publicURL, 10*time.Minute)
	if err != nil {
		a.error(w, http.StatusInternalServerError, "authorization_create_failed", "Anmeldung konnte nicht gestartet werden.")
		return
	}
	a.writeJSON(w, http.StatusCreated, map[string]any{"authorizationId": value.AuthorizationID, "userCode": value.UserCode, "verificationUrl": value.VerificationURL, "expiresAt": value.ExpiresAt.Format(time.RFC3339), "pollIntervalSeconds": int(value.PollInterval.Seconds())})
}

func (a *API) pollAuthorization(w http.ResponseWriter, r *http.Request) {
	deviceID, _, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	if !a.requireCompatibleClient(w, r, deviceID) {
		return
	}
	token, err := a.store.PollDeviceAuthorization(r.Context(), deviceID, r.PathValue("id"), 15*time.Minute)
	switch {
	case errors.Is(err, store.ErrAuthorizationPending):
		a.error(w, http.StatusAccepted, "authorization_pending", "Anmeldung wartet auf Bestätigung.")
	case errors.Is(err, store.ErrAuthorizationSlowDown):
		w.Header().Set("Retry-After", "5")
		a.error(w, http.StatusTooManyRequests, "slow_down", "Bitte halte das vorgegebene Abfrageintervall ein.")
	case errors.Is(err, store.ErrAuthorizationDenied):
		a.error(w, http.StatusForbidden, "authorization_denied", "Anmeldung wurde abgelehnt.")
	case errors.Is(err, store.ErrAuthorizationExpired), errors.Is(err, store.ErrAuthorizationUsed):
		a.error(w, http.StatusGone, "authorization_expired", "Anmeldung ist abgelaufen oder wurde bereits abgeschlossen.")
	case err != nil:
		a.error(w, http.StatusInternalServerError, "authorization_poll_failed", "Anmeldestatus konnte nicht gelesen werden.")
	default:
		a.writeJSON(w, http.StatusOK, map[string]any{"accessToken": token, "tokenType": "Bearer", "expiresInSeconds": 900})
	}
}

type inventoryRequest struct {
	ScanID        string                        `json:"scanId"`
	ScannedAt     time.Time                     `json:"scannedAt"`
	ClientVersion string                        `json:"clientVersion"`
	Installations []store.InventoryInstallation `json:"installations"`
}

func (a *API) saveInventory(w http.ResponseWriter, r *http.Request) {
	deviceID, body, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	if !a.requireCompatibleClient(w, r, deviceID) {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		a.error(w, http.StatusUnauthorized, "user_authorization_required", "Persönliche Anmeldung ist erforderlich.")
		return
	}
	userToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if strings.TrimSpace(userToken) == "" {
		a.error(w, http.StatusUnauthorized, "user_authorization_required", "Persönliche Anmeldung ist erforderlich.")
		return
	}
	var request inventoryRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		a.error(w, http.StatusBadRequest, "invalid_json", "Inventardaten sind ungültig.")
		return
	}
	err := a.store.SaveDeviceInventory(r.Context(), deviceID, userToken, store.InventoryScan{ID: request.ScanID, ScannedAt: request.ScannedAt, ClientVersion: request.ClientVersion, Installations: request.Installations})
	if errors.Is(err, store.ErrUserTokenInvalid) {
		a.error(w, http.StatusUnauthorized, "user_authorization_invalid", "Persönliche Anmeldung ist abgelaufen oder passt nicht zu diesem Gerät.")
		return
	}
	if errors.Is(err, store.ErrInventoryConflict) {
		a.error(w, http.StatusConflict, "inventory_scan_conflict", "Diese Scan-ID wurde bereits mit anderem Inhalt verwendet.")
		return
	}
	if err != nil {
		a.error(w, http.StatusUnprocessableEntity, "inventory_invalid", "Inventar konnte nicht gespeichert werden.")
		return
	}
	a.writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (a *API) requireCompatibleClient(w http.ResponseWriter, r *http.Request, deviceID string) bool {
	device, err := a.store.ActiveDevice(r.Context(), deviceID)
	if err != nil {
		a.error(w, http.StatusInternalServerError, "device_read_failed", "Gerätestatus konnte nicht gelesen werden.")
		return false
	}
	update, updateErr := a.store.LatestClientUpdateRelease(r.Context(), "stable")
	if updateErr != nil && !errors.Is(updateErr, sql.ErrNoRows) {
		a.error(w, http.StatusInternalServerError, "client_update_read_failed", "Clientupdate konnte nicht gelesen werden.")
		return false
	}
	updateAvailable := updateErr == nil
	required := false
	if updateAvailable {
		comparison, compareErr := protocol.CompareSemanticVersions(device.ClientVersion, update.MinimumVersion)
		required = compareErr != nil || comparison < 0
	}
	active, activeErr := a.store.ActiveEventRelease(r.Context())
	if activeErr == nil && !a.now().Before(active.IssuedAt) && a.now().Before(active.ValidUntil) {
		comparison, compareErr := protocol.CompareSemanticVersions(device.ClientVersion, active.MinimumVersion)
		if compareErr != nil || comparison < 0 {
			required = true
			if !updateAvailable || semanticVersionLess(update.Version, active.MinimumVersion) {
				a.error(w, http.StatusServiceUnavailable, "compatible_client_update_unavailable", "Für dieses Event ist noch kein kompatibles signiertes Clientupdate verfügbar.")
				return false
			}
		}
	} else if activeErr != nil && !errors.Is(activeErr, sql.ErrNoRows) {
		a.error(w, http.StatusInternalServerError, "active_event_read_failed", "Aktives Event konnte nicht gelesen werden.")
		return false
	}
	if required {
		if !updateAvailable {
			a.error(w, http.StatusServiceUnavailable, "client_update_unavailable", "Ein erforderliches signiertes Clientupdate ist noch nicht verfügbar.")
			return false
		}
		a.writeJSON(w, http.StatusUpgradeRequired, map[string]any{"code": "client_update_required", "message": "Ein signiertes LANReady-Clientupdate ist erforderlich.", "requestId": w.Header().Get("X-Request-ID"), "clientUpdate": map[string]any{"required": true, "releaseUrl": "/v2/client/releases/latest?channel=stable"}})
		return false
	}
	return true
}

func (a *API) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authenticate(w, r); !ok {
		return
	}
	blob, err := a.artifacts.OpenVerified(r.Context(), r.PathValue("digest"))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, sql.ErrNoRows) {
		a.error(w, http.StatusNotFound, "artifact_not_found", "Artefakt wurde nicht gefunden.")
		return
	}
	if err != nil {
		a.error(w, http.StatusInternalServerError, "artifact_unavailable", "Artefakt ist nicht verfügbar.")
		return
	}
	defer blob.File.Close()
	etag := `"sha256:` + blob.Digest + `"`
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", blob.ContentType)
	w.Header().Set("ETag", etag)
	start, end, partial, rangeErr := requestedRange(r.Header.Get("Range"), r.Header.Get("If-Range"), etag, blob.SizeBytes)
	if rangeErr != nil {
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(blob.SizeBytes, 10))
		a.error(w, http.StatusRequestedRangeNotSatisfiable, "range_invalid", "Downloadbereich ist ungültig.")
		return
	}
	length := blob.SizeBytes
	status := http.StatusOK
	if partial {
		length = end - start + 1
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, blob.SizeBytes))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(status)
	if r.Method == http.MethodHead || length == 0 {
		return
	}
	if _, err = blob.File.Seek(start, io.SeekStart); err != nil {
		return
	}
	_, _ = io.CopyN(w, blob.File, length)
}

func (a *API) eventRelease(w http.ResponseWriter, r *http.Request) {
	deviceID, _, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	eventID := r.PathValue("eventId")
	if !releaseEventIDPattern.MatchString(eventID) {
		a.error(w, http.StatusNotFound, "event_release_not_found", "Event-Release wurde nicht gefunden.")
		return
	}
	release, err := a.store.ActiveEventRelease(r.Context())
	if errors.Is(err, sql.ErrNoRows) || (err == nil && release.EventID != eventID) {
		a.error(w, http.StatusNotFound, "event_release_not_found", "Event-Release wurde nicht gefunden.")
		return
	}
	if err != nil {
		a.error(w, http.StatusInternalServerError, "event_release_read_failed", "Event-Release konnte nicht gelesen werden.")
		return
	}
	now := a.now()
	if now.Before(release.IssuedAt) || !now.Before(release.ValidUntil) {
		a.error(w, http.StatusGone, "event_release_expired", "Das aktive Event-Release ist nicht mehr gültig.")
		return
	}
	device, err := a.store.ActiveDevice(r.Context(), deviceID)
	if err != nil {
		a.error(w, http.StatusInternalServerError, "device_read_failed", "Gerätestatus konnte nicht gelesen werden.")
		return
	}
	comparison, compareErr := protocol.CompareSemanticVersions(device.ClientVersion, release.MinimumVersion)
	if compareErr != nil || comparison < 0 {
		update, updateErr := a.store.LatestClientUpdateRelease(r.Context(), "stable")
		if updateErr != nil || semanticVersionLess(update.Version, release.MinimumVersion) {
			a.error(w, http.StatusServiceUnavailable, "compatible_client_update_unavailable", "Für dieses Event ist noch kein kompatibles signiertes Clientupdate verfügbar.")
			return
		}
		a.writeJSON(w, http.StatusUpgradeRequired, map[string]any{"code": "client_update_required", "message": "Ein signiertes LANReady-Clientupdate ist erforderlich.", "requestId": w.Header().Get("X-Request-ID"), "clientUpdate": map[string]any{"required": true, "releaseUrl": "/v2/client/releases/latest?channel=stable"}})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, release.Sequence))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(release.EnvelopeJSON)
}

func semanticVersionLess(left, right string) bool {
	comparison, err := protocol.CompareSemanticVersions(left, right)
	return err != nil || comparison < 0
}

func (a *API) clientUpdateRelease(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.authenticate(w, r); !ok {
		return
	}
	channels, exists := r.URL.Query()["channel"]
	if !exists || len(r.URL.Query()) != 1 || len(channels) != 1 || channels[0] != "stable" {
		a.error(w, http.StatusBadRequest, "update_channel_invalid", "Updatekanal ist ungültig.")
		return
	}
	release, err := a.store.LatestClientUpdateRelease(r.Context(), "stable")
	if errors.Is(err, sql.ErrNoRows) {
		a.error(w, http.StatusNotFound, "client_update_not_found", "Clientupdate wurde nicht gefunden.")
		return
	}
	if err != nil {
		a.error(w, http.StatusInternalServerError, "client_update_read_failed", "Clientupdate konnte nicht gelesen werden.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, release.Sequence))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(release.EnvelopeJSON)
}

func requestedRange(header, ifRange, etag string, size int64) (int64, int64, bool, error) {
	if strings.TrimSpace(header) == "" || (ifRange != "" && ifRange != etag) {
		return 0, size - 1, false, nil
	}
	if size == 0 || !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false, errors.New("range is invalid")
	}
	parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, false, errors.New("range is invalid")
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, errors.New("range is invalid")
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return 0, 0, false, errors.New("range is invalid")
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true, nil
}

func (a *API) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (a *API) error(w http.ResponseWriter, status int, code, message string) {
	a.writeJSON(w, status, map[string]any{"code": code, "message": message, "fieldErrors": map[string]any{}, "requestId": w.Header().Get("X-Request-ID")})
}

func requestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "request-id-unavailable"
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func isJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}
