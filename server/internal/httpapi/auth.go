package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// WebAuthnService は internal/auth の WebAuthn が実装する。
type WebAuthnService interface {
	BeginRegistration(ctx context.Context, userID string) (optionsJSON []byte, challengeID string, err error)
	FinishRegistration(ctx context.Context, challengeID string, r *http.Request) (userID string, credentialID []byte, err error)
	BeginLogin(ctx context.Context) (optionsJSON []byte, challengeID string, err error)
	FinishLogin(ctx context.Context, challengeID string, r *http.Request) (userID string, credentialID []byte, err error)
}

// SessionService は internal/auth の Sessions が実装する。
type SessionService interface {
	Issue(ctx context.Context, userID string, credentialID []byte) (string, error)
	Revoke(ctx context.Context, token string) error
	Cookie(token string) *http.Cookie
	ClearCookie() *http.Cookie
}

// UserGetter は利用者情報の参照。*store.Store が実装する。
type UserGetter interface {
	GetUserByID(ctx context.Context, id string) (*store.User, error)
}

// Limiter はログイン試行のレート制限（auth.RateLimiter が実装する）。
type Limiter interface {
	Allow(key string) bool
	Fail(key string)
	Succeed(key string)
}

// BootstrapService は初回登録（auth.Bootstrap が実装する）。
type BootstrapService interface {
	Run(ctx context.Context, login, password string) (optionsJSON []byte, challengeID string, err error)
}

// EnrollmentService は登録コードによる端末追加（auth.Enrollment が実装する）。
type EnrollmentService interface {
	IssueCode(ctx context.Context, userID string) (code string, expiresAt time.Time, err error)
	Redeem(ctx context.Context, code string) (optionsJSON []byte, challengeID string, err error)
}

// AuthHandler は認証エンドポイント（Design.md §3.2）を提供する。
type AuthHandler struct {
	WebAuthn   WebAuthnService
	Sessions   SessionService
	Users      UserGetter
	Limiter    Limiter
	Bootstrap  BootstrapService // nil なら機能無効（features.passwordBootstrap）
	Enrollment EnrollmentService
	CookieName string
}

// limiterKey はレート制限のキー（クライアント IP 単位）。本アプリは
// 単一の信頼できるリバースプロキシ（Host Apache。CLAUDE.md §1）の背後に
// 置かれるため、RemoteAddr は常にプロキシのアドレスになる。プロキシが
// 付与する X-Forwarded-For の最右要素が実クライアント IP であり、
// クライアントが偽装した左側の値には影響されない。
func limiterKey(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RegisterRoutes は認証ルートを mux に登録する。
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register/begin", h.registerBegin)
	mux.HandleFunc("POST /api/auth/register/finish", h.registerFinish)
	mux.HandleFunc("POST /api/auth/login/begin", h.loginBegin)
	mux.HandleFunc("POST /api/auth/login/finish", h.loginFinish)
	mux.HandleFunc("GET /api/auth/me", h.me)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	mux.HandleFunc("POST /api/auth/bootstrap", h.bootstrap)
	mux.HandleFunc("POST /api/auth/enrollment-code", h.issueEnrollmentCode)
	mux.HandleFunc("POST /api/auth/enroll", h.enroll)
}

// issueEnrollmentCode はログイン済み端末から 6 桁コードを発行する
// （Design.md §3.4 手順 3）。
func (h *AuthHandler) issueEnrollmentCode(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	code, expiresAt, err := h.Enrollment.IssueCode(r.Context(), sess.UserID)
	if err != nil {
		WriteError(w, CodeInternalError, "code issue failed")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"code":      code,
		"expiresAt": expiresAt.Format(time.RFC3339),
	})
}

// enroll は新しい端末がコードと引き換えに登録セレモニーを開始する
// （Design.md §3.4 手順 4-5）。
func (h *AuthHandler) enroll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" {
		WriteError(w, CodeInvalidRequest, "code is required")
		return
	}
	key := "enroll:" + limiterKey(r)
	if h.Limiter != nil && !h.Limiter.Allow(key) {
		WriteError(w, CodeRateLimited, "too many failed attempts")
		return
	}
	options, challengeID, err := h.Enrollment.Redeem(r.Context(), body.Code)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			if h.Limiter != nil {
				h.Limiter.Fail(key)
			}
			WriteError(w, CodeInvalidRequest, "invalid enrollment code")
			return
		}
		WriteError(w, CodeInternalError, "enrollment failed")
		return
	}
	if h.Limiter != nil {
		h.Limiter.Succeed(key)
	}
	WriteJSON(w, http.StatusOK, ceremonyResponse{ChallengeID: challengeID, Options: options})
}

