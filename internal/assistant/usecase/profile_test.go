package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

func TestNormalizeProfile(t *testing.T) {
	price := int64(30_000)
	profile, err := NormalizeProfile(entity.Profile{
		FavoriteGenres: []string{" RPG ", "rpg", "Strategy"}, PreferredPlatforms: []string{" PC "},
		DefaultRegion: " global ", PreferredLanguages: []string{"ZH-cn", "en-us"},
		MaxPriceMinor: &price, Currency: "cny",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profile.FavoriteGenres, []string{"rpg", "strategy"}) || !reflect.DeepEqual(profile.PreferredLanguages, []string{"zh-CN", "en-US"}) || profile.DefaultRegion != "GLOBAL" || profile.Currency != "CNY" {
		t.Fatalf("profile=%+v", profile)
	}
	for name, candidate := range map[string]entity.Profile{
		"price without currency": {MaxPriceMinor: &price},
		"currency without price": {Currency: "CNY"},
		"bad region":             {DefaultRegion: "GLOBAL!"},
		"bad language":           {PreferredLanguages: []string{"zh_CN"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeProfile(candidate); !errors.Is(err, ErrInvalidProfile) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestProfileService(t *testing.T) {
	store := &profileStoreFake{}
	service := NewProfileService(store)
	if _, err := service.Get(context.Background(), auth.Principal{}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("guest err=%v", err)
	}
	principal := auth.Principal{UserID: 7}
	empty, err := service.Get(context.Background(), principal)
	if err != nil || empty.FavoriteGenres == nil || !empty.UpdatedAt.IsZero() {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	stored, err := service.Replace(context.Background(), principal, entity.Profile{FavoriteGenres: []string{"RPG"}})
	if err != nil || stored.FavoriteGenres[0] != "rpg" || store.userID != 7 {
		t.Fatalf("stored=%+v user=%d err=%v", stored, store.userID, err)
	}
	if err := service.Clear(context.Background(), principal); err != nil || !store.cleared {
		t.Fatalf("cleared=%v err=%v", store.cleared, err)
	}
}

type profileStoreFake struct {
	profile entity.Profile
	found   bool
	userID  int64
	cleared bool
}

func (s *profileStoreFake) LoadAssistantProfile(context.Context, int64) (entity.Profile, bool, error) {
	return s.profile, s.found, nil
}
func (s *profileStoreFake) ReplaceAssistantProfile(_ context.Context, userID int64, profile entity.Profile) (entity.Profile, error) {
	s.userID, s.profile, s.found = userID, profile, true
	s.profile.UpdatedAt = time.Unix(1, 0).UTC()
	return s.profile, nil
}
func (s *profileStoreFake) ClearAssistantProfile(context.Context, int64) error {
	s.cleared = true
	return nil
}
