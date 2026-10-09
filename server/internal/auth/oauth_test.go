package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

var loginT0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

type fakeAuthServer struct {
	exchangeFn func(ctx context.Context, code, verifier string) (*redmine.TokenSet, error)
	gotVerif   string
	gotCode    string
	calls      int
}

func (f *fakeAuthServer) AuthorizeURL(state, challenge string) string {
	return "https://redmine.example/redmine/oauth/authorize?state=" + url.QueryEscape(state) +
		"&code_challenge=" + url.QueryEscape(challenge)
}

func (f *fakeAuthServer) Exchange(ctx context.Context, code, verifier string) (*redmine.TokenSet, error) {
	f.calls++
	f.gotCode, f.gotVerif = code, verifier
	return f.exchangeFn(ctx, code, verifier)
}

type fakeIdentity struct {
	user     *redmine.CurrentUser
	err      error
	gotToken string
}

func (f *fakeIdentity) CurrentUser(_ context.Context, token string) (*redmine.CurrentUser, error) {
	f.gotToken = token
	return f.user, f.err
}

type loginEnv struct {
	svc      *OAuthLogin
	st       *store.Store
	vault    *credential.Vault
	sessions *Sessions
	as       *fakeAuthServer
	id       *fakeIdentity
	now      time.Time
}

func newLoginEnv(t *testing.T) *loginEnv {
	t.Helper()
	st, err := store.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	vault, err := credential.NewVault(st, kek, 1)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessions(st, Config{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, CookieName: "s"})
	e := &loginEnv{st: st, vault: vault, sessions: sessions, now: loginT0}
	e.as = &fakeAuthServer{exchangeFn: func(context.Context, string, string) (*redmine.TokenSet, error) {
		return &redmine.TokenSet{
			AccessToken: "AT", RefreshToken: "RT",
			Scopes: []string{"view_project"}, AccessExpiresAt: loginT0.Add(2 * time.Hour),
		}, nil
	}}
	e.id = &fakeIdentity{user: &redmine.CurrentUser{ID: 5, Login: "alice", DisplayName: "Alice A"}}
	e.svc = NewOAuthLogin(OAuthLoginDeps{
		Store: st, Vault: vault, OAuth: e.as, Identity: e.id, Sessions: sessions, StateTTL: 10 * time.Minute,
	})
	e.svc.now = func() time.Time { return e.now }
	return e
}

func stateFromURL(t *testing.T, authorizeURL string) (state, challenge string) {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state"), u.Query().Get("code_challenge")
}

func TestOAuthBeginStoresHashedStateAndEncryptedVerifier(t *testing.T) {
	e := newLoginEnv(t)
	ctx := context.Background()

	authURL, state, err := e.svc.Begin(ctx, "#issues/12")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	urlState, challenge := stateFromURL(t, authURL)
	if urlState != state || len(state) < 40 {
		t.Fatalf("state = %q (url: %q); want a long random value matching the URL", state, urlState)
	}

	var ct, nonce []byte
	var returnTo, expires string
	hash := sha256Hex(state)
	if err := e.st.DB().QueryRow(
		`SELECT verifier_ciphertext, verifier_nonce, return_to, expires_at FROM oauth_states WHERE state_hash = ?`, hash,
	).Scan(&ct, &nonce, &returnTo, &expires); err != nil {
		t.Fatalf("state row (keyed by hash): %v", err)
	}
	if n := 0; e.st.DB().QueryRow(`SELECT COUNT(*) FROM oauth_states WHERE state_hash = ?`, state).Scan(&n) == nil && n != 0 {
		t.Error("raw state is stored; only its hash may be")
	}
	if returnTo != "#issues/12" {
		t.Errorf("return_to = %q", returnTo)
	}
	if want := loginT0.Add(10 * time.Minute).Format(time.RFC3339Nano); expires != want {
		t.Errorf("expires_at = %q; want %q", expires, want)
	}

	verifier, err := e.vault.Open("oauth-verifier:"+hash, ct, nonce)
	if err != nil {
		t.Fatalf("verifier is not sealed with the expected AAD: %v", err)
	}
	if bytes.Contains(ct, verifier) {
		t.Error("verifier stored in plaintext")
	}
	sum := sha256.Sum256(verifier)
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Errorf("code_challenge = %q; want S256(verifier) = %q", challenge, want)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("verifier length %d outside RFC 7636 range 43..128", len(verifier))
	}
}

