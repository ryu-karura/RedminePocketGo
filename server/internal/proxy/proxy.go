package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

// TokenSource は中継対象ユーザーの OAuth アクセストークンを供給する
// （credential.Manager が実装。Design.md §4.4）。
type TokenSource interface {
	// AccessToken は有効なアクセストークンを返す（期限が近ければ更新済み）。
	AccessToken(ctx context.Context, userID string) (string, error)
	// ForceRefresh は上流が staleAccess を 401 で拒否したときに呼ぶ。
	ForceRefresh(ctx context.Context, userID, staleAccess string) (string, error)
	// MarkInvalid は更新後も上流に拒否される組を無効にする（再認可を求める）。
	MarkInvalid(ctx context.Context, userID string) error
}

// maxBodyBytes は中継する要求本文の上限。許可リストの書き込みは小さな JSON
// だけで、上流 401 後の再送のために本文をメモリに保持する。
const maxBodyBytes = 4 << 20

// headerAPIKey は受信したら 400 で拒否するヘッダー。サーバー自身も送らない
// （Redmine の API キーは一切使わない。CLAUDE.md §9-1）。
const headerAPIKey = "X-Redmine-Api-Key"

// stripHeaders は上流へ絶対に転送しないエンドツーエンドヘッダー（Design.md
// §6.3）。Authorization は利用者のものを除去し、サーバーが Bearer を付け直す。
// ホップバイホップヘッダー（Connection 等）は ReverseProxy が別途除去する。
var stripHeaders = []string{
	"Authorization",
	"Cookie",
	"X-Redmine-Switch-User",
	headerAPIKey,
}

// errUpstream5xx は上流 5xx を ErrorHandler へ渡すための番兵。
var errUpstream5xx = errors.New("proxy: upstream 5xx")

// errUpstream401 は上流 401 の番兵。ErrorHandler は何も書かずに記録だけし、
// Handler が更新して 1 回だけ再試行する（まだ何もクライアントへ書いていない）。
var errUpstream401 = errors.New("proxy: upstream 401")

type ctxKey int

const (
	ctxKeyUpstream ctxKey = iota
	ctxKeyAttempt
)

type upstreamTarget struct {
	rawPath     string // サブ URI 込みのエスケープ済みパス
	rawQuery    string
	accessToken string
}

// attempt は 1 回の中継の結果を ErrorHandler から Handler へ持ち帰る。
type attempt struct{ unauthorized bool }

// Proxy は許可リストに従って Redmine REST API へ中継する。
// 中継は httputil.ReverseProxy に委ね、ホップバイホップヘッダー除去・
// 応答ヘッダーとエンコーディングの透過・ストリーミングを正しく扱う。
// RoundTripper はリダイレクトを追従しないため、上流の 3xx でヘッダー
// （付与したアクセストークン）が外部へ再送される事故も起きない。
type Proxy struct {
	rp      *httputil.ReverseProxy
	tokens  TokenSource
	base    *url.URL // baseURL + subURI を結合した上流ルート
	subURI  string
	timeout time.Duration
}

// Config は中継の設定（config.Redmine から組み立てる）。
type Config struct {
	BaseURL string
	SubURI  string
	Timeout time.Duration
}

func New(tokens TokenSource, cfg Config) *Proxy {
	// サブ URI 結合はここだけで行う（ハードコード禁止。Design.md §6.1）。
	base, err := url.Parse(strings.TrimSuffix(cfg.BaseURL, "/") + cfg.SubURI)
	if err != nil {
		// config 検証を通っていれば baseURL は妥当。ここで失敗するのは
		// 設定不備なので、起動時にパニックさせず空ホストで無害化する。
		base = &url.URL{}
	}
	p := &Proxy{
		tokens:  tokens,
		base:    base,
		subURI:  cfg.SubURI,
		timeout: cfg.Timeout,
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		ModifyResponse: modifyResponse,
		ErrorHandler:   p.errorHandler,
	}
	return p
}

