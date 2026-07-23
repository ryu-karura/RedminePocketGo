package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealthzAlwaysOK(t *testing.T) {
	// healthz はプロセスが HTTP に応答できるかだけを見る（依存先の障害と無関係）。
	mux := http.NewServeMux()
	(&HealthHandler{Upstream: fakePinger{err: errors.New("upstream down")}}).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("body = %s; want status ok", rec.Body)
	}
}

func TestReadyzUpstreamReachable(t *testing.T) {
	mux := http.NewServeMux()
	(&HealthHandler{Upstream: fakePinger{}}).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}

func TestReadyzUpstreamUnreachable(t *testing.T) {
	mux := http.NewServeMux()
	(&HealthHandler{Upstream: fakePinger{err: errors.New("dial tcp: connection refused")}}).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Errorf("body = %s; want status error", rec.Body)
	}
}
