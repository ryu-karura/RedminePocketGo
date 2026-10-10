package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// Redmine を OAuth 2.0 認可サーバーとするログイン（認可コード + PKCE。
// Design.md §3.3, §3.4）。利用者のパスワードにも API キーにも触れない。

// ログイン失敗の分類。画面へは `#login?error=<code>` で渡る（Design.md §3.4）。
const (
	CodeInvalidState        = "invalid_state"        // state が不明・期限切れ・使用済み
	CodeExchangeFailed      = "exchange_failed"      // コード交換や利用者特定が拒否された
	CodeRedmineUnavailable  = "redmine_unavailable"  // Redmine に到達できない／障害
	CodeServerMisconfigured = "server_misconfigured" // 登録情報（Client ID / Secret）の不備
	CodeAccessDenied        = "access_denied"        // 利用者が同意を拒否した
)

// LoginError はログイン失敗。Code が上記のいずれか。
type LoginError struct {
	Code string
	Err  error
}

func (e *LoginError) Error() string {
	return fmt.Sprintf("auth: OAuth ログインに失敗しました (%s): %v", e.Code, e.Err)
}
func (e *LoginError) Unwrap() error { return e.Err }

// LoginCode は httpapi がパッケージ依存なしでコードを取り出すための口。
func (e *LoginError) LoginCode() string { return e.Code }

// AuthorizationServer は Redmine の OAuth エンドポイント（*redmine.OAuth が満たす）。
type AuthorizationServer interface {
	AuthorizeURL(state, codeChallenge string) string
	Exchange(ctx context.Context, code, codeVerifier string) (*redmine.TokenSet, error)
}

// IdentityProvider はトークンの持ち主の特定（*redmine.Client が満たす）。
type IdentityProvider interface {
	CurrentUser(ctx context.Context, accessToken string) (*redmine.CurrentUser, error)
}

type OAuthLoginDeps struct {
	Store    *store.Store
	Vault    *credential.Vault
	OAuth    AuthorizationServer
	Identity IdentityProvider
	Sessions *Sessions
	StateTTL time.Duration
	// Locks はログアウト後始末（GrantCleaner）と共有する利用者単位の排他。
	Locks *UserLocks
	// Revoker は不要になった付与の Redmine 側失効（ベストエフォート）。nil なら
	// 失効要求は送らない。
	Revoker Revoker
	Logger  *slog.Logger
}

// OAuthLogin はログインの開始（Begin）と完了（Complete）。
type OAuthLogin struct {
	d   OAuthLoginDeps
	now func() time.Time
}

func NewOAuthLogin(d OAuthLoginDeps) *OAuthLogin { return &OAuthLogin{d: d, now: time.Now} }

// 戻り先として許す画面ハッシュ。自由な URL は受け付けない（オープンリダイレクト防止）。
var returnToPattern = regexp.MustCompile(`^#(projects|issues|settings|issues/[0-9]{1,9}|issue-detail/[0-9]{1,9})$`)

const defaultReturnTo = "#projects"

