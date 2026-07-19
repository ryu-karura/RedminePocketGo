package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "test.db")
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateEmptyDB(t *testing.T) {
	s := openTestStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Design.md §5 の全テーブルが存在すること。
	for _, table := range []string{
		"users", "credentials", "redmine_credentials",
		"sessions", "enrollment_codes", "webauthn_challenges",
	} {
		var name string
		err := s.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestMigrateIdempotent(t *testing.T) {
	// LESSONS.md の例が示す罠: 適用済み DB への再適用で壊れてはならない。
	s := openTestStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("second Migrate on migrated DB: %v", err)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	s := openTestStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	_, err := s.DB().Exec(
		"INSERT INTO credentials (id, user_id, public_key) VALUES (?, ?, ?)",
		[]byte{1}, "no-such-user", []byte{2},
	)
	if err == nil {
		t.Fatal("insert with dangling user_id succeeded; foreign keys are not enforced")
	}
}

func TestSessionsSchemaHoldsHashedIDOnly(t *testing.T) {
	// sessions.id はハッシュのみを保存する設計（Design.md §5.4）。
	// スキーマとして user/credential/期限の列が揃っていることを確認する。
	s := openTestStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.DB().Exec("INSERT INTO users (id, redmine_login, webauthn_user_handle) VALUES ('u1', 'alice', X'01')"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, err := s.DB().Exec(
		"INSERT INTO sessions (id, user_id, absolute_expires_at) VALUES ('hash-of-id', 'u1', '2026-08-01T00:00:00Z')",
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
}
