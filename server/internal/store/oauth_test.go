package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/migrations"
)

func freshStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// applyOnlyInit は 0001 だけを適用済みにして旧スキーマの DB を作る
// （Migrate が既存データを壊さないことの検証用）。
func applyOnlyInit(t *testing.T, s *Store) {
	t.Helper()
	body, err := fs.ReadFile(migrations.FS, "0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		string(body),
		`INSERT INTO schema_migrations (version) VALUES ('0001_init.sql')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("setup legacy schema: %v", err)
		}
	}
}

func TestMigrateAddsOAuthTables(t *testing.T) {
	s := freshStore(t)
	for _, table := range []string{"oauth_tokens", "oauth_states"} {
		var name string
		if err := s.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestMigrateKeepsLegacyRowsAndForeignKeys(t *testing.T) {
	// 0002 は users を作り直す。外部キー有効のまま DROP すると子テーブルが
	// CASCADE で消えるため、既存データが残ることを確かめる。
	s := openTestStore(t)
	applyOnlyInit(t, s)
	for _, q := range []string{
		`INSERT INTO users (id, redmine_login, display_name, webauthn_user_handle) VALUES ('u1', 'alice', 'Alice', X'01')`,
		`INSERT INTO credentials (id, user_id, public_key) VALUES (X'AA', 'u1', X'BB')`,
		`INSERT INTO sessions (id, user_id, absolute_expires_at) VALUES ('sess-hash', 'u1', '2099-01-01T00:00:00Z')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("seed legacy row: %v\n%s", err, q)
		}
	}

	// 0002 だけを適用した時点で子テーブル（sessions）が残っていること。
	body, err := fs.ReadFile(migrations.FS, "0002_oauth.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.applyMigration("0002_oauth.sql", string(body)); err != nil {
		t.Fatalf("apply 0002: %v", err)
	}
	var kept int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM sessions").Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("sessions after 0002 = %d, %v; want 1（作り直しで子テーブルが消えていないこと）", kept, err)
	}

	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate on legacy DB: %v", err)
	}

	// 0003 は旧方式のセッションを破棄する（ログイン名の再利用で別人に結び付かないように）。
	for table, want := range map[string]int{"users": 1, "sessions": 0} {
		var n int
		if err := s.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Errorf("%s rows = %d, %v; want %d", table, n, err, want)
		}
	}
	var login string
	var rmID *int64
	if err := s.DB().QueryRow(
		`SELECT redmine_login, redmine_user_id FROM users WHERE id='u1'`,
	).Scan(&login, &rmID); err != nil {
		t.Fatalf("read migrated user: %v", err)
	}
	if login != "alice" || rmID != nil {
		t.Errorf("migrated user = %q %v; want alice, redmine_user_id NULL", login, rmID)
	}

	// 外部キー制約は適用後も有効であること（PRAGMA を戻し忘れていない）。
	var fk string
	if err := s.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != "1" {
		t.Errorf("PRAGMA foreign_keys = %q, %v; want 1", fk, err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sessions (id, user_id, absolute_expires_at) VALUES ('x', 'ghost', '2099-01-01T00:00:00Z')`); err == nil {
		t.Error("dangling sessions.user_id accepted after migration")
	}

	// 旧行は初回の OAuth ログインでログイン名から引き継がれる。
	u, err := s.UpsertOAuthUser(context.Background(), 9, "alice", "Alice")
	if err != nil || u.ID != "u1" || u.RedmineUserID != 9 {
		t.Errorf("legacy adoption = %+v, %v; want id u1 with redmine_user_id 9", u, err)
	}
}

func TestMigratedUsersSchema(t *testing.T) {
	s := freshStore(t)
	for _, q := range []string{
		`INSERT INTO users (id, redmine_login, redmine_user_id) VALUES ('a', 'alice', 1)`,
		`INSERT INTO users (id, redmine_login, redmine_user_id) VALUES ('b', 'bob', 2)`,
		`INSERT INTO users (id, redmine_login) VALUES ('legacy1', 'old1')`, // 未紐付けの旧行（NULL）は複数あってよい
		`INSERT INTO users (id, redmine_login) VALUES ('legacy2', 'old2')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("insert user: %v\n%s", err, q)
		}
	}
	// redmine_user_id は一意。
	if _, err := s.DB().Exec(`INSERT INTO users (id, redmine_login, redmine_user_id) VALUES ('c', 'carol', 1)`); err == nil {
		t.Error("duplicate redmine_user_id accepted")
	}
	// ログイン名は Redmine 側で付け替わりうるため一意の鍵にしない。
	if _, err := s.DB().Exec(`INSERT INTO users (id, redmine_login, redmine_user_id) VALUES ('d', 'alice', 4)`); err != nil {
		t.Errorf("same login for a different redmine_user_id rejected: %v", err)
	}
}

