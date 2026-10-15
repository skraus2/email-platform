package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

// Errors callers branch on.
var (
	// ErrInvalidCredentials: wrong password, unknown or disabled user — one
	// answer for all, so a login reveals nothing about accounts.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrBusy: too many logins hash at once; retry shortly.
	ErrBusy = errors.New("login busy")
	// ErrUnauthenticated: missing, malformed, unknown or expired session, or
	// the user is disabled.
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrUserNotFound    = errors.New("admin user not found")
	ErrUsernameTaken   = errors.New("admin username already exists")
)

// LockedError means the username is locked after repeated failures.
type LockedError struct{ RetryAfter time.Duration }

func (e *LockedError) Error() string { return "login locked" }

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

// ValidateUsername checks the account name format.
func ValidateUsername(u string) error {
	if !usernamePattern.MatchString(u) {
		return errors.New("username must match ^[a-z0-9][a-z0-9._-]{2,63}$")
	}
	return nil
}

// Hashing limits: argon2id with DefaultParams takes 64 MiB per hash, so at
// most hashSlots run at once per process; a login waits at most hashWait.
const (
	defaultHashSlots = 4
	defaultHashWait  = 2 * time.Second
	unknownNames     = 10_000
	tokenBytes       = 32
)

// Service manages admin accounts and sessions.
type Service struct {
	pool      *pgxpool.Pool
	logger    *slog.Logger
	idle      time.Duration
	maxAge    time.Duration
	params    Params
	hashSlots chan struct{}
	hashWait  time.Duration
	hashes    atomic.Int64
	unknown   *throttle
	dummyOnce sync.Once
	dummy     string
}

// NewService builds a Service whose sessions end after idle without use or
// maxAge after login.
func NewService(pool *pgxpool.Pool, logger *slog.Logger, idle, maxAge time.Duration) *Service {
	return &Service{
		pool: pool, logger: logger, idle: idle, maxAge: maxAge, params: DefaultParams,
		hashSlots: make(chan struct{}, defaultHashSlots), hashWait: defaultHashWait,
		unknown: newThrottle(unknownNames),
	}
}

// User is an admin account as listed.
type User struct {
	ID            uuid.UUID
	Username      string
	Disabled      bool
	Locked        bool
	CreatedAt     time.Time
	PasswordSetAt time.Time
}

// Principal is the administrator behind a valid session.
type Principal struct {
	UserID           uuid.UUID
	Username         string
	SessionExpiresAt time.Time
}

// Session is a new login. Token is shown once; only its hash is stored.
type Session struct {
	Token     string
	ExpiresAt time.Time
	UserID    uuid.UUID
}

func (s *Service) hash(pw string) (string, error) {
	s.hashes.Add(1)
	return HashPassword(pw, s.params)
}

func (s *Service) verify(encoded, pw string) (bool, bool, error) {
	s.hashes.Add(1)
	return VerifyPassword(encoded, pw, s.params)
}

// dummyHash is verified for unknown and disabled users, so their logins
// cost the same as real ones.
func (s *Service) dummyHash() string {
	s.dummyOnce.Do(func() {
		h, err := HashPassword("not a real password: "+uuid.NewString(), s.params)
		if err != nil {
			panic(fmt.Sprintf("dummy hash: %v", err)) // crypto/rand failure
		}
		s.dummy = h
	})
	return s.dummy
}

// CreateUser adds an enabled account.
func (s *Service) CreateUser(ctx context.Context, username, password string) (User, error) {
	if err := ValidateUsername(username); err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	h, err := s.hash(password)
	if err != nil {
		return User{}, err
	}
	row, err := store.New(s.pool).CreateAdminUser(ctx, store.CreateAdminUserParams{Username: username, PasswordHash: h})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return User{}, ErrUsernameTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("create admin user: %w", err)
	}
	return userFromRow(row, time.Now()), nil
}

func userFromRow(r store.AdminUser, now time.Time) User {
	return User{
		ID: r.ID, Username: r.Username, Disabled: r.DisabledAt != nil,
		Locked: r.LockedUntil != nil && r.LockedUntil.After(now), CreatedAt: r.CreatedAt, PasswordSetAt: r.PasswordChangedAt,
	}
}

// ListUsers returns all accounts by name.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := store.New(s.pool).ListAdminUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list admin users: %w", err)
	}
	now := time.Now()
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFromRow(r, now))
	}
	return out, nil
}

// SetDisabled disables or enables an account; both reset the lock, and
// disabling ends all of the user's sessions.
func (s *Service) SetDisabled(ctx context.Context, username string, disabled bool) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.SetAdminUserDisabled(ctx, store.SetAdminUserDisabledParams{Username: username, Disabled: disabled})
		if err != nil {
			return fmt.Errorf("set admin user disabled: %w", err)
		}
		if n == 0 {
			return ErrUserNotFound
		}
		if disabled {
			if _, err := q.DeleteAdminSessionsOfUser(ctx, username); err != nil {
				return fmt.Errorf("delete sessions: %w", err)
			}
		}
		return nil
	})
}

// SetPassword replaces the password, resets the lock and ends all of the
// user's sessions.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	h, err := s.hash(password)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.SetAdminPassword(ctx, store.SetAdminPasswordParams{Username: username, PasswordHash: h})
		if err != nil {
			return fmt.Errorf("set admin password: %w", err)
		}
		if n == 0 {
			return ErrUserNotFound
		}
		if _, err := q.DeleteAdminSessionsOfUser(ctx, username); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		return nil
	})
}

