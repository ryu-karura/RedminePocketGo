package proxy

import (
	"bytes"
	"compress/gzip"
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

func TestProxyPassesResponseHeadersAndStatus(t *testing.T) {
	// X-Total-Count（Redmine のページング）や ETag が欠落しないこと。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Total-Count", "123")
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(200)
		w.Write([]byte(`{"issues":[]}`))
	}))
	defer up.Close()
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Header().Get("X-Total-Count") != "123" {
		t.Errorf("X-Total-Count not relayed: %q", rec.Header().Get("X-Total-Count"))
	}
	if rec.Header().Get("ETag") != `"abc"` {
		t.Errorf("ETag not relayed: %q", rec.Header().Get("ETag"))
	}
}

func TestProxyGzipResponseDecodesCorrectly(t *testing.T) {
	// 上流が gzip を返しても、クライアントは正しい JSON を受け取れること。
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write([]byte(`{"ok":true}`))
		gz.Close()
	}))
	defer up.Close()
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h(rec, req)

	body := rec.Body.Bytes()
	// レスポンスが gzip のままなら JSON として読めない。ReverseProxy は
	// Content-Encoding を保って透過し、クライアント（ブラウザ）が復号できる。
	if ce := rec.Header().Get("Content-Encoding"); ce == "gzip" {
		// 透過されている場合、本文は gzip。ブラウザが解凍するので OK。
		gr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("passthrough body is not valid gzip: %v", err)
		}
		dec, _ := io.ReadAll(gr)
		if !strings.Contains(string(dec), `"ok":true`) {
			t.Errorf("decoded body wrong: %s", dec)
		}
	} else {
		// 解凍済みで透過された場合、本文はそのまま JSON
		if !strings.Contains(string(body), `"ok":true`) {
			t.Errorf("body wrong: %s", body)
		}
	}
}

func TestProxyDoesNotFollowRedirect(t *testing.T) {
	// 上流の 3xx を追従しない（API キーの外部再送を防ぐ）。
	var secondHit bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redmine/issues.json" {
			http.Redirect(w, r, "/redmine/other.json", http.StatusFound)
			return
		}
		secondHit = true
		w.WriteHeader(200)
	}))
	defer up.Close()
	h := newProxy(t, up.URL, &fakeLoader{key: makeKey(t, "k")})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)

	if secondHit {
		t.Error("relay followed the redirect; it must return the 3xx to the client instead")
	}
	if rec.Code != http.StatusFound {
		t.Errorf("status = %d; want 302 passed through", rec.Code)
	}
}
