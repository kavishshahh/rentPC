// Package auth implements password hashing (argon2id), opaque server-side
// sessions (cookie) and API keys (bearer), plus the HTTP middleware that
// resolves the current user.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	"github.com/cadencz/rentpc/apps/api/internal/db"
)

const (
	CookieName = "rentpc_session"
	tokenBytes = 32
)

var ErrInvalidCredentials = errors.New("invalid credentials")

// ---- passwords -------------------------------------------------------------

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---- tokens ----------------------------------------------------------------

// NewToken returns a random URL-safe token and its sha256 hex digest, which is
// what gets stored. Tokens themselves are never persisted.
func NewToken() (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---- users & sessions ------------------------------------------------------

type User struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
}

type Service struct {
	DB         *db.DB
	SessionTTL time.Duration
}

func (s *Service) Register(ctx context.Context, email, password, displayName string) (*User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}
	if displayName = strings.TrimSpace(displayName); displayName == "" {
		displayName = strings.Split(email, "@")[0]
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{}
	err = s.DB.QueryRow(ctx,
		`INSERT INTO users(email, password_hash, display_name) VALUES($1,$2,$3)
		 RETURNING id, email, display_name, created_at`,
		email, hash, displayName).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "users_email_key") {
			return nil, errors.New("email already registered")
		}
		return nil, err
	}
	return u, nil
}

func (s *Service) Authenticate(ctx context.Context, email, password string) (*User, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	u := &User{}
	var hash string
	err := s.DB.QueryRow(ctx,
		`SELECT id, email, display_name, created_at, password_hash FROM users WHERE email=$1`, email).
		Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Burn time so missing users and bad passwords look alike.
		VerifyPassword("$argon2id$v=19$m=65536,t=2,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(hash, password) {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// CreateSession returns the raw token to put in the cookie.
func (s *Service) CreateSession(ctx context.Context, userID, ua, ip string) (string, time.Time, error) {
	token, hash, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(s.SessionTTL)
	_, err = s.DB.Exec(ctx, `INSERT INTO sessions(id, user_id, expires_at, user_agent, ip) VALUES($1,$2,$3,$4,$5)`,
		hash, userID, exp, ua, ip)
	return token, exp, err
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, HashToken(token))
	return err
}

func (s *Service) UserBySession(ctx context.Context, token string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(ctx,
		`SELECT u.id, u.email, u.display_name, u.created_at FROM sessions s JOIN users u ON u.id=s.user_id
		 WHERE s.id=$1 AND s.expires_at > now()`, HashToken(token)).
		Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) UserByAPIKey(ctx context.Context, key string) (*User, error) {
	u := &User{}
	err := s.DB.QueryRow(ctx,
		`UPDATE api_keys k SET last_used=now() FROM users u WHERE u.id=k.user_id AND k.key_hash=$1
		 RETURNING u.id, u.email, u.display_name, u.created_at`, HashToken(key)).
		Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// CreateAPIKey returns the raw key (shown once) for programmatic / AI-agent access.
func (s *Service) CreateAPIKey(ctx context.Context, userID, label string) (string, error) {
	key, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO api_keys(user_id, key_hash, label) VALUES($1,$2,$3)`, userID, hash, label)
	return "rpk_" + key, err
}

// ---- middleware --------------------------------------------------------------

type ctxKey struct{}

// CurrentUser returns the authenticated user, if any.
func CurrentUser(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// Middleware resolves a user from the session cookie or an API key bearer
// token and stores it in the context. It never rejects; use Require for that.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u *User
		if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer rpk_") {
			u, _ = s.UserByAPIKey(r.Context(), strings.TrimPrefix(ah, "Bearer rpk_"))
		} else if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
			u, _ = s.UserBySession(r.Context(), c.Value)
		}
		if u != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
		}
		next.ServeHTTP(w, r)
	})
}

// Require rejects unauthenticated requests with 401.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if CurrentUser(r.Context()) == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authentication required"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
