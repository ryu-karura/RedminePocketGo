package credential

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func newTokenVault(t *testing.T) (*Vault, *store.Store, string) {
	t.Helper()
	st, err := store.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	u, err := st.UpsertOAuthUser(context.Background(), 1, "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	v, err := NewVault(st, kek, 1)
	if err != nil {
		t.Fatal(err)
	}
	return v, st, u.ID
}

func tokenSet(access, refresh string, expires time.Time) *redmine.TokenSet {
	return &redmine.TokenSet{
		AccessToken: access, RefreshToken: refresh,
		Scopes: []string{"view_project", "view_issues"}, AccessExpiresAt: expires,
	}
}

func TestTokensRoundTripAndAtRestEncryption(t *testing.T) {
	ctx := context.Background()
	v, st, uid := newTokenVault(t)

	if _, err := v.LoadTokens(ctx, uid); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("before save: err = %v; want ErrNoCredential", err)
	}
	if err := v.SaveTokens(ctx, uid, tokenSet("ACCESS-PLAIN", "REFRESH-PLAIN", t0.Add(2*time.Hour))); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}
	got, err := v.LoadTokens(ctx, uid)
	if err != nil {
		t.Fatalf("LoadTokens: %v", err)
	}
	if got.Access() != "ACCESS-PLAIN" || got.Refresh() != "REFRESH-PLAIN" ||
		strings.Join(got.Scopes, " ") != "view_project view_issues" || !got.AccessExpiresAt.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("round trip mismatch: scopes=%v exp=%v", got.Scopes, got.AccessExpiresAt)
	}

	// DB には平文が無く、アクセスとリフレッシュのノンスは別物。
	rec, err := st.GetOAuthTokens(ctx, uid)
	if err != nil || rec == nil {
		t.Fatalf("raw record: %v, %v", rec, err)
	}
	for label, b := range map[string][]byte{
		"access_ciphertext": rec.AccessCiphertext, "refresh_ciphertext": rec.RefreshCiphertext,
	} {
		if bytes.Contains(b, []byte("PLAIN")) {
			t.Errorf("%s contains plaintext", label)
		}
	}
	if bytes.Equal(rec.AccessNonce, rec.RefreshNonce) {
		t.Error("access and refresh share a nonce")
	}
	if rec.KeyVersion != 1 {
		t.Errorf("key_version = %d", rec.KeyVersion)
	}
}

