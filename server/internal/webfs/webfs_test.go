package webfs

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWebroot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html": "<!doctype html><title>rmapp</title>",
		"js/app.js":  "export const x = 1;",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestServesIndexAtRoot(t *testing.T) {
	h := Handler(writeWebroot(t), "", true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "rmapp") {
		t.Errorf("body %q is not index.html", rec.Body)
	}
}

func TestServesNestedFile(t *testing.T) {
	h := Handler(writeWebroot(t), "", true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/js/app.js", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q; want javascript", ct)
	}
}

func TestNoCacheHeader(t *testing.T) {
	tests := []struct {
		noCache bool
		want    string
	}{
		{true, "no-store"},
		{false, ""},
	}
	for _, tt := range tests {
		h := Handler(writeWebroot(t), "", tt.noCache)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if got := rec.Header().Get("Cache-Control"); got != tt.want {
			t.Errorf("noCache=%v: Cache-Control = %q; want %q", tt.noCache, got, tt.want)
		}
	}
}

func TestBaseURLSubpath(t *testing.T) {
	h := Handler(writeWebroot(t), "/rmapp", true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/rmapp/js/app.js", nil))
	if rec.Code != 200 {
		t.Errorf("under baseURL: status = %d; want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/js/app.js", nil))
	if rec.Code != 404 {
		t.Errorf("outside baseURL: status = %d; want 404", rec.Code)
	}
}

func TestMissingFileIs404(t *testing.T) {
	h := Handler(writeWebroot(t), "", true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/nope.png", nil))
	if rec.Code != 404 {
		t.Errorf("status = %d; want 404", rec.Code)
	}
}

func TestDirectoryListingIsNotServed(t *testing.T) {
	// index.html のないディレクトリの一覧生成はアセット構成の露出になる。
	h := Handler(writeWebroot(t), "", true)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/js/", nil))
	if rec.Code != 404 {
		t.Errorf("GET /js/ status = %d; want 404 (directory listing must be disabled)", rec.Code)
	}
}
