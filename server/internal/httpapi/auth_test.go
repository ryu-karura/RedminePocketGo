package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeWebAuthn struct {
	beginErr, finishErr error
	userID              string
}

func (f *fakeWebAuthn) BeginRegistration(context.Context, string) ([]byte, string, error) {
	return []byte(`{"publicKey":{}}`), "ch-1", f.beginErr
}
func (f *fakeWebAuthn) FinishRegistration(_ context.Context, _ string, _ *http.Request) (string, []byte, error) {
	return f.userID, []byte{1}, f.finishErr
}
func (f *fakeWebAuthn) BeginLogin(context.Context) ([]byte, string, error) {
	return []byte(`{"publicKey":{}}`), "ch-2", f.beginErr
}
func (f *fakeWebAuthn) FinishLogin(_ context.Context, _ string, _ *http.Request) (string, []byte, error) {
	return f.userID, []byte{1}, f.finishErr
}

type fakeSessions struct{ issueErr error }

func (f *fakeSessions) Issue(context.Context, string, []byte) (string, error) {
	return "tok-1", f.issueErr
}
func (f *fakeSessions) Revoke(context.Context, string) error { return nil }
func (f *fakeSessions) Cookie(token string) *http.Cookie {
	return &http.Cookie{Name: "rmapp_session", Value: token, Path: "/"}
}
func (f *fakeSessions) ClearCookie() *http.Cookie {
	return &http.Cookie{Name: "rmapp_session", Value: "", Path: "/", MaxAge: -1}
}

func authedCtx(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeySession, &SessionInfo{UserID: "u1"}))
}

func newAuthMux(wa *fakeWebAuthn, ss *fakeSessions) *http.ServeMux {
	mux := http.NewServeMux()
	(&AuthHandler{WebAuthn: wa, Sessions: ss}).RegisterRoutes(mux)
	return mux
}

func TestAuthEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		authed     bool
		wa         fakeWebAuthn
		ss         fakeSessions
		wantStatus int
		wantBody   string // 部分一致
		wantCookie bool
	}{
		{"register begin unauthenticated", "/api/auth/register/begin", false,
			fakeWebAuthn{}, fakeSessions{}, 401, CodeUnauthenticated, false},
		{"register begin ok", "/api/auth/register/begin", true,
			fakeWebAuthn{}, fakeSessions{}, 200, `"challengeId":"ch-1"`, false},
		{"register begin service failure", "/api/auth/register/begin", true,
			fakeWebAuthn{beginErr: fmt.Errorf("db down")}, fakeSessions{}, 500, CodeInternalError, false},
		{"register finish without challengeId", "/api/auth/register/finish", false,
			fakeWebAuthn{}, fakeSessions{}, 400, CodeInvalidRequest, false},
		{"register finish malformed ceremony", "/api/auth/register/finish?challengeId=x", false,
			fakeWebAuthn{finishErr: fmt.Errorf("%w: bad attestation", ErrInvalidRequest)},
			fakeSessions{}, 400, CodeInvalidRequest, false},
		{"register finish ok issues session", "/api/auth/register/finish?challengeId=x", false,
			fakeWebAuthn{userID: "u1"}, fakeSessions{}, 200, `"userId":"u1"`, true},
		{"login begin ok", "/api/auth/login/begin", false,
			fakeWebAuthn{}, fakeSessions{}, 200, `"challengeId":"ch-2"`, false},
		{"login finish without challengeId", "/api/auth/login/finish", false,
			fakeWebAuthn{}, fakeSessions{}, 400, CodeInvalidRequest, false},
		{"login finish bad assertion is 401", "/api/auth/login/finish?challengeId=x", false,
			fakeWebAuthn{finishErr: fmt.Errorf("%w: bad signature", ErrInvalidRequest)},
			fakeSessions{}, 401, CodeUnauthenticated, false},
		{"login finish upstream failure", "/api/auth/login/finish?challengeId=x", false,
			fakeWebAuthn{finishErr: fmt.Errorf("db down")}, fakeSessions{}, 500, CodeInternalError, false},
		{"login finish ok issues session", "/api/auth/login/finish?challengeId=x", false,
			fakeWebAuthn{userID: "u1"}, fakeSessions{}, 200, `"userId":"u1"`, true},
		{"login finish session issue failure", "/api/auth/login/finish?challengeId=x", false,
			fakeWebAuthn{userID: "u1"}, fakeSessions{issueErr: fmt.Errorf("db down")}, 500, CodeInternalError, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := newAuthMux(&tt.wa, &tt.ss)
			req := httptest.NewRequest("POST", tt.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			if tt.authed {
				req = authedCtx(req)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d; want %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body %q lacks %q", rec.Body, tt.wantBody)
			}
			gotCookie := false
			for _, c := range rec.Result().Cookies() {
				if c.Name == "rmapp_session" && c.Value != "" {
					gotCookie = true
				}
			}
			if gotCookie != tt.wantCookie {
				t.Errorf("session cookie set = %v; want %v", gotCookie, tt.wantCookie)
			}
			if tt.wantStatus == 200 {
				var v map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
					t.Errorf("200 body is not JSON: %v", err)
				}
			}
		})
	}
}
