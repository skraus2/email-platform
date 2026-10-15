//go:build integration

package admin_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/admin"
	"github.com/skraus2/email-platform/internal/testdb"
)

var fast = admin.Params{Memory: 64, Time: 1, Threads: 1}

const pw = "correct horse battery"

func newService(t *testing.T, logs *bytes.Buffer) (*admin.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.NewMigrated(t)
	logger := slog.New(slog.DiscardHandler)
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	s := admin.NewService(pool, logger, 30*time.Minute, 12*time.Hour)
	s.UseParams(fast)
	return s, pool
}

func TestCreateUserAndLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var logs bytes.Buffer
	s, pool := newService(t, &logs)
	u, err := s.CreateUser(ctx, "alice", pw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "alice", pw); !errors.Is(err, admin.ErrUsernameTaken) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, bad := range []struct{ name, pw string }{{"Al", pw}, {"Alice", pw}, {"alice2", "short"}} {
		if _, err := s.CreateUser(ctx, bad.name, bad.pw); err == nil {
			t.Errorf("%q/%q accepted", bad.name, bad.pw)
		}
	}
	var stored string
	if err := pool.QueryRow(ctx, "SELECT password_hash FROM admin_users WHERE id = $1", u.ID).Scan(&stored); err != nil || !strings.HasPrefix(stored, "$argon2id$") || strings.Contains(stored, pw) {
		t.Fatalf("stored hash %q %v", stored, err)
	}

	sess, err := s.Login(ctx, "alice", pw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Token) != 43 || sess.UserID != u.ID || time.Until(sess.ExpiresAt) < 11*time.Hour {
		t.Fatalf("session %+v", sess)
	}
	var tokens int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM admin_sessions WHERE token_hash = sha256(convert_to($1, 'UTF8')) AND admin_user_id = $2", sess.Token, u.ID).Scan(&tokens); err != nil || tokens != 1 {
		t.Fatalf("session not stored as the SHA-256 of its token (%d, %v)", tokens, err)
	}
	p, err := s.Authenticate(ctx, sess.Token)
	if err != nil || p.UserID != u.ID || p.Username != "alice" {
		t.Fatalf("authenticate: %+v %v", p, err)
	}
	if err := s.Logout(ctx, sess.Token); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"admin logout","admin_user_id":"`+u.ID.String()) {
		t.Fatalf("logout not logged with the account id: %s", logs.String())
	}
	if _, err := s.Authenticate(ctx, sess.Token); !errors.Is(err, admin.ErrUnauthenticated) {
		t.Fatalf("after logout: %v", err)
	}
	for _, bad := range []string{"", "x", strings.Repeat("A", 43), sess.Token + "A"} {
		if _, err := s.Authenticate(ctx, bad); !errors.Is(err, admin.ErrUnauthenticated) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
	if strings.Contains(logs.String(), pw) || strings.Contains(logs.String(), sess.Token) {
		t.Fatalf("logs contain password or token: %s", logs.String())
	}
}

func TestLoginFailuresLookAlike(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var logs bytes.Buffer
	s, _ := newService(t, &logs)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "bob", pw); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisabled(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ user, pw string }{
		{"alice", "wrong password!!"}, {"nobody", pw}, {"bob", pw}, {"Not Valid", pw},
	} {
		before := s.Hashes()
		if _, err := s.Login(ctx, tc.user, tc.pw); !errors.Is(err, admin.ErrInvalidCredentials) {
			t.Errorf("%s: %v, want ErrInvalidCredentials", tc.user, err)
		}
		if s.Hashes() != before+1 {
			t.Errorf("%s: %d hashes, want exactly 1 (same cost for every failure)", tc.user, s.Hashes()-before)
		}
	}
	if strings.Contains(logs.String(), "nobody") || strings.Contains(logs.String(), "wrong password") {
		t.Fatalf("failed attempts logged with name or password: %s", logs.String())
	}
}

