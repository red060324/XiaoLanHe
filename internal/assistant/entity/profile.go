package entity

import "time"

// Profile is the explicit, user-managed preference projection available to the
// read-only Assistant. Entitlements and purchase history are deliberately not
// represented here.
type Profile struct {
	FavoriteGenres     []string
	PreferredPlatforms []string
	DefaultRegion      string
	PreferredLanguages []string
	MaxPriceMinor      *int64
	Currency           string
	UpdatedAt          time.Time
}

func EmptyProfile() Profile {
	return Profile{
		FavoriteGenres:     []string{},
		PreferredPlatforms: []string{},
		PreferredLanguages: []string{},
	}
}
