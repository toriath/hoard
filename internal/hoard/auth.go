package hoard

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// User is an account that can sign in. All users have the same rights.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

// APIToken is a long-lived bearer credential for the REST API and MCP.
type APIToken struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"` // first characters, for recognition only
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

const (
	// SessionTTL is the sliding lifetime of a browser session.
	SessionTTL  = 30 * 24 * time.Hour
	tokenPrefix = "hoard_"

	argonTime    = 2
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
)

// ErrBadCredentials is returned for any failed login.
var ErrBadCredentials = errors.New("invalid username or password")

var reUsername = regexp.MustCompile(`^[a-zA-Z0-9._-]{2,40}$`)

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		panic(err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// hashSlots bounds concurrent Argon2 runs (64 MiB each) so that a burst of
// sign-in attempts cannot exhaust the container's memory.
var hashSlots = make(chan struct{}, 4)

func verifyPassword(encoded, pw string) bool {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash keeps login timing uniform for unknown usernames.
var dummyHash = hashPassword("hoard-dummy-password")

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// UserCount returns the number of accounts; zero means first-run setup.
func (s *Service) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser adds an account.
func (s *Service) CreateUser(ctx context.Context, username, password string) (*User, error) {
	username = strings.TrimSpace(username)
	if !reUsername.MatchString(username) {
		return nil, invalid("username must be 2-40 characters: letters, digits, dot, dash or underscore")
	}
	if err := checkPassword(password); err != nil {
		return nil, err
	}
	u := &User{ID: newID("usr"), Username: username, CreatedAt: now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id, username, password_hash, created_at) VALUES(?, ?, ?, ?)`,
		u.ID, u.Username, hashPassword(password), u.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("username %q is taken: %w", username, ErrConflict)
		}
		return nil, err
	}
	return u, nil
}

// SetupFirstUser creates the initial account; it fails once any user exists.
// Check and insert are one statement, so two racing setup requests cannot
// both succeed.
func (s *Service) SetupFirstUser(ctx context.Context, username, password string) (*User, error) {
	username = strings.TrimSpace(username)
	if !reUsername.MatchString(username) {
		return nil, invalid("username must be 2-40 characters: letters, digits, dot, dash or underscore")
	}
	if err := checkPassword(password); err != nil {
		return nil, err
	}
	u := &User{ID: newID("usr"), Username: username, CreatedAt: now()}
	r, err := s.db.ExecContext(ctx, `INSERT INTO users(id, username, password_hash, created_at)
		SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, u.ID, u.Username, hashPassword(password), u.CreatedAt)
	if err != nil {
		return nil, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("setup already completed: %w", ErrConflict)
	}
	return u, nil
}

// MaxPasswordLen bounds the work a single password check can cause.
const MaxPasswordLen = 256

// ValidUsername reports whether s could be an account name at all.
func ValidUsername(s string) bool { return reUsername.MatchString(s) }

func checkPassword(pw string) error {
	if len(pw) < 10 {
		return invalid("password must be at least 10 characters")
	}
	if len(pw) > MaxPasswordLen {
		return invalid("password is too long")
	}
	return nil
}

// ListUsers returns all accounts.
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes an account together with its sessions and tokens. The
// last remaining account cannot be removed.
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n <= 1 {
			return invalid("the last user cannot be deleted")
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if c, _ := r.RowsAffected(); c == 0 {
			return fmt.Errorf("user %q: %w", id, ErrNotFound)
		}
		return nil
	})
}

// ChangePassword verifies the current password, sets a new one and signs the
// user out everywhere else.
func (s *Service) ChangePassword(ctx context.Context, userID, current, next, keepSession string) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		return fmt.Errorf("user: %w", ErrNotFound)
	}
	if !verifyPassword(hash, current) {
		return fmt.Errorf("current password: %w", ErrBadCredentials)
	}
	if err := checkPassword(next); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hashPassword(next), userID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, sha(keepSession))
	return err
}

