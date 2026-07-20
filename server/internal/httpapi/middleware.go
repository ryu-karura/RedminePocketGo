package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// ミドルウェアの規定順序（CLAUDE.md §4.2）:
// RequestID → RecoverPanic → AccessLog → Session → RequireXHRForWrites → Handler

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeySession
)

// SessionInfo は認証済みリクエストの利用者情報。後続フェーズで拡張される。
type SessionInfo struct {
	UserID string
}

// SessionResolver は Cookie の生セッション値から利用者を解決する。
// 実装は internal/auth（フェーズ 2）。見つからない・期限切れは (nil, nil)。
type SessionResolver interface {
	ResolveSession(ctx context.Context, token string) (*SessionInfo, error)
}

// Chain は規定順序で全ミドルウェアを合成する。
func Chain(logger *slog.Logger, resolver SessionResolver, cookieName string) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		h = RequireXHRForWrites(h)
		h = Session(resolver, cookieName, logger)(h)
		h = AccessLog(logger)(h)
		h = RecoverPanic(logger)(h)
		h = RequestID(h)
		return h
	}
}

// RequestID は X-Request-Id を受け入れ（なければ生成し）、コンテキストと
// レスポンスヘッダーに載せる。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

// RequestIDFrom はコンテキストからリクエスト ID を取り出す。
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// RecoverPanic はパニックを 500 の internal_error エンベロープに変換する。
// パニック値は記録するが、ボディや Cookie は記録しない（CLAUDE.md §4.6）。
func RecoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					logger.Error("panic recovered",
						"panic", p,
						"method", r.Method,
						"path", r.URL.Path,
						"request_id", RequestIDFrom(r.Context()),
					)
					WriteError(w, CodeInternalError, "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog は 1 リクエスト 1 行の構造化ログを書く。パニック時も
// ステータス 500 で記録してから再パニックし、外側の RecoverPanic に渡す。
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				p := recover()
				status := rec.status
				if p != nil {
					status = http.StatusInternalServerError
				}
				logger.Info("access",
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"duration_ms", time.Since(start).Milliseconds(),
					"request_id", RequestIDFrom(r.Context()),
				)
				if p != nil {
					panic(p)
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Session は Cookie のセッション値を解決してコンテキストに載せる。
// 未認証はエラーにしない（許可の判定はハンドラ側の責務）。解決自体の
// 失敗（DB 障害など）のみ、原因を記録した上で 500 を返す。
func Session(resolver SessionResolver, cookieName string, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(cookieName)
			if err != nil || c.Value == "" {
				next.ServeHTTP(w, r)
				return
			}
			info, err := resolver.ResolveSession(r.Context(), c.Value)
			if err != nil {
				// セッション値そのものは記録しない（CLAUDE.md §4.6）。
				logger.Error("session resolution failed",
					"error", err,
					"request_id", RequestIDFrom(r.Context()),
				)
				WriteError(w, CodeInternalError, "session resolution failed")
				return
			}
			if info != nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxKeySession, info))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SessionFrom はコンテキストから利用者情報を取り出す。未認証なら nil。
func SessionFrom(ctx context.Context) *SessionInfo {
	info, _ := ctx.Value(ctxKeySession).(*SessionInfo)
	return info
}

// WithSession はコンテキストに利用者情報を載せる。ミドルウェアを経ない
// 単体テストや、認証済み前提のサブハンドラの組み立てに使う。
func WithSession(ctx context.Context, info *SessionInfo) context.Context {
	return context.WithValue(ctx, ctxKeySession, info)
}

// RequireXHRForWrites は更新系メソッドに X-Requested-With:
// XMLHttpRequest を要求する（CSRF 対策。テンプレートの慣例）。
func RequireXHRForWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
				WriteError(w, CodeForbidden, "X-Requested-With: XMLHttpRequest is required for write requests")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
