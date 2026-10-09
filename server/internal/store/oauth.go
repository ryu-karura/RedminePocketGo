package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ErrNoTokenRow は更新対象の OAuth トークンの組が存在しない
// （並行削除などのレース）ことを示す。
var ErrNoTokenRow = errors.New("store: 対象の OAuth トークンがありません")

// UpsertOAuthUser は Redmine のユーザー ID を鍵に利用者を作成・更新して返す
// （Design.md §4.1, §5.1, §5.5）。
//
//  1. redmine_user_id が一致する行があれば、ログイン名・表示名を更新する。
//  2. 無ければ、redmine_user_id が未設定（旧方式の行）で、ログイン名が一致する
//     行に ID を紐付けて引き継ぐ。旧方式の行は Redmine のパスワードで本人確認
//     済みのため。別の Redmine ユーザーに紐付いた行は決して乗っ取らない。
//  3. それも無ければ新規に作る。
func (s *Store) UpsertOAuthUser(ctx context.Context, redmineUserID int64, login, displayName string) (*User, error) {
	if redmineUserID <= 0 {
		return nil, errors.New("store: Redmine ユーザー ID が不正です")
	}
	if login == "" {
		return nil, errors.New("store: Redmine ログイン名が空です")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: トランザクション開始に失敗しました: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Commit 後は no-op

	now := fmtTime(time.Now().UTC())
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE redmine_user_id = ?`, redmineUserID).Scan(&id)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET redmine_login = ?, display_name = ?, updated_at = ? WHERE id = ?`,
			login, displayName, now, id); err != nil {
			return nil, fmt.Errorf("store: ユーザー更新に失敗しました: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM users WHERE redmine_login = ? COLLATE NOCASE AND redmine_user_id IS NULL
			 ORDER BY created_at LIMIT 1`, login).Scan(&id)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx,
				`UPDATE users SET redmine_user_id = ?, display_name = ?, updated_at = ? WHERE id = ?`,
				redmineUserID, displayName, now, id); err != nil {
				return nil, fmt.Errorf("store: ユーザーの引き継ぎに失敗しました: %w", err)
			}
		case errors.Is(err, sql.ErrNoRows):
			if id, err = newUUID(); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO users (id, redmine_user_id, redmine_login, display_name) VALUES (?, ?, ?, ?)`,
				id, redmineUserID, login, displayName); err != nil {
				return nil, fmt.Errorf("store: ユーザー作成に失敗しました: %w", err)
			}
		default:
			return nil, fmt.Errorf("store: ユーザー検索に失敗しました: %w", err)
		}
	default:
		return nil, fmt.Errorf("store: ユーザー検索に失敗しました: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: ユーザー保存のコミットに失敗しました: %w", err)
	}
	return &User{ID: id, RedmineUserID: redmineUserID, RedmineLogin: login, DisplayName: displayName}, nil
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store: 乱数生成に失敗しました: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// OAuthTokens は暗号化された OAuth トークンの組（Design.md §5.2）。
// 暗号化・復号は internal/credential の責務で、ここは暗号文のみを扱う。
type OAuthTokens struct {
	UserID            string
	AccessCiphertext  []byte
	AccessNonce       []byte
	RefreshCiphertext []byte
	RefreshNonce      []byte
	KeyVersion        int
	Scopes            string // 空白区切り
	AccessExpiresAt   time.Time
	Status            string // active / invalid
	RefreshedAt       time.Time
}

