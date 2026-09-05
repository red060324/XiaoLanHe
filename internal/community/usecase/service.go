package usecase

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/red060324/XiaoLanHe/internal/community/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

var (
	ErrInvalidInput    = errors.New("invalid community input")
	ErrUnauthenticated = auth.ErrUnauthenticated
	ErrForbidden       = errors.New("community forbidden")
	ErrNotFound        = errors.New("community content not found")
	ErrGameNotFound    = errors.New("community game not found")
)

type Cursor struct {
	CreatedAt time.Time
	ID        int64
}

type PostFilter struct {
	GameID   int64
	Cursor   Cursor
	Limit    int
	ViewerID int64
	Query    string
}

type CommentFilter struct {
	PostID int64
	Cursor Cursor
	Limit  int
}

type Store interface {
	ListPosts(context.Context, PostFilter) ([]entity.Post, error)
	GetPost(context.Context, int64, int64, bool) (entity.Post, error)
	CreatePost(context.Context, int64, entity.PostDraft) (entity.Post, error)
	UpdatePost(context.Context, int64, int64, entity.PostDraft) (entity.Post, error)
	DeletePost(context.Context, int64) error
	ModeratePost(context.Context, int64, entity.Status) (entity.Post, error)
	ListComments(context.Context, CommentFilter) ([]entity.Comment, error)
	GetComment(context.Context, int64, bool) (entity.Comment, error)
	CreateComment(context.Context, int64, int64, string) (entity.Comment, error)
	UpdateComment(context.Context, int64, string) (entity.Comment, error)
	DeleteComment(context.Context, int64) error
	ModerateComment(context.Context, int64, entity.Status) (entity.Comment, error)
	SetReaction(context.Context, int64, int64, entity.ReactionType, bool) (entity.ReactionSummary, error)
}

type Catalog interface {
	GameExists(context.Context, int64) (bool, error)
}

type Service struct {
	store   Store
	catalog Catalog
}

func NewService(store Store, catalog Catalog) *Service {
	return &Service{store: store, catalog: catalog}
}

type ListPostsInput struct {
	GameID, ViewerID int64
	Cursor           string
	Query            string
	Limit            int
}

type PostPage struct {
	Items      []entity.Post
	NextCursor string
}

