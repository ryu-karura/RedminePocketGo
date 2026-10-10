package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// UserGetter は利用者情報の参照。*store.Store が実装する。
type UserGetter interface {
	GetUserByID(ctx context.Context, id string) (*store.User, error)
}

// GrantInfoGetter は設定画面向けの Redmine 連携状態の参照。*store.Store が実装する。
// 暗号文を含む行をそのまま返すが、このパッケージは状態・スコープ・更新時刻しか
// 読まず、応答にも載せない。
type GrantInfoGetter interface {
	GetOAuthTokens(ctx context.Context, userID string) (*store.OAuthTokens, error)
}

// LogoutCleaner はログアウト後の Redmine 側の後始末（他の端末のセッションが
// 残っていなければトークンを失効）。失敗してもログアウト自体は成功させるため、
// エラーは返さない（実装側でログに残す）。
type LogoutCleaner interface {
	AfterLogout(ctx context.Context, userID string)
}

// Limiter はログイン試行のレート制限（auth.RateLimiter が実装する）。
type Limiter interface {
	Allow(key string) bool
	Fail(key string)
	Succeed(key string)
}

// AuthHandler は現在のセッションに関するエンドポイント（Design.md §3.3）。
// ログインそのもの（login / callback）は OAuthHandler が担う。
type AuthHandler struct {
	Sessions OAuthSessions
	Users    UserGetter
	Grants   GrantInfoGetter // nil なら me() の redmineStatus は常に unlinked
	Cleanup  LogoutCleaner   // nil なら後始末なし
	Logger   *slog.Logger

	CookieName string
	// LoginPath は GET /api/auth/login の公開パス（baseURL 込み）。再認可の
	// 遷移先 URL を組み立てるのに使う。
	LoginPath string
}

// clientIP はレート制限のキー（クライアント IP 単位）。直接の接続元が
// 信頼するプロキシ（Host Apache。CLAUDE.md §1）のときだけ、プロキシが
// 付与した X-Forwarded-For の最右要素を実クライアント IP とする。それ以外
// （プロキシを経由せず直接つないできた相手）のヘッダーは偽装できるため
// 無視し、接続元アドレスをそのまま使う。
func clientIP(r *http.Request, trusted []*net.IPNet) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	pip := net.ParseIP(peer)
	if pip == nil || !ipInNets(pip, trusted) {
		return peer
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
			return ip.String()
		}
	}
	return peer
}

func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// RegisterRoutes は認証関連ルートを mux に登録する。
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/me", h.me)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	mux.HandleFunc("POST /api/auth/reauthorize", h.reauthorize)
}

func (h *AuthHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// me は SPA 起動時に呼ばれる現在セッション情報（Design.md §3.3）。
func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	sess := SessionFrom(r.Context())
	if sess == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	u, err := h.Users.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		h.logger().Error("me: user lookup failed", "error", err)
		WriteError(w, CodeInternalError, "user lookup failed")
		return
	}
	if u == nil {
		// セッションはあるが利用者が消えている（削除済み）→ 未認証扱い
		http.SetCookie(w, h.Sessions.ClearCookie())
		WriteError(w, CodeUnauthenticated, "user no longer exists")
		return
	}
	status, scopes, refreshedAt := h.grant(r.Context(), u.ID)
	resp := map[string]any{
		"userId":        u.ID,
		"redmineLogin":  u.RedmineLogin,
		"displayName":   u.DisplayName,
		"redmineStatus": status,
		"redmineScopes": scopes,
	}
	if !refreshedAt.IsZero() {
		resp["redmineRefreshedAt"] = refreshedAt.UTC().Format(time.RFC3339)
	}
	WriteJSON(w, http.StatusOK, resp)
}

// grant は設定画面向けの Redmine 連携状態（"active" / "invalid" / "unlinked"）、
// 付与スコープ、最終更新時刻。参照に失敗しても me() 全体は失敗させず unlinked
// 扱いにする（連携状態はあくまで表示用の付随情報のため）。
func (h *AuthHandler) grant(ctx context.Context, userID string) (status string, scopes []string, refreshedAt time.Time) {
	if h.Grants == nil {
		return "unlinked", []string{}, time.Time{}
	}
	t, err := h.Grants.GetOAuthTokens(ctx, userID)
	if err != nil {
		h.logger().Warn("me: grant lookup failed", "error", err)
		return "unlinked", []string{}, time.Time{}
	}
	if t == nil {
		return "unlinked", []string{}, time.Time{}
	}
	scopes = strings.Fields(t.Scopes)
	if scopes == nil {
		scopes = []string{}
	}
	return t.Status, scopes, t.RefreshedAt
}

// logout はセッションを破棄する。Cookie が無くても冪等に成功する。
// 破棄の後、他の端末のセッションが残っていなければ Redmine 側のトークンも
// 失効させる（Cleanup）。トークンは利用者単位で端末間共有のため、残っている
// 間は失効させない。
func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.CookieName); err == nil && c.Value != "" {
		if err := h.Sessions.Revoke(r.Context(), c.Value); err != nil {
			h.logger().Error("logout: revoke failed", "error", err)
			WriteError(w, CodeInternalError, "logout failed")
			return
		}
	}
	http.SetCookie(w, h.Sessions.ClearCookie())
	if sess := SessionFrom(r.Context()); sess != nil && h.Cleanup != nil {
		h.Cleanup.AfterLogout(r.Context(), sess.UserID)
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"loggedOut": true})
}

// reauthorize はトークン無効（redmine_credential_invalid）時の再認可の入口。
// SPA が返された URL へ遷移すると、通常のログインフローが再実行される。
func (h *AuthHandler) reauthorize(w http.ResponseWriter, r *http.Request) {
	if SessionFrom(r.Context()) == nil {
		WriteError(w, CodeUnauthenticated, "login required")
		return
	}
	returnTo := "#settings"
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<10))
		if err != nil {
			WriteError(w, CodeInvalidRequest, "unreadable body")
			return
		}
		if len(strings.TrimSpace(string(b))) > 0 {
			var body struct {
				Return string `json:"return"`
			}
			if err := json.Unmarshal(b, &body); err != nil {
				WriteError(w, CodeInvalidRequest, "malformed JSON body")
				return
			}
			if body.Return != "" {
				returnTo = body.Return // 許可リストでの検証は Begin 側で行う
			}
		}
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"loginUrl": h.LoginPath + "?return=" + url.QueryEscape(returnTo),
	})
}

// WriteJSON は JSON レスポンスの共通出口。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // ヘッダー送信後のため失敗は握りつぶすほかない
}
