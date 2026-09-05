package entry

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashpresenter "github.com/red060324/XiaoLanHe/internal/flashsale/presenter"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const maxFlashSaleBody = 4096

type HTTP struct {
	service *flashsale.Service
	auth    httpauth.Authenticator
	origin  string
}

func NewHTTP(service *flashsale.Service, authenticator httpauth.Authenticator, publicOrigin string) *HTTP {
	return &HTTP{service: service, auth: authenticator, origin: publicOrigin}
}

func (h *HTTP) Register(router *server.Hertz) {
	public := router.Group("/api/flash-sales", httpauth.Optional(h.auth))
	public.GET("", h.list)
	public.GET("/:activityId", h.get)
	public.POST("/:activityId/reservations", httpauth.RequireOrigin(h.origin), httpauth.Require(h.auth), h.reserve)
	router.GET("/api/flash-sale-requests/:requestId", httpauth.Require(h.auth), h.getRequest)
	admin := router.Group("/api/admin/flash-sales", httpauth.RequireOrigin(h.origin), httpauth.RequireRole(h.auth, auth.RoleAdmin))
	admin.POST("", h.create)
	admin.PUT("/:activityId", h.update)
	admin.POST("/:activityId/activate", h.activate)
	admin.POST("/:activityId/cancel", h.cancel)
}

func (h *HTTP) list(ctx context.Context, c *app.RequestContext) {
	limit, err := optionalLimit(string(c.Query("limit")))
	if err != nil {
		h.writeError(ctx, c, "list_flash_sales", err)
		return
	}
	items, next, err := h.service.ListActivities(ctx, string(c.Query("cursor")), limit)
	if err != nil {
		h.writeError(ctx, c, "list_flash_sales", err)
		return
	}
	now := time.Now().UTC()
	responses := make([]flashpresenter.ActivityResponse, len(items))
	for i := range items {
		responses[i] = flashpresenter.PresentActivity(items[i], false, now)
	}
	c.JSON(consts.StatusOK, map[string]any{"items": responses, "nextCursor": next})
}

func (h *HTTP) get(ctx context.Context, c *app.RequestContext) {
	id, err := activityID(c)
	if err != nil {
		h.writeError(ctx, c, "get_flash_sale", flashsale.ErrNotFound)
		return
	}
	activity, err := h.service.GetActivity(ctx, id)
	if err != nil {
		h.writeError(ctx, c, "get_flash_sale", err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"flashSale": flashpresenter.PresentActivity(activity, false, time.Now().UTC())})
}