func (s *Service) ListPosts(ctx context.Context, in ListPostsInput) (PostPage, error) {
	query := strings.TrimSpace(in.Query)
	cursor, limit, err := pageInput(in.Cursor, in.Limit)
	if err != nil || in.GameID < 0 || in.ViewerID < 0 || utf8.RuneCountInString(query) > 100 {
		return PostPage{}, ErrInvalidInput
	}
	items, err := s.store.ListPosts(ctx, PostFilter{GameID: in.GameID, Cursor: cursor, Limit: limit + 1, ViewerID: in.ViewerID, Query: query})
	if err != nil {
		return PostPage{}, err
	}
	result := PostPage{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return result, nil
}

func (s *Service) GetPost(ctx context.Context, id, viewerID int64) (entity.Post, error) {
	if id <= 0 || viewerID < 0 {
		return entity.Post{}, ErrInvalidInput
	}
	return s.store.GetPost(ctx, id, viewerID, false)
}

func (s *Service) CreatePost(ctx context.Context, principal auth.Principal, draft entity.PostDraft) (entity.Post, error) {
	if principal.UserID <= 0 {
		return entity.Post{}, ErrUnauthenticated
	}
	draft, err := draft.Normalize()
	if err != nil {
		return entity.Post{}, ErrInvalidInput
	}
	if err := s.validateGame(ctx, draft.GameID); err != nil {
		return entity.Post{}, err
	}
	return s.store.CreatePost(ctx, principal.UserID, draft)
}

func (s *Service) UpdatePost(ctx context.Context, principal auth.Principal, id int64, draft entity.PostDraft) (entity.Post, error) {
	if principal.UserID <= 0 {
		return entity.Post{}, ErrUnauthenticated
	}
	if id <= 0 {
		return entity.Post{}, ErrNotFound
	}
	current, err := s.store.GetPost(ctx, id, principal.UserID, true)
	if err != nil {
		return entity.Post{}, err
	}
	if current.Author.ID != principal.UserID {
		return entity.Post{}, ErrForbidden
	}
	draft, err = draft.Normalize()
	if err != nil {
		return entity.Post{}, ErrInvalidInput
	}
	if err := s.validateGame(ctx, draft.GameID); err != nil {
		return entity.Post{}, err
	}
	return s.store.UpdatePost(ctx, id, principal.UserID, draft)
}

func (s *Service) DeletePost(ctx context.Context, principal auth.Principal, id int64) error {
	if principal.UserID <= 0 {
		return ErrUnauthenticated
	}
	current, err := s.store.GetPost(ctx, id, principal.UserID, true)
	if err != nil {
		return err
	}
	if current.Author.ID != principal.UserID {
		return ErrForbidden
	}
	return s.store.DeletePost(ctx, id)
}

func (s *Service) ModeratePost(ctx context.Context, principal auth.Principal, id int64, value string) (entity.Post, error) {
	if !principal.IsAdmin() {
		return entity.Post{}, ErrForbidden
	}
	status, err := entity.ParseModerationStatus(value)
	if err != nil {
		return entity.Post{}, ErrInvalidInput
	}
	if id <= 0 {
		return entity.Post{}, ErrNotFound
	}
	return s.store.ModeratePost(ctx, id, status)
}

type ListCommentsInput struct {
	PostID int64
	Cursor string
	Limit  int
}

type CommentPage struct {
	Items      []entity.Comment
	NextCursor string
}

func (s *Service) ListComments(ctx context.Context, in ListCommentsInput) (CommentPage, error) {
	if in.PostID <= 0 {
		return CommentPage{}, ErrNotFound
	}
	cursor, limit, err := pageInput(in.Cursor, in.Limit)
	if err != nil {
		return CommentPage{}, ErrInvalidInput
	}
	items, err := s.store.ListComments(ctx, CommentFilter{PostID: in.PostID, Cursor: cursor, Limit: limit + 1})
	if err != nil {
		return CommentPage{}, err
	}
	result := CommentPage{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		last := result.Items[len(result.Items)-1]
		result.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return result, nil
}

func (s *Service) CreateComment(ctx context.Context, principal auth.Principal, postID int64, content string) (entity.Comment, error) {
	if principal.UserID <= 0 {
		return entity.Comment{}, ErrUnauthenticated
	}
	if postID <= 0 {
		return entity.Comment{}, ErrNotFound
	}
	content, err := entity.NormalizeComment(content)
	if err != nil {
		return entity.Comment{}, ErrInvalidInput
	}
	if _, err := s.store.GetPost(ctx, postID, principal.UserID, false); err != nil {
		return entity.Comment{}, err
	}
	return s.store.CreateComment(ctx, postID, principal.UserID, content)
}

func (s *Service) UpdateComment(ctx context.Context, principal auth.Principal, id int64, content string) (entity.Comment, error) {
	if principal.UserID <= 0 {
		return entity.Comment{}, ErrUnauthenticated
	}
	current, err := s.store.GetComment(ctx, id, true)
	if err != nil {
		return entity.Comment{}, err
	}
	if current.Author.ID != principal.UserID {
		return entity.Comment{}, ErrForbidden
	}
	content, err = entity.NormalizeComment(content)
	if err != nil {
		return entity.Comment{}, ErrInvalidInput
	}
	return s.store.UpdateComment(ctx, id, content)
}

func (s *Service) DeleteComment(ctx context.Context, principal auth.Principal, id int64) error {
	if principal.UserID <= 0 {
		return ErrUnauthenticated
	}
	current, err := s.store.GetComment(ctx, id, true)
	if err != nil {
		return err
	}
	if current.Author.ID != principal.UserID {
		return ErrForbidden
	}
	return s.store.DeleteComment(ctx, id)
}

func (s *Service) ModerateComment(ctx context.Context, principal auth.Principal, id int64, value string) (entity.Comment, error) {
	if !principal.IsAdmin() {
		return entity.Comment{}, ErrForbidden
	}
	status, err := entity.ParseModerationStatus(value)
	if err != nil {
		return entity.Comment{}, ErrInvalidInput
	}
	if id <= 0 {
		return entity.Comment{}, ErrNotFound
	}
	return s.store.ModerateComment(ctx, id, status)
}

func (s *Service) SetReaction(ctx context.Context, principal auth.Principal, postID int64, value string, active bool) (entity.ReactionSummary, error) {
	if principal.UserID <= 0 {
		return entity.ReactionSummary{}, ErrUnauthenticated
	}
	reaction, err := entity.ParseReaction(value)
	if err != nil {
		return entity.ReactionSummary{}, ErrInvalidInput
	}
	if postID <= 0 {
		return entity.ReactionSummary{}, ErrNotFound
	}
	if _, err := s.store.GetPost(ctx, postID, principal.UserID, false); err != nil {
		return entity.ReactionSummary{}, err
	}
	return s.store.SetReaction(ctx, postID, principal.UserID, reaction, active)
}

func (s *Service) validateGame(ctx context.Context, gameID int64) error {
	if gameID == 0 {
		return nil
	}
	exists, err := s.catalog.GameExists(ctx, gameID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrGameNotFound
	}
	return nil
}

func pageInput(value string, limit int) (Cursor, int, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return Cursor{}, 0, ErrInvalidInput
	}
	if value == "" {
		return Cursor{}, limit, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, 0, ErrInvalidInput
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 {
		return Cursor{}, 0, ErrInvalidInput
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || micros <= 0 {
		return Cursor{}, 0, ErrInvalidInput
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return Cursor{}, 0, ErrInvalidInput
	}
	return Cursor{CreatedAt: time.UnixMicro(micros).UTC(), ID: id}, limit, nil
}

func encodeCursor(createdAt time.Time, id int64) string {
	value := fmt.Sprintf("%d:%d", createdAt.UnixMicro(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}
