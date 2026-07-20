package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Credential はパスキー 1 件（Design.md §5.2）。端末ごとに 1 行。
type Credential struct {
	ID             []byte
	UserID         string
	PublicKey      []byte
	SignCount      uint32
	AAGUID         []byte
	Transports     string
	DeviceLabel    string
	DeviceKind     string
	BackupEligible bool
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

func (s *Store) InsertCredential(ctx context.Context, c *Credential) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO credentials
		 (id, user_id, public_key, sign_count, aaguid, transports, device_label, device_kind, backup_eligible, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.UserID, c.PublicKey, c.SignCount, c.AAGUID,
		c.Transports, c.DeviceLabel, c.DeviceKind, c.BackupEligible, fmtTime(c.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("store: パスキー保存に失敗しました: %w", err)
	}
	return nil
}

// ListCredentialsByUser は利用者のパスキー一覧を返す（作成順）。
func (s *Store) ListCredentialsByUser(ctx context.Context, userID string) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, public_key, sign_count, aaguid, transports, device_label, device_kind, backup_eligible, created_at, last_used_at
		 FROM credentials WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: パスキー一覧の取得に失敗しました: %w", err)
	}
	defer rows.Close()

	var out []Credential
	for rows.Next() {
		var (
			c        Credential
			created  string
			lastUsed sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.UserID, &c.PublicKey, &c.SignCount, &c.AAGUID,
			&c.Transports, &c.DeviceLabel, &c.DeviceKind, &c.BackupEligible, &created, &lastUsed); err != nil {
			return nil, fmt.Errorf("store: パスキーの読み取りに失敗しました: %w", err)
		}
		if c.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("store: パスキーの時刻が不正です: %w", err)
		}
		if lastUsed.Valid {
			t, err := parseTime(lastUsed.String)
			if err != nil {
				return nil, fmt.Errorf("store: パスキーの時刻が不正です: %w", err)
			}
			c.LastUsedAt = &t
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: パスキー一覧の走査に失敗しました: %w", err)
	}
	return out, nil
}

// UpdateCredentialUsage は認証成功時に署名カウンタと最終使用時刻を更新する。
func (s *Store) UpdateCredentialUsage(ctx context.Context, id []byte, signCount uint32, usedAt time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE credentials SET sign_count = ?, last_used_at = ? WHERE id = ?",
		signCount, fmtTime(usedAt), id,
	); err != nil {
		return fmt.Errorf("store: パスキー使用記録の更新に失敗しました: %w", err)
	}
	return nil
}

// GetUserByHandle は WebAuthn ユーザーハンドルで利用者を引く
// （Discoverable Credential ログイン用）。未登録は (nil, nil)。
func (s *Store) GetUserByHandle(ctx context.Context, handle []byte) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT id, redmine_login, display_name, webauthn_user_handle
		 FROM users WHERE webauthn_user_handle = ?`, handle))
}

// InsertChallenge は進行中の WebAuthn セレモニー状態を保存する。
// userID は登録前ログインでは空（NULL 保存）。
func (s *Store) InsertChallenge(ctx context.Context, id, userID, kind string, data []byte, expiresAt time.Time) error {
	var uid any
	if userID != "" {
		uid = userID
	}
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO webauthn_challenges (id, user_id, kind, data, expires_at) VALUES (?, ?, ?, ?, ?)",
		id, uid, kind, data, fmtTime(expiresAt),
	); err != nil {
		return fmt.Errorf("store: チャレンジ保存に失敗しました: %w", err)
	}
	return nil
}

// TakeChallenge はチャレンジを 1 回限りで取り出す（取り出しと同時に削除）。
// 存在しない・期限切れ・kind 不一致は ok=false。
func (s *Store) TakeChallenge(ctx context.Context, id, kind string, now time.Time) (userID string, data []byte, ok bool, err error) {
	var uid sql.NullString
	var expires string
	err = s.db.QueryRowContext(ctx,
		"SELECT user_id, data, expires_at FROM webauthn_challenges WHERE id = ? AND kind = ?",
		id, kind,
	).Scan(&uid, &data, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("store: チャレンジ取得に失敗しました: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM webauthn_challenges WHERE id = ?", id); err != nil {
		return "", nil, false, fmt.Errorf("store: チャレンジ削除に失敗しました: %w", err)
	}
	exp, err := parseTime(expires)
	if err != nil {
		return "", nil, false, fmt.Errorf("store: チャレンジの時刻が不正です: %w", err)
	}
	if now.After(exp) {
		return "", nil, false, nil
	}
	return uid.String, data, true, nil
}

// DeleteExpiredChallenges は期限切れチャレンジを掃除する（定期実行想定）。
func (s *Store) DeleteExpiredChallenges(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM webauthn_challenges WHERE expires_at < ?", fmtTime(now),
	); err != nil {
		return fmt.Errorf("store: 期限切れチャレンジの削除に失敗しました: %w", err)
	}
	return nil
}
