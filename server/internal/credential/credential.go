// Package credential は Redmine の OAuth トークン（アクセス + リフレッシュ）の
// 暗号化保管と、更新の直列化を担う（Design.md §4.3, §4.4）。トークンは利用者
// 単位で 1 組。平文はリクエスト処理中のメモリ上にのみ存在し、DB には
// AES-256-GCM の暗号文とノンスだけを置く。Redmine の API キーは一切扱わない
// （CLAUDE.md §9-1）。
package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

var (
	// ErrNoCredential はそのユーザーにトークンが未保存。
	ErrNoCredential = errors.New("credential: OAuth トークンが未保存です")
	// ErrCredentialInvalid は保存済みのトークンが無効化されている（Redmine 側で
	// 取り消された等。再認可が必要）。
	ErrCredentialInvalid = errors.New("credential: OAuth トークンが無効です（再認可が必要）")
)

// Vault は暗号化保管庫。
type Vault struct {
	store      *store.Store
	gcm        cipher.AEAD
	keyVersion int
}

// NewVault は 32 バイトの KEK で AES-256-GCM の保管庫を作る。
func NewVault(st *store.Store, kek []byte, keyVersion int) (*Vault, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("credential: KEK は 32 バイト必要です（AES-256）。現在 %d バイト", len(kek))
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("credential: 暗号の初期化に失敗しました: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential: GCM の初期化に失敗しました: %w", err)
	}
	return &Vault{store: st, gcm: gcm, keyVersion: keyVersion}, nil
}
