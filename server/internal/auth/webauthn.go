package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// ErrChallengeInvalid は不明・期限切れ・使用済みのチャレンジ。
// httpapi.ErrInvalidRequest を包み、ハンドラが 4xx に写像できるようにする。
var ErrChallengeInvalid = fmt.Errorf("%w: auth: チャレンジが無効です（不明・期限切れ・使用済み）", httpapi.ErrInvalidRequest)

// ErrCeremonyFailed はセレモニー検証の失敗（署名不正など）。
var ErrCeremonyFailed = fmt.Errorf("%w: auth: WebAuthn セレモニーの検証に失敗しました", httpapi.ErrInvalidRequest)

// WebAuthnConfig は config.WebAuthn から組み立てる。
type WebAuthnConfig struct {
	RPID         string
	RPName       string
	Origins      []string
	// UserVerification は required / preferred / discouraged（config が検証済み）。
	UserVerification string
	ChallengeTTL     time.Duration
}

// WebAuthn は登録・認証セレモニーを担う。チャレンジ状態は DB に置き、
// 1 回限りで消費する（Design.md §5.6）。
type WebAuthn struct {
	wa    *webauthn.WebAuthn
	store *store.Store
	ttl   time.Duration
	now   func() time.Time
}

func NewWebAuthn(st *store.Store, cfg WebAuthnConfig) (*WebAuthn, error) {
	uv := protocol.UserVerificationRequirement(cfg.UserVerification)
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPName,
		RPOrigins:     cfg.Origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// Discoverable Credential を要求し、ユーザー名なしログインを可能にする（Design.md §3.1）
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: uv,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: WebAuthn 初期化に失敗しました: %w", err)
	}
	return &WebAuthn{wa: wa, store: st, ttl: cfg.ChallengeTTL, now: time.Now}, nil
}

// waUser は store.User を webauthn.User に適合させる。
type waUser struct {
	u     *store.User
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return w.u.WebAuthnUserHandle }
func (w waUser) WebAuthnName() string                       { return w.u.RedmineLogin }
func (w waUser) WebAuthnDisplayName() string                { return w.u.DisplayName }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func (w *WebAuthn) loadUser(ctx context.Context, u *store.User) (waUser, error) {
	stored, err := w.store.ListCredentialsByUser(ctx, u.ID)
	if err != nil {
		return waUser{}, err
	}
	creds := make([]webauthn.Credential, 0, len(stored))
	for _, c := range stored {
		creds = append(creds, webauthn.Credential{
			ID:        c.ID,
			PublicKey: c.PublicKey,
			Authenticator: webauthn.Authenticator{
				AAGUID:    c.AAGUID,
				SignCount: c.SignCount,
			},
		})
	}
	return waUser{u: u, creds: creds}, nil
}

// BeginRegistration は登録セレモニーを開始し、クライアントに返す
// オプション JSON とチャレンジ ID を返す。
func (w *WebAuthn) BeginRegistration(ctx context.Context, userID string) (optionsJSON []byte, challengeID string, err error) {
	u, err := w.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if u == nil {
		return nil, "", fmt.Errorf("auth: 利用者 %s が存在しません", userID)
	}
	wu, err := w.loadUser(ctx, u)
	if err != nil {
		return nil, "", err
	}
	creation, sd, err := w.wa.BeginRegistration(wu)
	if err != nil {
		return nil, "", fmt.Errorf("auth: 登録セレモニー開始に失敗しました: %w", err)
	}
	challengeID, err = w.saveChallenge(ctx, u.ID, "register", sd)
	if err != nil {
		return nil, "", err
	}
	optionsJSON, err = json.Marshal(creation)
	if err != nil {
		return nil, "", fmt.Errorf("auth: オプションの整形に失敗しました: %w", err)
	}
	return optionsJSON, challengeID, nil
}

