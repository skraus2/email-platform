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
	dummy     string
	// afterVerify runs between password check and session creation (tests).
	afterVerify func()
}

// NewService builds a Service whose sessions end after idle without use or
// maxAge after login.
func NewService(pool *pgxpool.Pool, logger *slog.Logger, idle, maxAge time.Duration) *Service {
	s := &Service{
		pool: pool, logger: logger, idle: idle, maxAge: maxAge,
		hashSlots: make(chan struct{}, defaultHashSlots), hashWait: defaultHashWait,
		unknown: newThrottle(unknownNames),
	}
	s.setParams(DefaultParams)
	return s
}

// setParams sets the hashing cost and builds the dummy hash with it, up
// front, so the first unknown-name login costs the same as later ones.
func (s *Service) setParams(p Params) {
	s.params = p
	h, err := HashPassword("not a real password: "+uuid.NewString(), p)
	if err != nil {
		panic(fmt.Sprintf("dummy hash: %v", err)) // crypto/rand failure
	}
	s.dummy = h
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
	return userFromRow(row, false), nil
}

func userFromRow(r store.AdminUser, locked bool) User {
	return User{
		ID: r.ID, Username: r.Username, Disabled: r.DisabledAt != nil,
		Locked: locked, CreatedAt: r.CreatedAt, PasswordSetAt: r.PasswordChangedAt,
	}
}

// ListUsers returns all accounts by name.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := store.New(s.pool).ListAdminUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list admin users: %w", err)
	}
	out := make([]User, 0, len(rows))
	for _, r := range rows {
		out = append(out, userFromRow(r.AdminUser, r.Locked))
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
	q := store.New(s.pool)
	// Names that cannot exist get the same answer and cost, without lookup
	// or throttle entry (so they cannot fill the throttle).
	valid := ValidateUsername(username) == nil && len(password) <= 4*MaxPasswordLength
	var user *store.AdminUser
	if valid {
		row, err := q.GetAdminUserByUsername(ctx, username)
		switch {
		case err == nil:
			user = &row
		case !errors.Is(err, pgx.ErrNoRows):
			return Session{}, fmt.Errorf("get admin user: %w", err)
		}
	}

	wait := time.NewTimer(s.hashWait)
	defer wait.Stop()
	select {
	case s.hashSlots <- struct{}{}:
		defer func() { <-s.hashSlots }()
	case <-wait.C:
		return Session{}, ErrBusy
	case <-ctx.Done():
		return Session{}, ctx.Err()
	}

	// The attempt counts as a failure before any hashing, in the same step
	// as the lock check, so a burst of parallel guesses cannot pass the
	// check before the first failures are recorded. A success resets it.
	switch {
	case user != nil:
		if _, err := q.ClaimAdminLoginAttempt(ctx, user.ID); errors.Is(err, pgx.ErrNoRows) {
			f, err := q.RecordAdminLoginFailure(ctx, user.ID) // attempts during a lock extend it
			if err != nil {
				return Session{}, fmt.Errorf("record login failure: %w", err)
			}
			s.logger.WarnContext(ctx, "admin login refused: locked", slog.String("admin_user_id", user.ID.String()))
			return Session{}, &LockedError{RetryAfter: time.Duration(max(f.LockSeconds, 1)) * time.Second}
		} else if err != nil {
			return Session{}, fmt.Errorf("claim login attempt: %w", err)
		}
	case valid:
		if d := s.unknown.claim(username, time.Now()); d > 0 {
			s.logger.WarnContext(ctx, "admin login refused: locked")
			// Whole seconds, like the database path, so the value does not
			// tell unknown names from real ones.
			return Session{}, &LockedError{RetryAfter: (d + time.Second - 1).Truncate(time.Second)}
		}
	}

	encoded := s.dummy
	if user != nil && user.DisabledAt == nil {
		encoded = user.PasswordHash
	}
	ok, outdated, err := s.verify(encoded, password)
	if err != nil {
		return Session{}, fmt.Errorf("verify password: %w", err)
	}
	if user == nil || user.DisabledAt != nil || !ok {
		if user != nil {
			s.logger.WarnContext(ctx, "admin login failed", slog.String("admin_user_id", user.ID.String()))
		} else {
			s.logger.WarnContext(ctx, "admin login failed")
		}
		return Session{}, ErrInvalidCredentials
	}

	// An outdated hash is replaced (still inside the hash slot, before any
	// row lock is held).
	var rehashed string
	if outdated {
		if h, err := s.hash(password); err == nil {
			rehashed = h
		}
	}
	if s.afterVerify != nil {
		s.afterVerify()
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Session{}, fmt.Errorf("session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	var row store.AdminSession
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tq := store.New(tx)
		// The account must still be in the state the password was checked
		// against; a concurrent password change or disable waits here or
		// has already made this fail.
		if _, err := tq.LockAdminUserForLogin(ctx, store.LockAdminUserForLoginParams{
			ID: user.ID, PasswordChangedAt: user.PasswordChangedAt,
		}); err != nil {
			return err
		}
		if err := tq.RecordAdminLoginSuccess(ctx, user.ID); err != nil {
			return fmt.Errorf("record login success: %w", err)
		}
		if rehashed != "" {
			if err := tq.RehashAdminPassword(ctx, store.RehashAdminPasswordParams{ID: user.ID, PasswordHash: rehashed, OldHash: user.PasswordHash}); err != nil {
				return fmt.Errorf("rehash password: %w", err)
			}
		}
		var err error
		row, err = tq.InsertAdminSession(ctx, store.InsertAdminSessionParams{
			AdminUserID: user.ID, TokenHash: sum[:], MaxAgeSeconds: s.maxAge.Seconds(),
		})
		if err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		s.logger.WarnContext(ctx, "admin login failed: account changed during login", slog.String("admin_user_id", user.ID.String()))
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	s.logger.InfoContext(ctx, "admin login", slog.String("admin_user_id", user.ID.String()))
	return Session{Token: token, ExpiresAt: row.ExpiresAt, UserID: user.ID}, nil
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

// Logout deletes the session of token; unknown or malformed tokens are
// ErrUnauthenticated.
func (s *Service) Logout(ctx context.Context, token string) error {
	h, ok := tokenHash(token)
	if !ok {
		return ErrUnauthenticated
	}
	userID, err := store.New(s.pool).DeleteAdminSession(ctx, h)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("delete admin session: %w", err)
	}
	s.logger.InfoContext(ctx, "admin logout", slog.String("admin_user_id", userID.String()))
	return nil
}
