package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// ErrBadCredentials は Redmine 認証情報の不一致（詳細は返さない）。
var ErrBadCredentials = fmt.Errorf("%w: auth: Redmine 認証に失敗しました", httpapi.ErrUnauthenticated)

// ErrRedmineUnavailable は Redmine 側の障害。
var ErrRedmineUnavailable = fmt.Errorf("%w: auth: Redmine に接続できません", httpapi.ErrUpstream)

// APIKeyVault は取得した Redmine API キーの暗号化保管
// （internal/credential、フェーズ 3）が実装する。
type APIKeyVault interface {
	SaveAPIKey(ctx context.Context, userID, apiKey string) error
}

// BootstrapConfig は Redmine への接続設定（config.Redmine から組み立てる）。
type BootstrapConfig struct {
	BaseURL string
	SubURI  string
	Timeout time.Duration
}

// Bootstrap は初回登録（Design.md §3.3）: Redmine の認証情報で本人確認し、
// API キーを保管して、その場でパスキー登録セレモニーを開始する。
// パスワードはこの処理の中でのみ使用し、保存も記録もしない。
type Bootstrap struct {
	store      *store.Store
	webauthn   *WebAuthn
	vault      APIKeyVault
	client     *http.Client
	accountURL string
}

func NewBootstrap(st *store.Store, wa *WebAuthn, vault APIKeyVault, cfg BootstrapConfig) *Bootstrap {
	return &Bootstrap{
		store:    st,
		webauthn: wa,
		vault:    vault,
		client:   &http.Client{Timeout: cfg.Timeout},
		// サブ URI は設定から与える（ハードコード禁止。CLAUDE.md §4.3）
		accountURL: strings.TrimSuffix(cfg.BaseURL, "/") + cfg.SubURI + "/my/account.json",
	}
}

// redmineAccount は /my/account.json のレスポンス（必要な項目のみ）。
type redmineAccount struct {
	User struct {
		Login     string `json:"login"`
		Firstname string `json:"firstname"`
		Lastname  string `json:"lastname"`
		APIKey    string `json:"api_key"`
	} `json:"user"`
}

// verifyAccount は Redmine の Basic 認証で本人確認し、アカウント情報を返す
// （Run・Relink の共通部分）。
func (b *Bootstrap) verifyAccount(ctx context.Context, login, password string) (redmineAccount, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.accountURL, nil)
	if err != nil {
		return redmineAccount{}, fmt.Errorf("auth: リクエスト作成に失敗しました: %w", err)
	}
	req.SetBasicAuth(login, password)

	resp, err := b.client.Do(req)
	if err != nil {
		return redmineAccount{}, fmt.Errorf("%w: %w", ErrRedmineUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// 続行
	case resp.StatusCode == http.StatusUnauthorized:
		return redmineAccount{}, ErrBadCredentials
	case resp.StatusCode >= 500:
		return redmineAccount{}, fmt.Errorf("%w (status %d)", ErrRedmineUnavailable, resp.StatusCode)
	default:
		return redmineAccount{}, fmt.Errorf("%w (unexpected status %d)", ErrRedmineUnavailable, resp.StatusCode)
	}

	var account redmineAccount
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		return redmineAccount{}, fmt.Errorf("%w: レスポンスの解釈に失敗しました", ErrRedmineUnavailable)
	}
	if account.User.Login == "" || account.User.APIKey == "" {
		return redmineAccount{}, fmt.Errorf("%w: レスポンスに必要な項目がありません", ErrRedmineUnavailable)
	}
	return account, nil
}

// Relink はログイン済み利用者が Redmine の認証情報を再入力し、無効化された
// API キーを新しいものに差し替える（Design.md §4.4 手順 3-4・§7.9）。
// 再紐付けは同じ Redmine アカウントに対してのみ許可する（キーはユーザー単位で
// 1 つという設計 §4.1 を守るため、別アカウントへの付け替えは扱わない）。
func (b *Bootstrap) Relink(ctx context.Context, userID, login, password string) error {
	account, err := b.verifyAccount(ctx, login, password)
	if err != nil {
		return err
	}
	u, err := b.store.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("auth: 利用者の参照に失敗しました: %w", err)
	}
	if u == nil {
		return fmt.Errorf("auth: 利用者が見つかりません")
	}
	if u.RedmineLogin != account.User.Login {
		return ErrBadCredentials
	}
	return b.vault.SaveAPIKey(ctx, userID, account.User.APIKey)
}

// Run はブートストラップを実行し、登録セレモニーの開始情報を返す。
// 不存在ユーザーでも Redmine への問い合わせを必ず行うため、処理時間で
// ユーザーの存在は判別できない（Design.md §3.1）。
func (b *Bootstrap) Run(ctx context.Context, login, password string) (optionsJSON []byte, challengeID string, err error) {
	account, err := b.verifyAccount(ctx, login, password)
	if err != nil {
		return nil, "", err
	}

	u, err := b.store.GetUserByLogin(ctx, account.User.Login)
	if err != nil {
		return nil, "", err
	}
	if u == nil {
		id, err := newUUID()
		if err != nil {
			return nil, "", err
		}
		handle, err := newUserHandle()
		if err != nil {
			return nil, "", err
		}
		u = &store.User{
			ID:                 id,
			RedmineLogin:       account.User.Login,
			DisplayName:        strings.TrimSpace(account.User.Firstname + " " + account.User.Lastname),
			WebAuthnUserHandle: handle,
		}
		if err := b.store.CreateUser(ctx, u); err != nil {
			return nil, "", err
		}
	}
	if err := b.vault.SaveAPIKey(ctx, u.ID, account.User.APIKey); err != nil {
		return nil, "", err
	}

	// その場でパスキー登録セレモニーを開始する（§3.3 手順 5）
	return b.webauthn.BeginRegistration(ctx, u.ID)
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("auth: 乱数生成に失敗しました: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

func newUserHandle() ([]byte, error) {
	// 64 バイト全体をランダムに使う（webauthn.User の推奨）
	h := make([]byte, 64)
	if _, err := rand.Read(h); err != nil {
		return nil, fmt.Errorf("auth: 乱数生成に失敗しました: %w", err)
	}
	return h, nil
}
