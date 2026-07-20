package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrDuplicateCredential は既に登録済みのパスキー（Credential ID の
// 主キー衝突）。ハンドラは 4xx に写像する。
var ErrDuplicateCredential = errors.New("store: このパスキーは既に登録されています")

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
		if isConstraintErr(err) {
			return ErrDuplicateCredential
		}
		return fmt.Errorf("store: パスキー保存に失敗しました: %w", err)
	}
	return nil
}

// isConstraintErr は主キー/一意制約違反かを判定する（modernc.org/sqlite は
// メッセージに "constraint" を含む）。
func isConstraintErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "constraint")
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

// TakeChallenge はチャレンジを 1 回限りで取り出す。DELETE ... RETURNING で
// 取り出しと削除を単一文にし、同一チャレンジの同時消費を防ぐ（同時実行で
// 行を取れるのは 1 つだけ）。存在しない・期限切れ・kind 不一致は ok=false。
func (s *Store) TakeChallenge(ctx context.Context, id, kind string, now time.Time) (userID string, data []byte, ok bool, err error) {
	var uid sql.NullString
	var expires string
	err = s.db.QueryRowContext(ctx,
		"DELETE FROM webauthn_challenges WHERE id = ? AND kind = ? RETURNING user_id, data, expires_at",
		id, kind,
	).Scan(&uid, &data, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("store: チャレンジ取得に失敗しました: %w", err)
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

// InsertEnrollmentCode は端末追加コードを保存する（ハッシュのみ）。
// 同じハッシュが既にあれば主キー衝突でエラーになる（呼び出し側で再生成）。
func (s *Store) InsertEnrollmentCode(ctx context.Context, codeHash, userID string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO enrollment_codes (code_hash, user_id, expires_at) VALUES (?, ?, ?)",
		codeHash, userID, fmtTime(expiresAt),
	)
	if err != nil {
		return fmt.Errorf("store: 登録コード保存に失敗しました: %w", err)
	}
	return nil
}

// ConsumeEnrollmentCode はコードを 1 回限りで消費する。未使用の行だけを
// 単一文でマークして取り出すため、同時消費でも成立するのは 1 つだけ。
// 不明・期限切れ・使用済みは ok=false（期限切れの行も used に倒すが害はない）。
func (s *Store) ConsumeEnrollmentCode(ctx context.Context, codeHash string, now time.Time) (userID string, ok bool, err error) {
	var expires string
	err = s.db.QueryRowContext(ctx,
		"UPDATE enrollment_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL RETURNING user_id, expires_at",
		fmtTime(now), codeHash,
	).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		// 不明・使用済み（既に used_at が入っている）
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: 登録コード取得に失敗しました: %w", err)
	}
	exp, err := parseTime(expires)
	if err != nil {
		return "", false, fmt.Errorf("store: 登録コードの時刻が不正です: %w", err)
	}
	if now.After(exp) {
		return "", false, nil
	}
	return userID, true, nil
}

// RenameCredential は利用者自身のパスキーの表示名を変更する。
// 対象が無い・他人のものなら false。
func (s *Store) RenameCredential(ctx context.Context, id []byte, userID, label string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		"UPDATE credentials SET device_label = ? WHERE id = ? AND user_id = ?",
		label, id, userID,
	)
	if err != nil {
		return false, fmt.Errorf("store: パスキー名の変更に失敗しました: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: 変更結果の確認に失敗しました: %w", err)
	}
	return n > 0, nil
}

// DeleteCredentialAndSessions は利用者自身のパスキーを削除し、同一
// トランザクションで該当セッションを即失効させる（Design.md §3.5）。
func (s *Store) DeleteCredentialAndSessions(ctx context.Context, id []byte, userID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: トランザクション開始に失敗しました: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		"DELETE FROM credentials WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return false, fmt.Errorf("store: パスキー削除に失敗しました: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: 削除結果の確認に失敗しました: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM sessions WHERE credential_id = ?", id); err != nil {
		return false, fmt.Errorf("store: パスキーのセッション失効に失敗しました: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: 削除のコミットに失敗しました: %w", err)
	}
	return true, nil
}
