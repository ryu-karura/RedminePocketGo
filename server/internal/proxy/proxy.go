package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
)

// KeyLoader は中継対象ユーザーの API キーを取り出す（credential.Vault が実装）。
type KeyLoader interface {
	LoadAPIKey(ctx context.Context, userID string) (*credential.APIKey, error)
	MarkInvalid(ctx context.Context, userID string) error
}

// headerAPIKey はサーバーが付与する Redmine 認証ヘッダー。受信したら 400。
const headerAPIKey = "X-Redmine-Api-Key"

// stripHeaders は上流へ絶対に転送しないエンドツーエンドヘッダー（Design.md
// §6.3）。ホップバイホップヘッダー（Connection 等）は ReverseProxy が
// 別途除去する。
var stripHeaders = []string{
	"Authorization",
	"Cookie",
	"X-Redmine-Switch-User",
	headerAPIKey,
}

// 上流ステータスを内部エラーへ写像するための番兵（ModifyResponse →
// ErrorHandler で受け渡す）。
var (
	errUpstream401 = errors.New("proxy: upstream 401")
	errUpstream5xx = errors.New("proxy: upstream 5xx")
)

type ctxKey int

const (
	ctxKeyUpstream ctxKey = iota
	ctxKeyUserID
)

type upstreamTarget struct {
	rawPath  string // サブ URI 込みのエスケープ済みパス
	rawQuery string
	apiKey   string
}

// Proxy は許可リストに従って Redmine REST API へ中継する。
// 中継は httputil.ReverseProxy に委ね、ホップバイホップヘッダー除去・
// 応答ヘッダーとエンコーディングの透過・ストリーミングを正しく扱う。
// RoundTripper はリダイレクトを追従しないため、上流の 3xx でヘッダー
// （付与した API キー）が外部へ再送される事故も起きない。
type Proxy struct {
	rp      *httputil.ReverseProxy
	loader  KeyLoader
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

func New(loader KeyLoader, cfg Config) *Proxy {
	// サブ URI 結合はここだけで行う（ハードコード禁止。Design.md §6.1）。
	base, err := url.Parse(strings.TrimSuffix(cfg.BaseURL, "/") + cfg.SubURI)
	if err != nil {
		// config 検証を通っていれば baseURL は妥当。ここで失敗するのは
		// 設定不備なので、起動時にパニックさせず空ホストで無害化する。
		base = &url.URL{}
	}
	p := &Proxy{
		loader:  loader,
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

		apiPath := strings.TrimPrefix(r.URL.Path, prefix)
		if !strings.HasPrefix(apiPath, "/") {
			apiPath = "/" + apiPath
		}
		if !Allowed(r.Method, apiPath) {
			httpapi.WriteError(w, httpapi.CodeNotFound, "no such upstream endpoint")
			return
		}

		key, err := p.loader.LoadAPIKey(r.Context(), sess.UserID)
		if err != nil {
			switch {
			case errors.Is(err, credential.ErrNoCredential):
				httpapi.WriteError(w, httpapi.CodeRedmineCredentialInvalid, "redmine account not linked")
			case errors.Is(err, credential.ErrCredentialInvalid):
				httpapi.WriteError(w, httpapi.CodeRedmineCredentialInvalid, "redmine credential is invalid; re-link required")
			default:
				httpapi.WriteError(w, httpapi.CodeInternalError, "credential load failed")
			}
			return
		}

		// クライアントが送った通りのエスケープを保って上流へ渡す。
		escapedAPIPath := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		if !strings.HasPrefix(escapedAPIPath, "/") {
			escapedAPIPath = "/" + escapedAPIPath
		}

		ctx := r.Context()
		if p.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, p.timeout)
			defer cancel()
		}
		ctx = context.WithValue(ctx, ctxKeyUpstream, upstreamTarget{
			rawPath:  singleJoin(p.base.EscapedPath(), escapedAPIPath),
			rawQuery: r.URL.RawQuery,
			apiKey:   key.Value(),
		})
		ctx = context.WithValue(ctx, ctxKeyUserID, sess.UserID)

		p.rp.ServeHTTP(w, r.WithContext(ctx))
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

	// 転送禁止のエンドツーエンドヘッダーを除去してから API キーを付与する。
	for _, h := range stripHeaders {
		pr.Out.Header.Del(h)
	}
	pr.Out.Header.Set(headerAPIKey, tgt.apiKey)
}

// modifyResponse は上流ステータスを内部エラーへ写像する。番兵を返すと
// ReverseProxy が ErrorHandler を呼ぶ。
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
		// 上流 401 = 保存済みキーが無効。無効化して 409 で再紐付けを促す。
		if userID, ok := r.Context().Value(ctxKeyUserID).(string); ok {
			_ = p.loader.MarkInvalid(r.Context(), userID)
		}
		httpapi.WriteError(w, httpapi.CodeRedmineCredentialInvalid, "redmine credential is invalid; re-link required")
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