// bootstrap は Redmine 認証情報での初回登録（Design.md §3.3）。
// 成功すると登録セレモニーの開始情報を返す。
func (h *AuthHandler) bootstrap(w http.ResponseWriter, r *http.Request) {
	if h.Bootstrap == nil {
		WriteError(w, CodeNotFound, "password bootstrap is disabled")
		return
	}
	var body struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Login == "" || body.Password == "" {
		WriteError(w, CodeInvalidRequest, "login and password are required")
		return
	}
	// キーは攻撃者が値を選べる login ではなくクライアント IP にする
	// （login キーだと標的ユーザーを狙ったロックアウト DoS が可能）。
	key := "bootstrap:" + limiterKey(r)
	if h.Limiter != nil && !h.Limiter.Allow(key) {
		WriteError(w, CodeRateLimited, "too many failed attempts")
		return
	}
	options, challengeID, err := h.Bootstrap.Run(r.Context(), body.Login, body.Password)
	switch {
	case err == nil:
		if h.Limiter != nil {
			h.Limiter.Succeed(key)
		}
		WriteJSON(w, http.StatusOK, ceremonyResponse{ChallengeID: challengeID, Options: options})
	case errors.Is(err, ErrUnauthenticated):
		if h.Limiter != nil {
			h.Limiter.Fail(key)
		}
		WriteError(w, CodeUnauthenticated, "redmine authentication failed")
	case errors.Is(err, ErrUpstream):
		WriteError(w, CodeUpstreamError, "redmine is unavailable")
	default:
		WriteError(w, CodeInternalError, "bootstrap failed")
	}
}

// me は SPA 起動時に呼ばれる現在セッション情報（Design.md §3.2）。
func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	u, err := h.Users.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		WriteError(w, CodeInternalError, "user lookup failed")
		return
	}
	if u == nil {
		// セッションはあるが利用者が消えている（削除済み）→ 未認証扱い
		http.SetCookie(w, h.Sessions.ClearCookie())
		WriteError(w, CodeUnauthenticated, "user no longer exists")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"userId":       u.ID,
		"redmineLogin": u.RedmineLogin,
		"displayName":  u.DisplayName,
	})
}

// logout はセッションを破棄する。Cookie が無くても冪等に成功する。
func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.CookieName); err == nil && c.Value != "" {
		if err := h.Sessions.Revoke(r.Context(), c.Value); err != nil {
			WriteError(w, CodeInternalError, "logout failed")
			return
		}
	}
	http.SetCookie(w, h.Sessions.ClearCookie())
	WriteJSON(w, http.StatusOK, map[string]bool{"loggedOut": true})
}

// ceremonyResponse は begin 系の共通レスポンス。options はそのまま
// navigator.credentials に渡せる形。
type ceremonyResponse struct {
	ChallengeID string          `json:"challengeId"`
	Options     json.RawMessage `json:"options"`
}

// registerBegin はログイン済み利用者のパスキー追加登録を開始する。
// （未認証の登録開始はブートストラップ／登録コードの経路が担う）
func (h *AuthHandler) registerBegin(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	options, challengeID, err := h.WebAuthn.BeginRegistration(r.Context(), sess.UserID)
	if err != nil {
		WriteError(w, CodeInternalError, "begin registration failed")
		return
	}
	WriteJSON(w, http.StatusOK, ceremonyResponse{ChallengeID: challengeID, Options: options})
}

func (h *AuthHandler) registerFinish(w http.ResponseWriter, r *http.Request) {
	challengeID := r.URL.Query().Get("challengeId")
	if challengeID == "" {
		WriteError(w, CodeInvalidRequest, "challengeId query parameter is required")
		return
	}
	userID, credentialID, err := h.WebAuthn.FinishRegistration(r.Context(), challengeID, r)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			WriteError(w, CodeInvalidRequest, "registration ceremony failed")
			return
		}
		WriteError(w, CodeInternalError, "finish registration failed")
		return
	}
	// 登録完了 = その端末でログイン済みにする（Design.md §3.3 手順 5-6）
	h.issueSession(w, r, userID, credentialID)
}

func (h *AuthHandler) loginBegin(w http.ResponseWriter, r *http.Request) {
	options, challengeID, err := h.WebAuthn.BeginLogin(r.Context())
	if err != nil {
		WriteError(w, CodeInternalError, "begin login failed")
		return
	}
	WriteJSON(w, http.StatusOK, ceremonyResponse{ChallengeID: challengeID, Options: options})
}

func (h *AuthHandler) loginFinish(w http.ResponseWriter, r *http.Request) {
	challengeID := r.URL.Query().Get("challengeId")
	if challengeID == "" {
		WriteError(w, CodeInvalidRequest, "challengeId query parameter is required")
		return
	}
	key := limiterKey(r)
	if h.Limiter != nil && !h.Limiter.Allow(key) {
		WriteError(w, CodeRateLimited, "too many failed login attempts")
		return
	}
	userID, credentialID, err := h.WebAuthn.FinishLogin(r.Context(), challengeID, r)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			if h.Limiter != nil {
				h.Limiter.Fail(key)
			}
			// 認証失敗の詳細は返さない
			WriteError(w, CodeUnauthenticated, "authentication failed")
			return
		}
		WriteError(w, CodeInternalError, "finish login failed")
		return
	}
	if h.Limiter != nil {
		h.Limiter.Succeed(key)
	}
	h.issueSession(w, r, userID, credentialID)
}

func (h *AuthHandler) issueSession(w http.ResponseWriter, r *http.Request, userID string, credentialID []byte) {
	token, err := h.Sessions.Issue(r.Context(), userID, credentialID)
	if err != nil {
		WriteError(w, CodeInternalError, "session issue failed")
		return
	}
	http.SetCookie(w, h.Sessions.Cookie(token))
	WriteJSON(w, http.StatusOK, map[string]string{"userId": userID})
}

// WriteJSON は JSON レスポンスの共通出口。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // ヘッダー送信後のため失敗は握りつぶすほかない
}
