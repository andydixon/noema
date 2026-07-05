package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// User is an operator account.
type User struct {
	ID          ids.ID
	Username    string
	Role        Role
	Disabled    bool
	TOTPEnabled bool
	LockedUntil *time.Time
	CreatedAt   time.Time
}

// Session is an authenticated browser or CLI session.
type Session struct {
	ID         ids.ID
	UserID     ids.ID
	CSRFToken  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

// Principal is the authenticated identity attached to a request context.
type Principal struct {
	User      User
	SessionID ids.ID // empty for API-token authentication
	ViaCookie bool   // cookie sessions need CSRF checks; bearer tokens do not
}

// Errors returned by the service.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrLocked             = errors.New("account temporarily locked")
	ErrDisabled           = errors.New("account disabled")
	ErrRateLimited        = errors.New("too many attempts")
	ErrUnauthenticated    = errors.New("unauthenticated")
)

// Service performs authentication operations against the database.
type Service struct {
	db          *pgxpool.Pool
	idleTTL     time.Duration
	absoluteTTL time.Duration
	limiter     *ipLimiter
	now         func() time.Time
	secrets     *secrets.Resolver
	pending     pendingStore
}

// NewService creates the authentication service.
func NewService(db *pgxpool.Pool, idleTTL, absoluteTTL time.Duration) *Service {
	return &Service{db: db, idleTTL: idleTTL, absoluteTTL: absoluteTTL, limiter: newIPLimiter(20, 10), now: time.Now}
}

// SweepLimiter releases idle rate-limit buckets; call periodically.
func (s *Service) SweepLimiter() { s.limiter.Sweep(); s.pending.sweep(s.now()) }

