package webadmin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerguy/lan_installer/internal/auth"
	"github.com/containerguy/lan_installer/internal/secretbox"
	"github.com/containerguy/lan_installer/internal/store"
)

func TestLoginDashboardAndCSRF(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, err := auth.HashPassword("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BootstrapAdmin(context.Background(), "admin", h); err != nil {
		t.Fatal(err)
	}
	vault, err := secretbox.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	a := New(s, false, vault)
	loginPage := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	loginPageRes := httptest.NewRecorder()
	a.ServeHTTP(loginPageRes, loginPage)
	loginCookies := loginPageRes.Result().Cookies()
	if len(loginCookies) != 1 {
		t.Fatal("login csrf cookie missing")
	}
	body := loginPageRes.Body.String()
	marker := `name="csrf_token" value="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatal("login csrf field missing")
	}
	start += len(marker)
	end := strings.Index(body[start:], `"`)
	csrf := body[start : start+end]
	form := url.Values{"username": {"admin"}, "password": {"a sufficiently long password"}, "csrf_token": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(loginCookies[0])
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("login: %d %s", res.Code, res.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.MaxAge > 0 {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("session cookie missing")
	}
	dash := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	dash.AddCookie(session)
	dashRes := httptest.NewRecorder()
	a.ServeHTTP(dashRes, dash)
	if dashRes.Code != http.StatusOK || !strings.Contains(dashRes.Body.String(), "Management") {
		t.Fatalf("dashboard: %d %s", dashRes.Code, dashRes.Body.String())
	}
	logout := httptest.NewRequest(http.MethodPost, "/admin/logout", nil)
	logout.AddCookie(session)
	logoutRes := httptest.NewRecorder()
	a.ServeHTTP(logoutRes, logout)
	if logoutRes.Code != http.StatusForbidden {
		t.Fatalf("missing csrf accepted: %d", logoutRes.Code)
	}
}
