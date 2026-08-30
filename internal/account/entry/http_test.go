package entry

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/account/entity"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

func TestHTTP(t *testing.T) {
	store := &httpStore{user: entity.User{ID: 42, Username: "player_one", DisplayName: "Player One", Role: auth.RoleUser, Status: "active"}}
	service := account.NewService(store, httpHasher{}, 7*24*time.Hour)
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(service, false, "https://play.example").Register(router)

	register := ut.PerformRequest(router.Engine, "POST", "/api/auth/register", &ut.Body{Body: bytes.NewBufferString(`{"username":"player_one","displayName":"Player One","password":"password1"}`), Len: -1}, ut.Header{Key: "Origin", Value: "https://play.example"})
	cookie := string(register.Header().Peek("Set-Cookie"))
	if register.Code != 201 || !strings.Contains(register.Body.String(), `"displayName":"Player One"`) || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") || !strings.Contains(strings.ToLower(cookie), "max-age=604800") {
		t.Fatalf("status=%d body=%s cookie=%s", register.Code, register.Body.String(), cookie)
	}

	unknown := ut.PerformRequest(router.Engine, "POST", "/api/auth/register", &ut.Body{Body: bytes.NewBufferString(`{"username":"player_one","displayName":"Player One","password":"password1","admin":true}`), Len: -1})
	if unknown.Code != 400 || !strings.Contains(unknown.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	store.principal = auth.Principal{UserID: 42, Username: "player_one", DisplayName: "Player One", Role: auth.RoleUser}
	me := ut.PerformRequest(router.Engine, "GET", "/api/me", nil, ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=session"})
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"displayName":"Player One"`) {
		t.Fatalf("status=%d body=%s", me.Code, me.Body.String())
	}
}

type httpHasher struct{}

func (httpHasher) Hash(value string) (string, error) { return "hash:" + value, nil }
func (httpHasher) Compare(hash, value string) error {
	if hash != "hash:"+value {
		return errors.New("mismatch")
	}
	return nil
}

type httpStore struct {
	user      entity.User
	principal auth.Principal
}

func (s *httpStore) Register(context.Context, string, string, string, string, time.Time) (entity.User, error) {
	return s.user, nil
}
func (s *httpStore) FindCredential(context.Context, string) (entity.User, string, error) {
	return s.user, "hash:password1", nil
}
func (*httpStore) ReplaceSession(context.Context, int64, string, string, time.Time) error { return nil }
func (s *httpStore) FindSession(context.Context, string, time.Time) (auth.Principal, error) {
	if s.principal.UserID == 0 {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return s.principal, nil
}
func (*httpStore) RevokeSession(context.Context, string) error { return nil }
