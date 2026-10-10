package auth

import (
	"context"
	"testing"
	"time"
)

// blocked は fn が wait の間に終わらない（ロックで待たされている）ことを確かめ、
// release の後で終わることを確かめる。
func assertBlockedUntil(t *testing.T, fn func(), release func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
		t.Fatal("finished while the per-user lock was held; it must wait")
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not finish after the lock was released")
	}
}

func TestUserLocksSerializeSameUserOnly(t *testing.T) {
	l := NewUserLocks()
	unlock := l.Lock("u1")
	// 別の利用者は待たされない。
	other := make(chan struct{})
	go func() { l.Lock("u2")(); close(other) }()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("a different user was blocked")
	}
	assertBlockedUntil(t, func() { l.Lock("u1")() }, unlock)
}

func TestUserLocksNilIsNoop(t *testing.T) {
	var l *UserLocks
	l.Lock("u1")() // panic しない
}

// 競合: ログアウトの後始末が「他にセッションが無い」と数えた直後に別端末の
// ログインが新しい組とセッションを作ると、新しい組を失効・削除してしまう。
// ロックで後始末とログインを直列化すると、後始末はログイン後の状態を見る。
func TestAfterLogoutWaitsForInFlightLogin(t *testing.T) {
	e := newCleanerEnv(t)
	unlock := e.locks.Lock(e.userID) // 別端末のログインがトークン保存〜セッション発行の最中
	assertBlockedUntil(t,
		func() { e.cleaner.AfterLogout(context.Background(), e.userID) },
		func() {
			e.addSession(t) // ログイン側がセッションを作り終える
			unlock()
		})
	if len(e.rev.calls) != 0 {
		t.Errorf("revoke calls = %v; the concurrent login's grant must not be revoked", e.rev.calls)
	}
	if tk, err := e.vault.LoadTokens(context.Background(), e.userID); err != nil || tk.Access() != "AT" {
		t.Errorf("the new login's tokens were removed: %v, %v", tk, err)
	}
}

func TestCompleteHoldsUserLockWhileSavingTokensAndSession(t *testing.T) {
	e := newLoginEnv(t)
	ctx := context.Background()
	_, state, err := e.svc.Begin(ctx, "#issues/3")
	if err != nil {
		t.Fatal(err)
	}
	// Redmine の利用者 5 に対応する users 行を先に作り、その ID でロックを保持する。
	u, err := e.st.UpsertOAuthUser(ctx, 5, "alice", "Alice A")
	if err != nil {
		t.Fatal(err)
	}
	unlock := e.locks.Lock(u.ID)
	assertBlockedUntil(t, func() {
		if _, _, err := e.svc.Complete(ctx, "code", state); err != nil {
			t.Errorf("Complete: %v", err)
		}
	}, unlock)
}
