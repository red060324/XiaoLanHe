package entry

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/community/entity"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
)

func TestHTTP(t *testing.T) {
	store := &httpStore{post: entity.Post{
		ID: 9, Author: entity.Author{ID: 7, Username: "player", DisplayName: "Player"},
		Title: "Boss Guide", Content: "Use frost damage.", Status: entity.StatusPublished,
		Reactions: entity.ReactionSummary{Counts: map[entity.ReactionType]int64{}},
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0),
	}}
	router := server.Default()
	NewHTTP(community.NewService(store, httpCatalog{}), httpAuthenticator{}, "https://play.example").Register(router)

	feed := ut.PerformRequest(router.Engine, "GET", "/api/community/posts", nil)
	if feed.Code != 200 || !strings.Contains(feed.Body.String(), `"title":"Boss Guide"`) {
		t.Fatalf("feed status=%d body=%s", feed.Code, feed.Body.String())
	}
	invalid := ut.PerformRequest(router.Engine, "GET", "/api/community/posts/not-an-id", nil)
	if invalid.Code != 400 || !strings.Contains(invalid.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	body := &ut.Body{Body: bytes.NewBufferString(`{"title":"New Guide","content":"Route"}`), Len: -1}
	anonymous := ut.PerformRequest(router.Engine, "POST", "/api/community/posts", body)
	if anonymous.Code != 401 {
		t.Fatalf("anonymous status=%d body=%s", anonymous.Code, anonymous.Body.String())
	}
	body = &ut.Body{Body: bytes.NewBufferString(`{"title":"New Guide","content":"Route"}`), Len: -1}
	created := ut.PerformRequest(router.Engine, "POST", "/api/community/posts", body,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"},
		ut.Header{Key: "Origin", Value: "https://play.example"})
	if created.Code != 201 || !store.created {
		t.Fatalf("created status=%d body=%s created=%v", created.Code, created.Body.String(), store.created)
	}

	t.Run("accepts surrogate escaped emoji boundaries", func(t *testing.T) {
		escapedEmoji := `\uD83D\uDE00`
		payload := `{"title":"` + strings.Repeat(escapedEmoji, 160) + `","content":"` + strings.Repeat(escapedEmoji, 10000) + `"}`
		store.created = false
		body := &ut.Body{Body: bytes.NewBufferString(payload), Len: -1}
		response := ut.PerformRequest(router.Engine, "POST", "/api/community/posts", body,
			ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"},
			ut.Header{Key: "Origin", Value: "https://play.example"})
		if response.Code != 201 || !store.created {
			t.Fatalf("status=%d created=%v", response.Code, store.created)
		}
		if got := utf8.RuneCountInString(store.createdDraft.Title); got != 160 {
			t.Fatalf("title runes=%d", got)
		}
		if got := utf8.RuneCountInString(store.createdDraft.Content); got != 10000 {
			t.Fatalf("content runes=%d", got)
		}
	})

	t.Run("rejects content over rune limit", func(t *testing.T) {
		escapedEmoji := `\uD83D\uDE00`
		payload := `{"title":"` + escapedEmoji + `","content":"` + strings.Repeat(escapedEmoji, 10001) + `"}`
		store.created = false
		body := &ut.Body{Body: bytes.NewBufferString(payload), Len: -1}
		response := ut.PerformRequest(router.Engine, "POST", "/api/community/posts", body,
			ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"},
			ut.Header{Key: "Origin", Value: "https://play.example"})
		if response.Code != 400 || store.created {
			t.Fatalf("status=%d created=%v", response.Code, store.created)
		}
	})

	body = &ut.Body{Body: bytes.NewBufferString(`{"title":"Changed","content":"Route"}`), Len: -1}
	forbidden := ut.PerformRequest(router.Engine, "PUT", "/api/community/posts/9", body,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=other"},
		ut.Header{Key: "Origin", Value: "https://play.example"})
	if forbidden.Code != 403 {
		t.Fatalf("forbidden status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
}

type httpStore struct {
	post         entity.Post
	created      bool
	createdDraft entity.PostDraft
}

func (s *httpStore) ListPosts(context.Context, community.PostFilter) ([]entity.Post, error) {
	return []entity.Post{s.post}, nil
}
func (s *httpStore) GetPost(_ context.Context, id, _ int64, _ bool) (entity.Post, error) {
	if id != s.post.ID {
		return entity.Post{}, community.ErrNotFound
	}
	return s.post, nil
}
func (s *httpStore) CreatePost(_ context.Context, authorID int64, draft entity.PostDraft) (entity.Post, error) {
	s.created = true
	s.createdDraft = draft
	result := s.post
	result.Author.ID, result.Title, result.Content = authorID, draft.Title, draft.Content
	return result, nil
}
func (s *httpStore) UpdatePost(context.Context, int64, int64, entity.PostDraft) (entity.Post, error) {
	return s.post, nil
}
func (s *httpStore) DeletePost(context.Context, int64) error { return nil }
func (s *httpStore) ModeratePost(context.Context, int64, entity.Status) (entity.Post, error) {
	return s.post, nil
}
func (s *httpStore) ListComments(context.Context, community.CommentFilter) ([]entity.Comment, error) {
	return nil, nil
}
func (s *httpStore) GetComment(context.Context, int64, bool) (entity.Comment, error) {
	return entity.Comment{}, community.ErrNotFound
}
func (s *httpStore) CreateComment(context.Context, int64, int64, string) (entity.Comment, error) {
	return entity.Comment{}, nil
}
func (s *httpStore) UpdateComment(context.Context, int64, string) (entity.Comment, error) {
	return entity.Comment{}, nil
}
func (s *httpStore) DeleteComment(context.Context, int64) error { return nil }
func (s *httpStore) ModerateComment(context.Context, int64, entity.Status) (entity.Comment, error) {
	return entity.Comment{}, nil
}
func (s *httpStore) SetReaction(context.Context, int64, int64, entity.ReactionType, bool) (entity.ReactionSummary, error) {
	return entity.ReactionSummary{Counts: map[entity.ReactionType]int64{}}, nil
}

type httpCatalog struct{}

func (httpCatalog) GameExists(context.Context, int64) (bool, error) { return true, nil }

type httpAuthenticator struct{}

func (httpAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "user":
		return auth.Principal{UserID: 7, Role: auth.RoleUser}, nil
	case "other":
		return auth.Principal{UserID: 8, Role: auth.RoleUser}, nil
	case "admin":
		return auth.Principal{UserID: 1, Role: auth.RoleAdmin}, nil
	default:
		return auth.Principal{}, auth.ErrUnauthenticated
	}
}
