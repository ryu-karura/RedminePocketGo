package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

func authedCtx(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeySession, &SessionInfo{UserID: "u1"}))
}

type fakeUsers struct {
	users map[string]*store.User
	err   error
}

func (f *fakeUsers) GetUserByID(_ context.Context, id string) (*store.User, error) {
	return f.users[id], f.err
}

type fakeGrants struct {
	tokens map[string]*store.OAuthTokens
	err    error
}

func (f *fakeGrants) GetOAuthTokens(_ context.Context, userID string) (*store.OAuthTokens, error) {
	return f.tokens[userID], f.err
}

type fakeCleanup struct{ after []string }

func (f *fakeCleanup) AfterLogout(_ context.Context, userID string) {
	f.after = append(f.after, userID)
}

type authEnv struct {
	mux      *http.ServeMux
	sessions *oauthSessions
	cleanup  *fakeCleanup
}

func newAuthEnv(users UserGetter, grants GrantInfoGetter) *authEnv {
	e := &authEnv{sessions: &oauthSessions{}, cleanup: &fakeCleanup{}}
	e.mux = http.NewServeMux()
	(&AuthHandler{
		Sessions: e.sessions, Users: users, Grants: grants, Cleanup: e.cleanup,
		CookieName: "rmapp_session", LoginPath: "/app/api/auth/login",
	}).RegisterRoutes(e.mux)
	return e
}

var aliceUsers = &fakeUsers{users: map[string]*store.User{
	"u1": {ID: "u1", RedmineUserID: 5, RedmineLogin: "alice", DisplayName: "Alice"},
}}

func TestMe(t *testing.T) {
	refreshed := time.Date(2026, 10, 9, 3, 4, 5, 0, time.UTC)
	grants := &fakeGrants{tokens: map[string]*store.OAuthTokens{
		"u1": {UserID: "u1", Status: "active", Scopes: "view_project view_issues", RefreshedAt: refreshed},
	}}

	t.Run("unauthenticated", func(t *testing.T) {
		e := newAuthEnv(aliceUsers, grants)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/me", nil))
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), CodeUnauthenticated) {
			t.Errorf("status = %d, body = %s; want 401 unauthenticated", rec.Code, rec.Body)
		}
	})

	t.Run("authenticated with grant details", func(t *testing.T) {
		e := newAuthEnv(aliceUsers, grants)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
		if rec.Code != 200 {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
		var got struct {
			UserID, RedmineLogin, DisplayName, RedmineStatus string
			RedmineScopes                                    []string
			RedmineRefreshedAt                               string
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.UserID != "u1" || got.RedmineLogin != "alice" || got.DisplayName != "Alice" || got.RedmineStatus != "active" ||
			strings.Join(got.RedmineScopes, " ") != "view_project view_issues" || got.RedmineRefreshedAt != "2026-10-09T03:04:05Z" {
			t.Errorf("me = %+v", got)
		}
	})

	t.Run("the response never carries token material", func(t *testing.T) {
		g := &fakeGrants{tokens: map[string]*store.OAuthTokens{"u1": {
			UserID: "u1", Status: "active", AccessCiphertext: []byte("CIPHERTEXT-A"), RefreshCiphertext: []byte("CIPHERTEXT-R"),
			AccessNonce: []byte("NONCE"), RefreshedAt: refreshed,
		}}}
		e := newAuthEnv(aliceUsers, g)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
		for _, bad := range []string{"CIPHERTEXT", "NONCE", "ciphertext", "access", "refresh_token"} {
			if strings.Contains(rec.Body.String(), bad) {
				t.Errorf("response contains %q: %s", bad, rec.Body)
			}
		}
	})

	t.Run("session for a deleted user is unauthenticated", func(t *testing.T) {
		e := newAuthEnv(&fakeUsers{users: map[string]*store.User{}}, grants)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
		if rec.Code != 401 {
			t.Errorf("status = %d; want 401", rec.Code)
		}
	})

	t.Run("user lookup failure is a 500", func(t *testing.T) {
		e := newAuthEnv(&fakeUsers{err: fmt.Errorf("db down")}, grants)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
		if rec.Code != 500 {
			t.Errorf("status = %d; want 500", rec.Code)
		}
	})

	t.Run("redmineStatus", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			grants GrantInfoGetter
			want   string
		}{
			{"no Grants wired", nil, "unlinked"},
			{"no token row", &fakeGrants{tokens: map[string]*store.OAuthTokens{}}, "unlinked"},
			{"active", grants, "active"},
			{"invalid", &fakeGrants{tokens: map[string]*store.OAuthTokens{"u1": {Status: "invalid"}}}, "invalid"},
			{"lookup failure degrades to unlinked", &fakeGrants{err: fmt.Errorf("db down")}, "unlinked"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				e := newAuthEnv(aliceUsers, tt.grants)
				rec := httptest.NewRecorder()
				e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
				want := fmt.Sprintf(`"redmineStatus":%q`, tt.want)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
					t.Errorf("status=%d body=%s; want 200 containing %s", rec.Code, rec.Body, want)
				}
			})
		}
	})
}

