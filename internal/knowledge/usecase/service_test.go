package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

func TestServiceAuthorizationAndValidation(t *testing.T) {
	provider := &providerFake{}
	service := NewService(provider)
	draft := entity.DocumentDraft{SourceType: "guide", Title: "Guide", ContentText: "body"}
	if _, err := service.Create(context.Background(), auth.Principal{}, draft); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.Create(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleUser}, draft); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
	created, err := service.Create(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}, draft)
	if err != nil || created.SourceKey == "" || !strings.Contains(provider.text, "Title: Guide") {
		t.Fatalf("created=%+v text=%q err=%v", created, provider.text, err)
	}
	if _, err := service.Search(context.Background(), entity.SearchInput{Query: strings.Repeat("x", 101)}); !errors.Is(err, entity.ErrInvalidInput) || provider.searchCalls != 0 {
		t.Fatalf("err=%v calls=%d", err, provider.searchCalls)
	}
	if _, err := service.Delete(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}, "../bad"); !errors.Is(err, entity.ErrInvalidInput) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.Search(context.Background(), entity.SearchInput{Query: "guide", GameCode: "game\ninjected"}); !errors.Is(err, entity.ErrInvalidInput) {
		t.Fatalf("unsafe filter err=%v", err)
	}
	if _, err := service.List(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}, entity.ListInput{Status: "unknown"}); !errors.Is(err, entity.ErrInvalidInput) {
		t.Fatalf("unknown status err=%v", err)
	}
	if _, err := service.List(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}, entity.ListInput{Page: 10_001}); !errors.Is(err, entity.ErrInvalidInput) {
		t.Fatalf("excessive page err=%v", err)
	}
}

func TestCreatePreservesSafeSourceKeyOnProviderFailure(t *testing.T) {
	provider := &providerFake{createErr: entity.ErrUnavailable}
	service := NewService(provider)
	created, err := service.Create(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}, entity.DocumentDraft{SourceType: "guide", Title: "Guide", ContentText: "body"})
	if !errors.Is(err, entity.ErrUnavailable) || !entity.IsManagedSource(created.SourceKey) {
		t.Fatalf("created=%+v err=%v", created, err)
	}
}

type providerFake struct {
	text        string
	searchCalls int
	createErr   error
}

func (*providerFake) Health(context.Context) (entity.Health, error) { return entity.Health{}, nil }
func (p *providerFake) Search(_ context.Context, in entity.SearchInput) (entity.SearchResult, error) {
	p.searchCalls++
	return entity.SearchResult{Query: in.Query, Provider: "lightrag", Mode: in.Mode}, nil
}
func (p *providerFake) Create(_ context.Context, key, text string) (entity.AcceptedDocument, error) {
	p.text = text
	return entity.AcceptedDocument{TrackID: "track-1", SourceKey: key}, p.createErr
}
func (*providerFake) Track(context.Context, string) (entity.Track, error) { return entity.Track{}, nil }
func (*providerFake) List(context.Context, entity.ListInput) (entity.DocumentList, error) {
	return entity.DocumentList{}, nil
}
func (*providerFake) Delete(context.Context, string) (entity.DeleteResult, error) {
	return entity.DeleteResult{}, nil
}