func TestUpsertOAuthUser(t *testing.T) {
	ctx := context.Background()

	t.Run("creates then updates by redmine user id", func(t *testing.T) {
		s := freshStore(t)
		u1, err := s.UpsertOAuthUser(ctx, 7, "alice", "Alice A")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if u1.ID == "" || u1.RedmineUserID != 7 || u1.RedmineLogin != "alice" || u1.DisplayName != "Alice A" {
			t.Fatalf("created = %+v", u1)
		}
		// Redmine 側でログイン名・表示名が変わっても同一人物（id は不変）。
		u2, err := s.UpsertOAuthUser(ctx, 7, "alice2", "Alice B")
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if u2.ID != u1.ID || u2.RedmineLogin != "alice2" || u2.DisplayName != "Alice B" {
			t.Errorf("updated = %+v; want same id %s with new login/name", u2, u1.ID)
		}
		var n int
		_ = s.DB().QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
		if n != 1 {
			t.Errorf("users = %d; want 1", n)
		}
	})

	t.Run("adopts a legacy row by login", func(t *testing.T) {
		s := freshStore(t)
		if _, err := s.DB().Exec(`INSERT INTO users (id, redmine_login) VALUES ('legacy', 'bob')`); err != nil {
			t.Fatal(err)
		}
		u, err := s.UpsertOAuthUser(ctx, 42, "bob", "Bob")
		if err != nil {
			t.Fatalf("adopt: %v", err)
		}
		if u.ID != "legacy" || u.RedmineUserID != 42 {
			t.Errorf("adopted = %+v; want id legacy, redmine_user_id 42", u)
		}
	})

	t.Run("does not adopt a row that already belongs to another redmine user", func(t *testing.T) {
		s := freshStore(t)
		first, err := s.UpsertOAuthUser(ctx, 1, "shared", "First")
		if err != nil {
			t.Fatal(err)
		}
		// 別の Redmine ユーザー（id 2）が同名のログイン名を名乗っても、
		// id 1 の行を乗っ取らず別の利用者として作られる。
		second, err := s.UpsertOAuthUser(ctx, 2, "shared", "Second")
		if err != nil {
			t.Fatalf("second: %v", err)
		}
		if second.ID == first.ID {
			t.Fatal("login collision let a different redmine user take over an existing row")
		}
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		s := freshStore(t)
		if _, err := s.UpsertOAuthUser(ctx, 0, "x", "x"); err == nil {
			t.Error("redmine user id 0 accepted")
		}
		if _, err := s.UpsertOAuthUser(ctx, 5, "", "x"); err == nil {
			t.Error("empty login accepted")
		}
	})
}

func TestOAuthTokensRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	u, err := s.UpsertOAuthUser(ctx, 1, "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}

	if got, err := s.GetOAuthTokens(ctx, u.ID); err != nil || got != nil {
		t.Fatalf("GetOAuthTokens before save = %v, %v; want nil, nil", got, err)
	}

	exp := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	in := OAuthTokens{
		UserID:           u.ID,
		AccessCiphertext: []byte("ac"), AccessNonce: []byte("an"),
		RefreshCiphertext: []byte("rc"), RefreshNonce: []byte("rn"),
		KeyVersion: 3, Scopes: "view_project view_issues", AccessExpiresAt: exp,
	}
	if err := s.SaveOAuthTokens(ctx, in); err != nil {
		t.Fatalf("SaveOAuthTokens: %v", err)
	}
	got, err := s.GetOAuthTokens(ctx, u.ID)
	if err != nil || got == nil {
		t.Fatalf("GetOAuthTokens = %v, %v", got, err)
	}
	if string(got.AccessCiphertext) != "ac" || string(got.AccessNonce) != "an" ||
		string(got.RefreshCiphertext) != "rc" || string(got.RefreshNonce) != "rn" ||
		got.KeyVersion != 3 || got.Scopes != "view_project view_issues" ||
		!got.AccessExpiresAt.Equal(exp) || got.Status != "active" || got.RefreshedAt.IsZero() {
		t.Errorf("round trip = %+v", got)
	}

	// 無効化 → 保存し直すと active に戻る（再認可）。
	if err := s.SetOAuthTokenStatus(ctx, u.ID, "invalid"); err != nil {
		t.Fatalf("SetOAuthTokenStatus: %v", err)
	}
	if got, _ := s.GetOAuthTokens(ctx, u.ID); got.Status != "invalid" {
		t.Errorf("status = %q; want invalid", got.Status)
	}
	in.AccessCiphertext = []byte("ac2")
	if err := s.SaveOAuthTokens(ctx, in); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if got, _ := s.GetOAuthTokens(ctx, u.ID); got.Status != "active" || string(got.AccessCiphertext) != "ac2" {
		t.Errorf("after re-save = %+v; want active with new ciphertext", got)
	}

	if err := s.SetOAuthTokenStatus(ctx, "no-such-user", "invalid"); !errors.Is(err, ErrNoTokenRow) {
		t.Errorf("status of missing row: err = %v; want ErrNoTokenRow", err)
	}
	if err := s.SetOAuthTokenStatus(ctx, u.ID, "bogus"); err == nil {
		t.Error("bogus status accepted")
	}

	// 利用者を消すとトークンも消える（CASCADE）。
	if _, err := s.DB().Exec("DELETE FROM users WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetOAuthTokens(ctx, u.ID); got != nil {
		t.Errorf("tokens survived user deletion: %+v", got)
	}
}

func TestOAuthStateSingleUse(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	if err := s.InsertOAuthState(ctx, "h1", []byte("vc"), []byte("vn"), "#projects", now.Add(10*time.Minute)); err != nil {
		t.Fatalf("InsertOAuthState: %v", err)
	}
	if err := s.InsertOAuthState(ctx, "h1", nil, nil, "", now.Add(time.Minute)); err == nil {
		t.Error("duplicate state hash accepted")
	}

	st, ok, err := s.ConsumeOAuthState(ctx, "h1", now)
	if err != nil || !ok {
		t.Fatalf("first consume = %v, %v; want ok", ok, err)
	}
	if string(st.VerifierCiphertext) != "vc" || string(st.VerifierNonce) != "vn" || st.ReturnTo != "#projects" {
		t.Errorf("consumed = %+v", st)
	}
	if _, ok, _ := s.ConsumeOAuthState(ctx, "h1", now); ok {
		t.Error("state consumed twice")
	}
	if _, ok, _ := s.ConsumeOAuthState(ctx, "unknown", now); ok {
		t.Error("unknown state accepted")
	}

	// 期限切れは消費できない。
	if err := s.InsertOAuthState(ctx, "h2", []byte("x"), []byte("y"), "", now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ConsumeOAuthState(ctx, "h2", now); ok {
		t.Error("expired state accepted")
	}
	if err := s.DeleteExpiredOAuthStates(ctx, now); err != nil {
		t.Fatalf("DeleteExpiredOAuthStates: %v", err)
	}
	var n int
	_ = s.DB().QueryRow("SELECT COUNT(*) FROM oauth_states WHERE state_hash='h2'").Scan(&n)
	if n != 0 {
		t.Error("expired state not deleted")
	}
}

func TestOAuthStateConsumeIsAtomic(t *testing.T) {
	// 同じ state の並行消費は、ちょうど 1 つだけが成功する（コールバックの
	// 二重実行・リプレイ対策）。
	ctx := context.Background()
	s := freshStore(t)
	now := time.Now().UTC()
	if err := s.InsertOAuthState(ctx, "race", []byte("v"), []byte("n"), "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, err := s.ConsumeOAuthState(ctx, "race", now); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Errorf("successful consumes = %d; want exactly 1", wins.Load())
	}
}

// ---- 0003: 旧方式（パスキー・API キー）の削除 ----

func TestMigrateDropsLegacySchema(t *testing.T) {
	s := freshStore(t)
	for _, table := range []string{"credentials", "redmine_credentials", "enrollment_codes", "webauthn_challenges"} {
		var name string
		err := s.DB().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err == nil {
			t.Errorf("legacy table %s still exists", table)
		}
	}
	for table, col := range map[string]string{"users": "webauthn_user_handle", "sessions": "credential_id"} {
		rows, err := s.DB().Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if name == col {
				t.Errorf("%s.%s still exists", table, col)
			}
		}
		rows.Close()
	}
}

