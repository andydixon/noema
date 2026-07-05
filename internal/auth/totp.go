package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP mandates HMAC-SHA1; authenticator apps expect it
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/pkg/ids"
)

// Second factor (spec §26 / Phase 16): RFC 6238 TOTP with single-use
// recovery codes. The TOTP secret is stored encrypted under the master key;
// recovery codes are stored hashed. Login becomes two steps when enabled:
// password first, then a short-lived pending token exchanged with a code.

// ErrSecondFactorRequired is returned by Login when the password was right
// but the account requires a code; the returned token is the pending token.
var ErrSecondFactorRequired = errors.New("second factor required")

// ErrTOTPUnavailable is returned when enrolment is impossible (no master key).
var ErrTOTPUnavailable = errors.New("two-factor authentication needs NOEMA_MASTER_KEY so the secret can be stored encrypted")

const (
	totpPeriod   = 30
	totpDigits   = 6
	totpSkew     = 1 // accept ±1 period
	pendingTTL   = 5 * time.Minute
	recoveryN    = 10
	recoveryBits = 10 // bytes → 16 base32 chars
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a fresh 160-bit base32 secret.
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return b32.EncodeToString(buf), nil
}

// TOTPCode computes the code for a secret at time t.
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	counter := uint64(t.Unix() / totpPeriod) // #nosec G115 -- Unix time after 1970 is non-negative
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1_000_000), nil
}

// VerifyTOTP checks a code against the secret within ±totpSkew periods.
func VerifyTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return false
	}
	ok := false
	for i := -totpSkew; i <= totpSkew; i++ {
		want, err := TOTPCode(secret, now.Add(time.Duration(i)*totpPeriod*time.Second))
		if err != nil {
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			ok = true // keep looping: constant work regardless of which window matched
		}
	}
	return ok
}

// OTPAuthURL renders the standard enrolment URI for authenticator apps.
func OTPAuthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {secret}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// newRecoveryCodes returns plaintext codes and their hashes.
func newRecoveryCodes() ([]string, [][]byte, error) {
	var plain []string
	var hashes [][]byte
	for i := 0; i < recoveryN; i++ {
		buf := make([]byte, recoveryBits)
		if _, err := rand.Read(buf); err != nil {
			return nil, nil, err
		}
		c := strings.ToLower(b32.EncodeToString(buf))
		c = c[:4] + "-" + c[4:8] + "-" + c[8:12] + "-" + c[12:16]
		plain = append(plain, c)
		hashes = append(hashes, hashRecovery(c))
	}
	return plain, hashes, nil
}

func hashRecovery(code string) []byte {
	norm := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
	h := sha256.Sum256([]byte("noema-recovery:" + norm))
	return h[:]
}

// pending holds password-verified logins awaiting a second factor.
type pending struct {
	userID  ids.ID
	ip      string
	ua      string
	expires time.Time
	tries   int
}

type pendingStore struct {
	mu sync.Mutex
	m  map[string]*pending
}

func (p *pendingStore) put(hash string, v *pending) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]*pending{}
	}
	p.m[hash] = v
}

func (p *pendingStore) get(hash string, now time.Time) (*pending, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[hash]
	if !ok || now.After(v.expires) {
		delete(p.m, hash)
		return nil, false
	}
	return v, true
}

func (p *pendingStore) del(hash string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, hash)
}

func (p *pendingStore) sweep(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.m {
		if now.After(v.expires) {
			delete(p.m, k)
		}
	}
}

// SetSecrets provides the resolver used to encrypt TOTP secrets at rest.
func (s *Service) SetSecrets(r *secrets.Resolver) { s.secrets = r }

func (s *Service) totpSecret(ctx context.Context, userID ids.ID) (string, bool, error) {
	var enc *string
	var enabled bool
	if err := s.db.QueryRow(ctx, `SELECT totp_secret_enc, totp_enabled FROM users WHERE id=$1`, userID).Scan(&enc, &enabled); err != nil {
		return "", false, err
	}
	if enc == nil || *enc == "" {
		return "", enabled, nil
	}
	if s.secrets == nil {
		return "", enabled, ErrTOTPUnavailable
	}
	plain, err := s.secrets.Resolve(*enc)
	if err != nil {
		return "", enabled, err
	}
	return plain, enabled, nil
}