// CreateUser adds an account. Usernames are unique case-insensitively.
func (s *Service) CreateUser(ctx context.Context, username, password string, role Role) (User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 2 || len(username) > 64 {
		return User{}, errors.New("username must be 2-64 characters")
	}
	if !role.Valid() {
		return User{}, fmt.Errorf("invalid role %q", role)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	u := User{ID: ids.New(), Username: username, Role: role, CreatedAt: s.now()}
	_, err = s.db.Exec(ctx, `INSERT INTO users (id, username, password_hash, role, created_at) VALUES ($1,$2,$3,$4,$5)`,
		u.ID, u.Username, hash, string(u.Role), u.CreatedAt)
	if err != nil {
		return User{}, storage.Translate(err)
	}
	return u, nil
}

// CountUsers is used to decide whether bootstrap is needed.
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// ListUsers returns all accounts ordered by name.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT id, username, role, disabled, totp_enabled, locked_until, created_at FROM users ORDER BY lower(username)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var role string
		if err := rows.Scan(&u.ID, &u.Username, &role, &u.Disabled, &u.TOTPEnabled, &u.LockedUntil, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.Role = Role(role)
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser fetches one account.
func (s *Service) GetUser(ctx context.Context, id ids.ID) (User, error) {
	var u User
	var role string
	err := s.db.QueryRow(ctx, `SELECT id, username, role, disabled, totp_enabled, locked_until, created_at FROM users WHERE id=$1`, id).
		Scan(&u.ID, &u.Username, &role, &u.Disabled, &u.TOTPEnabled, &u.LockedUntil, &u.CreatedAt)
	u.Role = Role(role)
	return u, storage.Translate(err)
}

// SetUserRole changes an account's role.
func (s *Service) SetUserRole(ctx context.Context, id ids.ID, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("invalid role %q", role)
	}
	tag, err := s.db.Exec(ctx, `UPDATE users SET role=$2, updated_at=now() WHERE id=$1`, id, string(role))
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// SetUserDisabled enables or disables an account and revokes its sessions when disabling.
func (s *Service) SetUserDisabled(ctx context.Context, id ids.ID, disabled bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE users SET disabled=$2, updated_at=now() WHERE id=$1`, id, disabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	if disabled {
		_, err = s.db.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id)
	}
	return err
}

// ChangePassword sets a new password and revokes every other session (T12).
func (s *Service) ChangePassword(ctx context.Context, id ids.ID, newPassword string, keepSession ids.ID) error {
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now(), updated_at=now() WHERE id=$1`, id, hash); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND id<>$2 AND revoked_at IS NULL`, id, keepSession)
	return err
}

// Login verifies credentials, applying per-IP and per-account throttling, and
// returns a new session plus the raw token to set as a cookie.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (Session, string, error) {
	if !s.limiter.Allow(clientKey(ip)) {
		return Session{}, "", ErrRateLimited
	}
	var (
		u        User
		role     string
		hash     string
		failed   int
		disabled bool
	)
	err := s.db.QueryRow(ctx, `SELECT id, username, role, password_hash, disabled, failed_attempts, locked_until, totp_enabled FROM users WHERE lower(username)=lower($1)`, strings.TrimSpace(username)).
		Scan(&u.ID, &u.Username, &role, &hash, &disabled, &failed, &u.LockedUntil, &u.TOTPEnabled)
	if err != nil {
		VerifyPassword(dummyHash, password) //nolint:errcheck // timing equalisation only
		return Session{}, "", ErrInvalidCredentials
	}
	u.Role = Role(role)
	if u.LockedUntil != nil && u.LockedUntil.After(s.now()) {
		return Session{}, "", ErrLocked
	}
	ok, err := VerifyPassword(hash, password)
	if err != nil {
		return Session{}, "", err
	}
	if !ok {
		failed++
		var until *time.Time
		if d := lockoutDuration(failed); d > 0 {
			t := s.now().Add(d)
			until = &t
		}
		s.db.Exec(ctx, `UPDATE users SET failed_attempts=$2, locked_until=$3 WHERE id=$1`, u.ID, failed, until) //nolint:errcheck
		if until != nil {
			return Session{}, "", ErrLocked
		}
		return Session{}, "", ErrInvalidCredentials
	}
	if disabled {
		return Session{}, "", ErrDisabled
	}
	if _, err := s.db.Exec(ctx, `UPDATE users SET failed_attempts=0, locked_until=NULL WHERE id=$1`, u.ID); err != nil {
		return Session{}, "", err
	}
	if u.TOTPEnabled {
		// Password accepted; hand back a pending token instead of a session.
		token, hash := newToken()
		s.pending.put(string(hash), &pending{userID: u.ID, ip: clientKey(ip), ua: truncate(userAgent, 256), expires: s.now().Add(pendingTTL)})
		return Session{}, token, ErrSecondFactorRequired
	}
	return s.createSession(ctx, u.ID, ip, userAgent)
}

func (s *Service) createSession(ctx context.Context, userID ids.ID, ip, userAgent string) (Session, string, error) {
	token, hash := newToken()
	csrf, _ := newToken()
	now := s.now()
	sess := Session{ID: ids.New(), UserID: userID, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(s.idleTTL), IP: clientKey(ip), UserAgent: truncate(userAgent, 256)}
	_, err := s.db.Exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, sess.ID, sess.UserID, hash, sess.CSRFToken, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt, sess.IP, sess.UserAgent)
	if err != nil {
		return Session{}, "", err
	}
	return sess, token, nil
}

// Authenticate resolves a session token to a principal, sliding the idle
// expiry forward. Absolute expiry is measured from session creation.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	var (
		p        Principal
		role     string
		created  time.Time
		expires  time.Time
		lastSeen time.Time
	)
	err := s.db.QueryRow(ctx, `SELECT s.id, s.csrf_token, s.created_at, s.last_seen_at, s.expires_at, u.id, u.username, u.role, u.disabled, u.totp_enabled, u.created_at
		FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL`, hashToken(token)).
		Scan(&p.SessionID, new(string), &created, &lastSeen, &expires, &p.User.ID, &p.User.Username, &role, &p.User.Disabled, &p.User.TOTPEnabled, &p.User.CreatedAt)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	now := s.now()
	if now.After(expires) || now.After(created.Add(s.absoluteTTL)) || p.User.Disabled {
		return Principal{}, ErrUnauthenticated
	}
	p.User.Role = Role(role)
	p.ViaCookie = true
	// Touch at most once a minute to avoid a write per request.
	if now.Sub(lastSeen) > time.Minute {
		s.db.Exec(ctx, `UPDATE sessions SET last_seen_at=$2, expires_at=$3 WHERE id=$1`, p.SessionID, now, now.Add(s.idleTTL)) //nolint:errcheck
	}
	return p, nil
}

// AuthenticateToken resolves a personal API token.
func (s *Service) AuthenticateToken(ctx context.Context, token string) (Principal, error) {
	var (
		p       Principal
		role    string
		tokenID ids.ID
		expires *time.Time
	)
	err := s.db.QueryRow(ctx, `SELECT t.id, t.expires_at, u.id, u.username, u.role, u.disabled, u.totp_enabled, u.created_at
		FROM api_tokens t JOIN users u ON u.id=t.user_id WHERE t.token_hash=$1 AND t.revoked_at IS NULL`, hashToken(token)).
		Scan(&tokenID, &expires, &p.User.ID, &p.User.Username, &role, &p.User.Disabled, &p.User.TOTPEnabled, &p.User.CreatedAt)
	if err != nil || p.User.Disabled || (expires != nil && expires.Before(s.now())) {
		return Principal{}, ErrUnauthenticated
	}
	p.User.Role = Role(role)
	s.db.Exec(ctx, `UPDATE api_tokens SET last_used_at=now() WHERE id=$1`, tokenID) //nolint:errcheck
	return p, nil
}

// CSRFToken returns the CSRF token bound to a session.
func (s *Service) CSRFToken(ctx context.Context, sessionID ids.ID) (string, error) {
	var t string
	err := s.db.QueryRow(ctx, `SELECT csrf_token FROM sessions WHERE id=$1 AND revoked_at IS NULL`, sessionID).Scan(&t)
	return t, storage.Translate(err)
}

// Logout revokes one session.
func (s *Service) Logout(ctx context.Context, sessionID ids.ID) error {
	_, err := s.db.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, sessionID)
	return err
}

// RevokeSession revokes a session belonging to userID; returns ErrNotFound otherwise.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID ids.ID) error {
	tag, err := s.db.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, sessionID, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// ListSessions returns a user's live sessions, newest first.
func (s *Service) ListSessions(ctx context.Context, userID ids.ID) ([]Session, error) {
	rows, err := s.db.Query(ctx, `SELECT id, user_id, created_at, last_seen_at, expires_at, ip, user_agent FROM sessions
		WHERE user_id=$1 AND revoked_at IS NULL AND expires_at > now() ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.UserID, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt, &x.IP, &x.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// CreateAPIToken issues a personal token; the raw value is returned once.
func (s *Service) CreateAPIToken(ctx context.Context, userID ids.ID, name string, expires *time.Time) (string, error) {
	token, hash := newToken()
	_, err := s.db.Exec(ctx, `INSERT INTO api_tokens (id, user_id, name, token_hash, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		ids.New(), userID, truncate(name, 64), hash, expires)
	if err != nil {
		return "", err
	}
	return "noema_" + token, nil
}

// APIToken is a personal token's metadata (never the token itself).
type APIToken struct {
	ID         ids.ID
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
}

// ListAPITokens lists a user's live tokens.
func (s *Service) ListAPITokens(ctx context.Context, userID ids.ID) ([]APIToken, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, created_at, last_used_at, expires_at FROM api_tokens WHERE user_id=$1 AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken revokes a token owned by userID.
func (s *Service) RevokeAPIToken(ctx context.Context, userID, tokenID ids.ID) error {
	tag, err := s.db.Exec(ctx, `UPDATE api_tokens SET revoked_at=now() WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, tokenID, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// PurgeExpiredSessions deletes sessions long past expiry. Sessions are not
// cognitive history, so removing them is fine; audit_log keeps the record.
func (s *Service) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now() - interval '30 days' OR revoked_at < now() - interval '30 days'`)
	return err
}

func clientKey(ip string) string {
	if a, err := netip.ParseAddr(ip); err == nil {
		if a.Is6() {
			return netip.PrefixFrom(a, 64).Masked().String() // one bucket per /64
		}
		return a.String()
	}
	return ip
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
