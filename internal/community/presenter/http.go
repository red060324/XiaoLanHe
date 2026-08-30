package presenter

import (
	"strconv"
	"time"

	"github.com/red060324/XiaoLanHe/internal/community/entity"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
)

type PostRequest struct {
	GameID  string `json:"gameId"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func (r PostRequest) Draft() (entity.PostDraft, error) {
	gameID, err := OptionalID(r.GameID)
	if err != nil {
		return entity.PostDraft{}, err
	}
	return entity.PostDraft{GameID: gameID, Title: r.Title, Content: r.Content}, nil
}

type CommentRequest struct {
	Content string `json:"content"`
}

type StatusRequest struct {
	Status string `json:"status"`
}

type AuthorResponse struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
}

type GameResponse struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type PostResponse struct {
	ID              string           `json:"id"`
	Title           string           `json:"title"`
	Content         string           `json:"content"`
	Status          string           `json:"status"`
	Author          AuthorResponse   `json:"author"`
	Game            *GameResponse    `json:"game,omitempty"`
	CommentCount    int64            `json:"commentCount"`
	ReactionCounts  map[string]int64 `json:"reactionCounts"`
	ViewerReactions []string         `json:"viewerReactions"`
	CreatedAt       string           `json:"createdAt"`
	UpdatedAt       string           `json:"updatedAt"`
}

type CommentResponse struct {
	ID        string         `json:"id"`
	PostID    string         `json:"postId"`
	Content   string         `json:"content"`
	Status    string         `json:"status"`
	Author    AuthorResponse `json:"author"`
	CreatedAt string         `json:"createdAt"`
	UpdatedAt string         `json:"updatedAt"`
}

type ReactionResponse struct {
	ReactionCounts  map[string]int64 `json:"reactionCounts"`
	ViewerReactions []string         `json:"viewerReactions"`
}

func PresentPost(post entity.Post) PostResponse {
	result := PostResponse{
		ID: strconv.FormatInt(post.ID, 10), Title: post.Title, Content: post.Content,
		Status: string(post.Status), Author: presentAuthor(post.Author),
		CommentCount: post.CommentCount, CreatedAt: formatTime(post.CreatedAt), UpdatedAt: formatTime(post.UpdatedAt),
	}
	if post.Game != nil {
		result.Game = &GameResponse{ID: strconv.FormatInt(post.Game.ID, 10), Slug: post.Game.Slug, Name: post.Game.Name}
	}
	result.ReactionCounts, result.ViewerReactions = presentReactions(post.Reactions)
	return result
}

func PresentPosts(page community.PostPage) map[string]any {
	items := make([]PostResponse, len(page.Items))
	for i := range page.Items {
		items[i] = PresentPost(page.Items[i])
	}
	return map[string]any{"items": items, "nextCursor": page.NextCursor}
}

func PresentComment(comment entity.Comment) CommentResponse {
	return CommentResponse{
		ID: strconv.FormatInt(comment.ID, 10), PostID: strconv.FormatInt(comment.PostID, 10),
		Content: comment.Content, Status: string(comment.Status), Author: presentAuthor(comment.Author),
		CreatedAt: formatTime(comment.CreatedAt), UpdatedAt: formatTime(comment.UpdatedAt),
	}
}

func PresentComments(page community.CommentPage) map[string]any {
	items := make([]CommentResponse, len(page.Items))
	for i := range page.Items {
		items[i] = PresentComment(page.Items[i])
	}
	return map[string]any{"items": items, "nextCursor": page.NextCursor}
}

func PresentReaction(summary entity.ReactionSummary) ReactionResponse {
	counts, viewer := presentReactions(summary)
	return ReactionResponse{ReactionCounts: counts, ViewerReactions: viewer}
}

func RequiredID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, community.ErrInvalidInput
	}
	return id, nil
}

func OptionalID(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return RequiredID(value)
}

func Limit(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, community.ErrInvalidInput
	}
	return limit, nil
}

func presentAuthor(author entity.Author) AuthorResponse {
	return AuthorResponse{ID: strconv.FormatInt(author.ID, 10), Username: author.Username, DisplayName: author.DisplayName}
}

func presentReactions(summary entity.ReactionSummary) (map[string]int64, []string) {
	counts := map[string]int64{
		string(entity.ReactionLike): summary.Counts[entity.ReactionLike], string(entity.ReactionHelpful): summary.Counts[entity.ReactionHelpful], string(entity.ReactionFunny): summary.Counts[entity.ReactionFunny],
	}
	viewer := make([]string, len(summary.ViewerReactions))
	for i := range summary.ViewerReactions {
		viewer[i] = string(summary.ViewerReactions[i])
	}
	return counts, viewer
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
