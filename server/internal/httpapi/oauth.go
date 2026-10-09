package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthLoginService は internal/auth の OAuthLogin が実装する。
type OAuthLoginService interface {
	Begin(ctx context.Context, returnTo string) (authorizeURL, state string, err error)
	Complete(ctx context.Context, code, state string) (sessionToken, returnTo string, err error)
}

// OAuthSessions はコールバックで使うセッション操作（*auth.Sessions が実装）。
type OAuthSessions interface {
	Revoke(ctx context.Context, token string) error
	Cookie(token string) *http.Cookie
	ClearCookie() *http.Cookie
}

// loginCoder は auth.LoginError が満たす。httpapi が auth を import せずに
// 失敗分類を取り出すための口。
type loginCoder interface{ LoginCode() string }

// OAuthHandler は OAuth ログインの 2 つの GET エンドポイント（Design.md §3.3）。
// どちらもブラウザのページ遷移で使うため JSON ではなくリダイレクトで応答し、
// 失敗は `<AppURL>#login?error=<code>` で SPA に伝える。
type OAuthHandler struct {
	Login    OAuthLoginService
	Sessions OAuthSessions
	Limiter  Limiter
	Logger   *slog.Logger

	SessionCookieName string

	// state Cookie: 認可要求を始めたブラウザとコールバックを受けるブラウザが
	// 同一であることの確認（ログイン CSRF 対策）。
	StateCookieName   string
	StateCookiePath   string
	StateCookieSecure bool
	StateCookieTTL    time.Duration

	// AppURL は SPA の URL（baseURL + "/"。例 "/" や "/app/"）。
	AppURL string
}

func (h *OAuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/login", h.login)
	mux.HandleFunc("GET /api/auth/callback", h.callback)
}

func (h *OAuthHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func (h *OAuthHandler) redirectError(w http.ResponseWriter, r *http.Request, code string) {
	h.redirect(w, r, h.AppURL+"#login?error="+url.QueryEscape(code))
}

func (h *OAuthHandler) redirect(w http.ResponseWriter, r *http.Request, target string) {
	// 認可コードや state を含む URL・応答はキャッシュさせない。
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *OAuthHandler) stateCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: h.StateCookieName, Value: value, Path: h.StateCookiePath,
		HttpOnly: true, Secure: h.StateCookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	}
}

func (h *OAuthHandler) login(w http.ResponseWriter, r *http.Request) {
	key := limiterKey(r)
	if h.Limiter != nil && !h.Limiter.Allow(key) {
		h.redirectError(w, r, "rate_limited")
		return
	}
	authorizeURL, state, err := h.Login.Begin(r.Context(), r.URL.Query().Get("return"))
	if err != nil {
		h.logger().Error("oauth login begin failed", "error", err)
		h.redirectError(w, r, "server_error")
		return
	}
	http.SetCookie(w, h.stateCookie(state, int(h.StateCookieTTL/time.Second)))
	h.redirect(w, r, authorizeURL)
}

func (h *OAuthHandler) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// state Cookie は使い捨て。ただし発行元ブラウザの state と一致したときだけ消す。
	// 無条件に消すと、他サイトからの GET で進行中のログインを壊せてしまう。
	if h.stateCookieMatches(r, q.Get("state")) {
		http.SetCookie(w, h.stateCookie("", -1))
	}
	key := limiterKey(r)
	if h.Limiter != nil && !h.Limiter.Allow(key) {
		h.redirectError(w, r, "rate_limited")
		return
	}

	// Redmine が拒否・失敗を返した場合（利用者の同意拒否など）。値は
	// 反射せず、既知のコードへ写像する。連続失敗には数えない。
	if e := q.Get("error"); e != "" {
		switch e {
		case "access_denied":
			h.redirectError(w, r, "access_denied")
		case "server_error", "temporarily_unavailable":
			h.redirectError(w, r, "redmine_unavailable")
		default:
			h.redirectError(w, r, "exchange_failed")
		}
		return
	}

	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" || !h.stateCookieMatches(r, state) {
		// state を消費する前に弾く（他人の state を焼かせない）。
		h.fail(w, r, key, "invalid_state")
		return
	}

	token, returnTo, err := h.Login.Complete(r.Context(), code, state)
	if err != nil {
		var lc loginCoder
		if !errors.As(err, &lc) {
			h.logger().Error("oauth login failed unexpectedly", "error", err)
			h.redirectError(w, r, "server_error")
			return
		}
		h.logger().Warn("oauth login failed", "code", lc.LoginCode(), "error", err)
		switch lc.LoginCode() {
		case "invalid_state", "exchange_failed":
			h.fail(w, r, key, lc.LoginCode()) // 総当たりの兆候としてレート制限に数える
		default: // redmine_unavailable / server_misconfigured など利用者起因でないもの
			h.redirectError(w, r, lc.LoginCode())
		}
		return
	}

	// セッション固定化対策: 以前のセッションがあれば失効させ、必ず新しいものを使う。
	if old, err := r.Cookie(h.SessionCookieName); err == nil && old.Value != "" {
		if err := h.Sessions.Revoke(r.Context(), old.Value); err != nil {
			h.logger().Warn("revoking the previous session failed", "error", err)
		}
	}
	http.SetCookie(w, h.Sessions.Cookie(token))
	if h.Limiter != nil {
		h.Limiter.Succeed(key)
	}
	// 戻り先は Begin で画面ハッシュに限定済みだが、念のため再確認する。
	if !strings.HasPrefix(returnTo, "#") {
		returnTo = "#projects"
	}
	h.redirect(w, r, h.AppURL+returnTo)
}

func (h *OAuthHandler) fail(w http.ResponseWriter, r *http.Request, key, code string) {
	if h.Limiter != nil {
		h.Limiter.Fail(key)
	}
	h.redirectError(w, r, code)
}

// stateCookieMatches は state Cookie がクエリの state と一致するか（定数時間）。
func (h *OAuthHandler) stateCookieMatches(r *http.Request, state string) bool {
	c, err := r.Cookie(h.StateCookieName)
	if err != nil || c.Value == "" || len(c.Value) != len(state) {
		return false
	}
	var diff byte
	for i := 0; i < len(state); i++ {
		diff |= c.Value[i] ^ state[i]
	}
	return diff == 0
}
