package store

import (
	"context"
	"testing"
	"time"
)

func migratedStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.CreateUser(context.Background(), &User{
		ID: "u1", RedmineLogin: "alice", DisplayName: "Alice",
		WebAuthnUserHandle: []byte{1, 2, 3},
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return s
}

func TestSessionRoundTrip(t *testing.T) {
	s := migratedStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	sess := &Session{
		IDHash:            "hash-1",
		UserID:            "u1",
		CredentialID:      []byte{9},
		CreatedAt:         now,
		LastSeenAt:        now,
		AbsoluteExpiresAt: now.Add(720 * time.Hour),
	}
	if err := s.InsertSession(ctx, sess); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	got, err := s.GetSession(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil || got.UserID != "u1" || !got.LastSeenAt.Equal(now) ||
		!got.AbsoluteExpiresAt.Equal(sess.AbsoluteExpiresAt) {
		t.Errorf("roundtrip mismatch: %+v", got)
	}

	// 未知のハッシュは (nil, nil)
	if got, err := s.GetSession(ctx, "nope"); err != nil || got != nil {
		t.Errorf("unknown hash: got %v, %v; want nil, nil", got, err)
	}
}

func TestSessionTouch(t *testing.T) {
	s := migratedStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.InsertSession(ctx, &Session{
		IDHash: "h", UserID: "u1", CreatedAt: now, LastSeenAt: now,
		AbsoluteExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(10 * time.Minute)
	if err := s.TouchSession(ctx, "h", later); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	got, _ := s.GetSession(ctx, "h")
	if !got.LastSeenAt.Equal(later) {
		t.Errorf("LastSeenAt = %v; want %v", got.LastSeenAt, later)
	}
}

func TestSessionDelete(t *testing.T) {
	s := migratedStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cred := []byte{7, 7}
	for _, h := range []string{"a", "b"} {
		if err := s.InsertSession(ctx, &Session{
			IDHash: h, UserID: "u1", CredentialID: cred,
			CreatedAt: now, LastSeenAt: now, AbsoluteExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.DeleteSession(ctx, "a"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if got, _ := s.GetSession(ctx, "a"); got != nil {
		t.Error("session a still present after delete")
	}

	// パスキー削除時の一括失効（Design.md §3.5）
	if err := s.DeleteSessionsByCredential(ctx, cred); err != nil {
		t.Fatalf("DeleteSessionsByCredential: %v", err)
	}
	if got, _ := s.GetSession(ctx, "b"); got != nil {
		t.Error("session b still present after credential revocation")
	}
}

func TestRenameAndDeleteCredentialScopedToOwner(t *testing.T) {
	s := migratedStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cred := &Credential{ID: []byte{1}, UserID: "u1", PublicKey: []byte{2}, CreatedAt: now}
	if err := s.InsertCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSession(ctx, &Session{
		IDHash: "h1", UserID: "u1", CredentialID: cred.ID,
		CreatedAt: now, LastSeenAt: now, AbsoluteExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// 他人のパスキーは変更も削除もできない
	if ok, err := s.RenameCredential(ctx, cred.ID, "someone-else", "x"); err != nil || ok {
		t.Errorf("rename by non-owner: ok=%v err=%v; want false", ok, err)
	}
	if ok, err := s.DeleteCredentialAndSessions(ctx, cred.ID, "someone-else"); err != nil || ok {
		t.Errorf("delete by non-owner: ok=%v err=%v; want false", ok, err)
	}

	if ok, err := s.RenameCredential(ctx, cred.ID, "u1", "iPhone"); err != nil || !ok {
		t.Fatalf("rename: ok=%v err=%v", ok, err)
	}
	creds, _ := s.ListCredentialsByUser(ctx, "u1")
	if len(creds) != 1 || creds[0].DeviceLabel != "iPhone" {
		t.Errorf("label not updated: %+v", creds)
	}

	// 削除で該当セッションも同時に失効する
	if ok, err := s.DeleteCredentialAndSessions(ctx, cred.ID, "u1"); err != nil || !ok {
		t.Fatalf("delete: ok=%v err=%v", ok, err)
	}
	if got, _ := s.GetSession(ctx, "h1"); got != nil {
		t.Error("session survived credential deletion")
	}
	if creds, _ := s.ListCredentialsByUser(ctx, "u1"); len(creds) != 0 {
		t.Error("credential not deleted")
	}
}

func TestEnrollmentCodeConcurrentSingleUse(t *testing.T) {
	s := migratedStore(t)
	ctx := context.Background()
	if err := s.InsertEnrollmentCode(ctx, "codehash", "u1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	const n = 20
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() {
			uid, ok, err := s.ConsumeEnrollmentCode(ctx, "codehash", time.Now())
			results <- (ok && err == nil && uid == "u1")
		}()
	}
	wins := 0
	for i := 0; i < n; i++ {
		if <-results {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("concurrent consume winners = %d; want exactly 1 (single-use violated)", wins)
	}
}