func sanitizeReturnTo(s string) string {
	if returnToPattern.MatchString(s) {
		return s
	}
	return defaultReturnTo
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: 乱数生成に失敗しました: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func verifierAAD(stateHash string) string { return "oauth-verifier:" + stateHash }

// Begin は認可要求を開始する。state と PKCE の code_verifier を生成し、state は
// ハッシュで、verifier は暗号化して保存する（10 分・1 回限り）。Redmine の認可
// URL と、発行元ブラウザに束縛するための生の state を返す。
func (l *OAuthLogin) Begin(ctx context.Context, returnTo string) (authorizeURL, state string, err error) {
	if state, err = randomToken(32); err != nil {
		return "", "", err
	}
	verifier, err := randomToken(32) // 43 文字（RFC 7636: 43〜128）
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	hash := sha256Hex(state)
	ct, nonce, err := l.d.Vault.Seal(verifierAAD(hash), []byte(verifier))
	if err != nil {
		return "", "", err
	}
	now := l.now()
	if err := l.d.Store.InsertOAuthState(ctx, hash, ct, nonce, sanitizeReturnTo(returnTo), now.Add(l.d.StateTTL)); err != nil {
		return "", "", err
	}
	// 溜まった期限切れの掃除（失敗してもログインには影響させない）。
	_ = l.d.Store.DeleteExpiredOAuthStates(ctx, now)

	return l.d.OAuth.AuthorizeURL(state, challenge), state, nil
}

// Complete は Redmine からの戻り（認可コード + state）でログインを完了し、
// 新しいセッションの生トークンと戻り先を返す。state は先に消費する（失敗しても
// 再利用できない）。失敗は *LoginError。
func (l *OAuthLogin) Complete(ctx context.Context, code, state string) (sessionToken, returnTo string, err error) {
	if code == "" || state == "" {
		return "", "", &LoginError{Code: CodeInvalidState, Err: errors.New("code または state がありません")}
	}
	hash := sha256Hex(state)
	st, ok, err := l.d.Store.ConsumeOAuthState(ctx, hash, l.now())
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", &LoginError{Code: CodeInvalidState, Err: errors.New("state が不明・期限切れ・使用済みです")}
	}
	verifier, err := l.d.Vault.Open(verifierAAD(hash), st.VerifierCiphertext, st.VerifierNonce)
	if err != nil {
		return "", "", err
	}

	ts, err := l.d.OAuth.Exchange(ctx, code, string(verifier))
	if err != nil {
		return "", "", classifyLoginError(err)
	}
	// ここから先で失敗すると、発行済みの組がどこにも保存されず Redmine 側に
	// 使われない付与として残る。保存できなかった組は失効させる。
	stored := false
	defer func() {
		if !stored {
			revokeGrant(ctx, l.d.Revoker, l.logger(), ts.AccessToken, ts.RefreshToken)
		}
	}()
	cu, err := l.d.Identity.CurrentUser(ctx, ts.AccessToken)
	if err != nil {
		return "", "", classifyLoginError(err)
	}

	user, err := l.d.Store.UpsertOAuthUser(ctx, cu.ID, cu.Login, cu.DisplayName)
	if err != nil {
		return "", "", err
	}

	// 失効は、ロックを放したあとに送る（defer は後入れ先出し）。
	var replaced *credential.Tokens
	defer func() {
		if replaced != nil {
			revokeGrant(ctx, l.d.Revoker, l.logger(), replaced.Access(), replaced.Refresh())
		}
	}()

	// 組の保存からセッション発行までを、同じ利用者のログアウト後始末と直列化する
	// （後始末が保存直後の新しい組を「最後のセッションの分」と誤って失効しない）。
	unlock := l.d.Locks.Lock(user.ID)
	defer unlock()
	// 再ログインで置き換わる旧い組。上書きすると Redmine 側で生き残るため失効させる。
	old, oldErr := l.d.Vault.LoadTokens(ctx, user.ID)
	if err := l.d.Vault.SaveTokens(ctx, user.ID, ts); err != nil {
		return "", "", err // 旧い組はそのまま残るので失効させない
	}
	if oldErr == nil {
		replaced = old
	}
	stored = true
	token, err := l.d.Sessions.Issue(ctx, user.ID)
	if err != nil {
		return "", "", err
	}
	return token, st.ReturnTo, nil
}

func classifyLoginError(err error) error {
	switch {
	case errors.Is(err, redmine.ErrInvalidClient):
		return &LoginError{Code: CodeServerMisconfigured, Err: err}
	case errors.Is(err, redmine.ErrUpstream):
		return &LoginError{Code: CodeRedmineUnavailable, Err: err}
	case errors.Is(err, redmine.ErrInvalidGrant),
		errors.Is(err, redmine.ErrOAuthRejected),
		errors.Is(err, redmine.ErrUnauthorized):
		return &LoginError{Code: CodeExchangeFailed, Err: err}
	}
	// 想定外（キャンセル・内部エラー等）は分類せず素通しして 500 にする。
	return err
}

func (l *OAuthLogin) logger() *slog.Logger {
	if l.d.Logger != nil {
		return l.d.Logger
	}
	return slog.Default()
}
