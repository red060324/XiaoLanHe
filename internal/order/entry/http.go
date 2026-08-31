package entry

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/order/entity"
	orderpresenter "github.com/red060324/XiaoLanHe/internal/order/presenter"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const maxOrderBody = 4096

type HTTP struct {
	service *order.Service
	auth    httpauth.Authenticator
	origin  string
}

func NewHTTP(service *order.Service, authenticator httpauth.Authenticator, publicOrigin string) *HTTP {
	return &HTTP{service: service, auth: authenticator, origin: publicOrigin}
}

func (h *HTTP) Register(router *server.Hertz) {
	read := router.Group("/api/orders", httpauth.Require(h.auth))
	read.GET("", h.list)
	read.GET("/:orderNo", h.get)
	write := router.Group("/api/orders", httpauth.RequireOrigin(h.origin), httpauth.Require(h.auth))
	write.POST("", h.create)
	write.POST("/:orderNo/payments/sandbox", h.pay)
}

func (h *HTTP) create(ctx context.Context, c *app.RequestContext) {
	var request orderpresenter.CreateRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxOrderBody, &request); err != nil {
		h.writeError(ctx, c, "create_order", order.ErrInvalidInput)
		return
	}
	input, err := request.Input(string(c.Request.Header.Peek("Idempotency-Key")))
	if err != nil {
		h.writeError(ctx, c, "create_order", err)
		return
	}
	principal, _ := httpauth.Principal(c)
	result, err := h.service.Create(ctx, principal, input)
	if err != nil {
		h.writeError(ctx, c, "create_order", err)
		return
	}
	status := consts.StatusCreated
	if result.Replayed {
		status = consts.StatusOK
	}
	c.JSON(status, map[string]any{"order": orderpresenter.PresentOrder(result.Order), "replayed": result.Replayed})
}

func (h *HTTP) list(ctx context.Context, c *app.RequestContext) {
	limit := 0
	if value := string(c.Query("limit")); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil {
			h.writeError(ctx, c, "list_orders", order.ErrInvalidInput)
			return
		}
	}
	principal, _ := httpauth.Principal(c)
	page, err := h.service.List(ctx, principal, order.ListInput{Cursor: string(c.Query("cursor")), Limit: limit})
	if err != nil {
		h.writeError(ctx, c, "list_orders", err)
		return
	}
	c.JSON(consts.StatusOK, orderpresenter.PresentPage(page))
}

func (h *HTTP) get(ctx context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	value, err := h.service.Get(ctx, principal, c.Param("orderNo"))
	if err != nil {
		h.writeError(ctx, c, "get_order", err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"order": orderpresenter.PresentOrder(value)})
}

func (h *HTTP) pay(ctx context.Context, c *app.RequestContext) {
	if len(c.Request.Body()) != 0 {
		h.writeError(ctx, c, "pay_order", order.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	result, err := h.service.Pay(ctx, principal, c.Param("orderNo"), string(c.Request.Header.Peek("Idempotency-Key")))
	if err != nil {
		h.writeError(ctx, c, "pay_order", err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"order": orderpresenter.PresentOrder(result.Order), "replayed": result.Replayed})
}

func (h *HTTP) writeError(ctx context.Context, c *app.RequestContext, operation string, err error) {
	if httpx.WriteDeadlineError(c, err) {
		return
	}
	switch {
	case errors.Is(err, order.ErrInvalidInput):
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, order.ErrUnauthenticated):
		httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", nil)
	case errors.Is(err, order.ErrForbidden):
		httpx.WriteError(c, consts.StatusForbidden, "forbidden", "permission denied", nil)
	case errors.Is(err, order.ErrNotFound):
		httpx.WriteError(c, consts.StatusNotFound, "order_not_found", "order not found", nil)
	case errors.Is(err, order.ErrPriceUnavailable):
		httpx.WriteError(c, consts.StatusConflict, "price_unavailable", "edition price is unavailable", nil)
	case errors.Is(err, order.ErrCouponIneligible):
		httpx.WriteError(c, consts.StatusConflict, "coupon_ineligible", "coupon cannot be used", nil)
	case errors.Is(err, order.ErrAlreadyOwned):
		httpx.WriteError(c, consts.StatusConflict, "already_owned", "edition is already owned", nil)
	case errors.Is(err, order.ErrIdempotencyConflict):
		httpx.WriteError(c, consts.StatusConflict, "idempotency_conflict", "idempotency key was used for another request", nil)
	case errors.Is(err, entity.ErrInvalidState):
		httpx.WriteError(c, consts.StatusConflict, "invalid_order_state", "order state does not allow payment", nil)
	default:
		slog.ErrorContext(ctx, "order dependency failed", "operation", operation, "error", err)
		httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "orders are unavailable", nil)
	}
}
