package entry

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
	"github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotionpresenter "github.com/red060324/XiaoLanHe/internal/promotion/presenter"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
)

type HTTP struct {
	service *promotion.Service
	auth    httpauth.Authenticator
	origin  string
}

func NewHTTP(service *promotion.Service, authenticator httpauth.Authenticator, publicOrigin string) *HTTP {
	return &HTTP{service: service, auth: authenticator, origin: publicOrigin}
}

func (h *HTTP) Register(router *server.Hertz) {
	router.GET("/api/deals", httpauth.Optional(h.auth), h.list)
	router.POST("/api/coupons/:code/claims", httpauth.RequireOrigin(h.origin), httpauth.Require(h.auth), h.claim)
}

func (h *HTTP) list(ctx context.Context, c *app.RequestContext) {
	gameID, err := optionalID(string(c.Query("gameId")))
	if err != nil {
		h.writeError(ctx, c, "list_deals", err)
		return
	}
	limit, err := optionalLimit(string(c.Query("limit")))
	if err != nil {
		h.writeError(ctx, c, "list_deals", err)
		return
	}
	principal, _ := httpauth.Principal(c)
	page, err := h.service.List(ctx, promotion.ListInput{Cursor: string(c.Query("cursor")), GameID: gameID, ViewerID: principal.UserID, Limit: limit})
	if err != nil {
		h.writeError(ctx, c, "list_deals", err)
		return
	}
	c.JSON(consts.StatusOK, promotionpresenter.PresentPage(page))
}

func (h *HTTP) claim(ctx context.Context, c *app.RequestContext) {
	if len(c.Request.Body()) != 0 {
		h.writeError(ctx, c, "claim_coupon", promotion.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	result, err := h.service.Claim(ctx, principal, c.Param("code"), string(c.Request.Header.Peek("Idempotency-Key")))
	if err != nil {
		h.writeError(ctx, c, "claim_coupon", err)
		return
	}
	status := consts.StatusCreated
	if result.Replayed {
		status = consts.StatusOK
	}
	c.JSON(status, map[string]any{"claim": promotionpresenter.PresentClaim(result.Claim), "replayed": result.Replayed})
}

func (h *HTTP) writeError(ctx context.Context, c *app.RequestContext, operation string, err error) {
	switch {
	case errors.Is(err, promotion.ErrInvalidInput):
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, promotion.ErrUnauthenticated):
		httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", nil)
	case errors.Is(err, promotion.ErrNotFound):
		httpx.WriteError(c, consts.StatusNotFound, "coupon_not_found", "coupon not found", nil)
	case errors.Is(err, entity.ErrUnavailable):
		httpx.WriteError(c, consts.StatusConflict, "coupon_unavailable", "coupon is unavailable", nil)
	case errors.Is(err, entity.ErrExhausted):
		httpx.WriteError(c, consts.StatusConflict, "coupon_exhausted", "coupon stock is exhausted", nil)
	case errors.Is(err, promotion.ErrClaimLimit):
		httpx.WriteError(c, consts.StatusConflict, "claim_limit_reached", "coupon claim limit reached", nil)
	case errors.Is(err, promotion.ErrIdempotencyConflict):
		httpx.WriteError(c, consts.StatusConflict, "idempotency_conflict", "idempotency key was used for another request", nil)
	default:
		slog.ErrorContext(ctx, "promotion dependency failed", "operation", operation, "error", err)
		httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "promotion is unavailable", nil)
	}
}

func optionalID(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, promotion.ErrInvalidInput
	}
	return id, nil
}

func optionalLimit(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, promotion.ErrInvalidInput
	}
	return limit, nil
}
