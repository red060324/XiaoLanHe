package usecase

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

var (
	ErrInvalidInput = errors.New("invalid catalog input")
	ErrNotFound     = errors.New("game not found")
	ErrConflict     = errors.New("catalog conflict")
	ErrForbidden    = errors.New("catalog forbidden")
)

var (
	slugPattern     = regexp.MustCompile(`^[a-z0-9-]{3,64}$`)
	codePattern     = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)
	regionPattern   = regexp.MustCompile(`^[A-Z0-9-]{2,16}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

type Store interface {
	List(context.Context, ListFilter) ([]entity.Game, error)
	FindBySlug(context.Context, string, Pricing, int64) (entity.Game, error)
	Save(context.Context, int64, entity.Draft) (entity.Game, error)
}

type Pricing struct{ Region, Currency string }

type ListFilter struct {
	Query    string
	BeforeID int64
	Limit    int
	Pricing  Pricing
	ViewerID int64
}

type ListInput struct {
	Query, Cursor, Region, Currency string
	Limit                           int
	ViewerID                        int64
}

type ListResult struct {
	Items      []entity.Game
	NextCursor string
}

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) List(ctx context.Context, in ListInput) (ListResult, error) {
	query := strings.TrimSpace(in.Query)
	if utf8.RuneCountInString(query) > 100 {
		return ListResult{}, ErrInvalidInput
	}
	pricing, err := normalizePricing(in.Region, in.Currency)
	if err != nil {
		return ListResult{}, err
	}
	beforeID, err := decodeCursor(in.Cursor)
	if err != nil {
		return ListResult{}, ErrInvalidInput
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return ListResult{}, ErrInvalidInput
	}
	items, err := s.store.List(ctx, ListFilter{Query: query, BeforeID: beforeID, Limit: limit + 1, Pricing: pricing, ViewerID: in.ViewerID})
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		result.NextCursor = encodeCursor(items[limit-1].ID)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, slug, region, currency string, viewerID int64) (entity.Game, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return entity.Game{}, ErrNotFound
	}
	pricing, err := normalizePricing(region, currency)
	if err != nil {
		return entity.Game{}, err
	}
	return s.store.FindBySlug(ctx, slug, pricing, viewerID)
}

func (s *Service) Create(ctx context.Context, principal auth.Principal, draft entity.Draft) (entity.Game, error) {
	if !principal.IsAdmin() {
		return entity.Game{}, ErrForbidden
	}
	if err := validateDraft(&draft); err != nil {
		return entity.Game{}, err
	}
	return s.store.Save(ctx, 0, draft)
}

func (s *Service) Update(ctx context.Context, principal auth.Principal, id int64, draft entity.Draft) (entity.Game, error) {
	if !principal.IsAdmin() {
		return entity.Game{}, ErrForbidden
	}
	if id <= 0 {
		return entity.Game{}, ErrNotFound
	}
	if err := validateDraft(&draft); err != nil {
		return entity.Game{}, err
	}
	return s.store.Save(ctx, id, draft)
}

func validateDraft(draft *entity.Draft) error {
	draft.Slug = strings.ToLower(strings.TrimSpace(draft.Slug))
	draft.Name = strings.TrimSpace(draft.Name)
	draft.Summary = strings.TrimSpace(draft.Summary)
	draft.Description = strings.TrimSpace(draft.Description)
	draft.Developer = strings.TrimSpace(draft.Developer)
	draft.Publisher = strings.TrimSpace(draft.Publisher)
	draft.CoverURL = strings.TrimSpace(draft.CoverURL)
	if !slugPattern.MatchString(draft.Slug) || utf8.RuneCountInString(draft.Name) < 1 || utf8.RuneCountInString(draft.Name) > 160 || utf8.RuneCountInString(draft.Summary) > 500 || utf8.RuneCountInString(draft.Description) > 20000 || utf8.RuneCountInString(draft.Developer) > 160 || utf8.RuneCountInString(draft.Publisher) > 160 {
		return ErrInvalidInput
	}
	if draft.CoverURL != "" {
		parsed, err := url.ParseRequestURI(draft.CoverURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return ErrInvalidInput
		}
	}
	editionCodes := map[string]bool{}
	for i := range draft.Editions {
		edition := &draft.Editions[i]
		edition.Code = strings.ToLower(strings.TrimSpace(edition.Code))
		edition.Name = strings.TrimSpace(edition.Name)
		edition.Description = strings.TrimSpace(edition.Description)
		if !codePattern.MatchString(edition.Code) || editionCodes[edition.Code] || edition.Name == "" || utf8.RuneCountInString(edition.Name) > 160 {
			return ErrInvalidInput
		}
		editionCodes[edition.Code] = true
		priceKeys := map[string]bool{}
		for j := range edition.Prices {
			price := &edition.Prices[j]
			price.Region = strings.ToUpper(strings.TrimSpace(price.Region))
			price.Currency = strings.ToUpper(strings.TrimSpace(price.Currency))
			key := price.Region + "\x00" + price.Currency
			if !regionPattern.MatchString(price.Region) || !currencyPattern.MatchString(price.Currency) || price.AmountMinor < 0 || priceKeys[key] {
				return ErrInvalidInput
			}
			priceKeys[key] = true
		}
	}
	return nil
}

func normalizePricing(region, currency string) (Pricing, error) {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		region = "GLOBAL"
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "USD"
	}
	if !regionPattern.MatchString(region) || !currencyPattern.MatchString(currency) {
		return Pricing{}, ErrInvalidInput
	}
	return Pricing{Region: region, Currency: currency}, nil
}

func decodeCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(string(decoded), 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalidInput
	}
	return id, nil
}

func encodeCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}
