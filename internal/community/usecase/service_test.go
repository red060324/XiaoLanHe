package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/community/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

func TestServiceListPosts(t *testing.T) {
	created := time.Date(2026, 8, 31, 10, 0, 0, 123000000, time.UTC)
	store := &fakeStore{posts: []entity.Post{{ID: 3, CreatedAt: created}, {ID: 2, CreatedAt: created.Add(-time.Second)}, {ID: 1, CreatedAt: created.Add(-2 * time.Second)}}}
	service := NewService(store, &fakeCatalog{exists: true})
	page, err := service.ListPosts(context.Background(), ListPostsInput{GameID: 9, ViewerID: 7, Query: " guide ", Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if store.postFilter.GameID != 9 || store.postFilter.ViewerID != 7 || store.postFilter.Query != "guide" || store.postFilter.Limit != 3 {
		t.Fatalf("filter=%+v", store.postFilter)
	}
	if _, err := service.ListPosts(context.Background(), ListPostsInput{Cursor: page.NextCursor, Limit: 2}); err != nil {
		t.Fatal(err)
	}
	if store.postFilter.Cursor.ID != 2 || !store.postFilter.Cursor.CreatedAt.Equal(created.Add(-time.Second)) {
		t.Fatalf("cursor=%+v", store.postFilter.Cursor)
	}
	for _, input := range []ListPostsInput{{GameID: -1}, {ViewerID: -1}, {Limit: 51}, {Cursor: "bad***"}, {Query: strings.Repeat("界", 101)}} {
		if _, err := service.ListPosts(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("input=%+v err=%v", input, err)
		}
	}
}

func TestServiceCreatePost(t *testing.T) {
	t.Run("requires authentication", func(t *testing.T) {
		store := &fakeStore{}
		_, err := NewService(store, &fakeCatalog{exists: true}).CreatePost(context.Background(), auth.Principal{}, entity.PostDraft{Title: "标题", Content: "内容"})
		if !errors.Is(err, ErrUnauthenticated) || store.createdPost {
			t.Fatalf("err=%v created=%v", err, store.createdPost)
		}
	})

	t.Run("rejects missing game", func(t *testing.T) {
		store := &fakeStore{}
		_, err := NewService(store, &fakeCatalog{}).CreatePost(context.Background(), user(4), entity.PostDraft{GameID: 8, Title: "标题", Content: "内容"})
		if !errors.Is(err, ErrGameNotFound) || store.createdPost {
			t.Fatalf("err=%v created=%v", err, store.createdPost)
		}
	})

	t.Run("propagates catalog failure", func(t *testing.T) {
		dependencyErr := errors.New("catalog down")
		_, err := NewService(&fakeStore{}, &fakeCatalog{err: dependencyErr}).CreatePost(context.Background(), user(4), entity.PostDraft{GameID: 8, Title: "标题", Content: "内容"})
		if !errors.Is(err, dependencyErr) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("normalizes and creates", func(t *testing.T) {
		store := &fakeStore{post: entity.Post{ID: 11}}
		post, err := NewService(store, &fakeCatalog{exists: true}).CreatePost(context.Background(), user(4), entity.PostDraft{GameID: 8, Title: " 标题 ", Content: " 内容 "})
		if err != nil || post.ID != 11 || store.postAuthorID != 4 || store.postDraft.Title != "标题" || store.postDraft.Content != "内容" {
			t.Fatalf("post=%+v draft=%+v err=%v", post, store.postDraft, err)
		}
	})
}

func TestServiceUpdatePost(t *testing.T) {
	t.Run("forbids another author", func(t *testing.T) {
		store := &fakeStore{post: entity.Post{ID: 5, Author: entity.Author{ID: 2}}}
		_, err := NewService(store, &fakeCatalog{exists: true}).UpdatePost(context.Background(), user(3), 5, entity.PostDraft{Title: "标题", Content: "内容"})
		if !errors.Is(err, ErrForbidden) || store.updatedPost {
			t.Fatalf("err=%v updated=%v", err, store.updatedPost)
		}
	})

	t.Run("updates owned post", func(t *testing.T) {
		store := &fakeStore{post: entity.Post{ID: 5, Author: entity.Author{ID: 3}}}
		_, err := NewService(store, &fakeCatalog{exists: true}).UpdatePost(context.Background(), user(3), 5, entity.PostDraft{GameID: 4, Title: " 标题 ", Content: " 内容 "})
		if err != nil || !store.updatedPost || store.postViewerID != 3 || store.postDraft.Title != "标题" {
			t.Fatalf("draft=%+v viewer=%d err=%v", store.postDraft, store.postViewerID, err)
		}
	})
}

func TestServiceDeletePost(t *testing.T) {
	store := &fakeStore{post: entity.Post{ID: 5, Author: entity.Author{ID: 2}}}
	service := NewService(store, &fakeCatalog{})
	if err := service.DeletePost(context.Background(), user(3), 5); !errors.Is(err, ErrForbidden) || store.deletedPost {
		t.Fatalf("err=%v deleted=%v", err, store.deletedPost)
	}
	store.post.Author.ID = 3
	if err := service.DeletePost(context.Background(), user(3), 5); err != nil || !store.deletedPost {
		t.Fatalf("err=%v deleted=%v", err, store.deletedPost)
	}
}

func TestServiceModeratePost(t *testing.T) {
	store := &fakeStore{post: entity.Post{ID: 5}}
	service := NewService(store, &fakeCatalog{})
	if _, err := service.ModeratePost(context.Background(), user(3), 5, "hidden"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.ModeratePost(context.Background(), admin(1), 5, "deleted"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.ModeratePost(context.Background(), admin(1), 5, "hidden"); err != nil || store.postStatus != entity.StatusHidden {
		t.Fatalf("status=%q err=%v", store.postStatus, err)
	}
}

func TestServiceListComments(t *testing.T) {
	created := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	store := &fakeStore{post: entity.Post{ID: 5}, comments: []entity.Comment{{ID: 1, CreatedAt: created}, {ID: 2, CreatedAt: created.Add(time.Second)}, {ID: 3, CreatedAt: created.Add(2 * time.Second)}}}
	service := NewService(store, &fakeCatalog{})
	page, err := service.ListComments(context.Background(), ListCommentsInput{PostID: 5, Limit: 2})
	if err != nil || len(page.Items) != 2 || page.NextCursor == "" || store.commentFilter.Limit != 3 {
		t.Fatalf("page=%+v filter=%+v err=%v", page, store.commentFilter, err)
	}
	if _, err := service.ListComments(context.Background(), ListCommentsInput{PostID: 5, Cursor: page.NextCursor, Limit: 2}); err != nil {
		t.Fatal(err)
	}
	if store.commentFilter.Cursor.ID != 2 || !store.commentFilter.Cursor.CreatedAt.Equal(created.Add(time.Second)) {
		t.Fatalf("cursor=%+v", store.commentFilter.Cursor)
	}
	if _, err := service.ListComments(context.Background(), ListCommentsInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceCreateComment(t *testing.T) {
	store := &fakeStore{post: entity.Post{ID: 5}, comment: entity.Comment{ID: 8}}
	service := NewService(store, &fakeCatalog{})
	if _, err := service.CreateComment(context.Background(), auth.Principal{}, 5, "评论"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.CreateComment(context.Background(), user(3), 5, "  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err=%v", err)
	}
	comment, err := service.CreateComment(context.Background(), user(3), 5, " 评论 ")
	if err != nil || comment.ID != 8 || store.commentAuthorID != 3 || store.commentContent != "评论" {
		t.Fatalf("comment=%+v content=%q err=%v", comment, store.commentContent, err)
	}
}

func TestServiceUpdateComment(t *testing.T) {
	store := &fakeStore{comment: entity.Comment{ID: 8, Author: entity.Author{ID: 2}}}
	service := NewService(store, &fakeCatalog{})
	if _, err := service.UpdateComment(context.Background(), user(3), 8, "评论"); !errors.Is(err, ErrForbidden) || store.updatedComment {
		t.Fatalf("err=%v updated=%v", err, store.updatedComment)
	}
	store.comment.Author.ID = 3
	if _, err := service.UpdateComment(context.Background(), user(3), 8, " 新评论 "); err != nil || !store.updatedComment || store.commentContent != "新评论" {
		t.Fatalf("content=%q err=%v", store.commentContent, err)
	}
}

func TestServiceDeleteComment(t *testing.T) {
	store := &fakeStore{comment: entity.Comment{ID: 8, Author: entity.Author{ID: 2}}}
	service := NewService(store, &fakeCatalog{})
	if err := service.DeleteComment(context.Background(), user(3), 8); !errors.Is(err, ErrForbidden) || store.deletedComment {
		t.Fatalf("err=%v deleted=%v", err, store.deletedComment)
	}
	store.comment.Author.ID = 3
	if err := service.DeleteComment(context.Background(), user(3), 8); err != nil || !store.deletedComment {
		t.Fatalf("err=%v deleted=%v", err, store.deletedComment)
	}
}

func TestServiceModerateComment(t *testing.T) {
	store := &fakeStore{comment: entity.Comment{ID: 8}}
	service := NewService(store, &fakeCatalog{})
	if _, err := service.ModerateComment(context.Background(), user(3), 8, "hidden"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.ModerateComment(context.Background(), admin(1), 8, "hidden"); err != nil || store.commentStatus != entity.StatusHidden {
		t.Fatalf("status=%q err=%v", store.commentStatus, err)
	}
}

func TestServiceSetReaction(t *testing.T) {
	store := &fakeStore{post: entity.Post{ID: 5}, reactions: entity.ReactionSummary{Counts: map[entity.ReactionType]int64{entity.ReactionLike: 1}}}
	service := NewService(store, &fakeCatalog{})
	if _, err := service.SetReaction(context.Background(), auth.Principal{}, 5, "like", true); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err=%v", err)
	}
	if _, err := service.SetReaction(context.Background(), user(3), 5, "love", true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err=%v", err)
	}
	summary, err := service.SetReaction(context.Background(), user(3), 5, "like", false)
	if err != nil || summary.Counts[entity.ReactionLike] != 1 || store.reactionActive || store.reaction != entity.ReactionLike || store.reactionUserID != 3 {
		t.Fatalf("summary=%+v reaction=%q active=%v err=%v", summary, store.reaction, store.reactionActive, err)
	}
}

func TestPageInput(t *testing.T) {
	created := time.Date(2026, 8, 31, 10, 0, 0, 123456000, time.UTC)
	cursor, limit, err := pageInput(encodeCursor(created, 9), 0)
	if err != nil || limit != 20 || cursor.ID != 9 || !cursor.CreatedAt.Equal(created) {
		t.Fatalf("cursor=%+v limit=%d err=%v", cursor, limit, err)
	}
	for _, value := range []string{"bad***", "MTow", "MToxOjE"} {
		if _, _, err := pageInput(value, 20); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("value=%q err=%v", value, err)
		}
	}
}

func user(id int64) auth.Principal { return auth.Principal{UserID: id, Role: auth.RoleUser} }
func admin(id int64) auth.Principal {
	return auth.Principal{UserID: id, Role: auth.RoleAdmin}
}

type fakeCatalog struct {
	exists bool
	err    error
}

func (c *fakeCatalog) GameExists(context.Context, int64) (bool, error) { return c.exists, c.err }

type fakeStore struct {
	posts           []entity.Post
	post            entity.Post
	postErr         error
	postFilter      PostFilter
	postDraft       entity.PostDraft
	postAuthorID    int64
	postViewerID    int64
	createdPost     bool
	updatedPost     bool
	deletedPost     bool
	postStatus      entity.Status
	comments        []entity.Comment
	comment         entity.Comment
	commentErr      error
	commentFilter   CommentFilter
	commentAuthorID int64
	commentContent  string
	updatedComment  bool
	deletedComment  bool
	commentStatus   entity.Status
	reactions       entity.ReactionSummary
	reaction        entity.ReactionType
	reactionUserID  int64
	reactionActive  bool
}

func (s *fakeStore) ListPosts(_ context.Context, filter PostFilter) ([]entity.Post, error) {
	s.postFilter = filter
	return s.posts, nil
}
func (s *fakeStore) GetPost(_ context.Context, _ int64, viewerID int64, _ bool) (entity.Post, error) {
	s.postViewerID = viewerID
	return s.post, s.postErr
}
func (s *fakeStore) CreatePost(_ context.Context, authorID int64, draft entity.PostDraft) (entity.Post, error) {
	s.createdPost, s.postAuthorID, s.postDraft = true, authorID, draft
	return s.post, nil
}
func (s *fakeStore) UpdatePost(_ context.Context, _ int64, viewerID int64, draft entity.PostDraft) (entity.Post, error) {
	s.updatedPost, s.postViewerID, s.postDraft = true, viewerID, draft
	return s.post, nil
}
func (s *fakeStore) DeletePost(context.Context, int64) error { s.deletedPost = true; return nil }
func (s *fakeStore) ModeratePost(_ context.Context, _ int64, status entity.Status) (entity.Post, error) {
	s.postStatus = status
	return s.post, nil
}
func (s *fakeStore) ListComments(_ context.Context, filter CommentFilter) ([]entity.Comment, error) {
	s.commentFilter = filter
	return s.comments, nil
}
func (s *fakeStore) GetComment(context.Context, int64, bool) (entity.Comment, error) {
	return s.comment, s.commentErr
}
func (s *fakeStore) CreateComment(_ context.Context, _ int64, authorID int64, content string) (entity.Comment, error) {
	s.commentAuthorID, s.commentContent = authorID, content
	return s.comment, nil
}
func (s *fakeStore) UpdateComment(_ context.Context, _ int64, content string) (entity.Comment, error) {
	s.updatedComment, s.commentContent = true, content
	return s.comment, nil
}
func (s *fakeStore) DeleteComment(context.Context, int64) error {
	s.deletedComment = true
	return nil
}
func (s *fakeStore) ModerateComment(_ context.Context, _ int64, status entity.Status) (entity.Comment, error) {
	s.commentStatus = status
	return s.comment, nil
}
func (s *fakeStore) SetReaction(_ context.Context, _ int64, userID int64, reaction entity.ReactionType, active bool) (entity.ReactionSummary, error) {
	s.reactionUserID, s.reaction, s.reactionActive = userID, reaction, active
	return s.reactions, nil
}
