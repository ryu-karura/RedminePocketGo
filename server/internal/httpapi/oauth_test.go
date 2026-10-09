package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeLoginErr struct{ code string }

func (e fakeLoginErr) Error() string     { return "login failed: " + e.code }
func (e fakeLoginErr) LoginCode() string { return e.code }

type fakeOAuthLogin struct {
	beginURL, beginState string
	beginErr             error
	gotReturn            string

	token, returnTo string
	completeErr     error
	completeCalls   int
	gotCode, gotSt  string
}

func (f *fakeOAuthLogin) Begin(_ context.Context, returnTo string) (string, string, error) {
	f.gotReturn = returnTo
	return f.beginURL, f.beginState, f.beginErr
}

func (f *fakeOAuthLogin) Complete(_ context.Context, code, state string) (string, string, error) {
	f.completeCalls++
	f.gotCode, f.gotSt = code, state
	return f.token, f.returnTo, f.completeErr
}

type oauthSessions struct{ revoked []string }

func (s *oauthSessions) Revoke(_ context.Context, token string) error {
	s.revoked = append(s.revoked, token)
	return nil
}
func (s *oauthSessions) Cookie(token string) *http.Cookie {
	return &http.Cookie{Name: "rmapp_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
}
func (s *oauthSessions) ClearCookie() *http.Cookie {
	return &http.Cookie{Name: "rmapp_session", Value: "", Path: "/", MaxAge: -1}
}

type oauthLimiter struct {
	allow            bool
	fails, successes int
}

func (l *oauthLimiter) Allow(string) bool { return l.allow }
func (l *oauthLimiter) Fail(string)       { l.fails++ }
func (l *oauthLimiter) Succeed(string)    { l.successes++ }

type oauthEnv struct {
	h    *OAuthHandler
	svc  *fakeOAuthLogin
	sess *oauthSessions
	lim  *oauthLimiter
	mux  *http.ServeMux
}

func newOAuthEnv() *oauthEnv {
	e := &oauthEnv{
		svc:  &fakeOAuthLogin{beginURL: "https://redmine.example/redmine/oauth/authorize?x=1", beginState: "STATE123", token: "SESSION-TOKEN", returnTo: "#issues/3"},
		sess: &oauthSessions{},
		lim:  &oauthLimiter{allow: true},
	}
	e.h = &OAuthHandler{
		Login: e.svc, Sessions: e.sess, Limiter: e.lim,
		SessionCookieName: "rmapp_session",
		StateCookieName:   "rmapp_oauth_state", StateCookiePath: "/api/auth/", StateCookieSecure: true, StateCookieTTL: 10 * time.Minute,
		AppURL: "/",
	}
	e.mux = http.NewServeMux()
	e.h.RegisterRoutes(e.mux)
	return e
}

func (e *oauthEnv) do(method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

func cookieNamed(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestOAuthLoginRedirectsToRedmineAndBindsState(t *testing.T) {
	e := newOAuthEnv()
	rr := e.do("GET", "/api/auth/login?return="+url.QueryEscape("#issues/9"))
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != e.svc.beginURL {
		t.Fatalf("status %d, Location %q; want 302 to the Redmine authorize URL", rr.Code, rr.Header().Get("Location"))
	}
	if e.svc.gotReturn != "#issues/9" {
		t.Errorf("return = %q", e.svc.gotReturn)
	}
	c := cookieNamed(rr, "rmapp_oauth_state")
	if c == nil {
		t.Fatal("state cookie not set (login CSRF binding)")
	}
	if c.Value != "STATE123" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/auth/" || c.MaxAge != 600 {
		t.Errorf("state cookie = %+v; want value STATE123, HttpOnly, Secure, Lax, Path=/api/auth/, MaxAge=600", c)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store", rr.Header().Get("Cache-Control"))
	}
}

func TestOAuthLoginFailures(t *testing.T) {
	t.Run("rate limited", func(t *testing.T) {
		e := newOAuthEnv()
		e.lim.allow = false
		rr := e.do("GET", "/api/auth/login")
		if got := rr.Header().Get("Location"); got != "/#login?error=rate_limited" {
			t.Errorf("Location = %q", got)
		}
		if cookieNamed(rr, "rmapp_oauth_state") != nil {
			t.Error("state cookie issued while rate limited")
		}
	})
	t.Run("begin fails", func(t *testing.T) {
		e := newOAuthEnv()
		e.svc.beginErr = errors.New("db down")
		rr := e.do("GET", "/api/auth/login")
		if got := rr.Header().Get("Location"); got != "/#login?error=server_error" {
			t.Errorf("Location = %q", got)
		}
		if strings.Contains(rr.Header().Get("Location"), "db") {
			t.Error("internal error text leaked into the redirect")
		}
	})
}

func stateCookie(v string) *http.Cookie { return &http.Cookie{Name: "rmapp_oauth_state", Value: v} }

func TestOAuthCallbackSuccess(t *testing.T) {
	e := newOAuthEnv()
	old := &http.Cookie{Name: "rmapp_session", Value: "OLD-SESSION"}
	rr := e.do("GET", "/api/auth/callback?code=abc&state=STATE123", stateCookie("STATE123"), old)

	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/#issues/3" {
		t.Fatalf("status %d, Location %q; want 302 to /#issues/3", rr.Code, rr.Header().Get("Location"))
	}
	if e.svc.gotCode != "abc" || e.svc.gotSt != "STATE123" {
		t.Errorf("Complete got code=%q state=%q", e.svc.gotCode, e.svc.gotSt)
	}
	sc := cookieNamed(rr, "rmapp_session")
	if sc == nil || sc.Value != "SESSION-TOKEN" {
		t.Fatalf("session cookie = %+v; want a new session", sc)
	}
	// セッション固定化対策: 以前のセッションは失効させる。
	if len(e.sess.revoked) != 1 || e.sess.revoked[0] != "OLD-SESSION" {
		t.Errorf("revoked = %v; want [OLD-SESSION]", e.sess.revoked)
	}
	// state Cookie は使い捨て。
	if c := cookieNamed(rr, "rmapp_oauth_state"); c == nil || c.MaxAge >= 0 {
		t.Errorf("state cookie not cleared: %+v", c)
	}
	if e.lim.successes != 1 || e.lim.fails != 0 {
		t.Errorf("limiter successes=%d fails=%d; want 1/0", e.lim.successes, e.lim.fails)
	}
}

func TestOAuthCallbackRejectsWithoutMatchingStateCookie(t *testing.T) {
	// ログイン CSRF: 攻撃者の code/state を被害者に踏ませても、被害者の
	// ブラウザには対応する state Cookie が無い。state を消費する前に弾く。
	for name, cookies := range map[string][]*http.Cookie{
		"no cookie":      nil,
		"other state":    {stateCookie("ATTACKER")},
		"empty cookie":   {stateCookie("")},
		"case-different": {stateCookie("state123")},
	} {
		t.Run(name, func(t *testing.T) {
			e := newOAuthEnv()
			rr := e.do("GET", "/api/auth/callback?code=abc&state=STATE123", cookies...)
			if got := rr.Header().Get("Location"); got != "/#login?error=invalid_state" {
				t.Errorf("Location = %q; want invalid_state", got)
			}
			if e.svc.completeCalls != 0 {
				t.Error("Complete was called without a matching state cookie (the state record would be burned)")
			}
			if cookieNamed(rr, "rmapp_session") != nil && cookieNamed(rr, "rmapp_session").Value != "" {
				t.Error("session issued")
			}
			if e.lim.fails != 1 {
				t.Errorf("limiter fails = %d; want 1", e.lim.fails)
			}
		})
	}
}

func TestOAuthCallbackFailureMapping(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		completeE error
		wantLoc   string
		wantFail  int
		wantCall  bool
	}{
		{"user denied consent", "error=access_denied&state=STATE123", nil, "/#login?error=access_denied", 0, false},
		{"redmine reports server_error", "error=server_error&state=STATE123", nil, "/#login?error=redmine_unavailable", 0, false},
		{"unknown error param is not reflected", "error=%3Cscript%3E&state=STATE123", nil, "/#login?error=exchange_failed", 0, false},
		{"missing code", "state=STATE123", nil, "/#login?error=invalid_state", 1, false},
		{"missing state", "code=abc", nil, "/#login?error=invalid_state", 1, false},
		{"invalid state", "code=abc&state=STATE123", fakeLoginErr{"invalid_state"}, "/#login?error=invalid_state", 1, true},
		{"exchange failed", "code=abc&state=STATE123", fakeLoginErr{"exchange_failed"}, "/#login?error=exchange_failed", 1, true},
		{"redmine down is not a brute-force signal", "code=abc&state=STATE123", fakeLoginErr{"redmine_unavailable"}, "/#login?error=redmine_unavailable", 0, true},
		{"misconfigured", "code=abc&state=STATE123", fakeLoginErr{"server_misconfigured"}, "/#login?error=server_misconfigured", 0, true},
		{"unexpected error", "code=abc&state=STATE123", errors.New("boom"), "/#login?error=server_error", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newOAuthEnv()
			e.svc.completeErr = tt.completeE
			rr := e.do("GET", "/api/auth/callback?"+tt.query, stateCookie("STATE123"))
			if got := rr.Header().Get("Location"); got != tt.wantLoc {
				t.Errorf("Location = %q; want %q", got, tt.wantLoc)
			}
			if (e.svc.completeCalls > 0) != tt.wantCall {
				t.Errorf("Complete calls = %d; want called=%v", e.svc.completeCalls, tt.wantCall)
			}
			if e.lim.fails != tt.wantFail {
				t.Errorf("limiter fails = %d; want %d", e.lim.fails, tt.wantFail)
			}
			if sc := cookieNamed(rr, "rmapp_session"); sc != nil && sc.Value != "" {
				t.Error("session cookie issued on failure")
			}
			if c := cookieNamed(rr, "rmapp_oauth_state"); c == nil || c.MaxAge >= 0 {
				t.Errorf("state cookie must be cleared on every outcome: %+v", c)
			}
		})
	}
}