func TestMigrateDoesNotLeaveAPIKeyCiphertextOnDisk(t *testing.T) {
	// 旧 API キーの暗号文は、削除後のファイルに残さない（DROP だけでは空きページに
	// 残るため secure_delete で消す）。
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	s, err := Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	applyOnlyInit(t, s)
	pattern := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x12}, 24)
	for _, q := range []string{
		`INSERT INTO users (id, redmine_login, webauthn_user_handle) VALUES ('u1', 'alice', X'01')`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(
		`INSERT INTO redmine_credentials (user_id, api_key_ciphertext, api_key_nonce) VALUES ('u1', ?, X'02')`, pattern); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(path); !bytes.Contains(raw, pattern) {
		t.Skip("test setup: pattern not found in the DB file before migrating")
	}

	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.DB().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, pattern) {
		t.Error("legacy API key ciphertext is still present in the database file after migration")
	}
}

func TestSessionsKeepWorkingAfterLegacyDrop(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	u, err := s.UpsertOAuthUser(ctx, 3, "carol", "Carol")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i, abs := range []time.Time{now.Add(time.Hour), now.Add(time.Hour), now.Add(-time.Hour)} {
		if err := s.InsertSession(ctx, &Session{
			IDHash: fmt.Sprintf("h%d", i), UserID: u.ID, CreatedAt: now, LastSeenAt: now, AbsoluteExpiresAt: abs,
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.CountActiveSessions(ctx, u.ID, now)
	if err != nil || n != 2 {
		t.Errorf("CountActiveSessions = %d, %v; want 2（期限切れは数えない）", n, err)
	}
	if n, _ := s.CountActiveSessions(ctx, "nobody", now); n != 0 {
		t.Errorf("CountActiveSessions(nobody) = %d", n)
	}
}

func TestDeleteOAuthTokens(t *testing.T) {
	ctx := context.Background()
	s := freshStore(t)
	u, _ := s.UpsertOAuthUser(ctx, 1, "alice", "A")
	if err := s.SaveOAuthTokens(ctx, OAuthTokens{
		UserID: u.ID, AccessCiphertext: []byte("a"), AccessNonce: []byte("n"),
		RefreshCiphertext: []byte("r"), RefreshNonce: []byte("n"), KeyVersion: 1, AccessExpiresAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOAuthTokens(ctx, u.ID); err != nil {
		t.Fatalf("DeleteOAuthTokens: %v", err)
	}
	if got, _ := s.GetOAuthTokens(ctx, u.ID); got != nil {
		t.Error("tokens still present")
	}
	if err := s.DeleteOAuthTokens(ctx, u.ID); err != nil {
		t.Errorf("deleting twice should be a no-op: %v", err)
	}
}
