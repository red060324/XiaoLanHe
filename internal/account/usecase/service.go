package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/account/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

var (
	ErrInvalidInput       = errors.New("invalid account input")
	ErrConflict           = errors.New("account conflict")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = auth.ErrUnauthenticated
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

const dummyPasswordHash = "$2a$12$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

type Store interface {
	Register(context.Context, string, string, string, string, time.Time) (entity.User, error)
	FindCredential(context.Context, string) (entity.User, string, error)
	ReplaceSession(context.Context, int64, string, string, time.Time) error
	FindSession(context.Context, string, time.Time) (auth.Principal, error)
	RevokeSession(context.Context, string) error
}

type PasswordHasher interface {
	Hash(string) (string, error)
	Compare(string, string) error
}

type RegisterInput struct {
	Username, DisplayName, Password string
}

type LoginInput struct{ Username, Password, CurrentToken string }

type Session struct {
	User      entity.User
	Token     string
	ExpiresAt time.Time
}

type Service struct {
	store    Store
	hasher   PasswordHasher
	now      func() time.Time
	newToken func() (string, error)
	ttl      time.Duration
}

func NewService(store Store, hasher PasswordHasher, ttl time.Duration) *Service {
	return &Service{store: store, hasher: hasher, now: time.Now, newToken: generateToken, ttl: ttl}
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	username, displayName, err := validateRegistration(in)
	if err != nil {
		return Session{}, err
	}
	passwordHash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return Session{}, err
	}
	token, err := s.newToken()
	if err != nil {
		return Session{}, err
	}
	expiresAt := s.now().Add(s.ttl)
	user, err := s.store.Register(ctx, username, displayName, passwordHash, hashToken(token), expiresAt)
	if err != nil {
		return Session{}, err
	}
	return Session{User: user, Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) Login(ctx context.Context, in LoginInput) (Session, error) {
	username := strings.ToLower(strings.TrimSpace(in.Username))
	if !usernamePattern.MatchString(username) || len(in.Password) < 8 || len(in.Password) > 72 {
		_ = s.hasher.Compare(dummyPasswordHash, in.Password)
		return Session{}, ErrInvalidCredentials
	}
	user, passwordHash, err := s.store.FindCredential(ctx, username)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			_ = s.hasher.Compare(dummyPasswordHash, in.Password)
			return Session{}, ErrInvalidCredentials
		}
		return Session{}, err
	}
	passwordErr := s.hasher.Compare(passwordHash, in.Password)
	if user.Status != "active" || passwordErr != nil {
		return Session{}, ErrInvalidCredentials
	}
	token, err := s.newToken()
	if err != nil {
		return Session{}, err
	}
	expiresAt := s.now().Add(s.ttl)
	currentTokenHash := ""
	if in.CurrentToken != "" {
		currentTokenHash = hashToken(in.CurrentToken)
	}
	if err := s.store.ReplaceSession(ctx, user.ID, currentTokenHash, hashToken(token), expiresAt); err != nil {
		return Session{}, err
	}
	return Session{User: user, Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (auth.Principal, error) {
	if token == "" {
		return auth.Principal{}, ErrUnauthenticated
	}
	principal, err := s.store.FindSession(ctx, hashToken(token), s.now())
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			return auth.Principal{}, ErrUnauthenticated
		}
		return auth.Principal{}, err
	}
	return principal, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.store.RevokeSession(ctx, hashToken(token))
}

func validateRegistration(in RegisterInput) (string, string, error) {
	username := strings.ToLower(strings.TrimSpace(in.Username))
	displayName := strings.TrimSpace(in.DisplayName)
	if !usernamePattern.MatchString(username) || utf8.RuneCountInString(displayName) < 1 || utf8.RuneCountInString(displayName) > 64 || len(in.Password) < 8 || len(in.Password) > 72 {
		return "", "", ErrInvalidInput
	}
	return username, displayName, nil
}

func generateToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
