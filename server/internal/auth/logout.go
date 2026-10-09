package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/credential"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// Revoker は Redmine のトークン失効（*redmine.OAuth が満たす）。
type Revoker interface {
	Revoke(ctx context.Context, token, tokenTypeHint string) error
}

// GrantCleaner はログアウト後の後始末。OAuth トークンの組は利用者単位で端末間
// 共有のため、他の端末のセッションが残っている間は失効させない。最後のセッション
// が消えたときに、Redmine 側で失効させ、ローカルの組も削除する（Design.md §4.4）。
type GrantCleaner struct {
	Store  *store.Store
	Vault  *credential.Vault
	OAuth  Revoker
	Logger *slog.Logger

	now func() time.Time
}

// revokeTimeout は Redmine への失効要求 1 件の上限。ログアウトを待たせすぎない。
const revokeTimeout = 10 * time.Second

func (c *GrantCleaner) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

// AfterLogout は httpapi.LogoutCleaner を実装する。失敗はログに残すだけで、
// ログアウト自体は成功させる。
func (c *GrantCleaner) AfterLogout(ctx context.Context, userID string) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	n, err := c.Store.CountActiveSessions(ctx, userID, now())
	if err != nil {
		// 数えられない時は、他の端末を巻き込まないよう何もしない側に倒す。
		c.logger().Error("logout cleanup: counting sessions failed", "error", err)
		return
	}
	if n > 0 {
		return
	}

	tokens, err := c.Vault.LoadTokens(ctx, userID)
	switch {
	case err == nil:
		// 失効要求は、リフレッシュ → アクセスの順（リフレッシュを先に殺す）。
		for _, t := range []struct{ token, hint string }{
			{tokens.Refresh(), "refresh_token"},
			{tokens.Access(), "access_token"},
		} {
			rctx, cancel := context.WithTimeout(ctx, revokeTimeout)
			if err := c.OAuth.Revoke(rctx, t.token, t.hint); err != nil {
				// 届かなくてもローカルは消す。Redmine 側の「マイアカウント」から
				// 手動で取り消せる（Manual.md）。
				c.logger().Warn("logout cleanup: remote revoke failed", "hint", t.hint, "error", err)
			}
			cancel()
		}
	case errors.Is(err, credential.ErrNoCredential), errors.Is(err, credential.ErrCredentialInvalid):
		// 無い／既に無効。送る失効要求は無い。
	default:
		c.logger().Error("logout cleanup: loading tokens failed", "error", err)
	}
	if err := c.Store.DeleteOAuthTokens(ctx, userID); err != nil {
		c.logger().Error("logout cleanup: deleting local tokens failed", "error", err)
	}
}
