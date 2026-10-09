package credential

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/redmine"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// OAuth トークン（アクセス + リフレッシュ）の暗号化保管と、更新の直列化
// （Design.md §4.3, §4.4）。

// ErrPersistFailed は、Redmine がリフレッシュトークンを入れ替えたのに新しい組を
// 保存できなかったことを示す。古い組はもう使えないため、利用者は再ログイン
// が必要になる（運用上は DB 障害として扱う）。
var ErrPersistFailed = errors.New("credential: 更新したトークンを永続化できませんでした（再ログインが必要になります）")

// Seal は plaintext を AES-256-GCM で暗号化する。aad（追加認証データ）に
// 用途と持ち主を入れて暗号文を行・列へ束縛し、入れ替えや別行への移植を
// 復号時に検出する。ノンスは呼び出しごとに乱数生成する。
func (v *Vault) Seal(aad string, plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, v.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("credential: ノンス生成に失敗しました: %w", err)
	}
	return v.gcm.Seal(nil, nonce, plaintext, []byte(aad)), nonce, nil
}

// Open は Seal で暗号化した値を復号する。aad が違えば失敗する。
func (v *Vault) Open(aad string, ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != v.gcm.NonceSize() {
		return nil, errors.New("credential: ノンス長が不正です")
	}
	pt, err := v.gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("credential: 復号に失敗しました: %w", err)
	}
	return pt, nil
}

func aadAccess(userID string) string  { return "oauth-access:" + userID }
func aadRefresh(userID string) string { return "oauth-refresh:" + userID }

// Tokens は復号済みのトークンの組。ログ・JSON・文字列化では内容を出さない
// （CLAUDE.md §4.4）。値レシーバにして、ポインタでも値でも伏字が効くようにする。
type Tokens struct {
	access          string
	refresh         string
	Scopes          []string
	AccessExpiresAt time.Time
}

func (t Tokens) Access() string  { return t.access }
func (t Tokens) Refresh() string { return t.refresh }

func (Tokens) String() string               { return "[redacted]" }
func (Tokens) GoString() string             { return "credential.Tokens{[redacted]}" }
func (Tokens) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }
func (Tokens) LogValue() slog.Value         { return slog.StringValue("[redacted]") }

// SaveTokens はトークンの組を暗号化して保存する（既存は上書き＝再認可）。
// 保存すると status は active に戻る。アクセスとリフレッシュは別のノンスで暗号化する。
func (v *Vault) SaveTokens(ctx context.Context, userID string, ts *redmine.TokenSet) error {
	ac, an, err := v.Seal(aadAccess(userID), []byte(ts.AccessToken))
	if err != nil {
		return err
	}
	rc, rn, err := v.Seal(aadRefresh(userID), []byte(ts.RefreshToken))
	if err != nil {
		return err
	}
	return v.store.SaveOAuthTokens(ctx, store.OAuthTokens{
		UserID:           userID,
		AccessCiphertext: ac, AccessNonce: an,
		RefreshCiphertext: rc, RefreshNonce: rn,
		KeyVersion:      v.keyVersion,
		Scopes:          strings.Join(ts.Scopes, " "),
		AccessExpiresAt: ts.AccessExpiresAt,
	})
}

// LoadTokens は復号したトークンの組を返す。未保存は ErrNoCredential、
// 無効化済み（再認可が必要）は ErrCredentialInvalid。
func (v *Vault) LoadTokens(ctx context.Context, userID string) (*Tokens, error) {
	rec, err := v.store.GetOAuthTokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, ErrNoCredential
	}
	if rec.Status == "invalid" {
		return nil, ErrCredentialInvalid
	}
	access, err := v.Open(aadAccess(userID), rec.AccessCiphertext, rec.AccessNonce)
	if err != nil {
		return nil, err
	}
	refresh, err := v.Open(aadRefresh(userID), rec.RefreshCiphertext, rec.RefreshNonce)
	if err != nil {
		return nil, err
	}
	return &Tokens{
		access: string(access), refresh: string(refresh),
		Scopes: strings.Fields(rec.Scopes), AccessExpiresAt: rec.AccessExpiresAt,
	}, nil
}

// MarkTokensInvalid は組を無効としてマークする（再認可が必要）。
func (v *Vault) MarkTokensInvalid(ctx context.Context, userID string) error {
	return v.store.SetOAuthTokenStatus(ctx, userID, "invalid")
}

// Refresher はトークンの更新口（redmine.OAuth が満たす）。
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (*redmine.TokenSet, error)
}

