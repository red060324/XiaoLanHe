package entry

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/red060324/XiaoLanHe/internal/community/entity"
	communitypresenter "github.com/red060324/XiaoLanHe/internal/community/presenter"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const maxCommunityBody = 32 << 10

type HTTP struct {
	service *community.Service
	auth    httpauth.Authenticator
	origin  string
}

func NewHTTP(service *community.Service, authenticator httpauth.Authenticator, publicOrigin string) *HTTP {
	return &HTTP{service: service, auth: authenticator, origin: publicOrigin}
}

func (h *HTTP) Register(router *server.Hertz) {
	public := router.Group("/api/community", httpauth.Optional(h.auth))
	public.GET("/posts", h.listPosts)
	public.GET("/posts/:id", h.getPost)
	public.GET("/posts/:id/comments", h.listComments)

	write := router.Group("/api/community", httpauth.RequireOrigin(h.origin), httpauth.Require(h.auth))
	write.POST("/posts", h.createPost)
	write.PUT("/posts/:id", h.updatePost)
	write.DELETE("/posts/:id", h.deletePost)
	write.POST("/posts/:id/comments", h.createComment)
	write.PUT("/comments/:id", h.updateComment)
	write.DELETE("/comments/:id", h.deleteComment)
	write.PUT("/posts/:id/reactions/:type", h.addReaction)
	write.DELETE("/posts/:id/reactions/:type", h.removeReaction)

	admin := router.Group("/api/admin/community", httpauth.RequireOrigin(h.origin), httpauth.RequireRole(h.auth, auth.RoleAdmin))
	admin.PUT("/posts/:id/status", h.moderatePost)
	admin.PUT("/comments/:id/status", h.moderateComment)
}

func (h *HTTP) listPosts(ctx context.Context, c *app.RequestContext) {
	gameID, err := communitypresenter.OptionalID(string(c.Query("gameId")))
	if err != nil {
		h.writeError(ctx, c, "list_posts", 0, err)
		return
	}
	limit, err := communitypresenter.Limit(string(c.Query("limit")))
	if err != nil {
		h.writeError(ctx, c, "list_posts", 0, err)
		return
	}
	principal, _ := httpauth.Principal(c)
	page, err := h.service.ListPosts(ctx, community.ListPostsInput{GameID: gameID, ViewerID: principal.UserID, Cursor: string(c.Query("cursor")), Query: string(c.Query("q")), Limit: limit})
	if err != nil {
		h.writeError(ctx, c, "list_posts", 0, err)
		return
	}
	c.JSON(consts.StatusOK, communitypresenter.PresentPosts(page))
}

func (h *HTTP) getPost(ctx context.Context, c *app.RequestContext) {
	id, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "get_post", 0, err)
		return
	}
	principal, _ := httpauth.Principal(c)
	post, err := h.service.GetPost(ctx, id, principal.UserID)
	if err != nil {
		h.writeError(ctx, c, "get_post", id, err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"post": communitypresenter.PresentPost(post)})
}

func (h *HTTP) createPost(ctx context.Context, c *app.RequestContext) {
	request, ok := postRequest(c)
	if !ok {
		h.writeError(ctx, c, "create_post", 0, community.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	post, err := h.service.CreatePost(ctx, principal, request)
	if err != nil {
		h.writeError(ctx, c, "create_post", 0, err)
		return
	}
	c.JSON(consts.StatusCreated, map[string]any{"post": communitypresenter.PresentPost(post)})
}

func (h *HTTP) updatePost(ctx context.Context, c *app.RequestContext) {
	id, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "update_post", 0, err)
		return
	}
	request, ok := postRequest(c)
	if !ok {
		h.writeError(ctx, c, "update_post", id, community.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	post, err := h.service.UpdatePost(ctx, principal, id, request)
	if err != nil {
		h.writeError(ctx, c, "update_post", id, err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"post": communitypresenter.PresentPost(post)})
}

func (h *HTTP) deletePost(ctx context.Context, c *app.RequestContext) {
	id, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "delete_post", 0, err)
		return
	}
	principal, _ := httpauth.Principal(c)
	if err := h.service.DeletePost(ctx, principal, id); err != nil {
		h.writeError(ctx, c, "delete_post", id, err)
		return
	}
	c.Status(consts.StatusNoContent)
}

func (h *HTTP) listComments(ctx context.Context, c *app.RequestContext) {
	postID, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "list_comments", 0, err)
		return
	}
	limit, err := communitypresenter.Limit(string(c.Query("limit")))
	if err != nil {
		h.writeError(ctx, c, "list_comments", postID, err)
		return
	}
	page, err := h.service.ListComments(ctx, community.ListCommentsInput{PostID: postID, Cursor: string(c.Query("cursor")), Limit: limit})
	if err != nil {
		h.writeError(ctx, c, "list_comments", postID, err)
		return
	}
	c.JSON(consts.StatusOK, communitypresenter.PresentComments(page))
}

