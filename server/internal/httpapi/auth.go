package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

// AuthHandler は認証エンドポイント（Design.md §3.2）を提供する。
type AuthHandler struct {
	WebAuthn WebAuthnService
	Sessions SessionService
}

// RegisterRoutes は認証ルートを mux に登録する。
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register/begin", h.registerBegin)
	mux.HandleFunc("POST /api/auth/register/finish", h.registerFinish)
	mux.HandleFunc("POST /api/auth/login/begin", h.loginBegin)
	mux.HandleFunc("POST /api/auth/login/finish", h.loginFinish)
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
