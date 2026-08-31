package entity

import "time"

type Game struct {
	ID          int64
	Slug        string
	Name        string
	Summary     string
	Description string
	Developer   string
	Publisher   string
	ReleaseDate *time.Time
	CoverURL    string
	Owned       bool
	Editions    []Edition
}

type Edition struct {
	ID          int64
	Code        string
	Name        string
	Description string
	Owned       bool
	Prices      []Price
}

type Price struct {
	AmountMinor int64
	Currency    string
	Region      string
}

type PurchaseOffer struct {
	GameID      int64
	GameSlug    string
	GameName    string
	EditionID   int64
	EditionCode string
	EditionName string
	AmountMinor int64
	Currency    string
	Region      string
}

type Draft struct {
	Slug        string
	Name        string
	Summary     string
	Description string
	Developer   string
	Publisher   string
	ReleaseDate *time.Time
	CoverURL    string
	Editions    []EditionDraft
}

type EditionDraft struct {
	Code, Name, Description string
	Prices                  []Price
}
