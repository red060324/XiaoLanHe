package httpauth

import (
	"context"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

func TestOptional(t *testing.T) {
	service := authService{principal: auth.Principal{UserID: 7, Role: auth.RoleUser}}
	h := server.Default()
	h.GET("/", Optional(service), principalHandler)
	response := ut.PerformRequest(h.Engine, "GET", "/", nil, ut.Header{Key: "Cookie", Value: CookieName + "=token"})
	if response.Code != consts.StatusOK || response.Body.String() != "7" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	h = server.Default()
	h.GET("/", Optional(authService{err: auth.ErrUnauthenticated}), principalHandler)
	response = ut.PerformRequest(h.Engine, "GET", "/", nil, ut.Header{Key: "Cookie", Value: CookieName + "=expired"})
	if response.Code != consts.StatusOK || response.Body.String() != "guest" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequire(t *testing.T) {
	h := server.Default()
	h.GET("/", Require(authService{err: auth.ErrUnauthenticated}), principalHandler)
	response := ut.PerformRequest(h.Engine, "GET", "/", nil)
	if response.Code != consts.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequireRole(t *testing.T) {
	h := server.Default()
	h.GET("/", RequireRole(authService{principal: auth.Principal{UserID: 7, Role: auth.RoleUser}}, auth.RoleAdmin), principalHandler)
	response := ut.PerformRequest(h.Engine, "GET", "/", nil, ut.Header{Key: "Cookie", Value: CookieName + "=token"})
	if response.Code != consts.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	h = server.Default()
	h.GET("/", RequireRole(authService{principal: auth.Principal{UserID: 8, Role: auth.RoleAdmin}}, auth.RoleAdmin), principalHandler)
	response = ut.PerformRequest(h.Engine, "GET", "/", nil, ut.Header{Key: "Cookie", Value: CookieName + "=token"})
	if response.Code != consts.StatusOK || response.Body.String() != "8" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequireOrigin(t *testing.T) {
	for _, test := range []struct {
		name, allowed, origin string
		want                  int
	}{
		{name: "non-browser request", want: consts.StatusOK},
		{name: "configured frontend", allowed: "https://play.example", origin: "https://play.example", want: consts.StatusOK},
		{name: "cross-site request", allowed: "https://play.example", origin: "https://evil.example", want: consts.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := server.Default()
			h.POST("/", RequireOrigin(test.allowed), func(_ context.Context, c *app.RequestContext) { c.Status(consts.StatusOK) })
			response := ut.PerformRequest(h.Engine, "POST", "/", nil, ut.Header{Key: "Origin", Value: test.origin})
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

type authService struct {
	principal auth.Principal
	err       error
}

func (s authService) Authenticate(context.Context, string) (auth.Principal, error) {
	if s.err != nil {
		return auth.Principal{}, s.err
	}
	return s.principal, nil
}

func principalHandler(_ context.Context, c *app.RequestContext) {
	principal, ok := Principal(c)
	if !ok {
		c.String(consts.StatusOK, "guest")
		return
	}
	c.String(consts.StatusOK, "%d", principal.UserID)
}
