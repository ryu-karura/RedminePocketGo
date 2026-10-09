package redmine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuth 2.0（認可コード + PKCE）の Redmine 側エンドポイント呼び出し
// （Design.md §3、実機探査の結果は §14）。

var (
	// ErrInvalidGrant は認可コード・リフレッシュトークンが確定的に無効
	// （invalid_grant）。リフレッシュでは、利用者が Redmine で取り消した、
	// 管理者がアプリを削除した、回転を取りこぼした、のいずれか——保存済みの
	// 組を無効化して再認可を求める（Design.md §4.4）。
	ErrInvalidGrant = errors.New("redmine: OAuth グラントが無効です (invalid_grant)")

	// ErrInvalidClient はアプリ側の設定不備（Client ID / Secret の誤り、
	// アプリの削除）。利用者のトークンの問題ではないので、保存済みの組を
	// 無効化してはならない（運用者の対応事項）。
	ErrInvalidClient = errors.New("redmine: OAuth クライアント認証に失敗しました (invalid_client)")

	// ErrOAuthRejected は上記以外の 4xx（invalid_request / invalid_scope 等）。
	// 要求側の不備であり、再試行しても直らない。
	ErrOAuthRejected = errors.New("redmine: OAuth 要求が拒否されました")
)

// OAuthConfig は Redmine に登録したアプリケーションの情報。
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scopes       []string
	// PublicBaseURL はブラウザから見える Redmine の起点 URL（末尾スラッシュなし）。
	// サーバー間通信の BaseURL と異なりうる。
	PublicBaseURL string
}

// OAuth は Client に OAuth のエンドポイント呼び出しを足したもの。
type OAuth struct {
	c   *Client
	cfg OAuthConfig
	now func() time.Time
}

// OAuth は設定を束ねた OAuth 呼び出し口を返す。
func (c *Client) OAuth(cfg OAuthConfig) *OAuth {
	return &OAuth{c: c, cfg: cfg, now: time.Now}
}

// AuthorizeURL は利用者のブラウザを遷移させる認可エンドポイントの URL。
// PKCE は常に S256（Redmine は強制しないが、付ければ検証される）。
// redirect_uri は設定から固定で与え、リクエストから組み立てない。
func (o *OAuth) AuthorizeURL(state, codeChallenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", o.cfg.ClientID)
	q.Set("redirect_uri", o.cfg.RedirectURI)
	q.Set("scope", strings.Join(o.cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return strings.TrimRight(o.cfg.PublicBaseURL, "/") + o.c.subURI + "/oauth/authorize?" + q.Encode()
}

// TokenSet はトークンエンドポイントの成功応答。トークンを含むため、
// 文字列化・JSON 化・ログ出力のいずれでも内容を出さない（CLAUDE.md §4.4）。
type TokenSet struct {
	AccessToken     string
	RefreshToken    string
	Scopes          []string
	AccessExpiresAt time.Time
}

func (TokenSet) String() string               { return "[redacted]" }
func (TokenSet) GoString() string             { return "redmine.TokenSet{[redacted]}" }
func (TokenSet) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }
func (TokenSet) LogValue() slog.Value         { return slog.StringValue("[redacted]") }

// Exchange は認可コードをトークンに交換する。再試行はしない。
func (o *OAuth) Exchange(ctx context.Context, code, codeVerifier string) (*TokenSet, error) {
	return o.tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {o.cfg.RedirectURI},
		"code_verifier": {codeVerifier},
	})
}

// Refresh はリフレッシュトークンで新しい組を得る。Redmine はリフレッシュの
// たびにリフレッシュトークンを入れ替え、古いものは即座に無効になる。成功
// 応答を失ったまま再送すると連鎖が壊れるため、再試行は絶対にしない。
// 呼び出し側は、新しい組を永続化してから使うこと（Design.md §4.4）。
func (o *OAuth) Refresh(ctx context.Context, refreshToken string) (*TokenSet, error) {
	return o.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	})
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type oauthErrorResponse struct {
	Error string `json:"error"`
}