func TestOAuthBeginSanitizesReturnTo(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "#projects"},
		{"#projects", "#projects"},
		{"#settings", "#settings"},
		{"#issues", "#issues"},
		{"#issues/12", "#issues/12"},
		{"#issue-detail/7", "#issue-detail/7"},
		{"https://evil.example/", "#projects"},
		{"//evil.example", "#projects"},
		{"/api/auth/logout", "#projects"},
		{"javascript:alert(1)", "#projects"},
		{"#issues/12/../../x", "#projects"},
		{"#issues/abc", "#projects"},
		{"#modal-issue-create/1", "#projects"},
		{"#login?error=x", "#projects"},
		{"#projects\r\nSet-Cookie: a=b", "#projects"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			e := newLoginEnv(t)
			_, state, err := e.svc.Begin(context.Background(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if err := e.st.DB().QueryRow(`SELECT return_to FROM oauth_states WHERE state_hash = ?`, sha256Hex(state)).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("return_to = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestOAuthCompleteSuccess(t *testing.T) {
	e := newLoginEnv(t)
	ctx := context.Background()
	_, state, _ := e.svc.Begin(ctx, "#issues/3")
	var verifier string
	{
		var ct, nonce []byte
		_ = e.st.DB().QueryRow(`SELECT verifier_ciphertext, verifier_nonce FROM oauth_states WHERE state_hash = ?`, sha256Hex(state)).Scan(&ct, &nonce)
		v, _ := e.vault.Open("oauth-verifier:"+sha256Hex(state), ct, nonce)
		verifier = string(v)
	}

	token, returnTo, err := e.svc.Complete(ctx, "the-code", state)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if e.as.gotCode != "the-code" || e.as.gotVerif != verifier {
		t.Errorf("exchange got code=%q verifier=%q; want the stored verifier", e.as.gotCode, e.as.gotVerif)
	}
	if e.id.gotToken != "AT" {
		t.Errorf("identity looked up with %q; want the new access token", e.id.gotToken)
	}
	if returnTo != "#issues/3" {
		t.Errorf("returnTo = %q", returnTo)
	}

	info, err := e.sessions.ResolveSession(ctx, token)
	if err != nil || info == nil {
		t.Fatalf("session from Complete does not resolve: %v, %v", info, err)
	}
	u, _ := e.st.GetUserByID(ctx, info.UserID)
	if u == nil || u.RedmineUserID != 5 || u.RedmineLogin != "alice" || u.DisplayName != "Alice A" {
		t.Errorf("user = %+v", u)
	}
	tk, err := e.vault.LoadTokens(ctx, info.UserID)
	if err != nil || tk.Access() != "AT" || tk.Refresh() != "RT" {
		t.Errorf("stored tokens = %v, %v", tk, err)
	}

	// 同じ state の再利用（コールバックのリプレイ）は拒否される。
	if _, _, err := e.svc.Complete(ctx, "the-code", state); !hasLoginCode(err, CodeInvalidState) {
		t.Errorf("replay err = %v; want invalid_state", err)
	}
	if e.as.calls != 1 {
		t.Errorf("exchange called %d times; want 1", e.as.calls)
	}
}

func hasLoginCode(err error, code string) bool {
	var le *LoginError
	return errors.As(err, &le) && le.Code == code
}

func TestOAuthCompleteFailures(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(e *loginEnv)
		state    func(real string) string
		wantCode string
	}{
		{"unknown state", nil, func(string) string { return "nope" }, CodeInvalidState},
		{"empty state", nil, func(string) string { return "" }, CodeInvalidState},
		{"expired state", func(e *loginEnv) { e.now = e.now.Add(11 * time.Minute) }, nil, CodeInvalidState},
		{"invalid_grant", func(e *loginEnv) {
			e.as.exchangeFn = func(context.Context, string, string) (*redmine.TokenSet, error) {
				return nil, redmine.ErrInvalidGrant
			}
		}, nil, CodeExchangeFailed},
		{"rejected", func(e *loginEnv) {
			e.as.exchangeFn = func(context.Context, string, string) (*redmine.TokenSet, error) {
				return nil, fmt.Errorf("%w: invalid_scope", redmine.ErrOAuthRejected)
			}
		}, nil, CodeExchangeFailed},
		{"invalid_client is our misconfiguration", func(e *loginEnv) {
			e.as.exchangeFn = func(context.Context, string, string) (*redmine.TokenSet, error) {
				return nil, redmine.ErrInvalidClient
			}
		}, nil, CodeServerMisconfigured},
		{"token endpoint down", func(e *loginEnv) {
			e.as.exchangeFn = func(context.Context, string, string) (*redmine.TokenSet, error) {
				return nil, fmt.Errorf("%w: 503", redmine.ErrUpstream)
			}
		}, nil, CodeRedmineUnavailable},
		{"identity 401", func(e *loginEnv) { e.id.err = redmine.ErrUnauthorized }, nil, CodeExchangeFailed},
		{"identity upstream", func(e *loginEnv) { e.id.err = fmt.Errorf("%w: 502", redmine.ErrUpstream) }, nil, CodeRedmineUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLoginEnv(t)
			ctx := context.Background()
			_, state, err := e.svc.Begin(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			if tt.prepare != nil {
				tt.prepare(e)
			}
			if tt.state != nil {
				state = tt.state(state)
			}
			token, _, err := e.svc.Complete(ctx, "c", state)
			if !hasLoginCode(err, tt.wantCode) {
				t.Fatalf("err = %v; want login code %q", err, tt.wantCode)
			}
			if token != "" {
				t.Error("a session token was returned on failure")
			}
			var n int
			_ = e.st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
			if n != 0 {
				t.Errorf("sessions = %d after a failed login", n)
			}
			_ = e.st.DB().QueryRow(`SELECT COUNT(*) FROM oauth_tokens`).Scan(&n)
			if n != 0 {
				t.Errorf("oauth_tokens = %d after a failed login", n)
			}
			// 失敗した試行の state も使用済み（やり直しは Begin から）。
			if tt.state == nil && tt.name != "expired state" {
				if _, _, err := e.svc.Complete(ctx, "c", state); !hasLoginCode(err, CodeInvalidState) {
					t.Errorf("reusing the state after a failed attempt: err = %v; want invalid_state", err)
				}
			}
			if msg := fmt.Sprint(err); strings.Contains(msg, "AT") && strings.Contains(msg, "RT") {
				t.Errorf("error leaks tokens: %v", err)
			}
		})
	}
}
