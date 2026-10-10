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
	// Locks はログイン（OAuthLogin.Complete）と共有する利用者単位の排他。
	Locks *UserLocks
	// IdleTimeout はセッションの無操作期限。正なら、無操作で失効済みの
	// セッションは「残っているセッション」に数えない。0 は絶対期限だけを見る。
	IdleTimeout time.Duration

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
	c.cleanup(ctx, userID, now())
}

// SweepOrphans は、有効なセッションを 1 つも持たない利用者の組を失効・削除する。
// 無操作や絶対期限でセッションが失われてもログアウトは呼ばれないため、定期的に
// 呼んで Redmine 側に使われない付与を残さない。
func (c *GrantCleaner) SweepOrphans(ctx context.Context) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	ids, err := c.Store.ListOAuthTokenUserIDs(ctx)
	if err != nil {
		c.logger().Error("grant sweep: listing token holders failed", "error", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		c.cleanup(ctx, id, now())
	}
}

// cleanup は他に使えるセッションが無ければ、組をローカルから取り出して削除し、
// Redmine へ失効要求を送る。
func (c *GrantCleaner) cleanup(ctx context.Context, userID string, now time.Time) {
	tokens := c.takeTokensIfLastSession(ctx, userID, now)
	if tokens == nil {
		return
	}
	// ロックの外で送る（Redmine が遅くても、同じ利用者のログインを待たせない）。
	revokeGrant(ctx, c.OAuth, c.logger(), tokens.Access(), tokens.Refresh())
}

// revokeGrant は組を Redmine 側で失効させる（ベストエフォート。リフレッシュ →
// アクセスの順に、リフレッシュを先に殺す）。届かなくても呼び出し元は続行する。
// Redmine の「マイアカウント」から手動で取り消せる（Manual.md）。
func revokeGrant(ctx context.Context, r Revoker, log *slog.Logger, access, refresh string) {
	if r == nil {
		return
	}
	for _, p := range []struct{ token, hint string }{
		{refresh, "refresh_token"},
		{access, "access_token"},
	} {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
		if err := r.Revoke(rctx, p.token, p.hint); err != nil {
			log.Warn("remote revoke failed", "hint", p.hint, "error", err)
		}
		cancel()
	}
}

// takeTokensIfLastSession は、他に有効なセッションが無いときだけ利用者の組を
// ローカルから取り出して削除し、失効要求用に返す。確認から削除までをログインと
// 同じ利用者ロックの中で行う。送るべき失効が無ければ nil を返す（他の端末が
// 使用中・組が無い・既に無効・読み出し失敗）。
func (c *GrantCleaner) takeTokensIfLastSession(ctx context.Context, userID string, now time.Time) *credential.Tokens {
	unlock := c.Locks.Lock(userID)
	defer unlock()

	var n int
	var err error
	if c.IdleTimeout > 0 {
		n, err = c.Store.CountLiveSessions(ctx, userID, now, now.Add(-c.IdleTimeout))
	} else {
		n, err = c.Store.CountActiveSessions(ctx, userID, now)
	}
	if err != nil {
		// 数えられない時は、他の端末を巻き込まないよう何もしない側に倒す。
		c.logger().Error("logout cleanup: counting sessions failed", "error", err)
		return nil
	}
	if n > 0 {
		return nil
	}

	tokens, err := c.Vault.LoadTokens(ctx, userID)
	switch {
	case err == nil:
	case errors.Is(err, credential.ErrNoCredential), errors.Is(err, credential.ErrCredentialInvalid):
		// 無い／既に無効。送る失効要求は無いが、行は消す。
		tokens = nil
	default:
		c.logger().Error("logout cleanup: loading tokens failed", "error", err)
		tokens = nil
	}
	if err := c.Store.DeleteOAuthTokens(ctx, userID); err != nil {
		c.logger().Error("logout cleanup: deleting local tokens failed", "error", err)
	}
	return tokens
}