// SaveOAuthTokens はトークンの組を保存する（既存は上書き）。保存すると
// status は active、refreshed_at は現在時刻になる。
func (s *Store) SaveOAuthTokens(ctx context.Context, t OAuthTokens) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_tokens
		   (user_id, access_ciphertext, access_nonce, refresh_ciphertext, refresh_nonce,
		    key_version, scopes, access_expires_at, status, refreshed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   access_ciphertext  = excluded.access_ciphertext,
		   access_nonce       = excluded.access_nonce,
		   refresh_ciphertext = excluded.refresh_ciphertext,
		   refresh_nonce      = excluded.refresh_nonce,
		   key_version        = excluded.key_version,
		   scopes             = excluded.scopes,
		   access_expires_at  = excluded.access_expires_at,
		   status             = 'active',
		   refreshed_at       = excluded.refreshed_at`,
		t.UserID, t.AccessCiphertext, t.AccessNonce, t.RefreshCiphertext, t.RefreshNonce,
		t.KeyVersion, t.Scopes, fmtTime(t.AccessExpiresAt), fmtTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("store: OAuth トークン保存に失敗しました: %w", err)
	}
	return nil
}

// GetOAuthTokens はトークンの組を返す。未保存は (nil, nil)。
func (s *Store) GetOAuthTokens(ctx context.Context, userID string) (*OAuthTokens, error) {
	var (
		t                  OAuthTokens
		expires, refreshed string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, access_ciphertext, access_nonce, refresh_ciphertext, refresh_nonce,
		        key_version, scopes, access_expires_at, status, refreshed_at
		 FROM oauth_tokens WHERE user_id = ?`, userID,
	).Scan(&t.UserID, &t.AccessCiphertext, &t.AccessNonce, &t.RefreshCiphertext, &t.RefreshNonce,
		&t.KeyVersion, &t.Scopes, &expires, &t.Status, &refreshed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: OAuth トークン取得に失敗しました: %w", err)
	}
	if t.AccessExpiresAt, err = parseTime(expires); err != nil {
		return nil, fmt.Errorf("store: OAuth トークンの期限が不正です: %w", err)
	}
	if t.RefreshedAt, err = parseTime(refreshed); err != nil {
		return nil, fmt.Errorf("store: OAuth トークンの更新時刻が不正です: %w", err)
	}
	return &t, nil
}

// SetOAuthTokenStatus は status を更新する（active / invalid）。対象行が
// 無ければ ErrNoTokenRow を返し、無効化の空振りを黙認しない。
func (s *Store) SetOAuthTokenStatus(ctx context.Context, userID, status string) error {
	if status != "active" && status != "invalid" {
		return fmt.Errorf("store: 不正な OAuth トークン状態 %q", status)
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE oauth_tokens SET status = ? WHERE user_id = ?", status, userID)
	if err != nil {
		return fmt.Errorf("store: OAuth トークン状態の更新に失敗しました: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: 更新結果の確認に失敗しました: %w", err)
	}
	if n == 0 {
		return ErrNoTokenRow
	}
	return nil
}

// DeleteOAuthTokens はトークンの組を削除する（Redmine 側で失効させた後など）。
// 存在しなくてもエラーにしない。
func (s *Store) DeleteOAuthTokens(ctx context.Context, userID string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM oauth_tokens WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("store: OAuth トークン削除に失敗しました: %w", err)
	}
	return nil
}

// OAuthState は進行中の認可要求から取り出した状態（Design.md §5.4）。
type OAuthState struct {
	VerifierCiphertext []byte
	VerifierNonce      []byte
	ReturnTo           string
}

// InsertOAuthState は認可要求の状態を保存する。stateHash は state の
// ハッシュ（生の値は保存しない）。
func (s *Store) InsertOAuthState(ctx context.Context, stateHash string, verifierCT, verifierNonce []byte, returnTo string, expiresAt time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_states (state_hash, verifier_ciphertext, verifier_nonce, return_to, expires_at)
		 VALUES (?, ?, ?, ?, ?)`,
		stateHash, verifierCT, verifierNonce, returnTo, fmtTime(expiresAt),
	); err != nil {
		return fmt.Errorf("store: OAuth state の保存に失敗しました: %w", err)
	}
	return nil
}

// ConsumeOAuthState は state を 1 回限りで取り出す。未使用かつ期限内の行だけを
// 単一の UPDATE ... RETURNING で使用済みにするため、並行・再送でも
// ちょうど 1 回しか成功しない。該当なし（不明・期限切れ・使用済み）は ok=false。
func (s *Store) ConsumeOAuthState(ctx context.Context, stateHash string, now time.Time) (OAuthState, bool, error) {
	var st OAuthState
	err := s.db.QueryRowContext(ctx,
		`UPDATE oauth_states SET used_at = ?
		 WHERE state_hash = ? AND used_at IS NULL AND expires_at > ?
		 RETURNING verifier_ciphertext, verifier_nonce, return_to`,
		fmtTime(now), stateHash, fmtTime(now),
	).Scan(&st.VerifierCiphertext, &st.VerifierNonce, &st.ReturnTo)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthState{}, false, nil
	}
	if err != nil {
		return OAuthState{}, false, fmt.Errorf("store: OAuth state の消費に失敗しました: %w", err)
	}
	return st, true, nil
}

// DeleteExpiredOAuthStates は期限切れ（と使用済みで期限を過ぎた）state を削除する。
func (s *Store) DeleteExpiredOAuthStates(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM oauth_states WHERE expires_at <= ?", fmtTime(now)); err != nil {
		return fmt.Errorf("store: 期限切れ OAuth state の削除に失敗しました: %w", err)
	}
	return nil
}
