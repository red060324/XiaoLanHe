package entry

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/catalog/entity"
	catalog "github.com/red060324/XiaoLanHe/internal/catalog/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const maxCatalogBody = 64 << 10

type HTTP struct {
	service *catalog.Service
	auth    httpauth.Authenticator
	origin  string
}

type gameRequest struct {
	Slug        string           `json:"slug"`
	Name        string           `json:"name"`
	Summary     string           `json:"summary"`
	Description string           `json:"description"`
	Developer   string           `json:"developer"`
	Publisher   string           `json:"publisher"`
	ReleaseDate string           `json:"releaseDate"`
	CoverURL    string           `json:"coverUrl"`
	Editions    []editionRequest `json:"editions"`
}

type editionRequest struct {
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Prices      []priceRequest `json:"prices"`
}

type priceRequest struct {
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
	Region      string `json:"region"`
}

type gameResponse struct {
	ID          string            `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Summary     string            `json:"summary"`
	Description string            `json:"description,omitempty"`
	Developer   string            `json:"developer,omitempty"`
	Publisher   string            `json:"publisher,omitempty"`
	ReleaseDate string            `json:"releaseDate,omitempty"`
	CoverURL    string            `json:"coverUrl,omitempty"`
	Owned       bool              `json:"owned"`
	Editions    []editionResponse `json:"editions,omitempty"`
}

type editionResponse struct {
	ID          string         `json:"id"`
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Price       *priceResponse `json:"price,omitempty"`
}

type priceResponse struct {
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
	Region      string `json:"region"`
}

func NewHTTP(service *catalog.Service, authenticator httpauth.Authenticator, publicOrigin string) *HTTP {
	return &HTTP{service: service, auth: authenticator, origin: publicOrigin}
}

func (h *HTTP) Register(router *server.Hertz) {
	games := router.Group("/api/games", httpauth.Optional(h.auth))
	games.GET("", h.list)
	games.GET("/:slug", h.get)
	admin := router.Group("/api/admin/games", httpauth.RequireOrigin(h.origin), httpauth.RequireRole(h.auth, auth.RoleAdmin))
	admin.POST("", h.create)
	admin.PUT("/:id", h.update)
}

func (h *HTTP) list(ctx context.Context, c *app.RequestContext) {
	limit := 0
	if raw := string(c.Query("limit")); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			h.writeError(c, catalog.ErrInvalidInput)
			return
		}
	}
	principal, _ := httpauth.Principal(c)
	result, err := h.service.List(ctx, catalog.ListInput{
		Query: string(c.Query("query")), Cursor: string(c.Query("cursor")), Limit: limit,
		Region: string(c.Query("region")), Currency: string(c.Query("currency")), ViewerID: principal.UserID,
	})
	if err != nil {
		h.writeError(c, err)
		return
	}
	items := make([]gameResponse, len(result.Items))
	for i := range result.Items {
		items[i] = presentGame(result.Items[i], false)
	}
	c.JSON(consts.StatusOK, struct {
		Items      []gameResponse `json:"items"`
		NextCursor string         `json:"nextCursor,omitempty"`
	}{Items: items, NextCursor: result.NextCursor})
}

func (h *HTTP) get(ctx context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	game, err := h.service.Get(ctx, c.Param("slug"), string(c.Query("region")), string(c.Query("currency")), principal.UserID)
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"game": presentGame(game, true)})
}

func (h *HTTP) create(ctx context.Context, c *app.RequestContext) {
	h.save(ctx, c, 0, consts.StatusCreated)
}

func (h *HTTP) update(ctx context.Context, c *app.RequestContext) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.writeError(c, catalog.ErrNotFound)
		return
	}
	h.save(ctx, c, id, consts.StatusOK)
}

func (h *HTTP) save(ctx context.Context, c *app.RequestContext, id int64, status int) {
	var request gameRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxCatalogBody, &request); err != nil {
		h.writeError(c, catalog.ErrInvalidInput)
		return
	}
	draft, err := request.draft()
	if err != nil {
		h.writeError(c, err)
		return
	}
	principal, _ := httpauth.Principal(c)
	var game entity.Game
	if id == 0 {
		game, err = h.service.Create(ctx, principal, draft)
	} else {
		game, err = h.service.Update(ctx, principal, id, draft)
	}
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(status, map[string]any{"game": presentGame(game, true)})
}

func (r gameRequest) draft() (entity.Draft, error) {
	draft := entity.Draft{Slug: r.Slug, Name: r.Name, Summary: r.Summary, Description: r.Description, Developer: r.Developer, Publisher: r.Publisher, CoverURL: r.CoverURL}
	if r.ReleaseDate != "" {
		date, err := time.Parse("2006-01-02", r.ReleaseDate)
		if err != nil {
			return entity.Draft{}, catalog.ErrInvalidInput
		}
		draft.ReleaseDate = &date
	}
	draft.Editions = make([]entity.EditionDraft, len(r.Editions))
	for i, edition := range r.Editions {
		prices := make([]entity.Price, len(edition.Prices))
		for j, price := range edition.Prices {
			prices[j] = entity.Price{AmountMinor: price.AmountMinor, Currency: price.Currency, Region: price.Region}
		}
		draft.Editions[i] = entity.EditionDraft{Code: edition.Code, Name: edition.Name, Description: edition.Description, Prices: prices}
	}
	return draft, nil
}

func presentGame(game entity.Game, detail bool) gameResponse {
	result := gameResponse{ID: strconv.FormatInt(game.ID, 10), Slug: game.Slug, Name: game.Name, Summary: game.Summary, CoverURL: game.CoverURL, Owned: game.Owned}
	if !detail {
		return result
	}
	result.Description, result.Developer, result.Publisher = game.Description, game.Developer, game.Publisher
	if game.ReleaseDate != nil {
		result.ReleaseDate = game.ReleaseDate.Format("2006-01-02")
	}
	result.Editions = make([]editionResponse, len(game.Editions))
	for i, edition := range game.Editions {
		item := editionResponse{ID: strconv.FormatInt(edition.ID, 10), Code: edition.Code, Name: edition.Name, Description: edition.Description}
		if len(edition.Prices) > 0 {
			price := edition.Prices[0]
			item.Price = &priceResponse{AmountMinor: price.AmountMinor, Currency: price.Currency, Region: price.Region}
		}
		result.Editions[i] = item
	}
	return result
}

func (h *HTTP) writeError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, catalog.ErrInvalidInput):
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, catalog.ErrForbidden):
		httpx.WriteError(c, consts.StatusForbidden, "forbidden", "permission denied", nil)
	case errors.Is(err, catalog.ErrNotFound):
		httpx.WriteError(c, consts.StatusNotFound, "not_found", "game not found", nil)
	case errors.Is(err, catalog.ErrConflict):
		httpx.WriteError(c, consts.StatusConflict, "conflict", "game conflicts with existing data", nil)
	default:
		httpx.WriteError(c, consts.StatusInternalServerError, "internal_error", "request failed", nil)
	}
}