func TestLockoutAndUnlock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, pool := newService(t, nil)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if _, err := s.Login(ctx, "alice", "wrong password!!"); !errors.Is(err, admin.ErrInvalidCredentials) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	// Locked: even the right password is refused, without hashing.
	before := s.Hashes()
	_, err := s.Login(ctx, "alice", pw)
	var locked *admin.LockedError
	if !errors.As(err, &locked) || locked.RetryAfter <= time.Minute || locked.RetryAfter > 2*time.Minute {
		t.Fatalf("locked login: %v (retry %v); the attempt during the lock should extend it to 2m", err, locked)
	}
	if s.Hashes() != before {
		t.Fatal("hashed while locked")
	}
	if err := s.Unlock(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "alice", pw); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	// Unknown names are throttled the same way.
	for range 5 {
		if _, err := s.Login(ctx, "ghost", pw); !errors.Is(err, admin.ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if _, err := s.Login(ctx, "ghost", pw); !errors.As(err, &locked) {
		t.Fatalf("unknown name not locked: %v", err)
	}
	// set-password and enable reset the lock too.
	for range 5 {
		_, _ = s.Login(ctx, "alice", "wrong password!!")
	}
	if err := s.SetPassword(ctx, "alice", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "alice", "another long password"); err != nil {
		t.Fatalf("after set-password: %v", err)
	}
	for range 5 {
		_, _ = s.Login(ctx, "alice", "wrong password!!")
	}
	if err := s.SetDisabled(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "alice", "another long password"); err != nil {
		t.Fatalf("after enable: %v", err)
	}
	// A successful login resets the count: 4 + success + 4 failures stay
	// unlocked.
	for range 4 {
		_, _ = s.Login(ctx, "alice", "wrong password!!")
	}
	if _, err := s.Login(ctx, "alice", "another long password"); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		_, _ = s.Login(ctx, "alice", "wrong password!!")
	}
	if _, err := s.Login(ctx, "alice", "another long password"); err != nil {
		t.Fatalf("counter not reset by the earlier success: %v", err)
	}
	// The database lock formula is capped at 15 minutes.
	if _, err := pool.Exec(ctx, "UPDATE admin_users SET failed_logins = 30, locked_until = now() + interval '1 minute' WHERE username = 'alice'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "alice", pw); !errors.As(err, &locked) || locked.RetryAfter > 15*time.Minute || locked.RetryAfter < 14*time.Minute {
		t.Fatalf("cap: %v %v", err, locked)
	}
	for _, name := range []string{"nobody"} {
		if err := s.Unlock(ctx, name); !errors.Is(err, admin.ErrUserNotFound) {
			t.Errorf("unlock %s: %v", name, err)
		}
	}
}

func TestSessionsEndOnDisableAndPasswordChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newService(t, nil)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Login(ctx, "alice", pw)
	b, _ := s.Login(ctx, "alice", pw)
	if a.Token == b.Token {
		t.Fatal("two logins share a token")
	}
	if err := s.SetDisabled(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{a.Token, b.Token} {
		if _, err := s.Authenticate(ctx, tok); !errors.Is(err, admin.ErrUnauthenticated) {
			t.Fatalf("session survived disabling: %v", err)
		}
	}
	if err := s.SetDisabled(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	c, err := s.Login(ctx, "alice", pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(ctx, "alice", "another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, c.Token); !errors.Is(err, admin.ErrUnauthenticated) {
		t.Fatalf("session survived a password change: %v", err)
	}
	if err := s.SetPassword(ctx, "alice", "short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestSessionExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, pool := newService(t, nil)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	idle, _ := s.Login(ctx, "alice", pw)
	absolute, _ := s.Login(ctx, "alice", pw)
	if _, err := pool.Exec(ctx, "UPDATE admin_sessions SET last_seen_at = now() - interval '31 minutes' WHERE token_hash = sha256(convert_to($1, 'UTF8'))", idle.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, idle.Token); !errors.Is(err, admin.ErrUnauthenticated) {
		t.Fatalf("idle session: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE admin_sessions SET created_at = now() - interval '13 hours', expires_at = now() - interval '1 second' WHERE token_hash = sha256(convert_to($1, 'UTF8'))", absolute.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, absolute.Token); !errors.Is(err, admin.ErrUnauthenticated) {
		t.Fatalf("expired session: %v", err)
	}
	// Use slides the idle timeout (at most once a minute).
	live, _ := s.Login(ctx, "alice", pw)
	if _, err := pool.Exec(ctx, "UPDATE admin_sessions SET last_seen_at = now() - interval '29 minutes' WHERE token_hash = sha256(convert_to($1, 'UTF8'))", live.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, live.Token); err != nil {
		t.Fatal(err)
	}
	var age time.Duration
	if err := pool.QueryRow(ctx, "SELECT now() - last_seen_at FROM admin_sessions WHERE token_hash = sha256(convert_to($1, 'UTF8'))", live.Token).Scan(&age); err != nil || age > time.Minute {
		t.Fatalf("idle timeout not refreshed: %v %v", age, err)
	}
}

func TestOutdatedHashIsUpgradedOnLogin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, pool := newService(t, nil)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	s.UseParams(admin.Params{Memory: 128, Time: 1, Threads: 1})
	if _, err := s.Login(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	var h string
	if err := pool.QueryRow(ctx, "SELECT password_hash FROM admin_users WHERE username = 'alice'").Scan(&h); err != nil || !strings.Contains(h, "m=128,") {
		t.Fatalf("hash not upgraded: %q %v", h, err)
	}
	if _, err := s.Login(ctx, "alice", pw); err != nil {
		t.Fatalf("login with the upgraded hash: %v", err)
	}
}

func TestLoginHashingIsBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newService(t, nil)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	s.SetHashLimits(1, 50*time.Millisecond)
	release := s.HoldHashSlot()
	if _, err := s.Login(ctx, "alice", pw); !errors.Is(err, admin.ErrBusy) {
		t.Fatalf("no hash slot: %v", err)
	}
	release()
	if _, err := s.Login(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
}

func TestParallelGuessesHashAtMostFiveTimes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newService(t, nil)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	before := s.Hashes()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { _, _ = s.Login(ctx, "alice", "wrong password!!") })
	}
	wg.Wait()
	if n := s.Hashes() - before; n > 5 {
		t.Fatalf("%d parallel guesses were verified, want at most 5", n)
	}
	var locked *admin.LockedError
	if _, err := s.Login(ctx, "alice", pw); !errors.As(err, &locked) {
		t.Fatalf("not locked after the burst: %v", err)
	}
	// Unknown names: the same bound.
	before = s.Hashes()
	for range 20 {
		wg.Go(func() { _, _ = s.Login(ctx, "ghost", pw) })
	}
	wg.Wait()
	if n := s.Hashes() - before; n > 5 {
		t.Fatalf("%d parallel guesses for an unknown name were hashed, want at most 5", n)
	}
}

func TestPasswordChangeDuringLoginWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, pool := newService(t, nil)
	if _, err := s.CreateUser(ctx, "alice", pw); err != nil {
		t.Fatal(err)
	}
	// The password changes (and sessions are ended) after the old password
	// was verified but before the session is created.
	s.AfterVerify(func() {
		if err := s.SetPassword(ctx, "alice", "another long password"); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Login(ctx, "alice", pw); !errors.Is(err, admin.ErrInvalidCredentials) {
		t.Fatalf("login racing a password change: %v", err)
	}
	s.AfterVerify(func() {
		if err := s.SetDisabled(ctx, "alice", true); err != nil {
			t.Error(err)
		}
	})
	if _, err := s.Login(ctx, "alice", "another long password"); !errors.Is(err, admin.ErrInvalidCredentials) {
		t.Fatalf("login racing a disable: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM admin_sessions").Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d sessions survived (%v)", n, err)
	}
}
