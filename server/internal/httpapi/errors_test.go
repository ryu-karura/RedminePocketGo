package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestWriteError(t *testing.T) {
	// Design.md §6.5 のエラーコード表と HTTP ステータスの対応。
	tests := []struct {
		code       string
		wantStatus int
	}{
		{CodeUnauthenticated, 401},
		{CodeForbidden, 403},
		{CodeNotFound, 404},
		{CodeInvalidRequest, 400},
		{CodeRedmineCredentialInvalid, 409},
		{CodeUpstreamError, 502},
		{CodeRateLimited, 429},
		{CodeInternalError, 500},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, tt.code, "for developers")

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d; want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", ct)
			}

			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("body is not envelope JSON: %v (%s)", err, rec.Body)
			}
			if envelope.Error.Code != tt.code {
				t.Errorf("error.code = %q; want %q", envelope.Error.Code, tt.code)
			}
			if envelope.Error.Message != "for developers" {
				t.Errorf("error.message = %q", envelope.Error.Message)
			}
		})
	}
}

func TestWriteErrorUnknownCodeFallsBackTo500(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, "no_such_code", "x")
	if rec.Code != 500 {
		t.Errorf("unknown code status = %d; want 500", rec.Code)
	}
}