func TestOAuthCallbackRateLimited(t *testing.T) {
	e := newOAuthEnv()
	e.lim.allow = false
	rr := e.do("GET", "/api/auth/callback?code=abc&state=STATE123", stateCookie("STATE123"))
	if got := rr.Header().Get("Location"); got != "/#login?error=rate_limited" {
		t.Errorf("Location = %q", got)
	}
	if e.svc.completeCalls != 0 {
		t.Error("Complete called while rate limited")
	}
}

func TestOAuthRedirectsRespectAppURL(t *testing.T) {
	e := newOAuthEnv()
	e.h.AppURL = "/app/"
	rr := e.do("GET", "/api/auth/callback?code=abc&state=STATE123", stateCookie("STATE123"))
	if got := rr.Header().Get("Location"); got != "/app/#issues/3" {
		t.Errorf("Location = %q; want /app/#issues/3", got)
	}
}

func TestOAuthRoutesAreGETOnly(t *testing.T) {
	e := newOAuthEnv()
	for _, p := range []string{"/api/auth/login", "/api/auth/callback"} {
		if rr := e.do("POST", p); rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d; want 405", p, rr.Code)
		}
	}
}

func TestOAuthCallbackNeverRedirectsToAnOffSiteReturnTo(t *testing.T) {
	for _, bad := range []string{"https://evil.example/", "//evil.example", "/x"} {
		e := newOAuthEnv()
		e.svc.returnTo = bad
		rr := e.do("GET", "/api/auth/callback?code=abc&state=STATE123", stateCookie("STATE123"))
		if got := rr.Header().Get("Location"); got != "/#projects" {
			t.Errorf("returnTo %q: Location = %q; want /#projects", bad, got)
		}
	}
}
