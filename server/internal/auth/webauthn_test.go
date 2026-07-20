package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	virtualwebauthn "github.com/descope/virtualwebauthn"

	"github.com/ryu-karura/RedminePocketGo/server/internal/httpapi"

	"github.com/ryu-karura/RedminePocketGo/server/internal/store"
)

const (
	testRPID   = "localhost"
	testOrigin = "http://localhost:8090"
)

func newTestWebAuthn(t *testing.T) (*WebAuthn, *store.Store) {
	t.Helper()
	st := testStore(t) // u1 / alice / handle []byte{1}
	w, err := NewWebAuthn(st, WebAuthnConfig{
		RPID: testRPID, RPName: "RedminePocketGo",
		Origins:          []string{testOrigin},
		UserVerification: "required",
		ChallengeTTL:     5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	return w, st
}

// 擬似認証器（UV あり、ユーザーハンドル付き = Discoverable）
func newVirtualAuthenticator() (virtualwebauthn.RelyingParty, virtualwebauthn.Authenticator, virtualwebauthn.Credential) {
	rp := virtualwebauthn.RelyingParty{ID: testRPID, Name: "RedminePocketGo", Origin: testOrigin}
	auth := virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{
		UserHandle: []byte{1},
	})
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	return rp, auth, cred
}

// 登録 begin→finish を擬似認証器で通し、保存されたパスキーを確認する。
func registerCredential(t *testing.T, w *WebAuthn) virtualwebauthn.Credential {
	t.Helper()
	ctx := context.Background()
	rp, auth, cred := newVirtualAuthenticator()

	optionsJSON, challengeID, err := w.BeginRegistration(ctx, "u1")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions: %v (%s)", err, optionsJSON)
	}
	body := virtualwebauthn.CreateAttestationResponse(rp, auth, cred, *attOpts)

	req := httptest.NewRequest("POST", "/api/auth/register/finish", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	userID, credID, err := w.FinishRegistration(ctx, challengeID, req)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	if userID != "u1" || len(credID) == 0 {
		t.Fatalf("FinishRegistration = %q, %v", userID, credID)
	}
	auth.AddCredential(cred)
	return cred
}

func TestRegistrationCeremony(t *testing.T) {
	w, st := newTestWebAuthn(t)
	registerCredential(t, w)

	creds, err := st.ListCredentialsByUser(context.Background(), "u1")
	if err != nil || len(creds) != 1 {
		t.Fatalf("stored credentials = %v, %v; want 1", creds, err)
	}
	if len(creds[0].PublicKey) == 0 {
		t.Error("public key not stored")
	}
}

func TestChallengeIsSingleUse(t *testing.T) {
	w, _ := newTestWebAuthn(t)
	ctx := context.Background()
	rp, auth, cred := newVirtualAuthenticator()

	optionsJSON, challengeID, err := w.BeginRegistration(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	attOpts, _ := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	body := virtualwebauthn.CreateAttestationResponse(rp, auth, cred, *attOpts)

	for i, wantErr := range []error{nil, ErrChallengeInvalid} {
		req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		_, _, err := w.FinishRegistration(ctx, challengeID, req)
		if !errors.Is(err, wantErr) && !(i == 0 && err == nil) {
			t.Fatalf("use %d: err = %v; want %v", i, err, wantErr)
		}
	}
}

func TestChallengeExpires(t *testing.T) {
	w, _ := newTestWebAuthn(t)
	ctx := context.Background()
	rp, auth, cred := newVirtualAuthenticator()

	optionsJSON, challengeID, err := w.BeginRegistration(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return time.Now().Add(6 * time.Minute) } // TTL 5 分を超過

	attOpts, _ := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	body := virtualwebauthn.CreateAttestationResponse(rp, auth, cred, *attOpts)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if _, _, err := w.FinishRegistration(ctx, challengeID, req); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("expired challenge: err = %v; want ErrChallengeInvalid", err)
	}
}

func TestLoginCeremony(t *testing.T) {
	w, st := newTestWebAuthn(t)
	ctx := context.Background()
	rp, auth, cred := newVirtualAuthenticator()

	// 事前に登録
	optionsJSON, challengeID, _ := w.BeginRegistration(ctx, "u1")
	attOpts, _ := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	body := virtualwebauthn.CreateAttestationResponse(rp, auth, cred, *attOpts)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if _, _, err := w.FinishRegistration(ctx, challengeID, req); err != nil {
		t.Fatal(err)
	}
	auth.AddCredential(cred)

	// ユーザー名なし（Discoverable）でログイン
	loginJSON, loginChallenge, err := w.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	asOpts, err := virtualwebauthn.ParseAssertionOptions(string(loginJSON))
	if err != nil {
		t.Fatalf("ParseAssertionOptions: %v (%s)", err, loginJSON)
	}
	asBody := virtualwebauthn.CreateAssertionResponse(rp, auth, cred, *asOpts)
	loginReq := httptest.NewRequest("POST", "/api/auth/login/finish", bytes.NewReader([]byte(asBody)))
	loginReq.Header.Set("Content-Type", "application/json")

	userID, credID, err := w.FinishLogin(ctx, loginChallenge, loginReq)
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if userID != "u1" || len(credID) == 0 {
		t.Errorf("FinishLogin = %q, %v; want u1", userID, credID)
	}

	// 最終使用時刻が記録される
	creds, _ := st.ListCredentialsByUser(ctx, "u1")
	if len(creds) != 1 || creds[0].LastUsedAt == nil {
		t.Errorf("last_used_at not recorded: %+v", creds)
	}
}

func TestLoginWithForeignCredentialFails(t *testing.T) {
	w, _ := newTestWebAuthn(t)
	ctx := context.Background()
	rp, auth, cred := newVirtualAuthenticator()
	auth.AddCredential(cred) // 登録していない鍵

	loginJSON, loginChallenge, err := w.BeginLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	asOpts, _ := virtualwebauthn.ParseAssertionOptions(string(loginJSON))
	asBody := virtualwebauthn.CreateAssertionResponse(rp, auth, cred, *asOpts)
	req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(asBody)))
	req.Header.Set("Content-Type", "application/json")

	if _, _, err := w.FinishLogin(ctx, loginChallenge, req); !errors.Is(err, ErrCeremonyFailed) {
		t.Fatalf("unregistered credential: err = %v; want ErrCeremonyFailed", err)
	}
}

func TestDuplicateRegistrationIsCeremonyError(t *testing.T) {
	w, _ := newTestWebAuthn(t)
	ctx := context.Background()
	rp, auth, cred := newVirtualAuthenticator()

	do := func() error {
		optionsJSON, challengeID, err := w.BeginRegistration(ctx, "u1")
		if err != nil {
			return err
		}
		attOpts, _ := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
		body := virtualwebauthn.CreateAttestationResponse(rp, auth, cred, *attOpts)
		req := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		_, _, err = w.FinishRegistration(ctx, challengeID, req)
		return err
	}
	if err := do(); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	// 同じ認証器・同じ鍵での再登録は 4xx 相当（ErrInvalidRequest 系）
	if err := do(); !errors.Is(err, ErrCeremonyFailed) || !errors.Is(err, httpapi.ErrInvalidRequest) {
		t.Fatalf("duplicate registration err = %v; want ErrCeremonyFailed wrapping ErrInvalidRequest", err)
	}
}