func (o *OAuth) tokenRequest(ctx context.Context, form url.Values) (*TokenSet, error) {
	body, status, err := o.post(ctx, "/oauth/token", form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, classifyOAuthError(status, body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("%w: トークン応答を解釈できません", ErrUpstream)
	}
	// 回転を取りこぼさないため、リフレッシュトークンが無い応答は成功扱いにしない。
	if tr.AccessToken == "" || tr.RefreshToken == "" {
		return nil, fmt.Errorf("%w: トークン応答にアクセストークンまたはリフレッシュトークンがありません", ErrUpstream)
	}
	return &TokenSet{
		AccessToken:     tr.AccessToken,
		RefreshToken:    tr.RefreshToken,
		Scopes:          strings.Fields(tr.Scope),
		AccessExpiresAt: o.now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}, nil
}

// Revoke はトークンを Redmine 側で失効させる（ログアウト用）。Doorkeeper は
// 未知のトークンでも 200 を返す。
func (o *OAuth) Revoke(ctx context.Context, token, tokenTypeHint string) error {
	body, status, err := o.post(ctx, "/oauth/revoke", url.Values{
		"token":           {token},
		"token_type_hint": {tokenTypeHint},
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		if e := classifyOAuthError(status, body); errors.Is(e, ErrInvalidClient) {
			return e
		}
		return fmt.Errorf("%w: 失効要求が status %d を返しました", ErrUpstream, status)
	}
	return nil
}

// post は client_id / client_secret を付けてフォームを POST する。再試行しない。
// 応答本文は呼び出し側で解釈し、エラー文にも載せない（トークンを含みうる）。
func (o *OAuth) post(ctx context.Context, path string, form url.Values) ([]byte, int, error) {
	form.Set("client_id", o.cfg.ClientID)
	form.Set("client_secret", o.cfg.ClientSecret)

	o.c.sem <- struct{}{}
	defer func() { <-o.c.sem }()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.c.root+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, fmt.Errorf("redmine: リクエスト作成に失敗しました: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, fmt.Errorf("redmine: リクエストが中断されました: %w", ctxErr)
		}
		// err には URL（クエリなし）しか含まれない。フォームは載らない。
		return nil, 0, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: 応答の読み取りに失敗しました: %w", ErrUpstream, err)
	}
	return b, resp.StatusCode, nil
}

func classifyOAuthError(status int, body []byte) error {
	if status >= 500 {
		return fmt.Errorf("%w: 上流が status %d を返しました", ErrUpstream, status)
	}
	var e oauthErrorResponse
	_ = json.Unmarshal(body, &e)
	switch e.Error {
	case "invalid_grant":
		return ErrInvalidGrant
	case "invalid_client", "unauthorized_client":
		return ErrInvalidClient
	}
	if status >= 400 && status < 500 {
		return fmt.Errorf("%w: status %d, error=%q", ErrOAuthRejected, status, e.Error)
	}
	return fmt.Errorf("%w: 上流が status %d を返しました", ErrUpstream, status)
}

// CurrentUser は Bearer トークンの持ち主（Redmine 側の利用者）。
// 利用者の同一性の鍵は ID（ログイン名は変わりうる）。
type CurrentUser struct {
	ID          int64
	Login       string
	DisplayName string
}

// CurrentUser は GET /users/current.json でトークンの持ち主を特定する。
// 応答に含まれうる api_key 等は読み取らない（CLAUDE.md §9-1）。
func (c *Client) CurrentUser(ctx context.Context, accessToken string) (*CurrentUser, error) {
	var wrap struct {
		User struct {
			ID        int64  `json:"id"`
			Login     string `json:"login"`
			Firstname string `json:"firstname"`
			Lastname  string `json:"lastname"`
		} `json:"user"`
	}
	if err := c.get(ctx, accessToken, "/users/current.json", nil, &wrap); err != nil {
		return nil, err
	}
	u := wrap.User
	if u.ID <= 0 || u.Login == "" {
		return nil, fmt.Errorf("%w: /users/current.json の応答に id または login がありません", ErrUpstream)
	}
	return &CurrentUser{
		ID:          u.ID,
		Login:       u.Login,
		DisplayName: strings.TrimSpace(u.Firstname + " " + u.Lastname),
	}, nil
}
