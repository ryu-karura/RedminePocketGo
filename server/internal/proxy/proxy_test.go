package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
)

// fakeLoader は KeyLoader のテスト実装。
type fakeLoader struct {
	key         *credential.APIKey
	loadErr     error
	markedValid string // MarkInvalid が呼ばれた userID
}

func (f *fakeLoader) LoadAPIKey(context.Context, string) (*credential.APIKey, error) {
	return f.key, f.loadErr
}
func (f *fakeLoader) MarkInvalid(_ context.Context, userID string) error {
	f.markedValid = userID
	return nil
}

// makeKey は credential.APIKey を生成する（value は非公開のため保管庫経由）。
func makeKey(t *testing.T, value string) *credential.APIKey {
	t.Helper()
	// credential パッケージ外から APIKey を作れないので、Vault 経由で作る。
	// ここでは value を検証したいだけなので、Save/Load を使わずリフレクション
	// を避け、実 Vault で往復させる代わりに簡易にラップする。
	return credential.NewTestAPIKey(value)
}

func authed(r *http.Request, userID string) *http.Request {
	return r.WithContext(httpapi.WithSession(r.Context(), &httpapi.SessionInfo{UserID: userID}))
}

// upstream は Redmine 役の httptest.Server。
func newUpstream(t *testing.T, status int, body string, capture func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			capture(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newProxy(t *testing.T, upstreamURL string, loader KeyLoader) http.HandlerFunc {
	return New(loader, Config{BaseURL: upstreamURL, SubURI: "/redmine", Timeout: 2 * time.Second}).Handler("/api/redmine")
}

func TestProxySuccessInjectsKeyAndJoinsSubURI(t *testing.T) {
	var gotPath, gotKey string
	up := newUpstream(t, 200, `{"issues":[]}`, func(r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Redmine-Api-Key")
	})
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "secret-key")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json?project_id=1", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if gotPath != "/redmine/issues.json" {
		t.Errorf("upstream path = %q; want /redmine/issues.json (sub-URI join)", gotPath)
	}
	if gotKey != "secret-key" {
		t.Errorf("upstream did not receive injected key: %q", gotKey)
	}
	if !strings.Contains(rec.Body.String(), "issues") {
		t.Errorf("body not relayed: %s", rec.Body)
	}
}

func TestProxyRejectsInboundAPIKey(t *testing.T) {
	up := newUpstream(t, 200, "{}", nil)
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	req.Header.Set("X-Redmine-API-Key", "attacker-supplied")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 400 || !strings.Contains(rec.Body.String(), httpapi.CodeInvalidRequest) {
		t.Errorf("status = %d body = %s; want 400 invalid_request", rec.Code, rec.Body)
	}
}

func TestProxyStripsForbiddenHeaders(t *testing.T) {
	var upstreamHeaders http.Header
	up := newUpstream(t, 200, "{}", func(r *http.Request) { upstreamHeaders = r.Header.Clone() })
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	req.Header.Set("Authorization", "Basic zzz")
	req.Header.Set("Cookie", "rmapp_session=abc")
	req.Header.Set("X-Redmine-Switch-User", "admin")
	rec := httptest.NewRecorder()
	h(rec, req)

	for _, banned := range []string{"Authorization", "Cookie", "X-Redmine-Switch-User"} {
		if upstreamHeaders.Get(banned) != "" {
			t.Errorf("forbidden header %s forwarded upstream: %q", banned, upstreamHeaders.Get(banned))
		}
	}
}

func TestProxyNotInAllowlistIs404(t *testing.T) {
	up := newUpstream(t, 200, "{}", func(*http.Request) { t.Error("upstream must not be called for disallowed path") })
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("DELETE", "/api/redmine/issues/1.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 404 {
		t.Errorf("status = %d; want 404", rec.Code)
	}
}

func TestProxyUpstream401MarksInvalidAnd409(t *testing.T) {
	up := newUpstream(t, 401, "unauthorized", nil)
	loader := &fakeLoader{key: makeKey(t, "k")}
	h := newProxy(t, up.URL, loader)

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 409 || !strings.Contains(rec.Body.String(), httpapi.CodeRedmineCredentialInvalid) {
		t.Errorf("status = %d body = %s; want 409 redmine_credential_invalid", rec.Code, rec.Body)
	}
	if loader.markedValid != "u1" {
		t.Errorf("credential not marked invalid; markedValid = %q", loader.markedValid)
	}
}

func TestProxyUpstream5xxIs502(t *testing.T) {
	up := newUpstream(t, 503, "boom", nil)
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), httpapi.CodeUpstreamError) {
		t.Errorf("status = %d body = %s; want 502 upstream_error", rec.Code, rec.Body)
	}
}

func TestProxyNoCredentialIs409(t *testing.T) {
	up := newUpstream(t, 200, "{}", nil)
	h := newProxy(t, up.URL, &fakeLoader{loadErr: credential.ErrNoCredential})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409", rec.Code)
	}
}

func TestProxyUnauthenticatedIs401(t *testing.T) {
	up := newUpstream(t, 200, "{}", nil)
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/api/redmine/issues.json", nil)) // no session
	if rec.Code != 401 {
		t.Errorf("status = %d; want 401", rec.Code)
	}
}

func TestProxyConnectionRefusedIs502(t *testing.T) {
	up := newUpstream(t, 200, "{}", nil)
	url := up.URL
	up.Close() // すぐ閉じて接続不能にする
	h := newProxy(t, url, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 502 {
		t.Errorf("status = %d; want 502 on connection failure", rec.Code)
	}
	_ = io.Discard
}
