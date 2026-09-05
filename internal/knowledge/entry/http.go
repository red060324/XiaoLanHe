package entry

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	knowledgepresenter "github.com/red060324/XiaoLanHe/internal/knowledge/presenter"
	knowledge "github.com/red060324/XiaoLanHe/internal/knowledge/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const maxKnowledgeBody = 1 << 20

type HTTP struct {
	service *knowledge.Service
	auth    httpauth.Authenticator
	origin  string
}

func NewHTTP(service *knowledge.Service, authenticator httpauth.Authenticator, publicOrigin string) *HTTP {
	return &HTTP{service: service, auth: authenticator, origin: publicOrigin}
}
func (h *HTTP) Register(router *server.Hertz) {
	read := router.Group("/api", httpauth.RequireRole(h.auth, auth.RoleAdmin))
	read.GET("/knowledge/search", h.search)
	read.GET("/admin/knowledge/tracks/:trackId", h.track)
	read.GET("/admin/knowledge/documents", h.list)
	write := router.Group("/api", httpauth.RequireOrigin(h.origin), httpauth.RequireRole(h.auth, auth.RoleAdmin))
	write.POST("/knowledge/documents", h.create)
	write.DELETE("/admin/knowledge/documents/:documentId", h.delete)
}
func (h *HTTP) create(ctx context.Context, c *app.RequestContext) {
	var request knowledgepresenter.DocumentRequest
	if httpx.DecodeJSON(c.Request.Body(), maxKnowledgeBody, &request) != nil {
		h.writeError(ctx, c, "create", entity.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	value, err := h.service.Create(ctx, principal, request.Draft())
	if err != nil {
		fields := map[string]string(nil)
		if entity.IsManagedSource(value.SourceKey) {
			fields = map[string]string{"sourceKey": value.SourceKey}
		}
		h.writeErrorWithFields(ctx, c, "create", err, fields)
		return
	}
	c.JSON(consts.StatusAccepted, knowledgepresenter.PresentAccepted(value))
}
func (h *HTTP) search(ctx context.Context, c *app.RequestContext) {
	limit, err := knowledgepresenter.PositiveInt(string(c.Query("limit")))
	if err != nil {
		h.writeError(ctx, c, "search", err)
		return
	}
	value, err := h.service.Search(ctx, entity.SearchInput{Query: string(c.Query("query")), Mode: entity.Mode(string(c.Query("mode"))), GameCode: string(c.Query("gameCode")), RegionCode: string(c.Query("regionCode")), Limit: limit})
	if err != nil {
		h.writeError(ctx, c, "search", err)
		return
	}
	c.JSON(consts.StatusOK, knowledgepresenter.PresentSearch(value))
}
func (h *HTTP) track(ctx context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	value, err := h.service.Track(ctx, principal, c.Param("trackId"))
	if err != nil {
		h.writeError(ctx, c, "track", err)
		return
	}
	c.JSON(consts.StatusOK, knowledgepresenter.PresentTrack(value))
}
func (h *HTTP) list(ctx context.Context, c *app.RequestContext) {
	page, err := knowledgepresenter.PositiveInt(string(c.Query("page")))
	if err != nil {
		h.writeError(ctx, c, "list", err)
		return
	}
	pageSize, err := knowledgepresenter.PositiveInt(string(c.Query("pageSize")))
	if err != nil {
		h.writeError(ctx, c, "list", err)
		return
	}
	principal, _ := httpauth.Principal(c)
	value, err := h.service.List(ctx, principal, entity.ListInput{Page: page, PageSize: pageSize, Status: strings.TrimSpace(string(c.Query("status"))), SortField: string(c.Query("sortField")), SortDirection: string(c.Query("sortDirection"))})
	if err != nil {
		h.writeError(ctx, c, "list", err)
		return
	}
	c.JSON(consts.StatusOK, knowledgepresenter.PresentList(value))
}
func (h *HTTP) delete(ctx context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	value, err := h.service.Delete(ctx, principal, c.Param("documentId"))
	if err != nil {
		h.writeError(ctx, c, "delete", err)
		return
	}
	c.JSON(consts.StatusAccepted, map[string]string{"documentId": value.DocumentID, "status": value.Status})
}
func (h *HTTP) writeError(ctx context.Context, c *app.RequestContext, operation string, err error) {
	h.writeErrorWithFields(ctx, c, operation, err, nil)
}

func (h *HTTP) writeErrorWithFields(ctx context.Context, c *app.RequestContext, operation string, err error, fields map[string]string) {
	if errors.Is(err, context.DeadlineExceeded) {
		httpx.WriteError(c, consts.StatusGatewayTimeout, "deadline_exceeded", "request deadline exceeded", fields)
		return
	}
	switch {
	case errors.Is(err, entity.ErrInvalidInput):
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", fields)
	case errors.Is(err, auth.ErrUnauthenticated):
		httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", fields)
	case errors.Is(err, knowledge.ErrForbidden):
		httpx.WriteError(c, consts.StatusForbidden, "forbidden", "permission denied", fields)
	case errors.Is(err, entity.ErrNotFound):
		httpx.WriteError(c, consts.StatusNotFound, "not_found", "knowledge resource not found", fields)
	case errors.Is(err, entity.ErrConflict):
		httpx.WriteError(c, consts.StatusConflict, "knowledge_conflict", "knowledge operation conflicts with current state", fields)
	case errors.Is(err, entity.ErrCapacity):
		httpx.WriteError(c, consts.StatusTooManyRequests, "capacity_exceeded", "knowledge capacity is temporarily exhausted", fields)
	case errors.Is(err, entity.ErrContract):
		httpx.WriteError(c, consts.StatusBadGateway, "dependency_contract_error", "knowledge dependency contract mismatch", fields)
	default:
		slog.ErrorContext(ctx, "knowledge dependency failed", "event", "knowledge.http", "operation", operation, "error_class", knowledgeErrorClass(err))
		httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "knowledge is unavailable", fields)
	}
}

func knowledgeErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, entity.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, entity.ErrNotFound):
		return "not_found"
	case errors.Is(err, entity.ErrConflict):
		return "conflict"
	case errors.Is(err, entity.ErrCapacity):
		return "capacity"
	case errors.Is(err, entity.ErrContract):
		return "contract"
	default:
		return "dependency"
	}
}