func (h *HTTP) reserve(ctx context.Context, c *app.RequestContext) {
	if len(c.Request.Body()) != 0 {
		h.writeError(ctx, c, "reserve_flash_sale", flashsale.ErrInvalidInput)
		return
	}
	id, err := activityID(c)
	if err != nil {
		h.writeError(ctx, c, "reserve_flash_sale", flashsale.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	request, err := h.service.Reserve(ctx, principal, id, string(c.Request.Header.Peek("Idempotency-Key")))
	if err != nil {
		h.writeError(ctx, c, "reserve_flash_sale", err)
		return
	}
	status := consts.StatusAccepted
	if request.Replayed {
		status = consts.StatusOK
	}
	c.JSON(status, map[string]any{"request": flashpresenter.PresentRequest(request), "replayed": request.Replayed})
}

func (h *HTTP) getRequest(ctx context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	request, err := h.service.GetRequest(ctx, principal, c.Param("requestId"))
	if err != nil {
		h.writeError(ctx, c, "get_flash_sale_request", err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"request": flashpresenter.PresentRequest(request)})
}

func (h *HTTP) create(ctx context.Context, c *app.RequestContext) {
	h.save(ctx, c, 0, consts.StatusCreated)
}
func (h *HTTP) update(ctx context.Context, c *app.RequestContext) {
	id, err := activityID(c)
	if err != nil {
		h.writeError(ctx, c, "update_flash_sale", flashsale.ErrNotFound)
		return
	}
	h.save(ctx, c, id, consts.StatusOK)
}

func (h *HTTP) save(ctx context.Context, c *app.RequestContext, id int64, status int) {
	var request flashpresenter.ActivityRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxFlashSaleBody, &request); err != nil {
		h.writeError(ctx, c, "save_flash_sale", flashsale.ErrInvalidInput)
		return
	}
	draft, err := request.Activity(id)
	if err != nil {
		h.writeError(ctx, c, "save_flash_sale", err)
		return
	}
	principal, _ := httpauth.Principal(c)
	var value entity.Activity
	if id == 0 {
		value, err = h.service.CreateActivity(ctx, principal, draft)
	} else {
		value, err = h.service.UpdateActivity(ctx, principal, draft)
	}
	if err != nil {
		h.writeError(ctx, c, "save_flash_sale", err)
		return
	}
	c.JSON(status, map[string]any{"flashSale": flashpresenter.PresentActivity(value, true, time.Now().UTC())})
}

func (h *HTTP) activate(ctx context.Context, c *app.RequestContext) { h.lifecycle(ctx, c, true) }
func (h *HTTP) cancel(ctx context.Context, c *app.RequestContext)   { h.lifecycle(ctx, c, false) }

func (h *HTTP) lifecycle(ctx context.Context, c *app.RequestContext, activate bool) {
	if len(c.Request.Body()) != 0 {
		h.writeError(ctx, c, "flash_sale_lifecycle", flashsale.ErrInvalidInput)
		return
	}
	id, err := activityID(c)
	if err != nil {
		h.writeError(ctx, c, "flash_sale_lifecycle", flashsale.ErrNotFound)
		return
	}
	principal, _ := httpauth.Principal(c)
	var value entity.Activity
	if activate {
		value, err = h.service.Activate(ctx, principal, id)
	} else {
		value, err = h.service.Cancel(ctx, principal, id)
	}
	if err != nil {
		h.writeError(ctx, c, "flash_sale_lifecycle", err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"flashSale": flashpresenter.PresentActivity(value, true, time.Now().UTC())})
}

func (h *HTTP) writeError(ctx context.Context, c *app.RequestContext, operation string, err error) {
	if httpx.WriteDeadlineError(c, err) {
		return
	}
	switch {
	case errors.Is(err, flashsale.ErrInvalidInput):
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, flashsale.ErrUnauthenticated):
		httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", nil)
	case errors.Is(err, flashsale.ErrForbidden):
		httpx.WriteError(c, consts.StatusForbidden, "forbidden", "permission denied", nil)
	case errors.Is(err, flashsale.ErrNotFound):
		httpx.WriteError(c, consts.StatusNotFound, "flash_sale_not_found", "flash sale not found", nil)
	case errors.Is(err, flashsale.ErrNotStarted):
		httpx.WriteError(c, consts.StatusConflict, "flash_sale_not_started", "flash sale has not started", nil)
	case errors.Is(err, flashsale.ErrEnded), errors.Is(err, entity.ErrInvalidState):
		httpx.WriteError(c, consts.StatusConflict, "flash_sale_ended", "flash sale is not accepting reservations", nil)
	case errors.Is(err, flashsale.ErrStockExhausted):
		httpx.WriteError(c, consts.StatusConflict, "stock_exhausted", "flash sale stock is exhausted", nil)
	case errors.Is(err, flashsale.ErrAlreadyReserved):
		httpx.WriteError(c, consts.StatusConflict, "already_reserved", "one reservation is allowed per user", nil)
	case errors.Is(err, flashsale.ErrAlreadyOwned):
		httpx.WriteError(c, consts.StatusConflict, "already_owned", "edition is already owned", nil)
	default:
		slog.ErrorContext(ctx, "flash sale dependency failed", "operation", operation, "outcome", "dependency_unavailable")
		httpx.WriteError(c, consts.StatusServiceUnavailable, "flash_sale_unavailable", "flash sale is unavailable", nil)
	}
}

func activityID(c *app.RequestContext) (int64, error) {
	id, err := strconv.ParseInt(c.Param("activityId"), 10, 64)
	if err != nil || id <= 0 {
		return 0, flashsale.ErrInvalidInput
	}
	return id, nil
}

func optionalLimit(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, flashsale.ErrInvalidInput
	}
	return limit, nil
}
