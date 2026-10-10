package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

func completeLogin(t *testing.T, e *loginEnv) error {
	t.Helper()
	ctx := context.Background()
	_, state, err := e.svc.Begin(ctx, "#issues/3")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = e.svc.Complete(ctx, "code", state)
	return err
}

func TestCompleteRevokesReplacedGrantOnRelogin(t *testing.T) {
	e := newLoginEnv(t)
	ctx := context.Background()
	u, err := e.st.UpsertOAuthUser(ctx, 5, "alice", "Alice A")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.vault.SaveTokens(ctx, u.ID, &redmine.TokenSet{AccessToken: "OLD-AT", RefreshToken: "OLD-RT", Scopes: []string{"view_project"}, AccessExpiresAt: loginT0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := completeLogin(t, e); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	want := [][2]string{{"OLD-RT", "refresh_token"}, {"OLD-AT", "access_token"}}
	if len(e.rev.calls) != 2 || e.rev.calls[0] != want[0] || e.rev.calls[1] != want[1] {
		t.Errorf("revoke calls = %v; want the replaced pair %v", e.rev.calls, want)
	}
	if tk, err := e.vault.LoadTokens(ctx, u.ID); err != nil || tk.Access() != "AT" {
		t.Errorf("stored tokens = %v, %v; want the new pair", tk, err)
	}
}

func TestCompleteFirstLoginRevokesNothing(t *testing.T) {
	e := newLoginEnv(t)
	if err := completeLogin(t, e); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(e.rev.calls) != 0 {
		t.Errorf("revoke calls = %v; want none on a first login", e.rev.calls)
	}
}

func TestCompleteRevokesFreshGrantWhenLoginFailsAfterExchange(t *testing.T) {
	e := newLoginEnv(t)
	e.id.err = redmine.ErrUpstream // コード交換は成功し、利用者特定で失敗
	err := completeLogin(t, e)
	if err == nil {
		t.Fatal("Complete succeeded; want failure")
	}
	want := [][2]string{{"RT", "refresh_token"}, {"AT", "access_token"}}
	if len(e.rev.calls) != 2 || e.rev.calls[0] != want[0] || e.rev.calls[1] != want[1] {
		t.Errorf("revoke calls = %v; want the unused fresh pair %v", e.rev.calls, want)
	}
}

func TestCompleteExchangeFailureRevokesNothing(t *testing.T) {
	e := newLoginEnv(t)
	e.as.exchangeFn = func(context.Context, string, string) (*redmine.TokenSet, error) {
		return nil, redmine.ErrInvalidGrant
	}
	if err := completeLogin(t, e); err == nil {
		t.Fatal("Complete succeeded; want failure")
	}
	if len(e.rev.calls) != 0 {
		t.Errorf("revoke calls = %v; want none (no token was issued)", e.rev.calls)
	}
}

func TestCompleteRevokeFailureDoesNotFailLogin(t *testing.T) {
	e := newLoginEnv(t)
	ctx := context.Background()
	u, _ := e.st.UpsertOAuthUser(ctx, 5, "alice", "Alice A")
	_ = e.vault.SaveTokens(ctx, u.ID, &redmine.TokenSet{AccessToken: "OLD-AT", RefreshToken: "OLD-RT", AccessExpiresAt: loginT0.Add(time.Hour)})
	e.rev.err = errors.New("redmine down")
	if err := completeLogin(t, e); err != nil {
		t.Fatalf("Complete: %v; a failed best-effort revoke must not fail the login", err)
	}
}

func TestSweepOrphansRevokesGrantsWithoutLiveSessions(t *testing.T) {
	e := newCleanerEnv(t) // alice: トークンあり・セッションなし
	ctx := context.Background()

	// bob: 有効なセッションあり → 触らない。
	bob, _ := e.st.UpsertOAuthUser(ctx, 6, "bob", "Bob")
	_ = e.vault.SaveTokens(ctx, bob.ID, &redmine.TokenSet{AccessToken: "BAT", RefreshToken: "BRT", AccessExpiresAt: loginT0.Add(time.Hour)})
	if _, err := e.sessions.Issue(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	// carol: セッションはあるが無操作で失効済み → 孤児として失効。
	carol, _ := e.st.UpsertOAuthUser(ctx, 7, "carol", "Carol")
	_ = e.vault.SaveTokens(ctx, carol.ID, &redmine.TokenSet{AccessToken: "CAT", RefreshToken: "CRT", AccessExpiresAt: loginT0.Add(time.Hour)})
	if _, err := e.sessions.Issue(ctx, carol.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB().Exec(`UPDATE sessions SET last_seen_at = ? WHERE user_id = ?`, loginT0.Add(-2*time.Hour).UTC().Format(time.RFC3339Nano), carol.ID); err != nil {
		t.Fatal(err)
	}

	e.cleaner.SweepOrphans(ctx)

	got := map[string]bool{}
	for _, c := range e.rev.calls {
		got[c[0]] = true
	}
	for _, tok := range []string{"AT", "RT", "CAT", "CRT"} {
		if !got[tok] {
			t.Errorf("token %s was not revoked; calls = %v", tok, e.rev.calls)
		}
	}
	if got["BAT"] || got["BRT"] {
		t.Errorf("bob's live grant was revoked; calls = %v", e.rev.calls)
	}
	var n int
	_ = e.st.DB().QueryRow("SELECT COUNT(*) FROM oauth_tokens").Scan(&n)
	if n != 1 {
		t.Errorf("oauth_tokens rows = %d; want only bob's", n)
	}
}

func TestAfterLogoutIgnoresIdleExpiredOtherSessions(t *testing.T) {
	// 無操作で失効済みの他端末セッションは使えない。それが残っているせいで
	// 最後のログアウトが失効を見送ってはならない。
	e := newCleanerEnv(t)
	e.addSession(t)
	if _, err := e.st.DB().Exec(`UPDATE sessions SET last_seen_at = ?`, loginT0.Add(-2*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	e.cleaner.AfterLogout(context.Background(), e.userID)
	if len(e.rev.calls) != 2 {
		t.Errorf("revoke calls = %v; want the grant revoked", e.rev.calls)
	}
}
