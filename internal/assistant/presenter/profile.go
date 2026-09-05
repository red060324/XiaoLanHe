package presenter

import (
	"time"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
)

type ProfileRequest struct {
	FavoriteGenres     []string `json:"favoriteGenres"`
	PreferredPlatforms []string `json:"preferredPlatforms"`
	DefaultRegion      string   `json:"defaultRegion"`
	PreferredLanguages []string `json:"preferredLanguages"`
	MaxPriceMinor      *int64   `json:"maxPriceMinor"`
	Currency           string   `json:"currency"`
}

func (r ProfileRequest) Entity() entity.Profile {
	return entity.Profile{
		FavoriteGenres: r.FavoriteGenres, PreferredPlatforms: r.PreferredPlatforms,
		DefaultRegion: r.DefaultRegion, PreferredLanguages: r.PreferredLanguages,
		MaxPriceMinor: r.MaxPriceMinor, Currency: r.Currency,
	}
}

type ProfileResponse struct {
	FavoriteGenres     []string `json:"favoriteGenres"`
	PreferredPlatforms []string `json:"preferredPlatforms"`
	DefaultRegion      string   `json:"defaultRegion"`
	PreferredLanguages []string `json:"preferredLanguages"`
	MaxPriceMinor      *int64   `json:"maxPriceMinor,omitempty"`
	Currency           string   `json:"currency,omitempty"`
	UpdatedAt          string   `json:"updatedAt,omitempty"`
}

func PresentProfile(profile entity.Profile) ProfileResponse {
	response := ProfileResponse{
		FavoriteGenres: nonNil(profile.FavoriteGenres), PreferredPlatforms: nonNil(profile.PreferredPlatforms),
		DefaultRegion: profile.DefaultRegion, PreferredLanguages: nonNil(profile.PreferredLanguages),
		MaxPriceMinor: profile.MaxPriceMinor, Currency: profile.Currency,
	}
	if !profile.UpdatedAt.IsZero() {
		response.UpdatedAt = profile.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return response
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
