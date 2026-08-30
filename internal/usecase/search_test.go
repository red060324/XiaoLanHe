package usecase

import (
	"context"
	"errors"
	"testing"
)

func TestWebSearchRun(t *testing.T) {
	want := errors.New("downstream")
	_, err := NewWebSearch(searchClientFunc(func(context.Context, string) (WebSearchResult, error) { return WebSearchResult{}, want })).Run(context.Background(), "q")
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

type searchClientFunc func(context.Context, string) (WebSearchResult, error)

func (f searchClientFunc) Search(ctx context.Context, query string) (WebSearchResult, error) {
	return f(ctx, query)
}
