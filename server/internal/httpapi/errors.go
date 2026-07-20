// Package httpapi はハンドラ、ミドルウェア、エラー表現を担う。
// エラーは常に単一のエンベロープ
// { "error": { "code": "...", "message": "..." } } で返す（Design.md §6.5）。
// message は開発者・ログ向けで、利用者に見せる文言は SPA が code から決める。
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
)

// サービス層（internal/auth など）がエラーの HTTP 分類を伝えるための番兵。
// httpapi ← サービス層の import 方向を保ったまま errors.Is で写像できる。
var (
	// ErrInvalidRequest は入力不正として 400 系に写像されるべきエラー。
	ErrInvalidRequest = errors.New("invalid request")
)

// Design.md §6.5 のエラーコード。snake_case の識別子。
const (
	CodeUnauthenticated          = "unauthenticated"            // 401
	CodeForbidden                = "forbidden"                  // 403
	CodeNotFound                 = "not_found"                  // 404
	CodeInvalidRequest           = "invalid_request"            // 400
	CodeRedmineCredentialInvalid = "redmine_credential_invalid" // 409
	CodeUpstreamError            = "upstream_error"             // 502
	CodeRateLimited              = "rate_limited"               // 429
	CodeInternalError            = "internal_error"             // 500
)

var statusByCode = map[string]int{
	CodeUnauthenticated:          http.StatusUnauthorized,
	CodeForbidden:                http.StatusForbidden,
	CodeNotFound:                 http.StatusNotFound,
	CodeInvalidRequest:           http.StatusBadRequest,
	CodeRedmineCredentialInvalid: http.StatusConflict,
	CodeUpstreamError:            http.StatusBadGateway,
	CodeRateLimited:              http.StatusTooManyRequests,
	CodeInternalError:            http.StatusInternalServerError,
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// StatusForCode はコードに対応する HTTP ステータスを返す。
// 未知のコードは 500 に落とす（コードの書き漏れをクラッシュにしない）。
func StatusForCode(code string) int {
	if s, ok := statusByCode[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// WriteError はエラーエンベロープを書き出す。
func WriteError(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(StatusForCode(code))
	// エンコード失敗はヘッダー送信後のため握りつぶすしかない。
	// 構造体は固定形でありエンコードは失敗しない。
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{Code: code, Message: message}})
}