func TestTokensTamperAndSwapAreRejected(t *testing.T) {
	ctx := context.Background()
	v, st, uid := newTokenVault(t)
	if err := v.SaveTokens(ctx, uid, tokenSet("A", "R", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.GetOAuthTokens(ctx, uid)

	// アクセスとリフレッシュの暗号文を入れ替える（用途を AAD に束縛している）。
	swapped := *rec
	swapped.AccessCiphertext, swapped.AccessNonce = rec.RefreshCiphertext, rec.RefreshNonce
	swapped.RefreshCiphertext, swapped.RefreshNonce = rec.AccessCiphertext, rec.AccessNonce
	if err := st.SaveOAuthTokens(ctx, swapped); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoadTokens(ctx, uid); err == nil {
		t.Error("swapped ciphertexts decrypted successfully")
	}

	// 別利用者の行へ暗号文を移す（利用者 ID を AAD に束縛している）。
	other, err := st.UpsertOAuthUser(ctx, 2, "bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	moved := *rec
	moved.UserID = other.ID
	if err := st.SaveOAuthTokens(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoadTokens(ctx, other.ID); err == nil {
		t.Error("ciphertext copied to another user decrypted successfully")
	}
}

func TestLoadTokensInvalidStatus(t *testing.T) {
	ctx := context.Background()
	v, _, uid := newTokenVault(t)
	if err := v.SaveTokens(ctx, uid, tokenSet("A", "R", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := v.MarkTokensInvalid(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoadTokens(ctx, uid); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("err = %v; want ErrCredentialInvalid", err)
	}
	// 再保存（再認可）で active に戻る。
	if err := v.SaveTokens(ctx, uid, tokenSet("A2", "R2", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got, err := v.LoadTokens(ctx, uid); err != nil || got.Access() != "A2" {
		t.Fatalf("after re-save: %v, %v", got, err)
	}
}

func TestTokensAreRedacted(t *testing.T) {
	ctx := context.Background()
	v, _, uid := newTokenVault(t)
	if err := v.SaveTokens(ctx, uid, tokenSet("SECRET-A", "SECRET-R", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	got, _ := v.LoadTokens(ctx, uid)
	b, _ := json.Marshal(got)
	for label, s := range map[string]string{
		"json": string(b), "%v": fmt.Sprintf("%v", got), "%+v": fmt.Sprintf("%+v", got),
		"%#v": fmt.Sprintf("%#v", *got), "slog": fmt.Sprint(slog.AnyValue(got).Resolve()),
	} {
		if strings.Contains(s, "SECRET") {
			t.Errorf("%s leaks a token: %s", label, s)
		}
	}
}

func TestSealOpenBindsAAD(t *testing.T) {
	v, _, _ := newTokenVault(t)
	ct, nonce, err := v.Seal("oauth-verifier:abc", []byte("verifier"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("verifier")) {
		t.Error("ciphertext contains plaintext")
	}
	if pt, err := v.Open("oauth-verifier:abc", ct, nonce); err != nil || string(pt) != "verifier" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	if _, err := v.Open("oauth-verifier:other", ct, nonce); err == nil {
		t.Error("Open with a different AAD succeeded")
	}
}

// ---- Manager（更新） ----

type fakeRefresher struct {
	mu    sync.Mutex
	calls int32
	seen  []string // 受け取ったリフレッシュトークン
	fn    func(ctx context.Context, refresh string, n int) (*redmine.TokenSet, error)
}

func (f *fakeRefresher) Refresh(ctx context.Context, refresh string) (*redmine.TokenSet, error) {
	n := int(atomic.AddInt32(&f.calls, 1))
	f.mu.Lock()
	f.seen = append(f.seen, refresh)
	f.mu.Unlock()
	return f.fn(ctx, refresh, n)
}

func (f *fakeRefresher) count() int { return int(atomic.LoadInt32(&f.calls)) }

func newManager(t *testing.T, r Refresher) (*Manager, *Vault, string) {
	t.Helper()
	v, _, uid := newTokenVault(t)
	m := NewManager(v, r, 60*time.Second)
	m.now = func() time.Time { return t0 }
	return m, v, uid
}

func rotating() *fakeRefresher {
	return &fakeRefresher{fn: func(_ context.Context, refresh string, n int) (*redmine.TokenSet, error) {
		return tokenSet(fmt.Sprintf("A%d", n), fmt.Sprintf("R%d", n), t0.Add(2*time.Hour)), nil
	}}
}

func TestManagerReturnsFreshTokenWithoutRefreshing(t *testing.T) {
	r := rotating()
	m, v, uid := newManager(t, r)
	ctx := context.Background()
	if err := v.SaveTokens(ctx, uid, tokenSet("A0", "R0", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	got, err := m.AccessToken(ctx, uid)
	if err != nil || got != "A0" {
		t.Fatalf("AccessToken = %q, %v; want A0", got, err)
	}
	if r.count() != 0 {
		t.Errorf("refresh calls = %d; want 0", r.count())
	}
}

func TestManagerRefreshesNearExpiryAndPersistsBeforeReturn(t *testing.T) {
	r := rotating()
	m, v, uid := newManager(t, r)
	ctx := context.Background()
	// 期限まで 30 秒（余裕 60 秒未満）→ リフレッシュ対象。
	if err := v.SaveTokens(ctx, uid, tokenSet("A0", "R0", t0.Add(30*time.Second))); err != nil {
		t.Fatal(err)
	}
	got, err := m.AccessToken(ctx, uid)
	if err != nil || got != "A1" {
		t.Fatalf("AccessToken = %q, %v; want A1", got, err)
	}
	if len(r.seen) != 1 || r.seen[0] != "R0" {
		t.Errorf("refresh used %v; want [R0]", r.seen)
	}
	stored, _ := v.LoadTokens(ctx, uid)
	if stored.Access() != "A1" || stored.Refresh() != "R1" {
		t.Errorf("stored = %q/%q; want the rotated pair A1/R1", stored.Access(), stored.Refresh())
	}
}

func TestManagerSingleFlightPerUser(t *testing.T) {
	release := make(chan struct{})
	r := &fakeRefresher{fn: func(_ context.Context, _ string, n int) (*redmine.TokenSet, error) {
		<-release
		return tokenSet(fmt.Sprintf("A%d", n), fmt.Sprintf("R%d", n), t0.Add(2*time.Hour)), nil
	}}
	m, v, uid := newManager(t, r)
	ctx := context.Background()
	if err := v.SaveTokens(ctx, uid, tokenSet("A0", "R0", t0.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}

	const n = 20
	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); got[i], errs[i] = m.AccessToken(ctx, uid) }(i)
	}
	time.Sleep(100 * time.Millisecond) // 全員が待機に入るのを待つ
	close(release)
	wg.Wait()

	if r.count() != 1 {
		t.Fatalf("refresh calls = %d; want exactly 1（並行リフレッシュは連鎖を壊す）", r.count())
	}
	for i := range got {
		if errs[i] != nil || got[i] != "A1" {
			t.Errorf("caller %d: %q, %v; want A1", i, got[i], errs[i])
		}
	}
}

func TestManagerInvalidGrantMarksInvalid(t *testing.T) {
	r := &fakeRefresher{fn: func(context.Context, string, int) (*redmine.TokenSet, error) {
		return nil, redmine.ErrInvalidGrant
	}}
	m, v, uid := newManager(t, r)
	ctx := context.Background()
	if err := v.SaveTokens(ctx, uid, tokenSet("A0", "R0", t0.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AccessToken(ctx, uid); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("err = %v; want ErrCredentialInvalid", err)
	}
	// 以後は上流を呼ばずに即 ErrCredentialInvalid。
	before := r.count()
	if _, err := m.AccessToken(ctx, uid); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("second call err = %v", err)
	}
	if r.count() != before {
		t.Error("refresh attempted for an already invalid grant")
	}
}

func TestManagerTransientAndClientErrorsDoNotInvalidate(t *testing.T) {
	for name, cause := range map[string]error{
		"upstream":       fmt.Errorf("%w: boom", redmine.ErrUpstream),
		"invalid_client": redmine.ErrInvalidClient,
		"rejected":       redmine.ErrOAuthRejected,
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRefresher{fn: func(context.Context, string, int) (*redmine.TokenSet, error) { return nil, cause }}
			m, v, uid := newManager(t, r)
			ctx := context.Background()
			if err := v.SaveTokens(ctx, uid, tokenSet("A0", "R0", t0.Add(-time.Minute))); err != nil {
				t.Fatal(err)
			}
			_, err := m.AccessToken(ctx, uid)
			if !errors.Is(err, cause) || errors.Is(err, ErrCredentialInvalid) {
				t.Fatalf("err = %v; want the cause and not ErrCredentialInvalid", err)
			}
			if _, err := v.LoadTokens(ctx, uid); err != nil {
				t.Errorf("grant was invalidated by a non-grant failure: %v", err)
			}
		})
	}
}

func TestManagerForceRefreshSkipsWhenAlreadyRotated(t *testing.T) {
	r := rotating()
	m, v, uid := newManager(t, r)
	ctx := context.Background()
	if err := v.SaveTokens(ctx, uid, tokenSet("A-new", "R-new", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	// 401 を受けたのは古い A-old。保存済みは既に A-new なので再リフレッシュしない。
	got, err := m.ForceRefresh(ctx, uid, "A-old")
	if err != nil || got != "A-new" || r.count() != 0 {
		t.Fatalf("ForceRefresh(stale) = %q, %v, calls=%d; want A-new without refreshing", got, err, r.count())
	}
	// 保存済みと同じトークンが 401 → 本当に失効しているのでリフレッシュする。
	got, err = m.ForceRefresh(ctx, uid, "A-new")
	if err != nil || got != "A1" || r.count() != 1 {
		t.Fatalf("ForceRefresh(current) = %q, %v, calls=%d; want A1 after one refresh", got, err, r.count())
	}
}

func TestManagerPersistsEvenIfCallerCancels(t *testing.T) {
	// Redmine がトークンを入れ替えた後に呼び出し元が切断しても、新しい組を
	// 保存しきる（取りこぼすと連鎖が壊れ、再ログインが必要になる）。
	started := make(chan struct{})
	release := make(chan struct{})
	r := &fakeRefresher{fn: func(ctx context.Context, _ string, n int) (*redmine.TokenSet, error) {
		close(started)
		<-release
		return tokenSet("A1", "R1", t0.Add(2*time.Hour)), nil
	}}
	m, v, uid := newManager(t, r)
	bg := context.Background()
	if err := v.SaveTokens(bg, uid, tokenSet("A0", "R0", t0.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = m.AccessToken(ctx, uid) }()
	<-started
	cancel()
	close(release)
	<-done

	stored, err := v.LoadTokens(bg, uid)
	if err != nil || stored.Refresh() != "R1" {
		t.Fatalf("stored after cancel = %v, %v; want the rotated pair persisted", stored, err)
	}
}

func TestManagerPersistFailureIsReported(t *testing.T) {
	var st *store.Store
	r := &fakeRefresher{fn: func(context.Context, string, int) (*redmine.TokenSet, error) {
		st.Close() // 回転は成功したが保存できない状況を作る
		return tokenSet("A1", "R1", t0.Add(time.Hour)), nil
	}}
	v, s, uid := newTokenVault(t)
	st = s
	m := NewManager(v, r, 60*time.Second)
	m.now = func() time.Time { return t0 }
	m.persistRetryDelay = time.Millisecond
	if err := v.SaveTokens(context.Background(), uid, tokenSet("A0", "R0", t0.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	_, err := m.AccessToken(context.Background(), uid)
	if !errors.Is(err, ErrPersistFailed) {
		t.Fatalf("err = %v; want ErrPersistFailed", err)
	}
	if strings.Contains(err.Error(), "R1") || strings.Contains(err.Error(), "A1") {
		t.Errorf("error leaks tokens: %v", err)
	}
}
