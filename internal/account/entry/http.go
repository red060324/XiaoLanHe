package entry

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/account/entity"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const maxAuthBody = 4096

type HTTP struct {
	service      *account.Service
	secureCookie bool
	publicOrigin string
}

type credentialsRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

type userResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

func NewHTTP(service *account.Service, secureCookie bool, publicOrigin string) *HTTP {
	return &HTTP{service: service, secureCookie: secureCookie, publicOrigin: publicOrigin}
}

func (h *HTTP) Register(router *server.Hertz) {
	origin := httpauth.RequireOrigin(h.publicOrigin)
	router.POST("/api/auth/register", origin, h.register)
	router.POST("/api/auth/login", origin, h.login)
	router.POST("/api/auth/logout", origin, h.logout)
	router.GET("/api/me", httpauth.Require(h.service), h.me)
}

func (h *HTTP) register(ctx context.Context, c *app.RequestContext) {
	var request credentialsRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxAuthBody, &request); err != nil {
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return
	}
	session, err := h.service.Register(ctx, account.RegisterInput{Username: request.Username, DisplayName: request.DisplayName, Password: request.Password})
	if err != nil {
		h.writeAccountError(c, err)
		return
	}
	h.setSessionCookie(c, session.Token, session.ExpiresAt)
	c.JSON(consts.StatusCreated, map[string]any{"user": presentUser(session.User)})
}

func (h *HTTP) login(ctx context.Context, c *app.RequestContext) {
	var request credentialsRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxAuthBody, &request); err != nil {
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
		return
	}
	session, err := h.service.Login(ctx, account.LoginInput{Username: request.Username, Password: request.Password, CurrentToken: string(c.Cookie(httpauth.CookieName))})
	if err != nil {
		h.writeAccountError(c, err)
		return
	}
	h.setSessionCookie(c, session.Token, session.ExpiresAt)
	c.JSON(consts.StatusOK, map[string]any{"user": presentUser(session.User)})
}

func (h *HTTP) logout(ctx context.Context, c *app.RequestContext) {
	if err := h.service.Logout(ctx, string(c.Cookie(httpauth.CookieName))); err != nil {
		httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "logout is unavailable", nil)
		return
	}
	c.SetCookie(httpauth.CookieName, "", -1, "/", "", protocol.CookieSameSiteLaxMode, h.secureCookie, true)
	c.Status(consts.StatusNoContent)
}

func (h *HTTP) me(_ context.Context, c *app.RequestContext) {
	principal, _ := httpauth.Principal(c)
	c.JSON(consts.StatusOK, map[string]any{"user": userResponse{ID: strconv.FormatInt(principal.UserID, 10), Username: principal.Username, DisplayName: principal.DisplayName, Role: string(principal.Role)}})
}

func (h *HTTP) setSessionCookie(c *app.RequestContext, token string, expiresAt time.Time) {
	maxAge := int(math.Ceil(time.Until(expiresAt).Seconds()))
	c.SetCookie(httpauth.CookieName, token, maxAge, "/", "", protocol.CookieSameSiteLaxMode, h.secureCookie, true)
}

func (h *HTTP) writeAccountError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, account.ErrInvalidInput):
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, account.ErrConflict):
		httpx.WriteError(c, consts.StatusConflict, "conflict", "username is unavailable", nil)
	case errors.Is(err, account.ErrInvalidCredentials):
		httpx.WriteError(c, consts.StatusUnauthorized, "invalid_credentials", "username or password is invalid", nil)
	default:
		httpx.WriteError(c, consts.StatusInternalServerError, "internal_error", "request failed", nil)
	}
}

func presentUser(user entity.User) userResponse {
	return userResponse{ID: strconv.FormatInt(user.ID, 10), Username: user.Username, DisplayName: user.DisplayName, Role: string(user.Role)}
}
