package auth

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(context.Background(), &store.User{
		ID: "u1", RedmineLogin: "alice", DisplayName: "Alice", WebAuthnUserHandle: []byte{1},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func newTestSessions(t *testing.T, now *time.Time) *Sessions {
	s := NewSessions(testStore(t), Config{
		IdleTimeout:     time.Hour,
		AbsoluteTimeout: 24 * time.Hour,
		CookieName:      "rmapp_session",
		SecureCookie:    true,
	})
	s.now = func() time.Time { return *now }
	return s
}

func TestIssueAndResolve(t *testing.T) {
	now := time.Now().UTC()
	s := newTestSessions(t, &now)
	ctx := context.Background()

	token, err := s.Issue(ctx, "u1", []byte{9})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	info, err := s.ResolveSession(ctx, token)
	if err != nil || info == nil || info.UserID != "u1" {
		t.Fatalf("ResolveSession = %+v, %v; want u1", info, err)
	}

	// 不正なトークンは (nil, nil)
	if info, err := s.ResolveSession(ctx, "bogus"); err != nil || info != nil {
		t.Errorf("bogus token: %+v, %v; want nil, nil", info, err)
	}
}

func TestIdleTimeoutSlides(t *testing.T) {
	now := time.Now().UTC()
	s := newTestSessions(t, &now)
	ctx := context.Background()
	token, _ := s.Issue(ctx, "u1", nil)

	// 50 分ごとにアクセスすればアイドル 1h を超えない
	for i := 0; i < 3; i++ {
		now = now.Add(50 * time.Minute)
		if info, err := s.ResolveSession(ctx, token); err != nil || info == nil {
			t.Fatalf("slide %d: %+v, %v", i, info, err)
		}
	}

	// 1h を超えて放置すると失効し、以後は復活しない
	now = now.Add(61 * time.Minute)
	if info, _ := s.ResolveSession(ctx, token); info != nil {
		t.Fatal("idle-expired session still resolves")
	}
	now = now.Add(-30 * time.Minute) // 時計が戻っても失効済みのまま
	if info, _ := s.ResolveSession(ctx, token); info != nil {
		t.Fatal("expired session revived")
	}
}

func TestAbsoluteTimeout(t *testing.T) {
	now := time.Now().UTC()
	s := newTestSessions(t, &now)
	ctx := context.Background()
	token, _ := s.Issue(ctx, "u1", nil)

	// アイドルを回避し続けても絶対タイムアウト 24h で失効する
	for i := 0; i < 25; i++ {
		now = now.Add(59 * time.Minute)
		s.ResolveSession(ctx, token)
	}
	if info, _ := s.ResolveSession(ctx, token); info != nil {
		t.Fatal("session outlived absolute timeout")
	}
}

func TestRevoke(t *testing.T) {
	now := time.Now().UTC()
	s := newTestSessions(t, &now)
	ctx := context.Background()
	token, _ := s.Issue(ctx, "u1", nil)
	if err := s.Revoke(ctx, token); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if info, _ := s.ResolveSession(ctx, token); info != nil {
		t.Fatal("revoked session still resolves")
	}
}

func TestCookieAttributes(t *testing.T) {
	now := time.Now().UTC()
	s := newTestSessions(t, &now)

	c := s.Cookie("tok")
	// Design.md §3.5: HttpOnly, Secure, SameSite=Lax, Path=/
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.Name != "rmapp_session" || c.Value != "tok" {
		t.Errorf("cookie attributes wrong: %+v", c)
	}

	cl := s.ClearCookie()
	if cl.MaxAge != -1 || cl.Value != "" {
		t.Errorf("clear cookie must expire immediately: %+v", cl)
	}
}
