package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Session は永続化されたセッション。ID は生値を保存せずハッシュのみ
// （Design.md §5.4）。二軸タイムアウトの判定は internal/auth が行う。
type Session struct {
	IDHash            string
	UserID            string
	CredentialID      []byte
	CreatedAt         time.Time
	LastSeenAt        time.Time
	AbsoluteExpiresAt time.Time
}

// User は rmapp の利用者（Design.md §5.1）。
type User struct {
	ID                 string
	RedmineLogin       string
	DisplayName        string
	WebAuthnUserHandle []byte
}

const timeLayout = time.RFC3339Nano

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// CreateUser は利用者を作成する。
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, redmine_login, display_name, webauthn_user_handle)
		 VALUES (?, ?, ?, ?)`,
		u.ID, u.RedmineLogin, u.DisplayName, u.WebAuthnUserHandle,
	)
	if err != nil {
		return fmt.Errorf("store: ユーザー作成に失敗しました: %w", err)
	}
	return nil
}

// GetUserByLogin は Redmine ログイン名で利用者を引く。未登録は (nil, nil)。
func (s *Store) GetUserByLogin(ctx context.Context, login string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, redmine_login, display_name, webauthn_user_handle
		 FROM users WHERE redmine_login = ?`, login))
}

// GetUserByID は ID で利用者を引く。未登録は (nil, nil)。
func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, redmine_login, display_name, webauthn_user_handle
		 FROM users WHERE id = ?`, id))
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.RedmineLogin, &u.DisplayName, &u.WebAuthnUserHandle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: ユーザー取得に失敗しました: %w", err)
	}
	return &u, nil
}

// InsertSession はセッションを保存する。
func (s *Store) InsertSession(ctx context.Context, sess *Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, credential_id, created_at, last_seen_at, absolute_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.IDHash, sess.UserID, sess.CredentialID,
		fmtTime(sess.CreatedAt), fmtTime(sess.LastSeenAt), fmtTime(sess.AbsoluteExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("store: セッション保存に失敗しました: %w", err)
	}
	return nil
}

// GetSession はハッシュでセッションを引く。存在しなければ (nil, nil)。
func (s *Store) GetSession(ctx context.Context, idHash string) (*Session, error) {
	var (
		sess                          Session
		created, lastSeen, absExpires string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, credential_id, created_at, last_seen_at, absolute_expires_at
		 FROM sessions WHERE id = ?`, idHash,
	).Scan(&sess.IDHash, &sess.UserID, &sess.CredentialID, &created, &lastSeen, &absExpires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: セッション取得に失敗しました: %w", err)
	}
	for _, p := range []struct {
		dst *time.Time
		src string
	}{{&sess.CreatedAt, created}, {&sess.LastSeenAt, lastSeen}, {&sess.AbsoluteExpiresAt, absExpires}} {
		t, err := parseTime(p.src)
		if err != nil {
			return nil, fmt.Errorf("store: セッションの時刻が不正です: %w", err)
		}
		*p.dst = t
	}
	return &sess, nil
}

// TouchSession はアイドルタイムアウト判定用の last_seen_at を進める。
func (s *Store) TouchSession(ctx context.Context, idHash string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET last_seen_at = ? WHERE id = ?", fmtTime(now), idHash,
	); err != nil {
		return fmt.Errorf("store: セッション更新に失敗しました: %w", err)
	}
	return nil
}

// DeleteSession はセッションを失効させる（ログアウト・期限切れ）。
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM sessions WHERE id = ?", idHash,
	); err != nil {
		return fmt.Errorf("store: セッション削除に失敗しました: %w", err)
	}
	return nil
}

// DeleteSessionsByCredential は該当パスキーの全セッションを即失効させる
// （端末削除時。Design.md §3.5）。
func (s *Store) DeleteSessionsByCredential(ctx context.Context, credentialID []byte) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM sessions WHERE credential_id = ?", credentialID,
	); err != nil {
		return fmt.Errorf("store: パスキーのセッション失効に失敗しました: %w", err)
	}
	return nil
}
