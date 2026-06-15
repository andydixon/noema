package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
)

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatal("not argon2id")
	}
	if ok, _ := VerifyPassword(h, "correct horse battery"); !ok {
		t.Fatal("should verify")
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Fatal("should not verify")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestLockoutSchedule(t *testing.T) {
	if lockoutDuration(4) != 0 || lockoutDuration(5) != 30*time.Second || lockoutDuration(7) != 2*time.Minute || lockoutDuration(40) != time.Hour {
		t.Fatal("unexpected lockout schedule")
	}
}

func TestIPLimiter(t *testing.T) {
	l := newIPLimiter(60, 3)
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatal("burst should allow")
		}
	}
	if l.Allow("a") {
		t.Fatal("should be limited")
	}
	if !l.Allow("b") {
		t.Fatal("other key unaffected")
	}
	now = now.Add(2 * time.Second)
	if !l.Allow("a") {
		t.Fatal("should refill")
	}
}

func TestLoginSessionLifecycle(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	svc := NewService(pool, time.Hour, 2*time.Hour)
	if _, err := svc.CreateUser(ctx, "andy", "a very long password", RoleAdministrator); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, "ANDY", "a very long password", RoleObserver); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("expected conflict on case-insensitive duplicate, got %v", err)
	}
	if _, _, err := svc.Login(ctx, "andy", "nope nope nope", "10.0.0.1", "t"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
	sess, token, err := svc.Login(ctx, "Andy", "a very long password", "10.0.0.1", "t")
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Authenticate(ctx, token)
	if err != nil || p.User.Username != "andy" || p.SessionID != sess.ID || !p.ViaCookie {
		t.Fatalf("authenticate: %v %+v", err, p)
	}
	if _, err := svc.Authenticate(ctx, "bogus"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("bogus token accepted")
	}

	// Lockout after repeated failures.
	for i := 0; i < 5; i++ {
		svc.Login(ctx, "andy", "wrong wrong wrong", "10.0.0.2", "t") //nolint:errcheck
	}
	if _, _, err := svc.Login(ctx, "andy", "a very long password", "10.0.0.2", "t"); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected lock, got %v", err)
	}

	// Idle expiry.
	svc.now = func() time.Time { return time.Now().Add(61 * time.Minute) }
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired session accepted")
	}
	svc.now = time.Now

	// API token.
	raw, err := svc.CreateAPIToken(ctx, p.User.ID, "cli", nil)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := svc.AuthenticateToken(ctx, strings.TrimPrefix(raw, "noema_"))
	if err != nil || ap.ViaCookie || ap.User.Role != RoleAdministrator {
		t.Fatalf("api token: %v %+v", err, ap)
	}

	// Disabling revokes sessions.
	sess2, token2, _ := svc.Login(ctx, "andy", "a very long password", "10.0.0.3", "t")
	_ = sess2
	if err := svc.SetUserDisabled(ctx, p.User.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, token2); err == nil {
		t.Fatal("disabled user session accepted")
	}
}

func TestMiddlewareCSRFAndRoles(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	svc := NewService(pool, time.Hour, 2*time.Hour)
	svc.CreateUser(ctx, "op", "a very long password", RoleOperator) //nolint:errcheck
	sess, token, err := svc.Login(ctx, "op", "a very long password", "127.0.0.1", "t")
	if err != nil {
		t.Fatal(err)
	}
	var failed bool
	onFail := func(w http.ResponseWriter, r *http.Request, authed bool) { failed = true; w.WriteHeader(401) }
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := svc.Middleware(RequireRole(RoleOperator, onFail)(ok))
	adminOnly := svc.Middleware(RequireRole(RoleAdministrator, onFail)(ok))

	do := func(handler http.Handler, method, csrf, origin string, cookie bool) int {
		req := httptest.NewRequest(method, "http://noema.local/x", nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
		}
		if csrf != "" {
			req.Header.Set(CSRFHeader, csrf)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if do(h, "GET", "", "", false) != 401 || !failed {
		t.Fatal("unauthenticated GET should fail")
	}
	if do(h, "GET", "", "", true) != 200 {
		t.Fatal("authenticated GET should pass")
	}
	if do(h, "POST", "", "", true) != 403 {
		t.Fatal("POST without CSRF should be forbidden")
	}
	if do(h, "POST", sess.CSRFToken, "", true) != 200 {
		t.Fatal("POST with CSRF should pass")
	}
	if do(h, "POST", sess.CSRFToken, "http://evil.example", true) != 403 {
		t.Fatal("cross-origin POST should be forbidden")
	}
	if do(h, "POST", "wrong", "", true) != 403 {
		t.Fatal("wrong CSRF should be forbidden")
	}
	if do(adminOnly, "GET", "", "", true) != 401 {
		t.Fatal("operator should not reach admin route")
	}
}
