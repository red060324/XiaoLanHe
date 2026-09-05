package entry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	knowledge "github.com/red060324/XiaoLanHe/internal/knowledge/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

func TestKnowledgeHTTP(t *testing.T) {
	provider := &providerFake{}
	authenticator := knowledgeAuthenticator{}
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(knowledge.NewService(provider), authenticator, "https://play.example").Register(router)
	admin := ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"}
	user := ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"}
	unauthorized := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=guide", nil)
	if unauthorized.Code != 401 {
		t.Fatalf("status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	forbidden := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=guide", nil, user)
	if forbidden.Code != 403 {
		t.Fatalf("status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
	searched := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=guide&mode=mix", nil, admin)
	if searched.Code != 200 || !strings.Contains(searched.Body.String(), `"provider":"lightrag"`) {
		t.Fatalf("status=%d body=%s", searched.Code, searched.Body.String())
	}
	created := ut.PerformRequest(router.Engine, "POST", "/api/knowledge/documents", &ut.Body{Body: bytes.NewBufferString(`{"sourceType":"guide","title":"Guide","contentText":"body"}`), Len: -1}, admin)
	if created.Code != 202 || !strings.Contains(created.Body.String(), `"trackId":"track-1"`) {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}
	crossOrigin := ut.PerformRequest(router.Engine, "DELETE", "/api/admin/knowledge/documents/doc-1", nil, admin, ut.Header{Key: "Origin", Value: "https://evil.example"})
	if crossOrigin.Code != 403 || provider.deleteCalls != 0 {
		t.Fatalf("status=%d calls=%d", crossOrigin.Code, provider.deleteCalls)
	}
	deleted := ut.PerformRequest(router.Engine, "DELETE", "/api/admin/knowledge/documents/doc-1", nil, admin)
	if deleted.Code != 202 || provider.deleteCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", deleted.Code, provider.deleteCalls, deleted.Body.String())
	}
	invalid := ut.PerformRequest(router.Engine, "GET", "/api/admin/knowledge/documents?pageSize=101", nil, admin)
	if invalid.Code != 400 {
		t.Fatalf("status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestKnowledgeHTTPRedactsDependencyError(t *testing.T) {
	const canary = "CANARY_LIGHTRAG_PROVIDER_BODY"
	provider := &providerFake{err: errors.New(canary)}
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(knowledge.NewService(provider), knowledgeAuthenticator{}, "https://play.example").Register(router)

	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	response := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=guide&mode=mix", nil, ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"})
	if response.Code != 503 || strings.Contains(output.String(), canary) || !strings.Contains(output.String(), `"error_class":"dependency"`) {
		t.Fatalf("status=%d logs=%s body=%s", response.Code, output.String(), response.Body.String())
	}
}

func TestKnowledgeCreateFailureReturnsOnlyDeterministicSourceKey(t *testing.T) {
	provider := &providerFake{err: entity.ErrUnavailable}
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(knowledge.NewService(provider), knowledgeAuthenticator{}, "https://play.example").Register(router)
	response := ut.PerformRequest(router.Engine, "POST", "/api/knowledge/documents", &ut.Body{Body: bytes.NewBufferString(`{"sourceType":"guide","title":"Guide","contentText":"body"}`), Len: -1}, ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"})
	body := response.Body.String()
	if response.Code != 503 || !strings.Contains(body, `"sourceKey":"xlh-`) || strings.Contains(body, "body") || strings.Contains(body, "Guide") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

type knowledgeAuthenticator struct{}

func (knowledgeAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "admin":
		return auth.Principal{UserID: 1, Role: auth.RoleAdmin}, nil
	case "user":
		return auth.Principal{UserID: 2, Role: auth.RoleUser}, nil
	default:
		return auth.Principal{}, auth.ErrUnauthenticated
	}
}

type providerFake struct {
	deleteCalls int
	err         error
}

func (*providerFake) Health(context.Context) (entity.Health, error) { return entity.Health{}, nil }
func (p *providerFake) Search(_ context.Context, input entity.SearchInput) (entity.SearchResult, error) {
	if p.err != nil {
		return entity.SearchResult{}, p.err
	}
	return entity.SearchResult{Query: input.Query, Provider: "lightrag", Mode: input.Mode, Items: []entity.Evidence{}}, nil
}
func (p *providerFake) Create(_ context.Context, _ string, _ string) (entity.AcceptedDocument, error) {
	if p.err != nil {
		return entity.AcceptedDocument{}, p.err
	}
	return entity.AcceptedDocument{TrackID: "track-1"}, nil
}
func (*providerFake) Track(context.Context, string) (entity.Track, error) {
	return entity.Track{TrackID: "track-1", Documents: []entity.Document{}}, nil
}
func (*providerFake) List(context.Context, entity.ListInput) (entity.DocumentList, error) {
	return entity.DocumentList{Items: []entity.Document{}, Page: 1, PageSize: 20}, nil
}
func (p *providerFake) Delete(_ context.Context, id string) (entity.DeleteResult, error) {
	p.deleteCalls++
	return entity.DeleteResult{DocumentID: id, Status: "deletion_started"}, nil
}
