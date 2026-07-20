package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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

// AuthHandler は認証エンドポイント（Design.md §3.2）を提供する。
type AuthHandler struct {
	WebAuthn   WebAuthnService
	Sessions   SessionService
	Users      UserGetter
	CookieName string
}

// RegisterRoutes は認証ルートを mux に登録する。
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register/begin", h.registerBegin)
	mux.HandleFunc("POST /api/auth/register/finish", h.registerFinish)
	mux.HandleFunc("POST /api/auth/login/begin", h.loginBegin)
	mux.HandleFunc("POST /api/auth/login/finish", h.loginFinish)
	mux.HandleFunc("GET /api/auth/me", h.me)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
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
	userID, credentialID, err := h.WebAuthn.FinishLogin(r.Context(), challengeID, r)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			// 認証失敗の詳細は返さない
			WriteError(w, CodeUnauthenticated, "authentication failed")
			return
		}
		WriteError(w, CodeInternalError, "finish login failed")
		return
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
