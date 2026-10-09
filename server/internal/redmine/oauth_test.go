package redmine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestOAuth(upstreamURL string) *OAuth {
	c := newTestClient(upstreamURL, 100)
	o := c.OAuth(OAuthConfig{
		ClientID:      "cid",
		ClientSecret:  "csecret",
		RedirectURI:   "https://app.example/api/auth/callback",
		Scopes:        []string{"view_project", "view_issues"},
		PublicBaseURL: "https://redmine.example",
	})
	o.now = func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }
	return o
}

func TestAuthorizeURL(t *testing.T) {
	o := newTestOAuth("http://unused")
	raw := o.AuthorizeURL("st4te", "ch4llenge")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	// ブラウザ向けは publicBaseURL + サブ URI。サーバー間の baseURL は使わない。
	if got := u.Scheme + "://" + u.Host + u.Path; got != "https://redmine.example/redmine/oauth/authorize" {
		t.Errorf("authorize endpoint = %q", got)
	}
	want := map[string]string{
		"response_type":         "code",
		"client_id":             "cid",
		"redirect_uri":          "https://app.example/api/auth/callback",
		"scope":                 "view_project view_issues",
		"state":                 "st4te",
		"code_challenge":        "ch4llenge",
		"code_challenge_method": "S256",
	}
	q := u.Query()
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q; want %q", k, q.Get(k), v)
		}
	}
	if strings.Contains(raw, "csecret") {
		t.Error("authorize URL leaks the client secret")
	}
}

func tokenServer(t *testing.T, status int, body string, capture func(r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			capture(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
}

const okTokenBody = `{"access_token":"AT","token_type":"Bearer","expires_in":7200,"refresh_token":"RT","scope":"view_project view_issues","created_at":1791547504}`

func TestExchangeSendsCodeVerifierAndClientCredentials(t *testing.T) {
	var gotPath, gotCT string
	var form url.Values
	srv := tokenServer(t, 200, okTokenBody, func(r *http.Request) {
		gotPath, gotCT = r.URL.Path, r.Header.Get("Content-Type")
		_ = r.ParseForm()
		form = r.PostForm
		if r.Method != http.MethodPost {
			t.Errorf("method = %s; want POST", r.Method)
		}
	})
	defer srv.Close()

	o := newTestOAuth(srv.URL)
	ts, err := o.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if gotPath != "/redmine/oauth/token" || !strings.HasPrefix(gotCT, "application/x-www-form-urlencoded") {
		t.Errorf("path = %q, content-type = %q", gotPath, gotCT)
	}
	for k, v := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": "the-verifier",
		"redirect_uri": "https://app.example/api/auth/callback", "client_id": "cid", "client_secret": "csecret",
	} {
		if form.Get(k) != v {
			t.Errorf("form %s = %q; want %q", k, form.Get(k), v)
		}
	}
	if ts.AccessToken != "AT" || ts.RefreshToken != "RT" {
		t.Errorf("tokens = %q / %q", ts.AccessToken, ts.RefreshToken)
	}
	if got := strings.Join(ts.Scopes, " "); got != "view_project view_issues" {
		t.Errorf("scopes = %q", got)
	}
	if want := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC); !ts.AccessExpiresAt.Equal(want) {
		t.Errorf("AccessExpiresAt = %v; want %v（ローカル時計 + expires_in）", ts.AccessExpiresAt, want)
	}
}

func TestRefreshSendsRefreshGrant(t *testing.T) {
	var form url.Values
	srv := tokenServer(t, 200, okTokenBody, func(r *http.Request) { _ = r.ParseForm(); form = r.PostForm })
	defer srv.Close()

	if _, err := newTestOAuth(srv.URL).Refresh(context.Background(), "old-rt"); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for k, v := range map[string]string{
		"grant_type": "refresh_token", "refresh_token": "old-rt", "client_id": "cid", "client_secret": "csecret",
	} {
		if form.Get(k) != v {
			t.Errorf("form %s = %q; want %q", k, form.Get(k), v)
		}
	}
	if form.Has("code") || form.Has("code_verifier") {
		t.Error("refresh grant must not carry code / code_verifier")
	}
}

