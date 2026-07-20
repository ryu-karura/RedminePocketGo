package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// ErrCodeInvalid は不明・期限切れ・使用済みの登録コード。
var ErrCodeInvalid = fmt.Errorf("%w: auth: 登録コードが無効です", httpapi.ErrInvalidRequest)

// Enrollment は 2 台目以降の端末追加（Design.md §3.4）: ログイン済み端末で
// 6 桁コードを発行し、新しい端末がコードと引き換えに登録セレモニーを開始する。
// コードは 10 分・1 回限りで、DB にはハッシュのみを置く。
type Enrollment struct {
	store    *store.Store
	webauthn *WebAuthn
	ttl      time.Duration
	now      func() time.Time
}

func NewEnrollment(st *store.Store, wa *WebAuthn) *Enrollment {
	return &Enrollment{store: st, webauthn: wa, ttl: 10 * time.Minute, now: time.Now}
}

// IssueCode は利用者に 6 桁の登録コードを発行する。
func (e *Enrollment) IssueCode(ctx context.Context, userID string) (code string, expiresAt time.Time, err error) {
	expiresAt = e.now().UTC().Add(e.ttl)
	// 6 桁 100 万通りのため、まれなハッシュ衝突（主キー重複）は再生成で逃がす
	for attempt := 0; attempt < 5; attempt++ {
		n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return "", time.Time{}, fmt.Errorf("auth: コード生成に失敗しました: %w", err)
		}
		code = fmt.Sprintf("%06d", n.Int64())
		if err := e.store.InsertEnrollmentCode(ctx, hashCode(code), userID, expiresAt); err == nil {
			return code, expiresAt, nil
		}
	}
	return "", time.Time{}, fmt.Errorf("auth: 登録コードの発行に失敗しました（衝突が続きました）")
}

// Redeem はコードを消費し、その利用者の登録セレモニーを開始する。
func (e *Enrollment) Redeem(ctx context.Context, code string) (optionsJSON []byte, challengeID string, err error) {
	userID, ok, err := e.store.ConsumeEnrollmentCode(ctx, hashCode(code), e.now().UTC())
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", ErrCodeInvalid
	}
	return e.webauthn.BeginRegistration(ctx, userID)
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
