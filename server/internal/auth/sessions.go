// Package auth は WebAuthn セレモニー、セッション、レート制限を担う。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"
	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

// Config はセッションの動作設定（config.Session から組み立てる）。
type Config struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	CookieName      string
	SecureCookie    bool
}

// Sessions はセッションの発行・検証・失効を担う。httpapi.SessionResolver を
// 実装する。トークンの生値はクライアントにのみ渡し、DB にはハッシュを置く。
type Sessions struct {
	store *store.Store
	cfg   Config
	now   func() time.Time
}

func NewSessions(st *store.Store, cfg Config) *Sessions {
	return &Sessions{store: st, cfg: cfg, now: time.Now}
}

// Issue は新しいセッションを発行し、クライアントに渡す生トークンを返す。
func (s *Sessions) Issue(ctx context.Context, userID string, credentialID []byte) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("auth: セッショントークン生成に失敗しました: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := s.now().UTC()
	if err := s.store.InsertSession(ctx, &store.Session{
		IDHash:            hashToken(token),
		UserID:            userID,
		CredentialID:      credentialID,
		CreatedAt:         now,
		LastSeenAt:        now,
		AbsoluteExpiresAt: now.Add(s.cfg.AbsoluteTimeout),
	}); err != nil {
		return "", err
	}
	return token, nil
}

// ResolveSession は httpapi.SessionResolver の実装。二軸タイムアウトを
// 判定し、期限切れは削除して未認証 (nil, nil) を返す。有効なら
// last_seen_at を進める（アイドル窓のスライド）。
func (s *Sessions) ResolveSession(ctx context.Context, token string) (*httpapi.SessionInfo, error) {
	sess, err := s.store.GetSession(ctx, hashToken(token))
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, nil
	}
	now := s.now().UTC()
	if now.After(sess.AbsoluteExpiresAt) || now.After(sess.LastSeenAt.Add(s.cfg.IdleTimeout)) {
		if err := s.store.DeleteSession(ctx, sess.IDHash); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err := s.store.TouchSession(ctx, sess.IDHash, now); err != nil {
		return nil, err
	}
	return &httpapi.SessionInfo{UserID: sess.UserID}, nil
}

// Revoke はセッションを失効させる（ログアウト）。
func (s *Sessions) Revoke(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, hashToken(token))
}

// Cookie はセッショントークンを載せた Cookie を返す（Design.md §3.5:
// HttpOnly, Secure, SameSite=Lax, Path=/）。有効期限はサーバー側で判定
// するためセッション Cookie とし、Max-Age は付けない。
func (s *Sessions) Cookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearCookie はログアウト時にブラウザ側の Cookie を消す。
func (s *Sessions) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
