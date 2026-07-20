// Package credential は Redmine API キーの暗号化保管を担う（Design.md §4.3）。
// API キーはユーザー単位で 1 つ。平文はリクエスト処理中のメモリ上にのみ
// 存在し、DB には AES-256-GCM の暗号文とノンスだけを置く。
package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

var (
	// ErrNoCredential はそのユーザーに API キーが未保存。
	ErrNoCredential = errors.New("credential: API キーが未登録です")
	// ErrCredentialInvalid は保存済みキーが無効化されている（Redmine 側で
	// 再生成された。再紐付けが必要）。
	ErrCredentialInvalid = errors.New("credential: API キーが無効です（再紐付けが必要）")
)

// APIKey は復号済みの API キーを包む。JSON 化すると必ず "[redacted]" に
// なり、ログや誤ったレスポンスへの露出を防ぐ（CLAUDE.md §4.4）。
type APIKey struct {
	value string
}

// Value は平文を返す。中継時のヘッダー付与にのみ使う。
func (k APIKey) Value() string { return k.value }

// MarshalJSON は常に "[redacted]" を返す。値レシーバにして、ポインタでも
// 値でも（%v / json.Marshal どちらでも）伏字が効くようにする。
func (k APIKey) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }

// String も伏せる（%v / %s での誤露出防止）。値レシーバ必須。
func (k APIKey) String() string { return "[redacted]" }

// NewTestAPIKey は既知の平文から APIKey を組み立てる。中継層のテストや、
// 保管庫を介さずキーを扱う配線で使う（redaction の性質は保たれる）。
func NewTestAPIKey(value string) *APIKey { return &APIKey{value: value} }

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

// SaveAPIKey は API キーを暗号化して保存する（既存があれば上書き＝再紐付け）。
// 保存すると status は active に戻る。
func (v *Vault) SaveAPIKey(ctx context.Context, userID, apiKey string) error {
	nonce := make([]byte, v.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("credential: ノンス生成に失敗しました: %w", err)
	}
	ciphertext := v.gcm.Seal(nil, nonce, []byte(apiKey), nil)
	return v.store.UpsertRedmineCredential(ctx, userID, ciphertext, nonce, v.keyVersion)
}

// LoadAPIKey は復号した API キーを返す。未登録は ErrNoCredential、
// 無効化済みは ErrCredentialInvalid。
func (v *Vault) LoadAPIKey(ctx context.Context, userID string) (*APIKey, error) {
	rec, err := v.store.GetRedmineCredential(ctx, userID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, ErrNoCredential
	}
	if rec.Status == "invalid" {
		return nil, ErrCredentialInvalid
	}
	if len(rec.Nonce) != v.gcm.NonceSize() {
		return nil, fmt.Errorf("credential: ノンス長が不正です")
	}
	plaintext, err := v.gcm.Open(nil, rec.Nonce, rec.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("credential: 復号に失敗しました: %w", err)
	}
	return &APIKey{value: string(plaintext)}, nil
}

// MarkInvalid はキーを無効としてマークする（中継が上流 401 を受けたとき）。
func (v *Vault) MarkInvalid(ctx context.Context, userID string) error {
	return v.store.SetRedmineCredentialStatus(ctx, userID, "invalid")
}