// BeginTOTP creates a pending secret for the user (not yet enforced) and
// returns it with the otpauth URI so the operator can add it to an app.
func (s *Service) BeginTOTP(ctx context.Context, userID ids.ID, issuer string) (secret, uri string, err error) {
	if s.secrets == nil || !s.secrets.CanEncrypt() {
		return "", "", ErrTOTPUnavailable
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", "", err
	}
	secret, err = GenerateTOTPSecret()
	if err != nil {
		return "", "", err
	}
	enc, err := s.secrets.Encrypt(secret)
	if err != nil {
		return "", "", err
	}
	if _, err := s.db.Exec(ctx, `UPDATE users SET totp_secret_enc=$2, totp_enabled=false, updated_at=now() WHERE id=$1`, userID, enc); err != nil {
		return "", "", err
	}
	return secret, OTPAuthURL(issuer, u.Username, secret), nil
}

// PendingTOTP returns the not-yet-confirmed secret and URI so the account
// page can show them again after a redirect without a second enrolment.
func (s *Service) PendingTOTP(ctx context.Context, userID ids.ID, issuer string) (secret, uri string, err error) {
	secret, enabled, err := s.totpSecret(ctx, userID)
	if err != nil || secret == "" || enabled {
		return "", "", errors.New("no pending enrolment")
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", "", err
	}
	return secret, OTPAuthURL(issuer, u.Username, secret), nil
}

// ConfirmTOTP verifies a first code against the pending secret, turns the
// second factor on and returns freshly generated recovery codes (shown once).
func (s *Service) ConfirmTOTP(ctx context.Context, userID ids.ID, code string) ([]string, error) {
	secret, _, err := s.totpSecret(ctx, userID)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, errors.New("no pending enrolment")
	}
	if !VerifyTOTP(secret, code, s.now()) {
		return nil, ErrInvalidCredentials
	}
	plain, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id=$1`, userID); err != nil {
		return nil, err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (id, user_id, code_hash) VALUES ($1,$2,$3)`, ids.New(), userID, h); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET totp_enabled=true, updated_at=now() WHERE id=$1`, userID); err != nil {
		return nil, err
	}
	return plain, tx.Commit(ctx)
}

// DisableTOTP removes the second factor and all recovery codes.
func (s *Service) DisableTOTP(ctx context.Context, userID ids.ID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id=$1`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET totp_secret_enc=NULL, totp_enabled=false, updated_at=now() WHERE id=$1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecoveryCodesLeft counts unused recovery codes.
func (s *Service) RecoveryCodesLeft(ctx context.Context, userID ids.ID) int {
	var n int
	s.db.QueryRow(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id=$1 AND used_at IS NULL`, userID).Scan(&n) //nolint:errcheck
	return n
}

// verifySecondFactor accepts a TOTP code or an unused recovery code.
func (s *Service) verifySecondFactor(ctx context.Context, userID ids.ID, code string) (usedRecovery bool, err error) {
	secret, enabled, err := s.totpSecret(ctx, userID)
	if err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	if VerifyTOTP(secret, code, s.now()) {
		return false, nil
	}
	if len(strings.TrimSpace(code)) >= 16 {
		tag, err := s.db.Exec(ctx, `UPDATE recovery_codes SET used_at=now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL`, userID, hashRecovery(code))
		if err == nil && tag.RowsAffected() == 1 {
			return true, nil
		}
	}
	return false, ErrInvalidCredentials
}

// CompleteLogin exchanges a pending token plus a second-factor code for a
// session. Five wrong codes void the pending login.
func (s *Service) CompleteLogin(ctx context.Context, pendingToken, code, ip, userAgent string) (Session, string, bool, error) {
	if !s.limiter.Allow(clientKey(ip)) {
		return Session{}, "", false, ErrRateLimited
	}
	hash := string(hashToken(pendingToken))
	p, ok := s.pending.get(hash, s.now())
	if !ok || p.ip != clientKey(ip) {
		return Session{}, "", false, ErrInvalidCredentials
	}
	usedRecovery, err := s.verifySecondFactor(ctx, p.userID, code)
	if err != nil {
		p.tries++
		if p.tries >= 5 {
			s.pending.del(hash)
		}
		return Session{}, "", false, err
	}
	s.pending.del(hash)
	sess, tok, err := s.createSession(ctx, p.userID, ip, userAgent)
	return sess, tok, usedRecovery, err
}
