package httpapi_test

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/auth"
	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// 実際の store / vault / セッション / Redmine OAuth クライアントを、疑似 Redmine
// （httptest）につないで、ログイン開始 → コールバック → セッション確立までを通す。
// PKCE の challenge と verifier が端から端まで噛み合うことも確かめる。
func TestOAuthLoginEndToEndAgainstFakeRedmine(t *testing.T) {
	var gotForm url.Values
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redmine/oauth/token":
			_ = r.ParseForm()
			gotForm = r.PostForm
			fmt.Fprint(w, `{"access_token":"AT-1","token_type":"Bearer","expires_in":7200,"refresh_token":"RT-1","scope":"view_project view_issues","created_at":1}`)
		case "/redmine/users/current.json":
			if r.Header.Get("Authorization") != "Bearer AT-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"user":{"id":11,"login":"carol","firstname":"Carol","lastname":"C"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()

	st, err := store.Open("file:" + filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	vault, err := credential.NewVault(st, kek, 1)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(st, auth.Config{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, CookieName: "rmapp_session"})

	client := redmine.NewClient(redmine.Config{BaseURL: fake.URL, SubURI: "/redmine", Timeout: 2 * time.Second, MaxConcurrency: 2, PageSize: 100})
	oauth := client.OAuth(redmine.OAuthConfig{
		ClientID: "cid", ClientSecret: "csecret",
		RedirectURI: "https://app.example/api/auth/callback",
		Scopes:      []string{"view_project", "view_issues"}, PublicBaseURL: "https://redmine.example",
	})
	mux := http.NewServeMux()
	(&httpapi.OAuthHandler{
		Login: auth.NewOAuthLogin(auth.OAuthLoginDeps{
			Store: st, Vault: vault, OAuth: oauth, Identity: client, Sessions: sessions, StateTTL: 10 * time.Minute,
		}),
		Sessions: sessions, Limiter: auth.NewRateLimiter(5, time.Minute),
		SessionCookieName: "rmapp_session",
		StateCookieName:   "rmapp_oauth_state", StateCookiePath: "/api/auth/", StateCookieTTL: 10 * time.Minute,
		AppURL: "/",
	}).RegisterRoutes(mux)

	// 1. ログイン開始: Redmine の認可 URL へ。state Cookie が付く。
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/auth/login?return="+url.QueryEscape("#issues/4"), nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("login status = %d", rr.Code)
	}
	loc, _ := url.Parse(rr.Header().Get("Location"))
	if loc.Host != "redmine.example" || loc.Path != "/redmine/oauth/authorize" {
		t.Fatalf("authorize URL = %s; want the public Redmine URL, not the server-to-server one", loc)
	}
	state, challenge := loc.Query().Get("state"), loc.Query().Get("code_challenge")
	var stateCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "rmapp_oauth_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil || stateCookie.Value != state {
		t.Fatalf("state cookie = %+v; want value == state %q", stateCookie, state)
	}

	// 2. Redmine での認証・同意の後、ブラウザは callback へ戻る。
	req := httptest.NewRequest("GET", "/api/auth/callback?code=CODE-1&state="+url.QueryEscape(state), nil)
	req.AddCookie(stateCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/#issues/4" {
		t.Fatalf("callback: %d Location=%q; want 302 /#issues/4", rr.Code, rr.Header().Get("Location"))
	}

	// 3. 疑似 Redmine が受け取った verifier は、最初に送った challenge の原像。
	sum := sha256.Sum256([]byte(gotForm.Get("code_verifier")))
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != challenge {
		t.Errorf("S256(code_verifier) = %q; want the challenge sent at login (%q)", got, challenge)
	}
	if gotForm.Get("code") != "CODE-1" || gotForm.Get("client_secret") != "csecret" || gotForm.Get("redirect_uri") != "https://app.example/api/auth/callback" {
		t.Errorf("token request form = %v", gotForm)
	}

	// 4. 発行されたセッションが有効で、利用者とトークンが保存されている。
	var sess *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "rmapp_session" && c.Value != "" {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("no session cookie after callback")
	}
	info, err := sessions.ResolveSession(req.Context(), sess.Value)
	if err != nil || info == nil {
		t.Fatalf("session does not resolve: %v, %v", info, err)
	}
	u, _ := st.GetUserByID(req.Context(), info.UserID)
	if u == nil || u.RedmineUserID != 11 || u.RedmineLogin != "carol" {
		t.Errorf("user = %+v", u)
	}
	tk, err := vault.LoadTokens(req.Context(), info.UserID)
	if err != nil || tk.Access() != "AT-1" || tk.Refresh() != "RT-1" {
		t.Errorf("stored tokens: %v, %v", tk, err)
	}
	if b, _ := json.Marshal(tk); string(b) != `"[redacted]"` {
		t.Errorf("tokens marshal to %s", b)
	}

	// 5. 同じ callback の再送（リプレイ）は拒否され、新しいセッションは増えない。
	req2 := httptest.NewRequest("GET", "/api/auth/callback?code=CODE-1&state="+url.QueryEscape(state), nil)
	req2.AddCookie(stateCookie)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req2)
	if got := rr.Header().Get("Location"); got != "/#login?error=invalid_state" {
		t.Errorf("replay Location = %q; want invalid_state", got)
	}
}
