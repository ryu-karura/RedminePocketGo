package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeResolver struct {
	sessions map[string]*SessionInfo
	err      error
}

func (f *fakeResolver) ResolveSession(_ context.Context, token string) (*SessionInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sessions[token], nil
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequestID(t *testing.T) {
	var got string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = RequestIDFrom(r.Context())
	}))

	t.Run("generates when absent", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
		if got == "" {
			t.Error("request id not set in context")
		}
		if rec.Header().Get("X-Request-Id") != got {
			t.Errorf("response header %q != context id %q", rec.Header().Get("X-Request-Id"), got)
		}
	})

	t.Run("keeps inbound id", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("X-Request-Id", "inbound-1")
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != "inbound-1" {
			t.Errorf("request id = %q; want inbound-1", got)
		}
	})
}

func TestRecoverPanic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	h := RecoverPanic(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 500 {
		t.Errorf("status = %d; want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), CodeInternalError) {
		t.Errorf("body %q lacks %s envelope", rec.Body, CodeInternalError)
	}
}

func TestAccessLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	h := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/hello", nil))

	line := buf.String()
	for _, want := range []string{"GET", "/hello", "418"} {
		if !strings.Contains(line, want) {
			t.Errorf("access log %q lacks %q", line, want)
		}
	}
}

func TestSessionMiddleware(t *testing.T) {
	resolver := &fakeResolver{sessions: map[string]*SessionInfo{
		"valid-token": {UserID: "u1"},
	}}

	var seen *SessionInfo
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = SessionFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		cookie     string
		resolver   SessionResolver
		wantUser   string
		wantStatus int
	}{
		{"valid cookie", "valid-token", resolver, "u1", 200},
		{"unknown token", "nope", resolver, "", 200},
		{"no cookie", "", resolver, "", 200},
		{"resolver failure", "valid-token", &fakeResolver{err: errors.New("db down")}, "", 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen = nil
			h := Session(tt.resolver, "rmapp_session")(inner)
			req := httptest.NewRequest("GET", "/x", nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "rmapp_session", Value: tt.cookie})
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d; want %d", rec.Code, tt.wantStatus)
			}
			gotUser := ""
			if seen != nil {
				gotUser = seen.UserID
			}
			if gotUser != tt.wantUser {
				t.Errorf("session user = %q; want %q", gotUser, tt.wantUser)
			}
		})
	}
}

func TestRequireXHRForWrites(t *testing.T) {
	h := RequireXHRForWrites(okHandler())
	tests := []struct {
		method     string
		xhr        bool
		wantStatus int
	}{
		{"GET", false, 200},
		{"HEAD", false, 200},
		{"POST", false, 403},
		{"PUT", false, 403},
		{"PATCH", false, 403},
		{"DELETE", false, 403},
		{"POST", true, 200},
		{"DELETE", true, 200},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/x", nil)
			if tt.xhr {
				req.Header.Set("X-Requested-With", "XMLHttpRequest")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Errorf("%s xhr=%v: status = %d; want %d", tt.method, tt.xhr, rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == 403 && !strings.Contains(rec.Body.String(), CodeForbidden) {
				t.Errorf("403 body %q lacks %s envelope", rec.Body, CodeForbidden)
			}
		})
	}
}

func TestChainOrder(t *testing.T) {
	// 規定の順序で合成され、末端のパニックでもリクエスト ID 付きで 500 が返る。
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	resolver := &fakeResolver{}

	h := Chain(logger, resolver, "rmapp_session")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("deep failure")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	if rec.Code != 500 {
		t.Errorf("status = %d; want 500", rec.Code)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("X-Request-Id missing; RequestID must wrap RecoverPanic")
	}
	if !strings.Contains(buf.String(), "500") {
		t.Errorf("access log %q lacks recovered status 500; AccessLog must wrap the recovered handler", buf.String())
	}
}
