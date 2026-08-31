package httpauth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const CookieName = "xlh_session"
const principalKey = "xiaolanhe.principal"

type Authenticator interface {
	Authenticate(context.Context, string) (auth.Principal, error)
}

func Optional(service Authenticator) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		token := string(c.Cookie(CookieName))
		if token == "" {
			c.Next(ctx)
			return
		}
		principal, err := service.Authenticate(ctx, token)
		if errors.Is(err, auth.ErrUnauthenticated) {
			c.Next(ctx)
			return
		}
		if err != nil {
			if !httpx.WriteDeadlineError(c, err) {
				httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "authentication is unavailable", nil)
			}
			c.Abort()
			return
		}
		c.Set(principalKey, principal)
		c.Next(ctx)
	}
}

func Require(service Authenticator) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		principal, ok := authenticate(ctx, c, service)
		if !ok {
			return
		}
		c.Set(principalKey, principal)
		c.Next(ctx)
	}
}

func RequireRole(service Authenticator, role auth.Role) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		principal, ok := authenticate(ctx, c, service)
		if !ok {
			return
		}
		if principal.Role != role {
			httpx.WriteError(c, consts.StatusForbidden, "forbidden", "permission denied", nil)
			c.Abort()
			return
		}
		c.Set(principalKey, principal)
		c.Next(ctx)
	}
}

func RequireOrigin(allowedOrigin string) app.HandlerFunc {
	allowedOrigin = strings.TrimRight(allowedOrigin, "/")
	return func(ctx context.Context, c *app.RequestContext) {
		origin := strings.TrimRight(string(c.Request.Header.Peek("Origin")), "/")
		if origin == "" {
			c.Next(ctx)
			return
		}
		parsed, err := url.Parse(origin)
		sameHost := err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && strings.EqualFold(parsed.Host, string(c.Host()))
		if origin != allowedOrigin && !sameHost {
			httpx.WriteError(c, consts.StatusForbidden, "forbidden", "origin is not allowed", nil)
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

func authenticate(ctx context.Context, c *app.RequestContext, service Authenticator) (auth.Principal, bool) {
	principal, err := service.Authenticate(ctx, string(c.Cookie(CookieName)))
	if errors.Is(err, auth.ErrUnauthenticated) {
		httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", nil)
		c.Abort()
		return auth.Principal{}, false
	}
	if err != nil {
		if !httpx.WriteDeadlineError(c, err) {
			httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "authentication is unavailable", nil)
		}
		c.Abort()
		return auth.Principal{}, false
	}
	return principal, true
}

func Principal(c *app.RequestContext) (auth.Principal, bool) {
	value, ok := c.Get(principalKey)
	if !ok {
		return auth.Principal{}, false
	}
	principal, ok := value.(auth.Principal)
	return principal, ok
}