func (h *HTTP) createComment(ctx context.Context, c *app.RequestContext) {
	postID, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "create_comment", 0, err)
		return
	}
	var request communitypresenter.CommentRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxCommunityBody, &request); err != nil {
		h.writeError(ctx, c, "create_comment", postID, community.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	comment, err := h.service.CreateComment(ctx, principal, postID, request.Content)
	if err != nil {
		h.writeError(ctx, c, "create_comment", postID, err)
		return
	}
	c.JSON(consts.StatusCreated, map[string]any{"comment": communitypresenter.PresentComment(comment)})
}

func (h *HTTP) updateComment(ctx context.Context, c *app.RequestContext) {
	id, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "update_comment", 0, err)
		return
	}
	var request communitypresenter.CommentRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxCommunityBody, &request); err != nil {
		h.writeError(ctx, c, "update_comment", id, community.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	comment, err := h.service.UpdateComment(ctx, principal, id, request.Content)
	if err != nil {
		h.writeError(ctx, c, "update_comment", id, err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"comment": communitypresenter.PresentComment(comment)})
}

func (h *HTTP) deleteComment(ctx context.Context, c *app.RequestContext) {
	id, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "delete_comment", 0, err)
		return
	}
	principal, _ := httpauth.Principal(c)
	if err := h.service.DeleteComment(ctx, principal, id); err != nil {
		h.writeError(ctx, c, "delete_comment", id, err)
		return
	}
	c.Status(consts.StatusNoContent)
}

func (h *HTTP) addReaction(ctx context.Context, c *app.RequestContext) {
	h.setReaction(ctx, c, true)
}

func (h *HTTP) removeReaction(ctx context.Context, c *app.RequestContext) {
	h.setReaction(ctx, c, false)
}

func (h *HTTP) setReaction(ctx context.Context, c *app.RequestContext, active bool) {
	postID, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "set_reaction", 0, err)
		return
	}
	principal, _ := httpauth.Principal(c)
	summary, err := h.service.SetReaction(ctx, principal, postID, c.Param("type"), active)
	if err != nil {
		h.writeError(ctx, c, "set_reaction", postID, err)
		return
	}
	c.JSON(consts.StatusOK, communitypresenter.PresentReaction(summary))
}

func (h *HTTP) moderatePost(ctx context.Context, c *app.RequestContext) {
	id, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "moderate_post", 0, err)
		return
	}
	var request communitypresenter.StatusRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxCommunityBody, &request); err != nil {
		h.writeError(ctx, c, "moderate_post", id, community.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	post, err := h.service.ModeratePost(ctx, principal, id, request.Status)
	if err != nil {
		h.writeError(ctx, c, "moderate_post", id, err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"post": communitypresenter.PresentPost(post)})
}

func (h *HTTP) moderateComment(ctx context.Context, c *app.RequestContext) {
	id, err := communitypresenter.RequiredID(c.Param("id"))
	if err != nil {
		h.writeError(ctx, c, "moderate_comment", 0, err)
		return
	}
	var request communitypresenter.StatusRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxCommunityBody, &request); err != nil {
		h.writeError(ctx, c, "moderate_comment", id, community.ErrInvalidInput)
		return
	}
	principal, _ := httpauth.Principal(c)
	comment, err := h.service.ModerateComment(ctx, principal, id, request.Status)
	if err != nil {
		h.writeError(ctx, c, "moderate_comment", id, err)
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"comment": communitypresenter.PresentComment(comment)})
}

func postRequest(c *app.RequestContext) (entity.PostDraft, bool) {
	var request communitypresenter.PostRequest
	if err := httpx.DecodeJSON(c.Request.Body(), maxCommunityBody, &request); err != nil {
		return entity.PostDraft{}, false
	}
	draft, err := request.Draft()
	return draft, err == nil
}

func (h *HTTP) writeError(ctx context.Context, c *app.RequestContext, operation string, resourceID int64, err error) {
	if httpx.WriteDeadlineError(c, err) {
		return
	}
	switch {
	case errors.Is(err, community.ErrInvalidInput):
		httpx.WriteError(c, consts.StatusBadRequest, "invalid_request", "request is invalid", nil)
	case errors.Is(err, community.ErrUnauthenticated):
		httpx.WriteError(c, consts.StatusUnauthorized, "unauthenticated", "authentication required", nil)
	case errors.Is(err, community.ErrForbidden):
		httpx.WriteError(c, consts.StatusForbidden, "forbidden", "permission denied", nil)
	case errors.Is(err, community.ErrGameNotFound):
		httpx.WriteError(c, consts.StatusNotFound, "game_not_found", "game not found", nil)
	case errors.Is(err, community.ErrNotFound):
		httpx.WriteError(c, consts.StatusNotFound, "not_found", "community content not found", nil)
	default:
		slog.ErrorContext(ctx, "community dependency failed", "operation", operation, "resource_id", resourceID, "error", err)
		httpx.WriteError(c, consts.StatusServiceUnavailable, "dependency_unavailable", "community is unavailable", nil)
	}
}
