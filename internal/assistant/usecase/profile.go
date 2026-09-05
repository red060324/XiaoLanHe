package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

var (
	ErrInvalidProfile  = errors.New("invalid assistant profile")
	ErrUnauthenticated = auth.ErrUnauthenticated
)

const maxAssistantProfileBytes = 4 << 10

var (
	languagePattern = regexp.MustCompile(`^[A-Za-z0-9-]{2,16}$`)
	regionPattern   = regexp.MustCompile(`^[A-Z0-9_-]{2,16}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

type ProfileStore interface {
	LoadAssistantProfile(context.Context, int64) (entity.Profile, bool, error)
	ReplaceAssistantProfile(context.Context, int64, entity.Profile) (entity.Profile, error)
	ClearAssistantProfile(context.Context, int64) error
}

type ProfileService struct{ store ProfileStore }

func NewProfileService(store ProfileStore) *ProfileService { return &ProfileService{store: store} }

func (s *ProfileService) Get(ctx context.Context, principal auth.Principal) (entity.Profile, error) {
	if principal.UserID <= 0 {
		return entity.Profile{}, ErrUnauthenticated
	}
	profile, found, err := s.store.LoadAssistantProfile(ctx, principal.UserID)
	if err != nil {
		return entity.Profile{}, err
	}
	if !found {
		return entity.EmptyProfile(), nil
	}
	return profile, nil
}

func (s *ProfileService) Replace(ctx context.Context, principal auth.Principal, profile entity.Profile) (entity.Profile, error) {
	if principal.UserID <= 0 {
		return entity.Profile{}, ErrUnauthenticated
	}
	normalized, err := NormalizeProfile(profile)
	if err != nil {
		return entity.Profile{}, err
	}
	return s.store.ReplaceAssistantProfile(ctx, principal.UserID, normalized)
}

func (s *ProfileService) Clear(ctx context.Context, principal auth.Principal) error {
	if principal.UserID <= 0 {
		return ErrUnauthenticated
	}
	return s.store.ClearAssistantProfile(ctx, principal.UserID)
}

func NormalizeProfile(profile entity.Profile) (entity.Profile, error) {
	var err error
	profile.FavoriteGenres, err = normalizeList(profile.FavoriteGenres, 10, 32, strings.ToLower, nil)
	if err != nil {
		return entity.Profile{}, ErrInvalidProfile
	}
	profile.PreferredPlatforms, err = normalizeList(profile.PreferredPlatforms, 10, 32, strings.ToLower, nil)
	if err != nil {
		return entity.Profile{}, ErrInvalidProfile
	}
	profile.PreferredLanguages, err = normalizeList(profile.PreferredLanguages, 5, 16, canonicalLanguage, languagePattern)
	if err != nil {
		return entity.Profile{}, ErrInvalidProfile
	}
	profile.DefaultRegion = strings.ToUpper(strings.TrimSpace(profile.DefaultRegion))
	profile.Currency = strings.ToUpper(strings.TrimSpace(profile.Currency))
	if profile.DefaultRegion != "" && !regionPattern.MatchString(profile.DefaultRegion) {
		return entity.Profile{}, ErrInvalidProfile
	}
	if (profile.MaxPriceMinor == nil) != (profile.Currency == "") {
		return entity.Profile{}, ErrInvalidProfile
	}
	if profile.MaxPriceMinor != nil && (*profile.MaxPriceMinor < 1 || *profile.MaxPriceMinor > 1_000_000_000 || !currencyPattern.MatchString(profile.Currency)) {
		return entity.Profile{}, ErrInvalidProfile
	}
	profile.UpdatedAt = profile.UpdatedAt.UTC()
	encoded, err := json.Marshal(profileEnvelope{
		FavoriteGenres: profile.FavoriteGenres, PreferredPlatforms: profile.PreferredPlatforms,
		PreferredLanguages: profile.PreferredLanguages, MaxPriceMinor: profile.MaxPriceMinor, Currency: profile.Currency,
	})
	if err != nil || len(encoded) > maxAssistantProfileBytes {
		return entity.Profile{}, ErrInvalidProfile
	}
	return profile, nil
}

type profileEnvelope struct {
	FavoriteGenres     []string `json:"favoriteGenres"`
	PreferredPlatforms []string `json:"preferredPlatforms"`
	PreferredLanguages []string `json:"preferredLanguages"`
	MaxPriceMinor      *int64   `json:"maxPriceMinor,omitempty"`
	Currency           string   `json:"currency,omitempty"`
}

func normalizeList(values []string, maxItems, maxRunes int, canonical func(string) string, pattern *regexp.Regexp) ([]string, error) {
	if len(values) > maxItems {
		return nil, ErrInvalidProfile
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = canonical(strings.TrimSpace(value))
		if utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > maxRunes || pattern != nil && !pattern.MatchString(value) {
			return nil, ErrInvalidProfile
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func canonicalLanguage(value string) string {
	parts := strings.Split(value, "-")
	if len(parts) > 0 {
		parts[0] = strings.ToLower(parts[0])
	}
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) == 2 {
			parts[i] = strings.ToUpper(parts[i])
		} else {
			parts[i] = strings.ToLower(parts[i])
		}
	}
	return strings.Join(parts, "-")
}