// FinishRegistration は登録セレモニーを完了し、パスキーを保存する。
// r のボディは認証器のアテステーションレスポンス。
func (w *WebAuthn) FinishRegistration(ctx context.Context, challengeID string, r *http.Request) (userID string, credentialID []byte, err error) {
	userID, sd, err := w.takeChallenge(ctx, challengeID, "register")
	if err != nil {
		return "", nil, err
	}
	u, err := w.store.GetUserByID(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	if u == nil {
		return "", nil, ErrChallengeInvalid
	}
	wu, err := w.loadUser(ctx, u)
	if err != nil {
		return "", nil, err
	}
	cred, err := w.wa.FinishRegistration(wu, *sd, r)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrCeremonyFailed, err)
	}
	if cred.Authenticator.CloneWarning {
		return "", nil, fmt.Errorf("%w: 署名カウンタの退行を検知しました", ErrCeremonyFailed)
	}

	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	if err := w.store.InsertCredential(ctx, &store.Credential{
		ID:             cred.ID,
		UserID:         u.ID,
		PublicKey:      cred.PublicKey,
		SignCount:      cred.Authenticator.SignCount,
		AAGUID:         cred.Authenticator.AAGUID,
		Transports:     strings.Join(transports, ","),
		BackupEligible: cred.Flags.BackupEligible,
		CreatedAt:      w.now().UTC(),
	}); err != nil {
		// 登録済みパスキーの再登録は利用者側の誤操作 → 4xx に倒す
		if errors.Is(err, store.ErrDuplicateCredential) {
			return "", nil, fmt.Errorf("%w: %w", ErrCeremonyFailed, err)
		}
		return "", nil, err
	}
	return u.ID, cred.ID, nil
}

// BeginLogin は Discoverable Credential での認証セレモニーを開始する。
func (w *WebAuthn) BeginLogin(ctx context.Context) (optionsJSON []byte, challengeID string, err error) {
	assertion, sd, err := w.wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, "", fmt.Errorf("auth: 認証セレモニー開始に失敗しました: %w", err)
	}
	challengeID, err = w.saveChallenge(ctx, "", "login", sd)
	if err != nil {
		return nil, "", err
	}
	optionsJSON, err = json.Marshal(assertion)
	if err != nil {
		return nil, "", fmt.Errorf("auth: オプションの整形に失敗しました: %w", err)
	}
	return optionsJSON, challengeID, nil
}

// FinishLogin は認証セレモニーを完了し、認証された利用者を返す。
// 成功時は署名カウンタと最終使用時刻を更新する。
func (w *WebAuthn) FinishLogin(ctx context.Context, challengeID string, r *http.Request) (userID string, credentialID []byte, err error) {
	_, sd, err := w.takeChallenge(ctx, challengeID, "login")
	if err != nil {
		return "", nil, err
	}

	var authed *store.User
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		u, err := w.store.GetUserByHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, errors.New("unknown user handle")
		}
		authed = u
		return w.loadUser(ctx, u)
	}
	_, cred, err := w.wa.FinishPasskeyLogin(handler, *sd, r)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrCeremonyFailed, err)
	}
	// 署名カウンタの退行は認証器の複製を示す。go-webauthn はここを
	// エラーにせず CloneWarning フラグで知らせるため、明示的に拒否する。
	if cred.Authenticator.CloneWarning {
		return "", nil, fmt.Errorf("%w: 署名カウンタの退行を検知しました（認証器の複製の疑い）", ErrCeremonyFailed)
	}
	if err := w.store.UpdateCredentialUsage(ctx, cred.ID, cred.Authenticator.SignCount, w.now().UTC()); err != nil {
		return "", nil, err
	}
	return authed.ID, cred.ID, nil
}

func (w *WebAuthn) saveChallenge(ctx context.Context, userID, kind string, sd *webauthn.SessionData) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("auth: チャレンジ ID 生成に失敗しました: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	data, err := json.Marshal(sd)
	if err != nil {
		return "", fmt.Errorf("auth: セレモニー状態の保存形式が不正です: %w", err)
	}
	if err := w.store.InsertChallenge(ctx, id, userID, kind, data, w.now().UTC().Add(w.ttl)); err != nil {
		return "", err
	}
	return id, nil
}

func (w *WebAuthn) takeChallenge(ctx context.Context, id, kind string) (string, *webauthn.SessionData, error) {
	userID, data, ok, err := w.store.TakeChallenge(ctx, id, kind, w.now().UTC())
	if err != nil {
		return "", nil, err
	}
	if !ok {
		return "", nil, ErrChallengeInvalid
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(data, &sd); err != nil {
		return "", nil, fmt.Errorf("auth: セレモニー状態の復元に失敗しました: %w", err)
	}
	return userID, &sd, nil
}
