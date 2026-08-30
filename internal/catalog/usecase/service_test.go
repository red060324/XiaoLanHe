package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

func TestServiceList(t *testing.T) {
	store := &catalogStore{items: []entity.Game{{ID: 3}, {ID: 2}, {ID: 1}}}
	result, err := NewService(store).List(context.Background(), ListInput{Query: " game ", Limit: 2, Region: "cn", Currency: "cny", ViewerID: 9})
	if err != nil || len(result.Items) != 2 || result.NextCursor == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if store.filter.Query != "game" || store.filter.Limit != 3 || store.filter.Pricing != (Pricing{Region: "CN", Currency: "CNY"}) || store.filter.ViewerID != 9 {
		t.Fatalf("filter = %#v", store.filter)
	}
	store.items = nil
	if _, err := NewService(store).List(context.Background(), ListInput{Cursor: "bad***"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor err = %v", err)
	}
	if _, err := NewService(store).List(context.Background(), ListInput{Limit: 51}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("limit err = %v", err)
	}
}

func TestServiceGet(t *testing.T) {
	store := &catalogStore{game: entity.Game{ID: 4, Slug: "example-game"}}
	game, err := NewService(store).Get(context.Background(), " Example-Game ", "", "", 8)
	if err != nil || game.ID != 4 || store.slug != "example-game" || store.pricing != (Pricing{Region: "GLOBAL", Currency: "USD"}) || store.viewerID != 8 {
		t.Fatalf("game=%#v slug=%q pricing=%#v viewer=%d err=%v", game, store.slug, store.pricing, store.viewerID, err)
	}
	if _, err := NewService(store).Get(context.Background(), "?", "", "", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestServiceGameExists(t *testing.T) {
	dependencyErr := errors.New("catalog unavailable")
	store := &catalogStore{exists: true, existsErr: dependencyErr}
	service := NewService(store)
	if exists, err := service.GameExists(context.Background(), 0); err != nil || exists || store.existsCalls != 0 {
		t.Fatalf("exists=%v calls=%d err=%v", exists, store.existsCalls, err)
	}
	if exists, err := service.GameExists(context.Background(), 9); !errors.Is(err, dependencyErr) || !exists || store.existsCalls != 1 || store.existsID != 9 {
		t.Fatalf("exists=%v id=%d calls=%d err=%v", exists, store.existsID, store.existsCalls, err)
	}
}

func TestServiceCreate(t *testing.T) {
	store := &catalogStore{game: entity.Game{ID: 5}}
	service := NewService(store)
	draft := entity.Draft{Slug: " Example-Game ", Name: " Example ", CoverURL: "https://example.invalid/cover.jpg", Editions: []entity.EditionDraft{{Code: " Standard ", Name: " Standard ", Prices: []entity.Price{{Region: "global", Currency: "usd", AmountMinor: 5999}}}}}
	if _, err := service.Create(context.Background(), auth.Principal{Role: auth.RoleUser}, draft); !errors.Is(err, ErrForbidden) || store.saved {
		t.Fatalf("user create err=%v saved=%v", err, store.saved)
	}
	game, err := service.Create(context.Background(), auth.Principal{Role: auth.RoleAdmin}, draft)
	if err != nil || game.ID != 5 || store.draft.Slug != "example-game" || store.draft.Editions[0].Code != "standard" || store.draft.Editions[0].Prices[0].Currency != "USD" {
		t.Fatalf("game=%#v draft=%#v err=%v", game, store.draft, err)
	}

	tooLong := draft
	tooLong.Developer = strings.Repeat("界", 161)
	if _, err := service.Create(context.Background(), auth.Principal{Role: auth.RoleAdmin}, tooLong); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("developer longer than database limit err=%v", err)
	}
}

func TestServiceUpdate(t *testing.T) {
	store := &catalogStore{game: entity.Game{ID: 5}}
	service := NewService(store)
	if _, err := service.Update(context.Background(), auth.Principal{Role: auth.RoleAdmin}, 0, entity.Draft{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

type catalogStore struct {
	items        []entity.Game
	game         entity.Game
	filter       ListFilter
	slug         string
	pricing      Pricing
	viewerID, id int64
	draft        entity.Draft
	saved        bool
	exists       bool
	existsErr    error
	existsCalls  int
	existsID     int64
}

func (s *catalogStore) List(_ context.Context, filter ListFilter) ([]entity.Game, error) {
	s.filter = filter
	return s.items, nil
}
func (s *catalogStore) FindBySlug(_ context.Context, slug string, pricing Pricing, viewerID int64) (entity.Game, error) {
	s.slug, s.pricing, s.viewerID = slug, pricing, viewerID
	return s.game, nil
}
func (s *catalogStore) Exists(_ context.Context, id int64) (bool, error) {
	s.existsCalls++
	s.existsID = id
	return s.exists, s.existsErr
}
func (s *catalogStore) Save(_ context.Context, id int64, draft entity.Draft) (entity.Game, error) {
	s.id, s.draft, s.saved = id, draft, true
	return s.game, nil
}
