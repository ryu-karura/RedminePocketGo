package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

type fakeUsers struct {
	users map[string]*store.User
	err   error
}

func (f *fakeUsers) GetUserByID(_ context.Context, id string) (*store.User, error) {
	return f.users[id], f.err
}

type fakeCredentials struct {
	status map[string]string // userID -> status ("" は未登録扱い)
	err    error
}

func (f *fakeCredentials) GetRedmineCredential(_ context.Context, userID string) (*store.RedmineCredential, error) {
	if f.err != nil {
		return nil, f.err
	}
	s, ok := f.status[userID]
	if !ok {
		return nil, nil
	}
	return &store.RedmineCredential{UserID: userID, Status: s}, nil
}

type revokeRecorder struct {
	fakeSessions
	revoked []string
}

func (r *revokeRecorder) Revoke(_ context.Context, token string) error {
	r.revoked = append(r.revoked, token)
	return nil
}

func TestMe(t *testing.T) {
	users := &fakeUsers{users: map[string]*store.User{
		"u1": {ID: "u1", RedmineLogin: "alice", DisplayName: "Alice"},
	}}
	mux := http.NewServeMux()
	(&AuthHandler{WebAuthn: &fakeWebAuthn{}, Sessions: &fakeSessions{}, Users: users,
		CookieName: "rmapp_session"}).RegisterRoutes(mux)

	t.Run("unauthenticated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/auth/me", nil))
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), CodeUnauthenticated) {
			t.Errorf("status = %d, body = %s; want 401 unauthenticated", rec.Code, rec.Body)
		}
	})

	t.Run("authenticated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
		if rec.Code != 200 {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
		}
		for _, want := range []string{`"userId":"u1"`, `"redmineLogin":"alice"`, `"displayName":"Alice"`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("body %q lacks %q", rec.Body, want)
			}
		}
	})

	t.Run("session for deleted user is unauthenticated", func(t *testing.T) {
		mux := http.NewServeMux()
		(&AuthHandler{WebAuthn: &fakeWebAuthn{}, Sessions: &fakeSessions{},
			Users: &fakeUsers{users: map[string]*store.User{}}, CookieName: "rmapp_session"}).RegisterRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
		if rec.Code != 401 {
			t.Errorf("status = %d; want 401", rec.Code)
		}
	})

	t.Run("redmineStatus reflects the credential vault, defaulting to unlinked", func(t *testing.T) {
		for _, tt := range []struct {
			name  string
			creds CredentialStatusGetter
			want  string
		}{
			{"no Credentials wired", nil, "unlinked"},
			{"no credential row", &fakeCredentials{status: map[string]string{}}, "unlinked"},
			{"active", &fakeCredentials{status: map[string]string{"u1": "active"}}, "active"},
			{"invalid", &fakeCredentials{status: map[string]string{"u1": "invalid"}}, "invalid"},
			{"lookup failure degrades to unlinked", &fakeCredentials{err: fmt.Errorf("db down")}, "unlinked"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				mux := http.NewServeMux()
				(&AuthHandler{WebAuthn: &fakeWebAuthn{}, Sessions: &fakeSessions{}, Users: users,
					Credentials: tt.creds, CookieName: "rmapp_session"}).RegisterRoutes(mux)
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, authedCtx(httptest.NewRequest("GET", "/api/auth/me", nil)))
				want := fmt.Sprintf(`"redmineStatus":%q`, tt.want)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
					t.Errorf("status=%d body=%s; want 200 containing %s", rec.Code, rec.Body, want)
				}
			})
		}
	})
}

func TestLogout(t *testing.T) {
	sessions := &revokeRecorder{}
	mux := http.NewServeMux()
	(&AuthHandler{WebAuthn: &fakeWebAuthn{}, Sessions: sessions,
		Users: &fakeUsers{}, CookieName: "rmapp_session"}).RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "rmapp_session", Value: "tok-9"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(sessions.revoked) != 1 || sessions.revoked[0] != "tok-9" {
		t.Errorf("revoked = %v; want [tok-9]", sessions.revoked)
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

	// Cookie なしでも冪等に 200
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/logout", nil))
	if rec.Code != 200 {
		t.Errorf("logout without cookie: status = %d; want 200", rec.Code)
	}
}