// Manager は有効なアクセストークンを供給する。期限が近ければリフレッシュし、
// 利用者ごとに更新を 1 本へ直列化する（single-flight）。Redmine はリフレッシュの
// たびにリフレッシュトークンを入れ替え、古いものは即座に無効になるため、
// 同じトークンでの並行更新や、入れ替え後の保存漏れは連鎖を壊す。
type Manager struct {
	vault *Vault
	oauth Refresher
	skew  time.Duration

	now               func() time.Time
	persistRetryDelay time.Duration

	mu      sync.Mutex
	flights map[string]*flight
}

type flight struct {
	done  chan struct{}
	token string
	err   error
}

// refreshTimeout は 1 回の更新（上流呼び出し + 保存）の上限。呼び出し元の
// キャンセルとは独立させる（下記）。
const refreshTimeout = 60 * time.Second

// persistAttempts は新しい組の保存の試行回数。
const persistAttempts = 3

// NewManager は skew（期限の何秒前から更新するか）を指定して作る。
func NewManager(v *Vault, oauth Refresher, skew time.Duration) *Manager {
	return &Manager{
		vault: v, oauth: oauth, skew: skew,
		now: time.Now, persistRetryDelay: 100 * time.Millisecond,
		flights: map[string]*flight{},
	}
}

// AccessToken は利用者の有効なアクセストークンを返す。期限まで skew 未満なら
// 先にリフレッシュする。未保存は ErrNoCredential、無効は ErrCredentialInvalid。
func (m *Manager) AccessToken(ctx context.Context, userID string) (string, error) {
	t, err := m.vault.LoadTokens(ctx, userID)
	if err != nil {
		return "", err
	}
	if m.fresh(t) {
		return t.Access(), nil
	}
	return m.refresh(ctx, userID, "")
}

// ForceRefresh は上流が staleAccess を 401 で拒否したときに呼ぶ。保存済みが
// 既に別のトークンへ入れ替わっていれば（他のリクエストが更新済み）それを
// 返し、同じなら本当に失効しているのでリフレッシュする。
func (m *Manager) ForceRefresh(ctx context.Context, userID, staleAccess string) (string, error) {
	t, err := m.vault.LoadTokens(ctx, userID)
	if err != nil {
		return "", err
	}
	if t.Access() != staleAccess {
		return t.Access(), nil
	}
	return m.refresh(ctx, userID, staleAccess)
}

func (m *Manager) fresh(t *Tokens) bool {
	return m.now().Add(m.skew).Before(t.AccessExpiresAt)
}

// refresh は利用者ごとに 1 本へ束ねる。先行する更新があれば、その結果を待つ。
func (m *Manager) refresh(ctx context.Context, userID, staleAccess string) (string, error) {
	m.mu.Lock()
	if f, ok := m.flights[userID]; ok {
		m.mu.Unlock()
		select {
		case <-f.done:
			return f.token, f.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	m.flights[userID] = f
	m.mu.Unlock()

	// 呼び出し元がキャンセルされても、Redmine が入れ替えた新しい組の保存は
	// やり切らなければならない（取りこぼすと連鎖が壊れる）。キャンセルは
	// 切り離し、代わりに自前の上限を設ける。
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	f.token, f.err = m.doRefresh(rctx, userID, staleAccess)

	m.mu.Lock()
	delete(m.flights, userID)
	m.mu.Unlock()
	close(f.done)
	return f.token, f.err
}

func (m *Manager) doRefresh(ctx context.Context, userID, staleAccess string) (string, error) {
	// 待っている間に別の更新が済んでいることがあるため、保存済みを読み直す。
	cur, err := m.vault.LoadTokens(ctx, userID)
	if err != nil {
		return "", err
	}
	if staleAccess != "" {
		if cur.Access() != staleAccess {
			return cur.Access(), nil
		}
	} else if m.fresh(cur) {
		return cur.Access(), nil
	}

	ts, err := m.oauth.Refresh(ctx, cur.Refresh())
	switch {
	case err == nil:
	case errors.Is(err, redmine.ErrInvalidGrant):
		// 確定的な失効（取り消し・アプリ削除・回転の取りこぼし）。再認可を求める。
		if mErr := m.vault.MarkTokensInvalid(ctx, userID); mErr != nil {
			return "", fmt.Errorf("credential: 無効化の記録に失敗しました: %w", mErr)
		}
		return "", ErrCredentialInvalid
	default:
		// 一時障害・クライアント設定不備・その他の拒否では、利用者の組を
		// 無効にしない（原因は利用者側ではない／すぐ直るため）。
		return "", err
	}

	// 先に永続化してから使う。
	var saveErr error
	for i := 0; i < persistAttempts; i++ {
		if saveErr = m.vault.SaveTokens(ctx, userID, ts); saveErr == nil {
			return ts.AccessToken, nil
		}
		time.Sleep(m.persistRetryDelay)
	}
	slog.Error("rotated OAuth tokens could not be persisted", "userID", userID, "error", saveErr)
	return "", ErrPersistFailed
}
