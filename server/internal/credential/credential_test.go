package credential

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

func testVault(t *testing.T) (*Vault, *store.Store) {
	t.Helper()
	st, err := store.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(context.Background(), &store.User{
		ID: "u1", RedmineLogin: "alice", WebAuthnUserHandle: []byte{1},
	}); err != nil {
		t.Fatal(err)
	}
	// 32 バイト鍵
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i)
	}
	v, err := NewVault(st, kek, 1)
	if err != nil {
		t.Fatal(err)
	}
	return v, st
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	v, _ := testVault(t)
	ctx := context.Background()

	if err := v.SaveAPIKey(ctx, "u1", "redmine-secret-key"); err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	key, err := v.LoadAPIKey(ctx, "u1")
	if err != nil {
		t.Fatalf("LoadAPIKey: %v", err)
	}
	if key.Value() != "redmine-secret-key" {
		t.Errorf("round-trip = %q; want redmine-secret-key", key.Value())
	}
}

func TestCiphertextIsNotPlaintext(t *testing.T) {
	v, st := testVault(t)
	ctx := context.Background()
	if err := v.SaveAPIKey(ctx, "u1", "redmine-secret-key"); err != nil {
		t.Fatal(err)
	}
	// DB に平文が現れないこと
	var ct, nonce []byte
	if err := st.DB().QueryRow(
		"SELECT api_key_ciphertext, api_key_nonce FROM redmine_credentials WHERE user_id = ?", "u1",
	).Scan(&ct, &nonce); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ct), "redmine-secret-key") {
		t.Error("plaintext api key found in ciphertext column")
	}
	if len(nonce) == 0 {
		t.Error("nonce not stored")
	}
}

func TestNoncePerRecord(t *testing.T) {
	v, st := testVault(t)
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.User{ID: "u2", RedmineLogin: "bob", WebAuthnUserHandle: []byte{2}}); err != nil {
		t.Fatal(err)
	}
	// 同じ平文でもノンスが異なる → 暗号文が異なる
	_ = v.SaveAPIKey(ctx, "u1", "same-key")
	_ = v.SaveAPIKey(ctx, "u2", "same-key")

	var ct1, ct2, n1, n2 []byte
	st.DB().QueryRow("SELECT api_key_ciphertext, api_key_nonce FROM redmine_credentials WHERE user_id='u1'").Scan(&ct1, &n1)
	st.DB().QueryRow("SELECT api_key_ciphertext, api_key_nonce FROM redmine_credentials WHERE user_id='u2'").Scan(&ct2, &n2)
	if string(n1) == string(n2) {
		t.Error("nonce reused across records")
	}
	if string(ct1) == string(ct2) {
		t.Error("identical ciphertext for same plaintext (nonce reuse)")
	}
}

func TestTamperedCiphertextFails(t *testing.T) {
	v, st := testVault(t)
	ctx := context.Background()
	if err := v.SaveAPIKey(ctx, "u1", "redmine-secret-key"); err != nil {
		t.Fatal(err)
	}
	// 暗号文を 1 バイト改竄 → GCM 認証で復号失敗
	st.DB().Exec("UPDATE redmine_credentials SET api_key_ciphertext = ? WHERE user_id='u1'", []byte("tampered-bytes-xxxxxxxxxxxxx"))
	if _, err := v.LoadAPIKey(ctx, "u1"); err == nil {
		t.Error("tampered ciphertext decrypted without error")
	}
}

func TestSaveOverwrites(t *testing.T) {
	v, _ := testVault(t)
	ctx := context.Background()
	_ = v.SaveAPIKey(ctx, "u1", "first")
	if err := v.SaveAPIKey(ctx, "u1", "second"); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	key, _ := v.LoadAPIKey(ctx, "u1")
	if key.Value() != "second" {
		t.Errorf("overwrite failed: got %q", key.Value())
	}
}

func TestLoadMissingIsNotFound(t *testing.T) {
	v, _ := testVault(t)
	if _, err := v.LoadAPIKey(context.Background(), "u1"); err != ErrNoCredential {
		t.Errorf("err = %v; want ErrNoCredential", err)
	}
}

func TestMarkInvalidAndStatus(t *testing.T) {
	v, _ := testVault(t)
	ctx := context.Background()
	_ = v.SaveAPIKey(ctx, "u1", "k")

	if err := v.MarkInvalid(ctx, "u1"); err != nil {
		t.Fatalf("MarkInvalid: %v", err)
	}
	// 無効化後は 409 相当（ErrCredentialInvalid）
	if _, err := v.LoadAPIKey(ctx, "u1"); err != ErrCredentialInvalid {
		t.Errorf("after MarkInvalid: err = %v; want ErrCredentialInvalid", err)
	}
	// 再保存で active に戻る
	if err := v.SaveAPIKey(ctx, "u1", "new-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.LoadAPIKey(ctx, "u1"); err != nil {
		t.Errorf("re-save should reactivate: %v", err)
	}
}

func TestAPIKeyMarshalJSONRedacts(t *testing.T) {
	v, _ := testVault(t)
	ctx := context.Background()
	_ = v.SaveAPIKey(ctx, "u1", "top-secret")
	key, _ := v.LoadAPIKey(ctx, "u1")

	b, err := json.Marshal(struct {
		Key *APIKey `json:"key"`
	}{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "top-secret") {
		t.Errorf("marshaled JSON leaks the key: %s", b)
	}
	if !strings.Contains(string(b), "[redacted]") {
		t.Errorf("marshaled JSON should show [redacted]: %s", b)
	}
}

func TestNewVaultRejectsBadKEK(t *testing.T) {
	st, _ := store.Open("file:" + filepath.Join(t.TempDir(), "t.db"))
	defer st.Close()
	if _, err := NewVault(st, make([]byte, 16), 1); err == nil {
		t.Error("16-byte KEK accepted; AES-256 requires 32 bytes")
	}
}

func TestAPIKeyRedactedByValueNotJustPointer(t *testing.T) {
	// 値で fmt/JSON しても平文が漏れないこと（値レシーバの回帰テスト）。
	k := NewTestAPIKey("top-secret")
	if got := k.String(); got != "[redacted]" {
		t.Errorf("pointer String() = %q", got)
	}
	// 値のまま %v / %s
	if s := fmtSprintf("%v / %s", *k, *k); strings.Contains(s, "top-secret") {
		t.Errorf("value formatting leaked the key: %s", s)
	}
	// 値のまま json.Marshal
	b, _ := jsonMarshal(*k)
	if strings.Contains(string(b), "top-secret") {
		t.Errorf("value json.Marshal leaked the key: %s", b)
	}
}