// Unlock clears the failure counter and lock of an account.
func (s *Service) Unlock(ctx context.Context, username string) error {
	n, err := store.New(s.pool).UnlockAdminUser(ctx, username)
	if err != nil {
		return fmt.Errorf("unlock admin user: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// Login checks the credentials and opens a session. Errors:
// ErrInvalidCredentials, *LockedError, ErrBusy; anything else is internal.
// Logs carry the account id only, never names of failed attempts,
// passwords or tokens.
func (s *Service) Login(ctx context.Context, username, password string) (Session, error) {
	now := time.Now()
	q := store.New(s.pool)
	var user *store.AdminUser
	if ValidateUsername(username) == nil && len(password) <= 4*MaxPasswordLength {
		row, err := q.GetAdminUserByUsername(ctx, username)
		switch {
		case err == nil:
			user = &row
		case !errors.Is(err, pgx.ErrNoRows):
			return Session{}, fmt.Errorf("get admin user: %w", err)
		}
	}

	// Locked names fail before any hashing, and each attempt extends the
	// lock.
	if user == nil {
		if d := s.unknown.locked(username, now); d > 0 {
			s.unknown.fail(username, now)
			return Session{}, &LockedError{RetryAfter: s.unknown.locked(username, now)}
		}
	} else if user.LockedUntil != nil && user.LockedUntil.After(now) {
		f, err := q.RecordAdminLoginFailure(ctx, user.ID)
		if err != nil {
			return Session{}, fmt.Errorf("record login failure: %w", err)
		}
		s.logger.WarnContext(ctx, "admin login refused: locked", slog.String("admin_user_id", user.ID.String()))
		return Session{}, &LockedError{RetryAfter: lockRemaining(f.LockedUntil, now)}
	}

	wait := time.NewTimer(s.hashWait)
	defer wait.Stop()
	select {
	case s.hashSlots <- struct{}{}:
	case <-wait.C:
		return Session{}, ErrBusy
	case <-ctx.Done():
		return Session{}, ctx.Err()
	}
	encoded := s.dummyHash()
	if user != nil && user.DisabledAt == nil {
		encoded = user.PasswordHash
	}
	ok, outdated, err := s.verify(encoded, password)
	<-s.hashSlots
	if err != nil {
		return Session{}, fmt.Errorf("verify password: %w", err)
	}
	if user == nil || user.DisabledAt != nil || !ok {
		if user == nil {
			s.unknown.fail(username, now)
			s.logger.WarnContext(ctx, "admin login failed")
			return Session{}, ErrInvalidCredentials
		}
		if _, err := q.RecordAdminLoginFailure(ctx, user.ID); err != nil {
			return Session{}, fmt.Errorf("record login failure: %w", err)
		}
		s.logger.WarnContext(ctx, "admin login failed", slog.String("admin_user_id", user.ID.String()))
		return Session{}, ErrInvalidCredentials
	}

	if err := q.RecordAdminLoginSuccess(ctx, user.ID); err != nil {
		return Session{}, fmt.Errorf("record login success: %w", err)
	}
	if outdated {
		if h, err := s.hash(password); err == nil {
			if err := q.RehashAdminPassword(ctx, store.RehashAdminPasswordParams{ID: user.ID, PasswordHash: h, OldHash: user.PasswordHash}); err != nil {
				s.logger.WarnContext(ctx, "admin password rehash failed", slog.String("admin_user_id", user.ID.String()), slog.Any("error", err))
			}
		}
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	row, err := q.InsertAdminSession(ctx, store.InsertAdminSessionParams{
		AdminUserID: user.ID, TokenHash: sum[:], ExpiresAt: now.Add(s.maxAge),
	})
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	s.logger.InfoContext(ctx, "admin login", slog.String("admin_user_id", user.ID.String()))
	return Session{Token: token, ExpiresAt: row.ExpiresAt, UserID: user.ID}, nil
}

func lockRemaining(until *time.Time, now time.Time) time.Duration {
	if until == nil {
		return time.Minute
	}
	return max(until.Sub(now), time.Second)
}

// tokenHash returns the stored hash of a well-formed token.
func tokenHash(token string) ([]byte, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenBytes || base64.RawURLEncoding.EncodeToString(raw) != token {
		return nil, false
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], true
}

// Authenticate returns the administrator of a live session and slides its
// idle timeout. Every failure is ErrUnauthenticated (or an internal error).
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	h, ok := tokenHash(token)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	q := store.New(s.pool)
	row, err := q.GetAdminSession(ctx, store.GetAdminSessionParams{TokenHash: h, IdleSeconds: s.idle.Seconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("get admin session: %w", err)
	}
	if err := q.TouchAdminSession(ctx, row.ID); err != nil {
		return Principal{}, fmt.Errorf("touch admin session: %w", err)
	}
	return Principal{UserID: row.AdminUserID, Username: row.Username, SessionExpiresAt: row.ExpiresAt}, nil
}

// Logout deletes the session of token (a no-op for unknown tokens).
func (s *Service) Logout(ctx context.Context, token string) error {
	h, ok := tokenHash(token)
	if !ok {
		return ErrUnauthenticated
	}
	n, err := store.New(s.pool).DeleteAdminSession(ctx, h)
	if err != nil {
		return fmt.Errorf("delete admin session: %w", err)
	}
	if n == 0 {
		return ErrUnauthenticated
	}
	s.logger.InfoContext(ctx, "admin logout")
	return nil
}
