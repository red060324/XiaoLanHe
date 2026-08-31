package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWebSearchRun(t *testing.T) {
	t.Run("propagates provider errors", func(t *testing.T) {
		want := errors.New("downstream")
		_, err := NewWebSearch(searchClientFunc(func(context.Context, string) (WebSearchResult, error) { return WebSearchResult{}, want })).Run(context.Background(), "q")
		if !errors.Is(err, want) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("normalizes the query before the provider", func(t *testing.T) {
		var seen string
		result, err := NewWebSearch(searchClientFunc(func(_ context.Context, query string) (WebSearchResult, error) {
			seen = query
			return WebSearchResult{Query: query}, nil
		})).Run(context.Background(), "  guide  ")
		if err != nil || seen != "guide" || result.Query != "guide" {
			t.Fatalf("seen=%q result=%#v err=%v", seen, result, err)
		}
	})

	for name, query := range map[string]string{"blank": " \t ", "too long": strings.Repeat("游", 101)} {
		t.Run("rejects "+name+" query", func(t *testing.T) {
			calls := 0
			_, err := NewWebSearch(searchClientFunc(func(context.Context, string) (WebSearchResult, error) {
				calls++
				return WebSearchResult{}, nil
			})).Run(context.Background(), query)
			if err == nil || calls != 0 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

type searchClientFunc func(context.Context, string) (WebSearchResult, error)

func (f searchClientFunc) Search(ctx context.Context, query string) (WebSearchResult, error) {
	return f(ctx, query)
}
