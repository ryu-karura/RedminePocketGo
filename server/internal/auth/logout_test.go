package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

type fakeRevoker struct {
	calls [][2]string // {token, hint}
	err   error
}

func (f *fakeRevoker) Revoke(_ context.Context, token, hint string) error {
	f.calls = append(f.calls, [2]string{token, hint})
	return f.err
}

type cleanerEnv struct {
	*loginEnv
	cleaner *GrantCleaner
	userID  string
}

func newCleanerEnv(t *testing.T) *cleanerEnv {
	t.Helper()
	e := newLoginEnv(t)
	ctx := context.Background()
	u, err := e.st.UpsertOAuthUser(ctx, 5, "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.vault.SaveTokens(ctx, u.ID, &redmine.TokenSet{
		AccessToken: "AT", RefreshToken: "RT", Scopes: []string{"view_project"}, AccessExpiresAt: loginT0.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	rev := e.rev
	c := &GrantCleaner{Store: e.st, Vault: e.vault, OAuth: rev, Locks: e.locks, IdleTimeout: time.Hour}
	c.now = func() time.Time { return loginT0 }
	return &cleanerEnv{loginEnv: e, cleaner: c, userID: u.ID}
}

func (e *cleanerEnv) addSession(t *testing.T) {
	t.Helper()
	if _, err := e.sessions.Issue(context.Background(), e.userID); err != nil {
		t.Fatal(err)
	}
}

func TestAfterLogoutKeepsTokensWhileOtherSessionsRemain(t *testing.T) {
	// トークンは利用者単位で端末間共有。他の端末が使っている間は失効させない。
	e := newCleanerEnv(t)
	e.addSession(t) // 別の端末のセッション
	e.cleaner.AfterLogout(context.Background(), e.userID)
	if len(e.rev.calls) != 0 {
		t.Errorf("revoke calls = %v; want none while another session remains", e.rev.calls)
	}
	if tk, err := e.vault.LoadTokens(context.Background(), e.userID); err != nil || tk.Access() != "AT" {
		t.Errorf("tokens were removed although another device is logged in: %v, %v", tk, err)
	}
}

func TestAfterLogoutRevokesAtRedmineWhenLastSession(t *testing.T) {
	e := newCleanerEnv(t)
	e.cleaner.AfterLogout(context.Background(), e.userID)

	want := [][2]string{{"RT", "refresh_token"}, {"AT", "access_token"}}
	if len(e.rev.calls) != 2 || e.rev.calls[0] != want[0] || e.rev.calls[1] != want[1] {
		t.Errorf("revoke calls = %v; want %v", e.rev.calls, want)
	}
	if _, err := e.vault.LoadTokens(context.Background(), e.userID); err == nil {
		t.Error("local tokens still present after the last logout")
	}
}

func TestAfterLogoutStillCleansUpWhenRevokeFails(t *testing.T) {
	// Redmine に届かなくてもログアウトは完了させる（ローカルの組は消す）。
	e := newCleanerEnv(t)
	e.rev.err = errors.New("redmine down")
	e.cleaner.AfterLogout(context.Background(), e.userID)
	if _, err := e.vault.LoadTokens(context.Background(), e.userID); err == nil {
		t.Error("local tokens kept after a failed remote revoke")
	}
}

func TestAfterLogoutWithoutTokens(t *testing.T) {
	e := newCleanerEnv(t)
	if err := e.st.DeleteOAuthTokens(context.Background(), e.userID); err != nil {
		t.Fatal(err)
	}
	e.cleaner.AfterLogout(context.Background(), e.userID) // panic / revoke しない
	if len(e.rev.calls) != 0 {
		t.Errorf("revoke calls = %v; want none", e.rev.calls)
	}
}

func TestAfterLogoutInvalidGrantIsNotRevoked(t *testing.T) {
	// 既に無効化された組は復号しても Redmine 側では死んでいる。失効要求は送らず削除だけ。
	e := newCleanerEnv(t)
	if err := e.vault.MarkTokensInvalid(context.Background(), e.userID); err != nil {
		t.Fatal(err)
	}
	e.cleaner.AfterLogout(context.Background(), e.userID)
	if len(e.rev.calls) != 0 {
		t.Errorf("revoke calls = %v; want none for an invalid grant", e.rev.calls)
	}
	var n int
	_ = e.st.DB().QueryRow("SELECT COUNT(*) FROM oauth_tokens").Scan(&n)
	if n != 0 {
		t.Errorf("oauth_tokens rows = %d; want the invalid row removed", n)
	}
}
