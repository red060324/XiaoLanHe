package entry

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

func TestHTTP(t *testing.T) {
	store := &httpStore{game: entity.Game{ID: 10, Slug: "example-game", Name: "Example Game", Summary: "Summary", Editions: []entity.Edition{{ID: 20, Code: "standard", Name: "Standard", Prices: []entity.Price{{AmountMinor: 5999, Currency: "USD", Region: "GLOBAL"}}}}}}
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(catalog.NewService(store), httpAuthenticator{}, "https://play.example").Register(router)

	list := ut.PerformRequest(router.Engine, "GET", "/api/games", nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"slug":"example-game"`) {
		t.Fatalf("status=%d body=%s", list.Code, list.Body.String())
	}
	detail := ut.PerformRequest(router.Engine, "GET", "/api/games/example-game", nil)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), `"amountMinor":5999`) {
		t.Fatalf("status=%d body=%s", detail.Code, detail.Body.String())
	}

	body := &ut.Body{Body: bytes.NewBufferString(`{"slug":"new-game","name":"New Game","editions":[]}`), Len: -1}
	anonymous := ut.PerformRequest(router.Engine, "POST", "/api/admin/games", body)
	if anonymous.Code != 401 {
		t.Fatalf("anonymous status=%d body=%s", anonymous.Code, anonymous.Body.String())
	}
	body = &ut.Body{Body: bytes.NewBufferString(`{"slug":"new-game","name":"New Game","editions":[]}`), Len: -1}
	created := ut.PerformRequest(router.Engine, "POST", "/api/admin/games", body, ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"}, ut.Header{Key: "Origin", Value: "https://play.example"})
	if created.Code != 201 || !store.saved {
		t.Fatalf("status=%d body=%s saved=%v", created.Code, created.Body.String(), store.saved)
	}
}

type httpStore struct {
	game  entity.Game
	saved bool
}

func (s *httpStore) List(context.Context, catalog.ListFilter) ([]entity.Game, error) {
	return []entity.Game{s.game}, nil
}
func (s *httpStore) FindBySlug(context.Context, string, catalog.Pricing, int64) (entity.Game, error) {
	return s.game, nil
}
func (s *httpStore) Exists(context.Context, int64) (bool, error) { return true, nil }
func (s *httpStore) Save(_ context.Context, _ int64, draft entity.Draft) (entity.Game, error) {
	s.saved = true
	game := s.game
	game.Slug, game.Name = draft.Slug, draft.Name
	return game, nil
}

type httpAuthenticator struct{}

func (httpAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if token != "admin" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return auth.Principal{UserID: 1, Role: auth.RoleAdmin}, nil
}