func TestTokenEndpointErrorClassification(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantIs  error
		wantNot error
	}{
		{"invalid_grant", 400, `{"error":"invalid_grant","error_description":"..."}`, ErrInvalidGrant, ErrUpstream},
		{"invalid_client is a config problem, not the user's grant", 401, `{"error":"invalid_client"}`, ErrInvalidClient, ErrInvalidGrant},
		{"unauthorized_client", 400, `{"error":"unauthorized_client"}`, ErrInvalidClient, ErrInvalidGrant},
		{"other 4xx", 400, `{"error":"invalid_scope"}`, ErrOAuthRejected, ErrInvalidGrant},
		{"5xx is transient", 503, `{}`, ErrUpstream, ErrInvalidGrant},
		{"html error page", 502, `<html>bad gateway</html>`, ErrUpstream, ErrInvalidGrant},
		{"not json on 200", 200, `<html>`, ErrUpstream, ErrInvalidGrant},
		{"no access_token", 200, `{"token_type":"Bearer","refresh_token":"RT","expires_in":10}`, ErrUpstream, ErrInvalidGrant},
		{"no refresh_token（回転を取りこぼすため成功扱いにしない）", 200, `{"access_token":"AT","expires_in":10}`, ErrUpstream, ErrInvalidGrant},
	}
	for _, tt := range tests {
		for _, call := range []string{"exchange", "refresh"} {
			t.Run(tt.name+"/"+call, func(t *testing.T) {
				var hits atomic.Int32
				srv := tokenServer(t, tt.status, tt.body, func(*http.Request) { hits.Add(1) })
				defer srv.Close()
				o := newTestOAuth(srv.URL)
				var err error
				if call == "exchange" {
					_, err = o.Exchange(context.Background(), "c", "v")
				} else {
					_, err = o.Refresh(context.Background(), "rt")
				}
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("err = %v; want errors.Is %v", err, tt.wantIs)
				}
				if errors.Is(err, tt.wantNot) {
					t.Errorf("err = %v; must not be %v", err, tt.wantNot)
				}
				// トークン POST は再送しない。回転済みのリフレッシュトークンを
				// 二重に使うと連鎖が壊れるため（Design.md §4.4）。
				if hits.Load() != 1 {
					t.Errorf("token endpoint hit %d times; want exactly 1（再試行禁止）", hits.Load())
				}
				if strings.Contains(err.Error(), "csecret") {
					t.Errorf("error leaks client secret: %v", err)
				}
			})
		}
	}
}

func TestTokenEndpointConnectionFailureIsUpstreamAndNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // 接続拒否
	_, err := newTestOAuth(url).Refresh(context.Background(), "rt")
	if !errors.Is(err, ErrUpstream) || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("err = %v; want ErrUpstream (not invalid_grant)", err)
	}
}

func TestRevoke(t *testing.T) {
	var gotPath string
	var form url.Values
	srv := tokenServer(t, 200, `{}`, func(r *http.Request) { gotPath = r.URL.Path; _ = r.ParseForm(); form = r.PostForm })
	defer srv.Close()
	if err := newTestOAuth(srv.URL).Revoke(context.Background(), "the-token", "refresh_token"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if gotPath != "/redmine/oauth/revoke" {
		t.Errorf("path = %q", gotPath)
	}
	for k, v := range map[string]string{
		"token": "the-token", "token_type_hint": "refresh_token", "client_id": "cid", "client_secret": "csecret",
	} {
		if form.Get(k) != v {
			t.Errorf("form %s = %q; want %q", k, form.Get(k), v)
		}
	}

	bad := tokenServer(t, 503, `{}`, nil)
	defer bad.Close()
	if err := newTestOAuth(bad.URL).Revoke(context.Background(), "t", "access_token"); !errors.Is(err, ErrUpstream) {
		t.Errorf("revoke on 503: err = %v; want ErrUpstream", err)
	}
}

func TestCurrentUser(t *testing.T) {
	var gotAuth, gotAPIKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAPIKey, gotPath = r.Header.Get("Authorization"), r.Header.Get("X-Redmine-Api-Key"), r.URL.Path
		// 実機では api_key は含まれない（探査結果）が、含まれても読み取らないこと。
		fmt.Fprint(w, `{"user":{"id":5,"login":"alice","firstname":"Alice","lastname":"Anderson","api_key":"LEAK","admin":false}}`)
	}))
	defer srv.Close()

	u, err := newTestClient(srv.URL, 100).CurrentUser(context.Background(), "access-1")
	if err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if gotPath != "/redmine/users/current.json" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer access-1" || gotAPIKey != "" {
		t.Errorf("Authorization = %q, X-Redmine-Api-Key = %q; want Bearer only", gotAuth, gotAPIKey)
	}
	if u.ID != 5 || u.Login != "alice" || u.DisplayName != "Alice Anderson" {
		t.Errorf("user = %+v", u)
	}
	if b, _ := json.Marshal(u); strings.Contains(string(b), "LEAK") {
		t.Error("CurrentUser carries api_key")
	}

	unauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer unauth.Close()
	if _, err := newTestClient(unauth.URL, 100).CurrentUser(context.Background(), "x"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401: err = %v; want ErrUnauthorized", err)
	}
	for _, bad := range []string{`{"user":{"id":0,"login":"x"}}`, `{"user":{"id":3,"login":""}}`, `{}`} {
		s := tokenServer(t, 200, bad, nil)
		if _, err := newTestClient(s.URL, 100).CurrentUser(context.Background(), "x"); !errors.Is(err, ErrUpstream) {
			t.Errorf("body %s: err = %v; want ErrUpstream", bad, err)
		}
		s.Close()
	}
}

func TestTokenSetIsRedacted(t *testing.T) {
	ts := &TokenSet{AccessToken: "SECRET-AT", RefreshToken: "SECRET-RT", Scopes: []string{"a"}}
	check := func(label, s string) {
		if strings.Contains(s, "SECRET") {
			t.Errorf("%s leaks a token: %s", label, s)
		}
	}
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	check("json", string(b))
	check("%v", fmt.Sprintf("%v", ts))
	check("%+v", fmt.Sprintf("%+v", ts))
	check("%#v", fmt.Sprintf("%#v", *ts))
	check("slog", fmt.Sprint(slog.AnyValue(ts).Resolve()))
}
