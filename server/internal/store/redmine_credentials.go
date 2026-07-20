package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RedmineCredential は暗号化された Redmine API キー（Design.md §5.3）。
type RedmineCredential struct {
	UserID     string
	Ciphertext []byte
	Nonce      []byte
	KeyVersion int
	Status     string // active / invalid
}

// UpsertRedmineCredential は API キーの暗号文を保存する（既存は上書き）。
// 保存すると status は active、verified_at は現在時刻になる。
func (s *Store) UpsertRedmineCredential(ctx context.Context, userID string, ciphertext, nonce []byte, keyVersion int) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO redmine_credentials
		   (user_id, api_key_ciphertext, api_key_nonce, key_version, status, verified_at)
		 VALUES (?, ?, ?, ?, 'active', ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   api_key_ciphertext = excluded.api_key_ciphertext,
		   api_key_nonce      = excluded.api_key_nonce,
		   key_version        = excluded.key_version,
		   status             = 'active',
		   verified_at        = excluded.verified_at`,
		userID, ciphertext, nonce, keyVersion, fmtTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("store: API キー保存に失敗しました: %w", err)
	}
	return nil
}

// GetRedmineCredential は暗号化レコードを返す。未登録は (nil, nil)。
func (s *Store) GetRedmineCredential(ctx context.Context, userID string) (*RedmineCredential, error) {
	var rc RedmineCredential
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, api_key_ciphertext, api_key_nonce, key_version, status
		 FROM redmine_credentials WHERE user_id = ?`, userID,
	).Scan(&rc.UserID, &rc.Ciphertext, &rc.Nonce, &rc.KeyVersion, &rc.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: API キー取得に失敗しました: %w", err)
	}
	return &rc, nil
}

// SetRedmineCredentialStatus は status を更新する（active / invalid）。
func (s *Store) SetRedmineCredentialStatus(ctx context.Context, userID, status string) error {
	if _, err := s.db.ExecContext(ctx,
		"UPDATE redmine_credentials SET status = ? WHERE user_id = ?", status, userID,
	); err != nil {
		return fmt.Errorf("store: API キー状態の更新に失敗しました: %w", err)
	}
	return nil
}
