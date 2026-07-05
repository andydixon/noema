package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
)

func TestTOTPVectors(t *testing.T) {
	// RFC 6238 test vector: secret "12345678901234567890" (base32 GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ), T=59 → 287082 (SHA1, 8 digits 94287082).
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := TOTPCode(secret, time.Unix(59, 0))
	if err != nil || code != "287082" {
		t.Fatalf("got %q %v", code, err)
	}
	if !VerifyTOTP(secret, "287082", time.Unix(59, 0)) || !VerifyTOTP(secret, "287082", time.Unix(59+30, 0)) || VerifyTOTP(secret, "287082", time.Unix(59+120, 0)) {
		t.Fatal("verification window wrong")
	}
	if VerifyTOTP(secret, "28708", time.Unix(59, 0)) || VerifyTOTP(secret, "000000", time.Unix(59, 0)) {
		t.Fatal("bad code accepted")
	}
	if u := OTPAuthURL("Noema", "andy", secret); u != "otpauth://totp/Noema:andy?algorithm=SHA1&digits=6&issuer=Noema&period=30&secret="+secret {
		t.Fatal(u)
	}
}

func TestTwoStepLoginAndRecovery(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	svc := NewService(pool, time.Hour, 2*time.Hour)
	sec, _ := secrets.New("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // 32 bytes base64
	svc.SetSecrets(sec)
	u, err := svc.CreateUser(ctx, "andy", "a-long-enough-password", RoleAdministrator)
	if err != nil {
		t.Fatal(err)
	}
	// Without a master key enrolment is refused.
	if _, _, err := NewService(pool, time.Hour, time.Hour).BeginTOTP(ctx, u.ID, "Noema"); !errors.Is(err, ErrTOTPUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	secret, uri, err := svc.BeginTOTP(ctx, u.ID, "Noema")
	if err != nil || secret == "" || uri == "" {
		t.Fatal(err)
	}
	// Pending enrolment does not change login yet.
	if _, _, err := svc.Login(ctx, "andy", "a-long-enough-password", "10.0.0.1", "t"); err != nil {
		t.Fatalf("login before confirm: %v", err)
	}
	if _, err := svc.ConfirmTOTP(ctx, u.ID, "000000"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong code accepted: %v", err)
	}
	code, _ := TOTPCode(secret, time.Now())
	codes, err := svc.ConfirmTOTP(ctx, u.ID, code)
	if err != nil || len(codes) != 10 {
		t.Fatalf("confirm: %v %d", err, len(codes))
	}
	// Login now stops at the second factor.
	_, pendingTok, err := svc.Login(ctx, "andy", "a-long-enough-password", "10.0.0.1", "t")
	if !errors.Is(err, ErrSecondFactorRequired) || pendingTok == "" {
		t.Fatalf("expected second factor, got %v", err)
	}
	if _, _, _, err := svc.CompleteLogin(ctx, pendingTok, "000000", "10.0.0.1", "t"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong code completed login: %v", err)
	}
	if _, _, _, err := svc.CompleteLogin(ctx, pendingTok, code, "10.0.0.2", "t"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("pending token accepted from another address")
	}
	sess, tok, usedRecovery, err := svc.CompleteLogin(ctx, pendingTok, code, "10.0.0.1", "t")
	if err != nil || tok == "" || usedRecovery || sess.UserID != u.ID {
		t.Fatalf("complete: %v", err)
	}
	if _, _, _, err := svc.CompleteLogin(ctx, pendingTok, code, "10.0.0.1", "t"); err == nil {
		t.Fatal("pending token reusable")
	}
	// Recovery code: works once.
	_, pendingTok, _ = svc.Login(ctx, "andy", "a-long-enough-password", "10.0.0.1", "t")
	if _, _, used, err := svc.CompleteLogin(ctx, pendingTok, codes[0], "10.0.0.1", "t"); err != nil || !used {
		t.Fatalf("recovery: %v", err)
	}
	if svc.RecoveryCodesLeft(ctx, u.ID) != 9 {
		t.Fatal("recovery code not consumed")
	}
	_, pendingTok, _ = svc.Login(ctx, "andy", "a-long-enough-password", "10.0.0.1", "t")
	if _, _, _, err := svc.CompleteLogin(ctx, pendingTok, codes[0], "10.0.0.1", "t"); err == nil {
		t.Fatal("recovery code reused")
	}
	// Disable restores single-step login and wipes codes.
	if err := svc.DisableTOTP(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "andy", "a-long-enough-password", "10.0.0.1", "t"); err != nil {
		t.Fatalf("login after disable: %v", err)
	}
	if svc.RecoveryCodesLeft(ctx, u.ID) != 0 {
		t.Fatal("codes survived disable")
	}
}
