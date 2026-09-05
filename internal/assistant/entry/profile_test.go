package entry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/assistant/entity"
	assistant "github.com/red060324/XiaoLanHe/internal/assistant/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

func TestProfileHTTP(t *testing.T) {
	store := &profileHTTPStore{}
	authenticator := profileAuthenticator{principal: auth.Principal{UserID: 42, Role: auth.RoleUser}}
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewProfileHTTP(assistant.NewProfileService(store), authenticator, "https://play.example").Register(router)
	cookie := ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=valid"}

	get := ut.PerformRequest(router.Engine, "GET", "/api/me/assistant-profile", nil, cookie)
	if get.Code != 200 || !strings.Contains(get.Body.String(), `"favoriteGenres":[]`) || strings.Contains(get.Body.String(), "updatedAt") {
		t.Fatalf("status=%d body=%s", get.Code, get.Body.String())
	}

	replaced := ut.PerformRequest(router.Engine, "PUT", "/api/me/assistant-profile", &ut.Body{Body: bytes.NewBufferString(`{"favoriteGenres":[" RPG ","rpg"],"preferredPlatforms":["PC"],"defaultRegion":"global","preferredLanguages":["zh-cn"],"maxPriceMinor":30000,"currency":"cny"}`), Len: -1}, cookie, ut.Header{Key: "Origin", Value: "https://play.example"})
	if replaced.Code != 200 || !strings.Contains(replaced.Body.String(), `"favoriteGenres":["rpg"]`) || !strings.Contains(replaced.Body.String(), `"defaultRegion":"GLOBAL"`) {
		t.Fatalf("status=%d body=%s", replaced.Code, replaced.Body.String())
	}

	unknown := ut.PerformRequest(router.Engine, "PUT", "/api/me/assistant-profile", &ut.Body{Body: bytes.NewBufferString(`{"admin":true}`), Len: -1}, cookie)
	if unknown.Code != 400 || !strings.Contains(unknown.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	foreignOrigin := ut.PerformRequest(router.Engine, "DELETE", "/api/me/assistant-profile", nil, cookie, ut.Header{Key: "Origin", Value: "https://evil.example"})
	if foreignOrigin.Code != 403 || store.cleared {
		t.Fatalf("status=%d cleared=%v body=%s", foreignOrigin.Code, store.cleared, foreignOrigin.Body.String())
	}

	deleted := ut.PerformRequest(router.Engine, "DELETE", "/api/me/assistant-profile", nil, cookie)
	if deleted.Code != 204 || !store.cleared {
		t.Fatalf("status=%d cleared=%v", deleted.Code, store.cleared)
	}

	unauthenticated := ut.PerformRequest(router.Engine, "GET", "/api/me/assistant-profile", nil)
	if unauthenticated.Code != 401 {
		t.Fatalf("status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
}

func TestProfileHTTPRedactsDependencyError(t *testing.T) {
	const canary = "CANARY_PRIVATE_PROFILE_STORAGE_ERROR"
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewProfileHTTP(assistant.NewProfileService(&profileHTTPStore{err: errors.New(canary)}), profileAuthenticator{principal: auth.Principal{UserID: 42, Role: auth.RoleUser}}, "https://play.example").Register(router)

	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	response := ut.PerformRequest(router.Engine, "GET", "/api/me/assistant-profile", nil, ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=valid"})
	if response.Code != 503 || strings.Contains(output.String(), canary) || !strings.Contains(output.String(), `"error_class":"dependency"`) {
		t.Fatalf("status=%d logs=%s body=%s", response.Code, output.String(), response.Body.String())
	}
}

type profileAuthenticator struct{ principal auth.Principal }

func (a profileAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	if token != "valid" {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return a.principal, nil
}

type profileHTTPStore struct {
	profile entity.Profile
	found   bool
	cleared bool
	err     error
}

func (s *profileHTTPStore) LoadAssistantProfile(context.Context, int64) (entity.Profile, bool, error) {
	return s.profile, s.found, s.err
}
func (s *profileHTTPStore) ReplaceAssistantProfile(_ context.Context, _ int64, profile entity.Profile) (entity.Profile, error) {
	if profile.DefaultRegion == "FAIL" {
		return entity.Profile{}, errors.New("storage failed")
	}
	s.profile, s.found = profile, true
	s.profile.UpdatedAt = time.Unix(10, 0).UTC()
	return s.profile, nil
}
func (s *profileHTTPStore) ClearAssistantProfile(context.Context, int64) error {
	s.cleared = true
	return nil
}
