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
