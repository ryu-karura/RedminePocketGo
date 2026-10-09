package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

// fakeTokens は TokenSource のテスト実装。
type fakeTokens struct {
	access     string // AccessToken が返す値
	accessErr  error
	refreshTo  string // ForceRefresh が返す新しいアクセストークン
	refreshErr error
	refreshes  []string // ForceRefresh に渡された「失効した」トークン
	markedFor  string   // MarkInvalid が呼ばれた userID
}

func (f *fakeTokens) AccessToken(context.Context, string) (string, error) {
	return f.access, f.accessErr
}
func (f *fakeTokens) ForceRefresh(_ context.Context, _, stale string) (string, error) {
	f.refreshes = append(f.refreshes, stale)
	return f.refreshTo, f.refreshErr
}
func (f *fakeTokens) MarkInvalid(_ context.Context, userID string) error {
	f.markedFor = userID
	return nil
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

func newProxy(t *testing.T, upstreamURL string, tokens TokenSource) http.HandlerFunc {
	return New(tokens, Config{BaseURL: upstreamURL, SubURI: "/redmine", Timeout: 2 * time.Second}).Handler("/api/redmine")
}

func TestProxySuccessInjectsBearerAndJoinsSubURI(t *testing.T) {
	var gotPath, gotAuth, gotAPIKey string
	up := newUpstream(t, 200, `{"issues":[]}`, func(r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Redmine-Api-Key")
	})
	h := newProxy(t, up.URL, &fakeTokens{access: "access-1"})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json?project_id=1", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if gotPath != "/redmine/issues.json" {
		t.Errorf("upstream path = %q; want /redmine/issues.json (sub-URI join)", gotPath)
	}
	if gotAuth != "Bearer access-1" {
		t.Errorf("upstream Authorization = %q; want the user's Bearer token", gotAuth)
	}
	if gotAPIKey != "" {
		t.Errorf("upstream received X-Redmine-Api-Key %q; API keys are never sent", gotAPIKey)
	}
	if !strings.Contains(rec.Body.String(), "issues") {
		t.Errorf("body not relayed: %s", rec.Body)
	}
}

func TestProxyRejectsInboundAPIKey(t *testing.T) {
	up := newUpstream(t, 200, "{}", nil)
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

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
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	req.Header.Set("Authorization", "Basic zzz")
	req.Header.Set("Cookie", "rmapp_session=abc")
	req.Header.Set("X-Redmine-Switch-User", "admin")
	rec := httptest.NewRecorder()
	h(rec, req)

	for _, banned := range []string{"Cookie", "X-Redmine-Switch-User"} {
		if upstreamHeaders.Get(banned) != "" {
			t.Errorf("forbidden header %s forwarded upstream: %q", banned, upstreamHeaders.Get(banned))
		}
	}
	// クライアントが送った Authorization は転送せず、サーバーが付けた Bearer に置き換える。
	if got := upstreamHeaders.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("upstream Authorization = %q; want the server's Bearer, not the client's", got)
	}
}

