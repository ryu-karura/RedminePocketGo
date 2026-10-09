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
	CreatedAt         time.Time
	LastSeenAt        time.Time
	AbsoluteExpiresAt time.Time
}

// User は rmapp の利用者（Design.md §5.1）。
type User struct {
	ID string
	// RedmineUserID は Redmine のユーザー ID（同一性の鍵）。旧方式の行で
	// 初回の OAuth ログイン前は 0。
	RedmineUserID int64
	RedmineLogin  string
	DisplayName   string
}

const timeLayout = time.RFC3339Nano

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

// GetUserByID は ID で利用者を引く。未登録は (nil, nil)。
func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, redmine_user_id, redmine_login, display_name
		 FROM users WHERE id = ?`, id))
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var (
		u    User
		rmID sql.NullInt64
	)
	err := row.Scan(&u.ID, &rmID, &u.RedmineLogin, &u.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: ユーザー取得に失敗しました: %w", err)
	}
	u.RedmineUserID = rmID.Int64
	return &u, nil
}

// InsertSession はセッションを保存する。
func (s *Store) InsertSession(ctx context.Context, sess *Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, last_seen_at, absolute_expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		sess.IDHash, sess.UserID,
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
		`SELECT id, user_id, created_at, last_seen_at, absolute_expires_at
		 FROM sessions WHERE id = ?`, idHash,
	).Scan(&sess.IDHash, &sess.UserID, &created, &lastSeen, &absExpires)
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

// CountActiveSessions は利用者の有効なセッション数（絶対期限内のもの）を返す。
// ログアウト時に、他の端末のセッションが残っているかの判定に使う。アイドル
// 期限はここでは見ない（過大に数えて、トークンを失効させない側に倒す）。
func (s *Store) CountActiveSessions(ctx context.Context, userID string, now time.Time) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sessions WHERE user_id = ? AND absolute_expires_at > ?",
		userID, fmtTime(now),
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: セッション数の取得に失敗しました: %w", err)
	}
	return n, nil
}
