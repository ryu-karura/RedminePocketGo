package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newTestEnrollment(t *testing.T) (*Enrollment, *WebAuthn) {
	t.Helper()
	st := testStore(t) // u1 あり
	wa, err := NewWebAuthn(st, WebAuthnConfig{
		RPID: testRPID, RPName: "RedminePocketGo", Origins: []string{testOrigin},
		UserVerification: "required", ChallengeTTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewEnrollment(st, wa), wa
}

func TestEnrollmentIssueAndRedeem(t *testing.T) {
	e, _ := newTestEnrollment(t)
	ctx := context.Background()

	code, expiresAt, err := e.IssueCode(ctx, "u1")
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	if !regexp.MustCompile(`^\d{6}$`).MatchString(code) {
		t.Errorf("code %q is not 6 digits", code)
	}
	if until := time.Until(expiresAt); until < 9*time.Minute || until > 11*time.Minute {
		t.Errorf("expiry %v is not ~10 minutes out", expiresAt)
	}

	options, challengeID, err := e.Redeem(ctx, code)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if challengeID == "" || !strings.Contains(string(options), "publicKey") {
		t.Errorf("ceremony not started: %q %s", challengeID, options)
	}

	// 1 回限り
	if _, _, err := e.Redeem(ctx, code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("second redeem err = %v; want ErrCodeInvalid", err)
	}
}

func TestEnrollmentUnknownCode(t *testing.T) {
	e, _ := newTestEnrollment(t)
	if _, _, err := e.Redeem(context.Background(), "000000"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("err = %v; want ErrCodeInvalid", err)
	}
}

func TestEnrollmentIssueCodeNonCollisionErrorNotRetried(t *testing.T) {
	e, _ := newTestEnrollment(t)
	if err := e.store.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err := e.IssueCode(context.Background(), "u1")
	if err == nil {
		t.Fatal("IssueCode: want error after store closed, got nil")
	}
	if !strings.Contains(err.Error(), "database is closed") {
		t.Errorf("IssueCode err = %v; want underlying \"database is closed\" reason preserved", err)
	}
}

func TestEnrollmentCodeExpires(t *testing.T) {
	e, _ := newTestEnrollment(t)
	ctx := context.Background()
	code, _, err := e.IssueCode(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if _, _, err := e.Redeem(ctx, code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("expired code err = %v; want ErrCodeInvalid", err)
	}
}
