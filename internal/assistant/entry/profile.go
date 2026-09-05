package entry

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	assistantpresenter "github.com/red060324/XiaoLanHe/internal/assistant/presenter"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const maxProfileBody = 8 << 10

type ProfileHTTP struct {
	service *assistant.ProfileService
	auth    httpauth.Authenticator
	origin  string
}

func NewProfileHTTP(service *assistant.ProfileService, authenticator httpauth.Authenticator, publicOrigin string) *ProfileHTTP {
	return &ProfileHTTP{service: service, auth: authenticator, origin: publicOrigin}
}

func (h *ProfileHTTP) Register(router *server.Hertz) {
	read := router.Group("/api/me", httpauth.Require(h.auth))
	read.GET("/assistant-profile", h.get)
	write := router.Group("/api/me", httpauth.RequireOrigin(h.origin), httpauth.Require(h.auth))
	write.PUT("/assistant-profile", h.replace)
	write.DELETE("/assistant-profile", h.clear)
}

func (h *ProfileHTTP) get(ctx context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	profile, err := h.service.Get(ctx, principal)
	if err != nil {
		h.writeError(ctx, c, "get", err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"profile": assistantpresenter.PresentProfile(profile)})
}

func (h *ProfileHTTP) replace(ctx context.Context, c *app.RequestContext) {
	var request assistantpresenter.ProfileRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxProfileBody, &request); err != nil {
		h.writeError(ctx, c, "replace", assistant.ErrInvalidProfile)
		return
	}
	principal, _ := httpauth.Principal(c)
	profile, err := h.service.Replace(ctx, principal, request.Entity())
	if err != nil {
		h.writeError(ctx, c, "replace", err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"profile": assistantpresenter.PresentProfile(profile)})
}

func (h *ProfileHTTP) clear(ctx context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	if err := h.service.Clear(ctx, principal); err != nil {
		h.writeError(ctx, c, "clear", err)
		return
	}
	c.Status(consts.StatusNoContent)
}

func (h *ProfileHTTP) writeError(ctx context.Context, c *app.RequestContext, operation string, err error) {
	if httpx.WriteDeadlineError(c, err) {
		return
	}
	if errors.Is(err, assistant.ErrInvalidProfile) {
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return
	}
	if errors.Is(err, assistant.ErrUnauthenticated) {
		httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", nil)
		return
	}
	slog.ErrorContext(ctx, "assistant profile dependency failed", "event", "assistant.profile", "operation", operation, "error_class", profileErrorClass(err))
	httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "assistant profile is unavailable", nil)
}

func profileErrorClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, assistant.ErrInvalidProfile):
		return "invalid_input"
	case errors.Is(err, assistant.ErrUnauthenticated):
		return "unauthenticated"
	default:
		return "dependency"
	}
}
