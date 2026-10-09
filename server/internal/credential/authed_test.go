package credential

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
)

// fakeUpstream は Upstream の一部だけを実装する（未実装メソッドは呼ばれたら panic）。
type fakeUpstream struct {
	Upstream
	tokens []string // 受け取ったトークンの履歴
	fn     func(token string, n int) error
}

func (f *fakeUpstream) ListProjects(_ context.Context, token string) ([]redmine.Project, error) {
	f.tokens = append(f.tokens, token)
	if err := f.fn(token, len(f.tokens)); err != nil {
		return nil, err
	}
	return []redmine.Project{{ID: 1, Name: "p"}}, nil
}

func (f *fakeUpstream) CountOpenIssues(_ context.Context, token string, _ int) (int, error) {
	f.tokens = append(f.tokens, token)
	if err := f.fn(token, len(f.tokens)); err != nil {
		return 0, err
	}
	return 7, nil
}

func newAuthed(t *testing.T, up *fakeUpstream, r Refresher) (*Authed, *Manager, string) {
	t.Helper()
	m, v, uid := newManager(t, r)
	if err := v.SaveTokens(context.Background(), uid, tokenSet("A0", "R0", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	return NewAuthed(up, m), m, uid
}

func TestAuthedPassesTheUsersToken(t *testing.T) {
	up := &fakeUpstream{fn: func(string, int) error { return nil }}
	a, _, uid := newAuthed(t, up, rotating())
	got, err := a.ListProjects(context.Background(), uid)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListProjects = %v, %v", got, err)
	}
	if len(up.tokens) != 1 || up.tokens[0] != "A0" {
		t.Errorf("upstream saw tokens %v; want [A0]", up.tokens)
	}
	if n, err := a.CountOpenIssues(context.Background(), uid, 3); err != nil || n != 7 {
		t.Errorf("CountOpenIssues = %d, %v (non-slice return types must work too)", n, err)
	}
}

func TestAuthedRefreshesOnceOnUnauthorized(t *testing.T) {
	r := rotating()
	up := &fakeUpstream{fn: func(tok string, n int) error {
		if n == 1 {
			return redmine.ErrUnauthorized // 1 回目だけ失効扱い
		}
		return nil
	}}
	a, _, uid := newAuthed(t, up, r)
	if _, err := a.ListProjects(context.Background(), uid); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(up.tokens) != 2 || up.tokens[0] != "A0" || up.tokens[1] != "A1" {
		t.Errorf("upstream saw %v; want [A0 A1] (retried with the refreshed token)", up.tokens)
	}
	if r.count() != 1 {
		t.Errorf("refresh calls = %d; want 1", r.count())
	}
}

func TestAuthedGivesUpAfterOneRetry(t *testing.T) {
	r := rotating()
	up := &fakeUpstream{fn: func(string, int) error { return redmine.ErrUnauthorized }}
	a, _, uid := newAuthed(t, up, r)
	_, err := a.ListProjects(context.Background(), uid)
	if !errors.Is(err, redmine.ErrUnauthorized) {
		t.Fatalf("err = %v; want ErrUnauthorized so the caller can invalidate and ask for re-authorization", err)
	}
	if len(up.tokens) != 2 || r.count() != 1 {
		t.Errorf("upstream calls = %d, refreshes = %d; want 2 and 1 (no loop)", len(up.tokens), r.count())
	}
}

func TestAuthedDoesNotRetryOtherErrorsAndPropagatesTokenErrors(t *testing.T) {
	t.Run("upstream error is not retried", func(t *testing.T) {
		r := rotating()
		up := &fakeUpstream{fn: func(string, int) error { return fmt.Errorf("%w: 503", redmine.ErrUpstream) }}
		a, _, uid := newAuthed(t, up, r)
		if _, err := a.ListProjects(context.Background(), uid); !errors.Is(err, redmine.ErrUpstream) {
			t.Fatalf("err = %v", err)
		}
		if len(up.tokens) != 1 || r.count() != 0 {
			t.Errorf("calls = %d, refreshes = %d; want 1 and 0", len(up.tokens), r.count())
		}
	})
	t.Run("no stored grant", func(t *testing.T) {
		up := &fakeUpstream{fn: func(string, int) error { t.Error("upstream called without a token"); return nil }}
		a, _, _ := newAuthed(t, up, rotating())
		if _, err := a.ListProjects(context.Background(), "someone-else"); !errors.Is(err, ErrNoCredential) {
			t.Errorf("err = %v; want ErrNoCredential", err)
		}
	})
	t.Run("refresh says invalid_grant", func(t *testing.T) {
		r := &fakeRefresher{fn: func(context.Context, string, int) (*redmine.TokenSet, error) { return nil, redmine.ErrInvalidGrant }}
		up := &fakeUpstream{fn: func(string, int) error { return redmine.ErrUnauthorized }}
		a, _, uid := newAuthed(t, up, r)
		if _, err := a.ListProjects(context.Background(), uid); !errors.Is(err, ErrCredentialInvalid) {
			t.Errorf("err = %v; want ErrCredentialInvalid", err)
		}
	})
}

func TestManagerEnsureAndMarkInvalid(t *testing.T) {
	r := rotating()
	m, v, uid := newManager(t, r)
	ctx := context.Background()
	if err := m.Ensure(ctx, uid); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("Ensure without grant: %v", err)
	}
	if err := v.SaveTokens(ctx, uid, tokenSet("A0", "R0", t0.Add(30*time.Second))); err != nil {
		t.Fatal(err)
	}
	// 期限が近ければ Ensure の時点で更新される。
	if err := m.Ensure(ctx, uid); err != nil || r.count() != 1 {
		t.Fatalf("Ensure = %v, refreshes = %d; want nil and 1", err, r.count())
	}
	if err := m.MarkInvalid(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(ctx, uid); !errors.Is(err, ErrCredentialInvalid) {
		t.Errorf("Ensure after MarkInvalid = %v; want ErrCredentialInvalid", err)
	}
}