// Handler は /api/redmine/ 配下のリクエストを中継する http.Handler を返す。
// prefix は API パスの前置（例 "/api/redmine"）。認証済みであることは
// 上位のミドルウェアが保証し、SessionFrom で利用者を得る。
func (p *Proxy) Handler(prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := httpapi.SessionFrom(r.Context())
		if sess == nil {
			httpapi.WriteError(w, httpapi.CodeUnauthenticated, "login required")
			return
		}

		// クライアントが API キーを注入してくる攻撃を弾く（Design.md §6.3）
		if r.Header.Get(headerAPIKey) != "" {
			httpapi.WriteError(w, httpapi.CodeInvalidRequest, "X-Redmine-API-Key must not be supplied by the client")
			return
		}

		// クエリ文字列の key= も Redmine は API キーとして受け付けうる。
		// ヘッダー同様に拒否する（API キー禁止。CLAUDE.md §9-1）。
		for name := range r.URL.Query() {
			if strings.EqualFold(name, "key") {
				httpapi.WriteError(w, httpapi.CodeInvalidRequest, "the key query parameter must not be supplied by the client")
				return
			}
		}

		apiPath := strings.TrimPrefix(r.URL.Path, prefix)
		if !strings.HasPrefix(apiPath, "/") {
			apiPath = "/" + apiPath
		}
		if !Allowed(r.Method, apiPath) {
			httpapi.WriteError(w, httpapi.CodeNotFound, "no such upstream endpoint")
			return
		}

		// 上流 401 の後に同じ要求を再送できるよう、本文は先に読み切る。
		var body []byte
		if r.Body != nil && r.Body != http.NoBody {
			var err error
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
			if err != nil {
				httpapi.WriteError(w, httpapi.CodeInvalidRequest, "request body is too large or unreadable")
				return
			}
		}

		token, err := p.tokens.AccessToken(r.Context(), sess.UserID)
		if err != nil {
			p.writeTokenError(w, err, "token load failed")
			return
		}

		// クライアントが送った通りのエスケープを保って上流へ渡す。
		escapedAPIPath := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		if !strings.HasPrefix(escapedAPIPath, "/") {
			escapedAPIPath = "/" + escapedAPIPath
		}
		target := upstreamTarget{
			rawPath:  singleJoin(p.base.EscapedPath(), escapedAPIPath),
			rawQuery: r.URL.RawQuery,
		}

		// 1 回目。上流 401（アクセストークンの失効）なら、更新して 1 回だけ再試行する。
		if !p.relay(w, r, body, target, token) {
			return
		}
		token, err = p.tokens.ForceRefresh(r.Context(), sess.UserID, token)
		if err != nil {
			p.writeTokenError(w, err, "token refresh failed")
			return
		}
		if !p.relay(w, r, body, target, token) {
			return
		}
		// 更新したばかりのトークンまで拒否される: 組を無効にして再認可を求める。
		if err := p.tokens.MarkInvalid(r.Context(), sess.UserID); err != nil {
			slog.Warn("marking OAuth tokens invalid failed", "error", err)
		}
		httpapi.WriteError(w, httpapi.CodeRedmineCredentialInvalid, "redmine rejected the refreshed token; re-authorization required")
	}
}

// relay は 1 回分の中継を行う。上流が 401 を返し、まだクライアントへ何も
// 書いていない場合に限って true（再試行可）を返す。それ以外は応答を書き終えている。
func (p *Proxy) relay(w http.ResponseWriter, r *http.Request, body []byte, target upstreamTarget, token string) (unauthorized bool) {
	ctx := r.Context()
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	att := &attempt{}
	target.accessToken = token
	ctx = context.WithValue(ctx, ctxKeyUpstream, target)
	ctx = context.WithValue(ctx, ctxKeyAttempt, att)

	req := r.Clone(ctx)
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	p.rp.ServeHTTP(w, req)
	return att.unauthorized
}

// writeTokenError はトークン取得・更新の失敗をエンベロープへ写像する。
func (p *Proxy) writeTokenError(w http.ResponseWriter, err error, internalMsg string) {
	switch {
	case errors.Is(err, credential.ErrNoCredential):
		httpapi.WriteError(w, httpapi.CodeRedmineCredentialInvalid, "redmine account not linked")
	case errors.Is(err, credential.ErrCredentialInvalid):
		httpapi.WriteError(w, httpapi.CodeRedmineCredentialInvalid, "redmine credential is invalid; re-authorization required")
	case errors.Is(err, redmine.ErrUpstream):
		httpapi.WriteError(w, httpapi.CodeUpstreamError, "redmine upstream error during token refresh")
	default:
		// クライアント設定不備・保存失敗・想定外。詳細はログのみ。
		slog.Error("token handling failed in relay", "error", err)
		httpapi.WriteError(w, httpapi.CodeInternalError, internalMsg)
	}
}

func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	tgt := pr.In.Context().Value(ctxKeyUpstream).(upstreamTarget)

	out := *p.base
	out.RawPath = tgt.rawPath
	if decoded, err := url.PathUnescape(tgt.rawPath); err == nil {
		out.Path = decoded
	} else {
		out.Path = tgt.rawPath
	}
	out.RawQuery = tgt.rawQuery
	pr.Out.URL = &out
	pr.Out.Host = p.base.Host

	// 転送禁止のエンドツーエンドヘッダーを除去してから Bearer を付与する。
	for _, h := range stripHeaders {
		pr.Out.Header.Del(h)
	}
	pr.Out.Header.Set("Authorization", "Bearer "+tgt.accessToken)
}

// modifyResponse は上流ステータスを内部エラーへ写像する。番兵を返すと
// ReverseProxy が ErrorHandler を呼ぶ（その時点で応答はまだ書かれていない）。
func modifyResponse(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errUpstream401
	case resp.StatusCode >= 500:
		return errUpstream5xx
	}
	return nil
}

func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errUpstream401):
		// 何も書かずに記録だけ。Handler が更新して再試行するか、409 を返す。
		if att, ok := r.Context().Value(ctxKeyAttempt).(*attempt); ok {
			att.unauthorized = true
		}
	case errors.Is(err, errUpstream5xx):
		httpapi.WriteError(w, httpapi.CodeUpstreamError, "redmine upstream error")
	default:
		// 接続失敗・タイムアウトなど
		httpapi.WriteError(w, httpapi.CodeUpstreamError, "redmine request failed")
	}
}

// singleJoin は 2 つのパス片を 1 つのスラッシュで連結する
// （二重スラッシュ・スラッシュ欠落を防ぐ）。
func singleJoin(a, b string) string {
	as := strings.HasSuffix(a, "/")
	bs := strings.HasPrefix(b, "/")
	switch {
	case as && bs:
		return a + b[1:]
	case !as && !bs:
		return a + "/" + b
	default:
		return a + b
	}
}
