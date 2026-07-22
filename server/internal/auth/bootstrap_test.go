package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

type recordingVault struct {
	saved map[string]string
	err   error
}

func (v *recordingVault) SaveAPIKey(_ context.Context, userID, apiKey string) error {
	if v.saved == nil {
		v.saved = map[string]string{}
	}
	v.saved[userID] = apiKey
	return v.err
}

// fakeRedmine は /redmine/my/account.json だけを持つ上流。
func fakeRedmine(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redmine/my/account.json" {
			t.Errorf("unexpected upstream path %s (sub-URI must come from config)", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const accountJSON = `{"user":{"login":"alice","firstname":"Alice","lastname":"Doe","api_key":"redmine-key-1"}}`

func newBootstrap(t *testing.T, upstream string) (*Bootstrap, *store.Store, *recordingVault) {
	t.Helper()
	st := testStoreEmpty(t)
	wa, err := NewWebAuthn(st, WebAuthnConfig{
		RPID: testRPID, RPName: "RedminePocketGo", Origins: []string{testOrigin},
		UserVerification: "required", ChallengeTTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	vault := &recordingVault{}
	b := NewBootstrap(st, wa, vault, BootstrapConfig{
		BaseURL: upstream, SubURI: "/redmine", Timeout: 2 * time.Second,
	})
	return b, st, vault
}

func TestBootstrapSuccessCreatesUserAndStartsCeremony(t *testing.T) {
	srv := fakeRedmine(t, 200, accountJSON)
	b, st, vault := newBootstrap(t, srv.URL)
	ctx := context.Background()

	options, challengeID, err := b.Run(ctx, "alice", "secret")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if challengeID == "" || !strings.Contains(string(options), "publicKey") {
		t.Errorf("ceremony not started: id=%q options=%s", challengeID, options)
	}

	u, err := st.GetUserByLogin(ctx, "alice")
	if err != nil || u == nil {
		t.Fatalf("user not created: %v, %v", u, err)
	}
	if u.DisplayName != "Alice Doe" || len(u.WebAuthnUserHandle) != 64 {
		t.Errorf("user fields wrong: %+v", u)
	}
	if vault.saved[u.ID] != "redmine-key-1" {
		t.Errorf("api key not handed to vault: %v", vault.saved)
	}

	// 再実行してもユーザーは複製されない
	if _, _, err := b.Run(ctx, "alice", "secret"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	u2, _ := st.GetUserByLogin(ctx, "alice")
	if u2.ID != u.ID {
		t.Errorf("user duplicated: %s vs %s", u.ID, u2.ID)
	}
}

func TestBootstrapBadCredentials(t *testing.T) {
	srv := fakeRedmine(t, 200, accountJSON)
	b, _, vault := newBootstrap(t, srv.URL)

	_, _, err := b.Run(context.Background(), "alice", "wrong-password")
	if !errors.Is(err, ErrBadCredentials) || !errors.Is(err, httpapi.ErrUnauthenticated) {
		t.Fatalf("err = %v; want ErrBadCredentials wrapping ErrUnauthenticated", err)
	}
	// 不存在ユーザーも同じ経路・同じエラー
	_, _, err = b.Run(context.Background(), "nobody", "whatever")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("unknown user err = %v; want ErrBadCredentials", err)
	}
	if len(vault.saved) != 0 {
		t.Error("vault touched on failed auth")
	}
}

func TestBootstrapUpstreamFailure(t *testing.T) {
	srv := fakeRedmine(t, 503, "oops")
	b, _, _ := newBootstrap(t, srv.URL)
	_, _, err := b.Run(context.Background(), "alice", "secret")
	if !errors.Is(err, ErrRedmineUnavailable) || !errors.Is(err, httpapi.ErrUpstream) {
		t.Fatalf("err = %v; want ErrRedmineUnavailable wrapping ErrUpstream", err)
	}

	// 接続不能も上流障害
	srv.Close()
	if _, _, err := b.Run(context.Background(), "alice", "secret"); !errors.Is(err, httpapi.ErrUpstream) {
		t.Fatalf("connection refused err = %v; want ErrUpstream", err)
	}
}

func TestBootstrapMalformedUpstreamBody(t *testing.T) {
	srv := fakeRedmine(t, 200, `{"user":{"login":""}}`)
	b, _, _ := newBootstrap(t, srv.URL)
	if _, _, err := b.Run(context.Background(), "alice", "secret"); !errors.Is(err, httpapi.ErrUpstream) {
		t.Fatalf("err = %v; want ErrUpstream for missing fields", err)
	}
}

func TestRelinkSuccessSavesNewAPIKey(t *testing.T) {
	srv := fakeRedmine(t, 200, accountJSON)
	b, st, vault := newBootstrap(t, srv.URL)
	ctx := context.Background()
	if _, _, err := b.Run(ctx, "alice", "secret"); err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByLogin(ctx, "alice")
	if err != nil || u == nil {
		t.Fatalf("setup: %v, %v", u, err)
	}
	vault.saved[u.ID] = "" // 失効させた状態を模す

	if err := b.Relink(ctx, u.ID, "alice", "secret"); err != nil {
		t.Fatalf("Relink: %v", err)
	}
	if vault.saved[u.ID] != "redmine-key-1" {
		t.Errorf("api key not refreshed: %v", vault.saved)
	}
}

func TestRelinkRejectsDifferentRedmineAccount(t *testing.T) {
	// 上流は login=alice のアカウントを返すが、対象ユーザーは bob。
	srv := fakeRedmine(t, 200, accountJSON)
	b, st, vault := newBootstrap(t, srv.URL)
	ctx := context.Background()
	bob := &store.User{ID: "u-bob", RedmineLogin: "bob", WebAuthnUserHandle: []byte("x")}
	if err := st.CreateUser(ctx, bob); err != nil {
		t.Fatal(err)
	}

	err := b.Relink(ctx, bob.ID, "alice", "secret")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v; want ErrBadCredentials (login mismatch)", err)
	}
	if len(vault.saved) != 0 {
		t.Error("vault touched despite login mismatch")
	}
}

func TestRelinkBadCredentials(t *testing.T) {
	srv := fakeRedmine(t, 200, accountJSON)
	b, st, vault := newBootstrap(t, srv.URL)
	ctx := context.Background()
	if _, _, err := b.Run(ctx, "alice", "secret"); err != nil {
		t.Fatal(err)
	}
	u, _ := st.GetUserByLogin(ctx, "alice")

	if err := b.Relink(ctx, u.ID, "alice", "wrong-password"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v; want ErrBadCredentials", err)
	}
	if vault.saved[u.ID] == "" {
		// setup 済みの値のまま変わっていないことを確認（上書きされていない）
	} else if vault.saved[u.ID] != "redmine-key-1" {
		t.Errorf("vault mutated on bad credentials: %v", vault.saved)
	}
}

func TestRelinkUpstreamFailure(t *testing.T) {
	srv := fakeRedmine(t, 503, "oops")
	b, st, _ := newBootstrap(t, srv.URL)
	ctx := context.Background()
	u := &store.User{ID: "u1", RedmineLogin: "alice", WebAuthnUserHandle: []byte("x")}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := b.Relink(ctx, u.ID, "alice", "secret"); !errors.Is(err, httpapi.ErrUpstream) {
		t.Fatalf("err = %v; want ErrUpstream", err)
	}
}
