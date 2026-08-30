package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/account/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

func TestServiceRegister(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	store := &accountStore{user: entity.User{ID: 7, Username: "player_one", DisplayName: "Player One", Role: auth.RoleUser, Status: "active"}}
	service := NewService(store, accountHasher{}, 7*24*time.Hour)
	service.now = func() time.Time { return now }
	service.newToken = func() (string, error) { return "raw-token", nil }

	session, err := service.Register(context.Background(), RegisterInput{Username: " Player_One ", DisplayName: " Player One ", Password: "password1"})
	if err != nil {
		t.Fatal(err)
	}
	if session.Token != "raw-token" || !session.ExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("session = %#v", session)
	}
	if store.username != "player_one" || store.displayName != "Player One" || store.passwordHash != "hash:password1" || store.tokenHash == "raw-token" || len(store.tokenHash) != 64 {
		t.Fatalf("stored username=%q display=%q password=%q token=%q", store.username, store.displayName, store.passwordHash, store.tokenHash)
	}

	for _, input := range []RegisterInput{
		{Username: "x", DisplayName: "Player", Password: "password1"},
		{Username: "player", DisplayName: " ", Password: "password1"},
		{Username: "player", DisplayName: "Player", Password: "short"},
	} {
		if _, err := service.Register(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("input=%#v err=%v", input, err)
		}
	}
}

func TestServiceLogin(t *testing.T) {
	store := &accountStore{user: entity.User{ID: 7, Username: "player", Role: auth.RoleUser, Status: "active"}, passwordHash: "hash:password1"}
	service := NewService(store, accountHasher{}, time.Hour)
	service.newToken = func() (string, error) { return "new-token", nil }

	session, err := service.Login(context.Background(), LoginInput{Username: " PLAYER ", Password: "password1", CurrentToken: "old-token"})
	if err != nil || session.Token != "new-token" || store.createdTokenHash == "new-token" || len(store.createdTokenHash) != 64 || len(store.replacedTokenHash) != 64 {
		t.Fatalf("session=%#v oldHash=%q newHash=%q err=%v", session, store.replacedTokenHash, store.createdTokenHash, err)
	}

	store.credentialErr = ErrInvalidCredentials
	if _, err := service.Login(context.Background(), LoginInput{Username: "unknown", Password: "password1"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
	store.credentialErr = nil
	store.user.Status = "disabled"
	if _, err := service.Login(context.Background(), LoginInput{Username: "player", Password: "password1"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceAuthenticate(t *testing.T) {
	store := &accountStore{principal: auth.Principal{UserID: 7, Username: "player", DisplayName: "Player", Role: auth.RoleUser}}
	service := NewService(store, accountHasher{}, time.Hour)
	if _, err := service.Authenticate(context.Background(), ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v", err)
	}
	principal, err := service.Authenticate(context.Background(), "token")
	if err != nil || principal.UserID != 7 || store.findTokenHash == "token" || len(store.findTokenHash) != 64 {
		t.Fatalf("principal=%#v hash=%q err=%v", principal, store.findTokenHash, err)
	}
}

func TestServiceLogout(t *testing.T) {
	store := &accountStore{}
	service := NewService(store, accountHasher{}, time.Hour)
	if err := service.Logout(context.Background(), ""); err != nil || store.revokedTokenHash != "" {
		t.Fatalf("empty logout hash=%q err=%v", store.revokedTokenHash, err)
	}
	if err := service.Logout(context.Background(), "token"); err != nil || store.revokedTokenHash == "token" || len(store.revokedTokenHash) != 64 {
		t.Fatalf("logout hash=%q err=%v", store.revokedTokenHash, err)
	}
}

type accountHasher struct{}

func (accountHasher) Hash(password string) (string, error) { return "hash:" + password, nil }
func (accountHasher) Compare(hash, password string) error {
	if hash != "hash:"+password {
		return errors.New("mismatch")
	}
	return nil
}

type accountStore struct {
	user                                                                 entity.User
	principal                                                            auth.Principal
	username, displayName, passwordHash, tokenHash                       string
	createdTokenHash, replacedTokenHash, findTokenHash, revokedTokenHash string
	credentialErr                                                        error
}

func (s *accountStore) Register(_ context.Context, username, displayName, passwordHash, tokenHash string, _ time.Time) (entity.User, error) {
	s.username, s.displayName, s.passwordHash, s.tokenHash = username, displayName, passwordHash, tokenHash
	return s.user, nil
}
func (s *accountStore) FindCredential(context.Context, string) (entity.User, string, error) {
	return s.user, s.passwordHash, s.credentialErr
}
func (s *accountStore) ReplaceSession(_ context.Context, _ int64, currentTokenHash, newTokenHash string, _ time.Time) error {
	s.replacedTokenHash, s.createdTokenHash = currentTokenHash, newTokenHash
	return nil
}
func (s *accountStore) FindSession(_ context.Context, tokenHash string, _ time.Time) (auth.Principal, error) {
	s.findTokenHash = tokenHash
	return s.principal, nil
}
func (s *accountStore) RevokeSession(_ context.Context, tokenHash string) error {
	s.revokedTokenHash = tokenHash
	return nil
}