// Login checks credentials and opens a session, returning its secret token.
func (s *Service) Login(ctx context.Context, username, password string) (string, *User, error) {
	var u User
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT id, username, created_at, password_hash FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.CreatedAt, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		verifyPassword(dummyHash, password)
		return "", nil, ErrBadCredentials
	}
	if err != nil {
		return "", nil, err
	}
	if !verifyPassword(hash, password) {
		return "", nil, ErrBadCredentials
	}
	token, err := s.NewSession(ctx, u.ID)
	return token, &u, err
}

// NewSession opens a session for a user.
func (s *Service) NewSession(ctx context.Context, userID string) (string, error) {
	token := randomString(48)
	ts := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES(?, ?, ?, ?)`,
		sha(token), userID, ts.Format(time.RFC3339), ts.Add(SessionTTL).Format(time.RFC3339))
	return token, err
}

// SessionUser resolves a session token. Sessions slide: each day of use
// extends the expiry.
func (s *Service) SessionUser(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var u User
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.created_at, s.expires_at FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, sha(token)).Scan(&u.ID, &u.Username, &u.CreatedAt, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	exp, _ := time.Parse(time.RFC3339, expires)
	if time.Now().After(exp) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, sha(token))
		return nil, ErrNotFound
	}
	if time.Until(exp) < SessionTTL-24*time.Hour {
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE token_hash = ?`,
			time.Now().UTC().Add(SessionTTL).Format(time.RFC3339), sha(token))
	}
	return &u, nil
}

// Logout ends a session.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, sha(token))
	return err
}

// CleanupSessions removes expired sessions.
func (s *Service) CleanupSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now())
	return err
}

// ---- API tokens ----

// CreateToken issues an API token. The secret is returned once and only its
// hash is stored.
func (s *Service) CreateToken(ctx context.Context, userID, name string) (*APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return nil, "", invalid("token name must be 1-80 characters")
	}
	secret := tokenPrefix + randomString(40)
	t := &APIToken{ID: newID("tok"), Name: name, Prefix: secret[:len(tokenPrefix)+4], CreatedAt: now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_tokens(id, user_id, name, token_hash, prefix, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		t.ID, userID, t.Name, sha(secret), t.Prefix, t.CreatedAt)
	return t, secret, err
}

// ListTokens returns all API tokens (never their secrets).
func (s *Service) ListTokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, prefix, created_at, last_used_at FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		var used sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt, &used); err != nil {
			return nil, err
		}
		if used.Valid {
			t.LastUsedAt = &used.String
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken revokes an API token.
func (s *Service) DeleteToken(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return fmt.Errorf("token %q: %w", id, ErrNotFound)
	}
	return nil
}

// TokenUser resolves a bearer token to its user.
func (s *Service) TokenUser(ctx context.Context, secret string) (*User, error) {
	if !strings.HasPrefix(secret, tokenPrefix) {
		return nil, ErrNotFound
	}
	hash := sha(secret)
	var u User
	var tokenID string
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.created_at, t.id FROM api_tokens t
		JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`, hash).Scan(&u.ID, &u.Username, &u.CreatedAt, &tokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Record usage at most once a minute per token.
	s.tokenMu.Lock()
	last := s.tokenUse[tokenID]
	stale := time.Since(last) > time.Minute
	if stale {
		s.tokenUse[tokenID] = time.Now()
	}
	s.tokenMu.Unlock()
	if stale {
		_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now(), tokenID)
	}
	return &u, nil
}

// ---- signed, expiring links ----
//
// Agents move binary files with plain curl. Signed URLs let them do that
// without ever putting the long-lived API token on a command line.

// Sign returns the expiry and signature query values for a payload.
func (s *Service) Sign(payload string, ttl time.Duration) (exp, sig string) {
	exp = strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	return exp, s.mac(payload, exp)
}

// Verify checks a signature produced by Sign.
func (s *Service) Verify(payload, exp, sig string) bool {
	ts, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > ts {
		return false
	}
	return hmac.Equal([]byte(s.mac(payload, exp)), []byte(sig))
}

func (s *Service) mac(payload, exp string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(payload + "\n" + exp))
	return hex.EncodeToString(m.Sum(nil))[:40]
}