func TestProxyNotInAllowlistIs404(t *testing.T) {
	up := newUpstream(t, 200, "{}", func(*http.Request) { t.Error("upstream must not be called for disallowed path") })
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

	req := authed(httptest.NewRequest("DELETE", "/api/redmine/issues/1.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 404 {
		t.Errorf("status = %d; want 404", rec.Code)
	}
}

// sequencedUpstream は 1 回目と 2 回目で応答を変える上流。受け取った
// Authorization と本文を記録する。
func sequencedUpstream(t *testing.T, statuses ...int) (*httptest.Server, *[]string, *[]string) {
	t.Helper()
	var auths, bodies []string
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		auths = append(auths, r.Header.Get("Authorization"))
		bodies = append(bodies, string(b))
		st := statuses[len(statuses)-1]
		if n < len(statuses) {
			st = statuses[n]
		}
		n++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &auths, &bodies
}

func TestProxyUpstream401RefreshesAndRetriesOnce(t *testing.T) {
	up, auths, bodies := sequencedUpstream(t, 401, 200)
	tokens := &fakeTokens{access: "old", refreshTo: "new"}
	h := newProxy(t, up.URL, tokens)

	req := authed(httptest.NewRequest("PUT", "/api/redmine/issues/1.json", strings.NewReader(`{"issue":{"notes":"hi"}}`)), "u1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("status = %d body = %s; want the retried 200", rec.Code, rec.Body)
	}
	if len(*auths) != 2 || (*auths)[0] != "Bearer old" || (*auths)[1] != "Bearer new" {
		t.Errorf("upstream saw %v; want [Bearer old, Bearer new]", *auths)
	}
	// 書き込みの本文は再試行でも同一に再送される。
	if len(*bodies) != 2 || (*bodies)[0] != `{"issue":{"notes":"hi"}}` || (*bodies)[1] != (*bodies)[0] {
		t.Errorf("bodies = %q; want the same body twice", *bodies)
	}
	if len(tokens.refreshes) != 1 || tokens.refreshes[0] != "old" {
		t.Errorf("ForceRefresh calls = %v; want one with the stale token", tokens.refreshes)
	}
	if tokens.markedFor != "" {
		t.Error("credential marked invalid although the retry succeeded")
	}
}

func TestProxyUpstream401TwiceMarksInvalidAnd409(t *testing.T) {
	up, auths, _ := sequencedUpstream(t, 401, 401)
	tokens := &fakeTokens{access: "old", refreshTo: "new"}
	h := newProxy(t, up.URL, tokens)

	rec := httptest.NewRecorder()
	h(rec, authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1"))

	if rec.Code != 409 || !strings.Contains(rec.Body.String(), httpapi.CodeRedmineCredentialInvalid) {
		t.Errorf("status = %d body = %s; want 409 redmine_credential_invalid", rec.Code, rec.Body)
	}
	if len(*auths) != 2 {
		t.Errorf("upstream hits = %d; want exactly 2 (one retry, no loop)", len(*auths))
	}
	if tokens.markedFor != "u1" {
		t.Errorf("credential not marked invalid after the retry was also rejected; markedFor = %q", tokens.markedFor)
	}
}

func TestProxyRefreshFailureMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"grant gone", credential.ErrCredentialInvalid, 409},
		{"redmine down during refresh", fmt.Errorf("%w: 503", redmine.ErrUpstream), 502},
		{"client misconfigured", redmine.ErrInvalidClient, 500},
		{"rotated tokens could not be saved", credential.ErrPersistFailed, 500},
		{"unexpected", errors.New("boom"), 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up, auths, _ := sequencedUpstream(t, 401)
			tokens := &fakeTokens{access: "old", refreshErr: tt.err}
			h := newProxy(t, up.URL, tokens)
			rec := httptest.NewRecorder()
			h(rec, authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1"))
			if rec.Code != tt.want {
				t.Errorf("status = %d (%s); want %d", rec.Code, rec.Body, tt.want)
			}
			if len(*auths) != 1 {
				t.Errorf("upstream hits = %d; want 1 (no retry when refresh fails)", len(*auths))
			}
			if strings.Contains(rec.Body.String(), "boom") {
				t.Errorf("internal error text leaked: %s", rec.Body)
			}
		})
	}
}

func TestProxyAccessTokenErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not linked", credential.ErrNoCredential, 409},
		{"invalid", credential.ErrCredentialInvalid, 409},
		{"redmine down during proactive refresh", fmt.Errorf("%w: 503", redmine.ErrUpstream), 502},
		{"client misconfigured", redmine.ErrInvalidClient, 500},
		{"unexpected", errors.New("db down"), 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := newUpstream(t, 200, "{}", func(*http.Request) { t.Error("upstream called without a token") })
			h := newProxy(t, up.URL, &fakeTokens{accessErr: tt.err})
			rec := httptest.NewRecorder()
			h(rec, authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1"))
			if rec.Code != tt.want {
				t.Errorf("status = %d (%s); want %d", rec.Code, rec.Body, tt.want)
			}
		})
	}
}

func TestProxyDoesNotRetryOn5xx(t *testing.T) {
	up, auths, _ := sequencedUpstream(t, 503)
	tokens := &fakeTokens{access: "tok"}
	h := newProxy(t, up.URL, tokens)
	rec := httptest.NewRecorder()
	h(rec, authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1"))
	if rec.Code != 502 || len(*auths) != 1 || len(tokens.refreshes) != 0 {
		t.Errorf("status=%d hits=%d refreshes=%d; want 502 with a single hit and no refresh", rec.Code, len(*auths), len(tokens.refreshes))
	}
}

func TestProxyRejectsOversizedWriteBody(t *testing.T) {
	up := newUpstream(t, 200, "{}", func(*http.Request) { t.Error("upstream called for an oversized body") })
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})
	big := strings.NewReader(strings.Repeat("x", maxBodyBytes+1))
	rec := httptest.NewRecorder()
	h(rec, authed(httptest.NewRequest("POST", "/api/redmine/issues.json", big), "u1"))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), httpapi.CodeInvalidRequest) {
		t.Errorf("status = %d body = %s; want 400 invalid_request", rec.Code, rec.Body)
	}
}

func TestProxyUpstream5xxIs502(t *testing.T) {
	up := newUpstream(t, 503, "boom", nil)
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), httpapi.CodeUpstreamError) {
		t.Errorf("status = %d body = %s; want 502 upstream_error", rec.Code, rec.Body)
	}
}

func TestProxyNoCredentialIs409(t *testing.T) {
	up := newUpstream(t, 200, "{}", nil)
	h := newProxy(t, up.URL, &fakeTokens{accessErr: credential.ErrNoCredential})

	req := authed(httptest.NewRequest("GET", "/api/redmine/issues.json", nil), "u1")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 409 {
		t.Errorf("status = %d; want 409", rec.Code)
	}
}

func TestProxyUnauthenticatedIs401(t *testing.T) {
	up := newUpstream(t, 200, "{}", nil)
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

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
	h := newProxy(t, url, &fakeTokens{access: "tok"})

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
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

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
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

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
	h := newProxy(t, up.URL, &fakeTokens{access: "tok"})

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
