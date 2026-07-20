package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"
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

type fakeLimiter struct {
	allow            bool
	fails, successes int
}

func (f *fakeLimiter) Allow(string) bool { return f.allow }
func (f *fakeLimiter) Fail(string)       { f.fails++ }
func (f *fakeLimiter) Succeed(string)    { f.successes++ }

func TestLoginFinishRateLimit(t *testing.T) {
	t.Run("locked is 429", func(t *testing.T) {
		lim := &fakeLimiter{allow: false}
		mux := http.NewServeMux()
		(&AuthHandler{WebAuthn: &fakeWebAuthn{userID: "u1"}, Sessions: &fakeSessions{},
			Limiter: lim, CookieName: "rmapp_session"}).RegisterRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/login/finish?challengeId=x", strings.NewReader("{}")))
		if rec.Code != 429 || !strings.Contains(rec.Body.String(), CodeRateLimited) {
			t.Errorf("status = %d body = %s; want 429 rate_limited", rec.Code, rec.Body)
		}
	})

	t.Run("failure counts, success resets", func(t *testing.T) {
		lim := &fakeLimiter{allow: true}
		mux := http.NewServeMux()
		(&AuthHandler{
			WebAuthn: &fakeWebAuthn{finishErr: fmt.Errorf("%w: bad signature", ErrInvalidRequest)},
			Sessions: &fakeSessions{}, Limiter: lim, CookieName: "rmapp_session"}).RegisterRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/login/finish?challengeId=x", strings.NewReader("{}")))
		if lim.fails != 1 {
			t.Errorf("fails = %d; want 1", lim.fails)
		}

		mux = http.NewServeMux()
		(&AuthHandler{WebAuthn: &fakeWebAuthn{userID: "u1"}, Sessions: &fakeSessions{},
			Limiter: lim, CookieName: "rmapp_session"}).RegisterRoutes(mux)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/auth/login/finish?challengeId=x", strings.NewReader("{}")))
		if lim.successes != 1 {
			t.Errorf("successes = %d; want 1", lim.successes)
		}
	})
}

type fakeBootstrap struct{ err error }

func (f *fakeBootstrap) Run(context.Context, string, string) ([]byte, string, error) {
	return []byte(`{"publicKey":{}}`), "ch-b", f.err
}

func TestBootstrapEndpoint(t *testing.T) {
	newMux := func(b BootstrapService, lim Limiter) *http.ServeMux {
		mux := http.NewServeMux()
		(&AuthHandler{WebAuthn: &fakeWebAuthn{}, Sessions: &fakeSessions{},
			Bootstrap: b, Limiter: lim, CookieName: "rmapp_session"}).RegisterRoutes(mux)
		return mux
	}
	post := func(mux *http.ServeMux, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/bootstrap", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(rec, req)
		return rec
	}
	valid := `{"login":"alice","password":"secret"}`

	tests := []struct {
		name       string
		service    BootstrapService
		limiter    *fakeLimiter
		body       string
		wantStatus int
		wantBody   string
	}{
		{"disabled is 404", nil, nil, valid, 404, CodeNotFound},
		{"malformed body", &fakeBootstrap{}, nil, "{", 400, CodeInvalidRequest},
		{"missing password", &fakeBootstrap{}, nil, `{"login":"alice"}`, 400, CodeInvalidRequest},
		{"bad credentials", &fakeBootstrap{err: fmt.Errorf("%w: no", ErrUnauthenticated)},
			&fakeLimiter{allow: true}, valid, 401, CodeUnauthenticated},
		{"upstream down", &fakeBootstrap{err: fmt.Errorf("%w: 503", ErrUpstream)},
			&fakeLimiter{allow: true}, valid, 502, CodeUpstreamError},
		{"rate limited", &fakeBootstrap{}, &fakeLimiter{allow: false}, valid, 429, CodeRateLimited},
		{"success", &fakeBootstrap{}, &fakeLimiter{allow: true}, valid, 200, `"challengeId":"ch-b"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(newMux(tt.service, limOrNil(tt.limiter)), tt.body)
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("status = %d body = %s; want %d containing %q", rec.Code, rec.Body, tt.wantStatus, tt.wantBody)
			}
			if tt.name == "bad credentials" && tt.limiter.fails != 1 {
				t.Errorf("limiter fails = %d; want 1", tt.limiter.fails)
			}
			if tt.name == "success" && tt.limiter.successes != 1 {
				t.Errorf("limiter successes = %d; want 1", tt.limiter.successes)
			}
		})
	}
}

func limOrNil(l *fakeLimiter) Limiter {
	if l == nil {
		return nil
	}
	return l
}

type fakeEnrollment struct{ redeemErr error }

func (f *fakeEnrollment) IssueCode(context.Context, string) (string, time.Time, error) {
	return "123456", time.Now().Add(10 * time.Minute), nil
}
func (f *fakeEnrollment) Redeem(context.Context, string) ([]byte, string, error) {
	return []byte(`{"publicKey":{}}`), "ch-e", f.redeemErr
}

func TestEnrollmentEndpoints(t *testing.T) {
	newMux := func(e EnrollmentService, lim Limiter) *http.ServeMux {
		mux := http.NewServeMux()
		(&AuthHandler{WebAuthn: &fakeWebAuthn{}, Sessions: &fakeSessions{},
			Enrollment: e, Limiter: lim, CookieName: "rmapp_session"}).RegisterRoutes(mux)
		return mux
	}

	t.Run("issue requires session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		newMux(&fakeEnrollment{}, nil).ServeHTTP(rec,
			httptest.NewRequest("POST", "/api/auth/enrollment-code", nil))
		if rec.Code != 401 {
			t.Errorf("status = %d; want 401", rec.Code)
		}
	})

	t.Run("issue ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		newMux(&fakeEnrollment{}, nil).ServeHTTP(rec,
			authedCtx(httptest.NewRequest("POST", "/api/auth/enrollment-code", nil)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"code":"123456"`) {
			t.Errorf("status = %d body = %s", rec.Code, rec.Body)
		}
	})

	t.Run("enroll missing code", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/enroll", strings.NewReader("{}"))
		newMux(&fakeEnrollment{}, nil).ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("status = %d; want 400", rec.Code)
		}
	})

	t.Run("enroll invalid code counts failure", func(t *testing.T) {
		lim := &fakeLimiter{allow: true}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/enroll", strings.NewReader(`{"code":"999999"}`))
		newMux(&fakeEnrollment{redeemErr: fmt.Errorf("%w: bad code", ErrInvalidRequest)}, lim).ServeHTTP(rec, req)
		if rec.Code != 400 || lim.fails != 1 {
			t.Errorf("status = %d fails = %d; want 400 and 1", rec.Code, lim.fails)
		}
	})

	t.Run("enroll rate limited", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/enroll", strings.NewReader(`{"code":"123456"}`))
		newMux(&fakeEnrollment{}, &fakeLimiter{allow: false}).ServeHTTP(rec, req)
		if rec.Code != 429 {
			t.Errorf("status = %d; want 429", rec.Code)
		}
	})

	t.Run("enroll ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/enroll", strings.NewReader(`{"code":"123456"}`))
		newMux(&fakeEnrollment{}, nil).ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"challengeId":"ch-e"`) {
			t.Errorf("status = %d body = %s", rec.Code, rec.Body)
		}
	})
}
