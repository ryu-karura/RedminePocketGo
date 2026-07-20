package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// 受信を拒否・上流へ転送しないヘッダー（Design.md §6.3）。
// X-Redmine-API-Key はサーバーが付与する。受信したら 400。
const headerAPIKey = "X-Redmine-Api-Key"

// stripHeaders は上流へ絶対に転送しないヘッダー。
var stripHeaders = []string{
	"Authorization",
	"Cookie",
	"X-Redmine-Switch-User",
	headerAPIKey,
}

// Proxy は許可リストに従って Redmine REST API へ中継する。
type Proxy struct {
	client  *http.Client
	loader  KeyLoader
	baseURL string // redmine.baseURL（末尾スラッシュなし）
	subURI  string // redmine.subURI（例 /redmine）
}

// Config は中継の設定（config.Redmine から組み立てる）。
type Config struct {
	BaseURL string
	SubURI  string
	Timeout time.Duration
}

func New(loader KeyLoader, cfg Config) *Proxy {
	return &Proxy{
		client:  &http.Client{Timeout: cfg.Timeout},
		loader:  loader,
		baseURL: strings.TrimSuffix(cfg.BaseURL, "/"),
		subURI:  cfg.SubURI,
	}
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

		p.relay(w, r, apiPath, sess.UserID, key)
	}
}

func (p *Proxy) relay(w http.ResponseWriter, r *http.Request, apiPath, userID string, key *credential.APIKey) {
	// サブ URI 結合はここだけで行う（ハードコード禁止。Design.md §6.1）
	upstreamURL := p.baseURL + p.subURI + apiPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		httpapi.WriteError(w, httpapi.CodeInternalError, "upstream request build failed")
		return
	}

	// 転送禁止ヘッダーを除いて透過し、必要なものだけ引き継ぐ
	copyForwardableHeaders(req.Header, r.Header)
	// サーバーだけが API キーを付与する
	req.Header.Set(headerAPIKey, key.Value())

	resp, err := p.client.Do(req)
	if err != nil {
		httpapi.WriteError(w, httpapi.CodeUpstreamError, "redmine request failed")
		return
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		// 上流 401 = 保存済みキーが無効。無効化して 409 で再紐付けを促す。
		if err := p.loader.MarkInvalid(r.Context(), userID); err != nil {
			httpapi.WriteError(w, httpapi.CodeInternalError, "failed to mark credential invalid")
			return
		}
		httpapi.WriteError(w, httpapi.CodeRedmineCredentialInvalid, "redmine credential is invalid; re-link required")
		return
	case resp.StatusCode >= 500:
		httpapi.WriteError(w, httpapi.CodeUpstreamError, fmt.Sprintf("redmine upstream error (status %d)", resp.StatusCode))
		return
	}

	// 正常応答はステータス・Content-Type を引き継いでそのまま返す
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// copyForwardableHeaders は転送禁止ヘッダーを除いて上流へ引き継ぐ。
func copyForwardableHeaders(dst, src http.Header) {
	for name, values := range src {
		if isStripped(name) {
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

func isStripped(name string) bool {
	for _, s := range stripHeaders {
		if http.CanonicalHeaderKey(name) == http.CanonicalHeaderKey(s) {
			return true
		}
	}
	return false
}