func TestLogout(t *testing.T) {
	e := newAuthEnv(aliceUsers, nil)
	req := authedCtx(httptest.NewRequest("POST", "/api/auth/logout", nil))
	req.AddCookie(&http.Cookie{Name: "rmapp_session", Value: "tok-9"})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(e.sessions.revoked) != 1 || e.sessions.revoked[0] != "tok-9" {
		t.Errorf("revoked = %v; want [tok-9]", e.sessions.revoked)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "rmapp_session" && c.MaxAge == -1 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("session cookie not cleared")
	}
	// このセッションの失効後に、Redmine 側トークンの後始末（最後の端末なら失効）を依頼する。
	if len(e.cleanup.after) != 1 || e.cleanup.after[0] != "u1" {
		t.Errorf("cleanup calls = %v; want [u1]", e.cleanup.after)
	}

	// Cookie もセッションも無くても冪等に 200（後始末は呼ばない）。
	e2 := newAuthEnv(aliceUsers, nil)
	rec = httptest.NewRecorder()
	e2.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/logout", nil))
	if rec.Code != 200 || len(e2.cleanup.after) != 0 {
		t.Errorf("anonymous logout: status = %d, cleanup = %v; want 200 and none", rec.Code, e2.cleanup.after)
	}
}

func TestReauthorize(t *testing.T) {
	t.Run("returns the login URL under the configured path", func(t *testing.T) {
		e := newAuthEnv(aliceUsers, nil)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("POST", "/api/auth/reauthorize", strings.NewReader(`{"return":"#issues/3"}`))))
		if rec.Code != 200 {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
		var got struct{ LoginURL string }
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.LoginURL != "/app/api/auth/login?return=%23issues%2F3" {
			t.Errorf("loginUrl = %q", got.LoginURL)
		}
	})
	t.Run("defaults to the settings screen", func(t *testing.T) {
		e := newAuthEnv(aliceUsers, nil)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("POST", "/api/auth/reauthorize", nil)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "return=%23settings") {
			t.Errorf("status = %d body = %s", rec.Code, rec.Body)
		}
	})
	t.Run("requires a session", func(t *testing.T) {
		e := newAuthEnv(aliceUsers, nil)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/reauthorize", nil))
		if rec.Code != 401 {
			t.Errorf("status = %d; want 401", rec.Code)
		}
	})
	t.Run("a malformed body is a 400", func(t *testing.T) {
		e := newAuthEnv(aliceUsers, nil)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("POST", "/api/auth/reauthorize", strings.NewReader(`{not json`))))
		if rec.Code != 400 {
			t.Errorf("status = %d; want 400", rec.Code)
		}
	})
}

func TestLegacyAuthRoutesAreGone(t *testing.T) {
	// パスキー・登録コード・パスワードブートストラップは廃止（Design.md §3）。
	e := newAuthEnv(aliceUsers, nil)
	for _, p := range []string{
		"/api/auth/register/begin", "/api/auth/register/finish", "/api/auth/login/begin", "/api/auth/login/finish",
		"/api/auth/bootstrap", "/api/auth/enrollment-code", "/api/auth/enroll", "/api/auth/relink", "/api/devices",
	} {
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, httptest.NewRequest("POST", p, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s is still routed (status %d)", p, rec.Code)
		}
	}
}
