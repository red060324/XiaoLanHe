package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/community/entity"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const communityReconciliationTimeout = 5 * time.Second

const postColumns = `
	p.id,p.author_id,u.user_name,coalesce(u.display_name,''),p.game_id,g.slug,g.name,
	p.title,p.content,p.status,p.created_at,p.updated_at,
	(select count(*) from community_comment c where c.post_id=p.id and c.status='published'),
	(select count(*) from community_reaction r where r.post_id=p.id and r.reaction_type='like'),
	(select count(*) from community_reaction r where r.post_id=p.id and r.reaction_type='helpful'),
	(select count(*) from community_reaction r where r.post_id=p.id and r.reaction_type='funny'),
	exists(select 1 from community_reaction r where r.post_id=p.id and r.user_id=? and r.reaction_type='like'),
	exists(select 1 from community_reaction r where r.post_id=p.id and r.user_id=? and r.reaction_type='helpful'),
	exists(select 1 from community_reaction r where r.post_id=p.id and r.user_id=? and r.reaction_type='funny')`

func (s *Store) ListPosts(ctx context.Context, filter community.PostFilter) ([]entity.Post, error) {
	cursorTime := mysqlCursorTime(filter.Cursor)
	rows, err := s.db.QueryContext(ctx, `select `+postColumns+`
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.status='published' and (?=0 or p.game_id=?)
			and (?=0 or (p.created_at,p.id)<(?,?))
			and (?='' or instr(lower(p.title),lower(?))>0 or instr(lower(p.content),lower(?))>0)
		order by p.created_at desc,p.id desc limit ?`,
		filter.ViewerID, filter.ViewerID, filter.ViewerID,
		filter.GameID, filter.GameID,
		filter.Cursor.ID, cursorTime, filter.Cursor.ID,
		filter.Query, filter.Query, filter.Query, filter.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entity.Post, 0, filter.Limit)
	for rows.Next() {
		item, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) GetPost(ctx context.Context, id, viewerID int64, includeNonPublic bool) (entity.Post, error) {
	return getPost(ctx, s.db, id, viewerID, includeNonPublic)
}

type communityQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getPost(ctx context.Context, queryer communityQueryer, id, viewerID int64, includeNonPublic bool) (entity.Post, error) {
	row := queryer.QueryRowContext(ctx, `select `+postColumns+`
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.id=? and p.status<>'deleted' and (? or p.status='published')`,
		viewerID, viewerID, viewerID, id, includeNonPublic,
	)
	post, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Post{}, community.ErrNotFound
	}
	return post, err
}

func (s *Store) CreatePost(ctx context.Context, authorID int64, draft entity.PostDraft) (entity.Post, error) {
	var id int64
	post, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.Post, error) {
		result, err := tx.ExecContext(ctx, `
			insert into community_post(author_id,game_id,title,content)
			values (?,nullif(?,0),?,?)`, authorID, draft.GameID, draft.Title, draft.Content)
		if err != nil {
			return entity.Post{}, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return entity.Post{}, err
		}
		return entity.Post{}, nil
	})
	if err == nil {
		return s.GetPost(ctx, id, authorID, true)
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return post, err
	}
	reconcileCtx, cancel := communityReconciliationContext(ctx)
	defer cancel()
	return s.reconcileCreatedPost(reconcileCtx, id, authorID, draft, err)
}

func (s *Store) UpdatePost(ctx context.Context, id, viewerID int64, draft entity.PostDraft) (entity.Post, error) {
	contextWasActive := ctx.Err() == nil
	result, err := s.db.ExecContext(ctx, `
		update community_post set game_id=nullif(?,0),title=?,content=?,updated_at=current_timestamp(6)
		where id=? and status<>'deleted'`, draft.GameID, draft.Title, draft.Content, id)
	if err != nil {
		if contextWasActive && isAmbiguousAutocommitWriteError(err) {
			return entity.Post{}, unknownAutocommitWrite(err)
		}
		return entity.Post{}, err
	}
	if err := s.requirePostAffected(ctx, result, id); err != nil {
		return entity.Post{}, err
	}
	return s.GetPost(ctx, id, viewerID, true)
}

func (s *Store) DeletePost(ctx context.Context, id int64) error {
	contextWasActive := ctx.Err() == nil
	result, err := s.db.ExecContext(ctx, `update community_post set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`, id)
	if err != nil {
		if !contextWasActive || !isAmbiguousAutocommitWriteError(err) {
			return err
		}
		return unknownAutocommitWrite(err)
	}
	return requireAffected(result, nil, community.ErrNotFound)
}

func (s *Store) ModeratePost(ctx context.Context, id int64, status entity.Status) (entity.Post, error) {
	contextWasActive := ctx.Err() == nil
	result, err := s.db.ExecContext(ctx, `update community_post set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`, status, id)
	if err != nil {
		if contextWasActive && isAmbiguousAutocommitWriteError(err) {
			return entity.Post{}, unknownAutocommitWrite(err)
		}
		return entity.Post{}, err
	}
	if err := s.requirePostAffected(ctx, result, id); err != nil {
		return entity.Post{}, err
	}
	return s.GetPost(ctx, id, 0, true)
}

func (s *Store) ListComments(ctx context.Context, filter community.CommentFilter) ([]entity.Comment, error) {
	cursorTime := mysqlCursorTime(filter.Cursor)
	rows, err := s.db.QueryContext(ctx, `
		select p.id,c.id,c.post_id,c.author_id,u.user_name,u.display_name,c.content,c.status,c.created_at,c.updated_at
		from community_post p
		left join community_comment c on c.post_id=p.id and c.status='published'
			and (?=0 or (c.created_at,c.id)>(?,?))
		left join user_account u on u.id=c.author_id
		where p.id=? and p.status='published'
		order by c.created_at,c.id limit ?`, filter.Cursor.ID, cursorTime, filter.Cursor.ID, filter.PostID, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entity.Comment, 0, filter.Limit)
	parentFound := false
	for rows.Next() {
		var parentID int64
		item, present, err := scanNullableComment(rows, &parentID)
		if err != nil {
			return nil, err
		}
		parentFound = true
		if present {
			items = append(items, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !parentFound {
		return nil, community.ErrNotFound
	}
	return items, nil
}

func (s *Store) GetComment(ctx context.Context, id int64, includeNonPublic bool) (entity.Comment, error) {
	return getComment(ctx, s.db, id, includeNonPublic)
}

func getComment(ctx context.Context, queryer communityQueryer, id int64, includeNonPublic bool) (entity.Comment, error) {
	row := queryer.QueryRowContext(ctx, `
		select c.id,c.post_id,c.author_id,u.user_name,coalesce(u.display_name,''),c.content,c.status,c.created_at,c.updated_at
		from community_comment c join user_account u on u.id=c.author_id
		where c.id=? and c.status<>'deleted' and (? or c.status='published')`, id, includeNonPublic)
	comment, err := scanComment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Comment{}, community.ErrNotFound
	}
	return comment, err
}

func (s *Store) CreateComment(ctx context.Context, postID, authorID int64, content string) (entity.Comment, error) {
	var id int64
	comment, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.Comment, error) {
		if err := lockPublishedPost(ctx, tx, postID); err != nil {
			return entity.Comment{}, err
		}
		result, err := tx.ExecContext(ctx, `insert into community_comment(post_id,author_id,content) values (?,?,?)`, postID, authorID, content)
		if err != nil {
			return entity.Comment{}, err
		}
		id, err = result.LastInsertId()
		if err != nil {
			return entity.Comment{}, err
		}
		return entity.Comment{}, nil
	})
	if err == nil {
		return s.GetComment(ctx, id, true)
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return comment, err
	}
	reconcileCtx, cancel := communityReconciliationContext(ctx)
	defer cancel()
	return s.reconcileCreatedComment(reconcileCtx, id, postID, authorID, content, err)
}

func (s *Store) UpdateComment(ctx context.Context, id int64, content string) (entity.Comment, error) {
	contextWasActive := ctx.Err() == nil
	result, err := s.db.ExecContext(ctx, `update community_comment set content=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`, content, id)
	if err != nil {
		if contextWasActive && isAmbiguousAutocommitWriteError(err) {
			return entity.Comment{}, unknownAutocommitWrite(err)
		}
		return entity.Comment{}, err
	}
	if err := s.requireCommentAffected(ctx, result, id); err != nil {
		return entity.Comment{}, err
	}
	return s.GetComment(ctx, id, true)
}

func (s *Store) DeleteComment(ctx context.Context, id int64) error {
	contextWasActive := ctx.Err() == nil
	result, err := s.db.ExecContext(ctx, `update community_comment set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`, id)
	if err != nil {
		if !contextWasActive || !isAmbiguousAutocommitWriteError(err) {
			return err
		}
		return unknownAutocommitWrite(err)
	}
	return requireAffected(result, nil, community.ErrNotFound)
}

func (s *Store) ModerateComment(ctx context.Context, id int64, status entity.Status) (entity.Comment, error) {
	contextWasActive := ctx.Err() == nil
	result, err := s.db.ExecContext(ctx, `update community_comment set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`, status, id)
	if err != nil {
		if contextWasActive && isAmbiguousAutocommitWriteError(err) {
			return entity.Comment{}, unknownAutocommitWrite(err)
		}
		return entity.Comment{}, err
	}
	if err := s.requireCommentAffected(ctx, result, id); err != nil {
		return entity.Comment{}, err
	}
	return s.GetComment(ctx, id, true)
}

func (s *Store) SetReaction(ctx context.Context, postID, userID int64, reaction entity.ReactionType, active bool) (entity.ReactionSummary, error) {
	summary, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.ReactionSummary, error) {
		if err := lockPublishedPost(ctx, tx, postID); err != nil {
			return entity.ReactionSummary{}, err
		}
		var err error
		if active {
			_, err = tx.ExecContext(ctx, `insert into community_reaction(post_id,user_id,reaction_type) values (?,?,?) on duplicate key update id=community_reaction.id`, postID, userID, reaction)
		} else {
			_, err = tx.ExecContext(ctx, `delete from community_reaction where post_id=? and user_id=? and reaction_type=?`, postID, userID, reaction)
		}
		if err != nil {
			return entity.ReactionSummary{}, err
		}
		return entity.ReactionSummary{}, nil
	})
	if err == nil {
		return s.reactionSummary(ctx, postID, userID)
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return summary, err
	}
	reconcileCtx, cancel := communityReconciliationContext(ctx)
	defer cancel()
	return s.reconcileReaction(reconcileCtx, postID, userID, reaction, active, err)
}

func lockPublishedPost(ctx context.Context, tx *sql.Tx, postID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `select id from community_post where id=? and status='published' for update`, postID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return community.ErrNotFound
	}
	return err
}

func (s *Store) reactionSummary(ctx context.Context, postID, viewerID int64) (entity.ReactionSummary, error) {
	return reactionSummary(ctx, s.db, postID, viewerID)
}

func reactionSummary(ctx context.Context, queryer communityQueryer, postID, viewerID int64) (entity.ReactionSummary, error) {
	var like, helpful, funny int64
	var viewerLike, viewerHelpful, viewerFunny int64
	err := queryer.QueryRowContext(ctx, `
		select
			count(case when reaction_type='like' then 1 end),
			count(case when reaction_type='helpful' then 1 end),
			count(case when reaction_type='funny' then 1 end),
			coalesce(max(case when user_id=? and reaction_type='like' then 1 else 0 end),0),
			coalesce(max(case when user_id=? and reaction_type='helpful' then 1 else 0 end),0),
			coalesce(max(case when user_id=? and reaction_type='funny' then 1 else 0 end),0)
		from community_reaction where post_id=?`, viewerID, viewerID, viewerID, postID).
		Scan(&like, &helpful, &funny, &viewerLike, &viewerHelpful, &viewerFunny)
	if err != nil {
		return entity.ReactionSummary{}, err
	}
	return newReactionSummary(like, helpful, funny, viewerLike != 0, viewerHelpful != 0, viewerFunny != 0), nil
}

func (s *Store) reconcileCreatedPost(ctx context.Context, id, authorID int64, draft entity.PostDraft, commitErr error) (entity.Post, error) {
	if id <= 0 {
		return entity.Post{}, commitErr
	}
	tx, err := s.beginCommunityReconciliation(ctx)
	if err != nil {
		return entity.Post{}, joinCommunityReconciliationError(commitErr, "begin post-create reconciliation", err)
	}
	defer func() { _ = tx.Rollback() }()

	post, err := getPost(ctx, tx, id, authorID, true)
	if errors.Is(err, community.ErrNotFound) {
		return entity.Post{}, commitErr
	}
	if err != nil {
		return entity.Post{}, joinCommunityReconciliationError(commitErr, "reconcile post create", err)
	}
	if post.Author.ID != authorID || postGameID(post) != draft.GameID || post.Title != draft.Title || post.Content != draft.Content {
		return entity.Post{}, errors.Join(commitErr, errors.New("community post create durable identity mismatch"))
	}
	return post, nil
}

func (s *Store) reconcileCreatedComment(ctx context.Context, id, postID, authorID int64, content string, commitErr error) (entity.Comment, error) {
	if id <= 0 {
		return entity.Comment{}, commitErr
	}
	tx, err := s.beginCommunityReconciliation(ctx)
	if err != nil {
		return entity.Comment{}, joinCommunityReconciliationError(commitErr, "begin comment-create reconciliation", err)
	}
	defer func() { _ = tx.Rollback() }()
	comment, err := getComment(ctx, tx, id, true)
	if errors.Is(err, community.ErrNotFound) {
		return entity.Comment{}, commitErr
	}
	if err != nil {
		return entity.Comment{}, joinCommunityReconciliationError(commitErr, "reconcile comment create", err)
	}
	if comment.PostID != postID || comment.Author.ID != authorID || comment.Content != content {
		return entity.Comment{}, errors.Join(commitErr, errors.New("community comment create durable identity mismatch"))
	}
	return comment, nil
}

func (s *Store) reconcileReaction(ctx context.Context, postID, userID int64, reaction entity.ReactionType, active bool, commitErr error) (entity.ReactionSummary, error) {
	tx, err := s.beginCommunityReconciliation(ctx)
	if err != nil {
		return entity.ReactionSummary{}, joinCommunityReconciliationError(commitErr, "begin reaction reconciliation", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from community_reaction where post_id=? and user_id=? and reaction_type=?)`, postID, userID, reaction).Scan(&exists); err != nil {
		return entity.ReactionSummary{}, joinCommunityReconciliationError(commitErr, "reconcile reaction identity", err)
	}
	if exists != active {
		return entity.ReactionSummary{}, errors.Join(commitErr, errors.New("community reaction durable state mismatch"))
	}
	summary, err := reactionSummary(ctx, tx, postID, userID)
	if err != nil {
		return entity.ReactionSummary{}, joinCommunityReconciliationError(commitErr, "read reconciled reaction summary", err)
	}
	return summary, nil
}

func (s *Store) beginCommunityReconciliation(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
}

func communityReconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), communityReconciliationTimeout)
}

func unknownAutocommitWrite(err error) error {
	return &mysqltx.CommitOutcomeUnknownError{Err: err}
}

func joinCommunityReconciliationError(writeErr error, operation string, err error) error {
	return errors.Join(writeErr, fmt.Errorf("%s: %w", operation, err))
}

func isAmbiguousAutocommitWriteError(err error) bool {
	if err == nil || errors.Is(err, driver.ErrBadConn) || isMySQLError(err) {
		// database/sql retries driver.ErrBadConn only when the driver proved that
		// no bytes were written, so it is not an unknown server outcome.
		return false
	}
	if errors.Is(err, drivermysql.ErrInvalidConn) || errors.Is(err, drivermysql.ErrPktSync) ||
		errors.Is(err, drivermysql.ErrPktSyncMul) || errors.Is(err, drivermysql.ErrMalformPkt) {
		// go-sql-driver/mysql converts transport read failures into
		// ErrInvalidConn and may report protocol framing failures after the
		// server has already applied an autocommit statement.
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrShortWrite) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func isMySQLError(err error) bool {
	var mysqlErr *drivermysql.MySQLError
	return errors.As(err, &mysqlErr)
}

func postGameID(post entity.Post) int64 {
	if post.Game == nil {
		return 0
	}
	return post.Game.ID
}

func requireAffected(result sql.Result, err, notFound error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound
	}
	return nil
}

func (s *Store) requirePostAffected(ctx context.Context, result sql.Result, id int64) error {
	return s.requireExistingAffected(ctx, result, `select exists(select 1 from community_post where id=? and status<>'deleted')`, id)
}

func (s *Store) requireCommentAffected(ctx context.Context, result sql.Result, id int64) error {
	return s.requireExistingAffected(ctx, result, `select exists(select 1 from community_comment where id=? and status<>'deleted')`, id)
}

func (s *Store) requireExistingAffected(ctx context.Context, result sql.Result, query string, id int64) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 0 {
		return nil
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, query, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return community.ErrNotFound
	}
	return nil
}

func mysqlCursorTime(cursor community.Cursor) time.Time {
	if cursor.ID == 0 {
		// MySQL DATETIME does not accept Go's year-one zero time in strict mode.
		// The cursor branch is disabled when ID is zero, so bind a valid sentinel.
		return time.Unix(0, 0).UTC()
	}
	return cursor.CreatedAt.UTC()
}

type scanner interface{ Scan(...any) error }

func scanPost(row scanner) (entity.Post, error) {
	var post entity.Post
	var gameID sql.NullInt64
	var gameSlug, gameName sql.NullString
	var status string
	var like, helpful, funny int64
	var viewerLike, viewerHelpful, viewerFunny bool
	err := row.Scan(
		&post.ID, &post.Author.ID, &post.Author.Username, &post.Author.DisplayName,
		&gameID, &gameSlug, &gameName, &post.Title, &post.Content, &status,
		&post.CreatedAt, &post.UpdatedAt, &post.CommentCount,
		&like, &helpful, &funny, &viewerLike, &viewerHelpful, &viewerFunny,
	)
	if err != nil {
		return entity.Post{}, err
	}
	post.Status = entity.Status(status)
	if gameID.Valid {
		post.Game = &entity.Game{ID: gameID.Int64, Slug: gameSlug.String, Name: gameName.String}
	}
	post.Reactions = newReactionSummary(like, helpful, funny, viewerLike, viewerHelpful, viewerFunny)
	return post, nil
}

func scanComment(row scanner) (entity.Comment, error) {
	var comment entity.Comment
	var status string
	err := row.Scan(&comment.ID, &comment.PostID, &comment.Author.ID, &comment.Author.Username, &comment.Author.DisplayName, &comment.Content, &status, &comment.CreatedAt, &comment.UpdatedAt)
	if err != nil {
		return entity.Comment{}, err
	}
	comment.Status = entity.Status(status)
	return comment, nil
}

func scanNullableComment(row scanner, parentID *int64) (entity.Comment, bool, error) {
	var (
		id, postID, authorID  sql.NullInt64
		username, displayName sql.NullString
		content, status       sql.NullString
		createdAt, updatedAt  sql.NullTime
	)
	if err := row.Scan(
		parentID, &id, &postID, &authorID, &username, &displayName,
		&content, &status, &createdAt, &updatedAt,
	); err != nil {
		return entity.Comment{}, false, err
	}
	if !id.Valid {
		return entity.Comment{}, false, nil
	}
	comment := entity.Comment{
		ID:     id.Int64,
		PostID: postID.Int64,
		Author: entity.Author{
			ID:          authorID.Int64,
			Username:    username.String,
			DisplayName: displayName.String,
		},
		Content:   content.String,
		Status:    entity.Status(status.String),
		CreatedAt: createdAt.Time,
		UpdatedAt: updatedAt.Time,
	}
	return comment, true, nil
}

func newReactionSummary(like, helpful, funny int64, viewerLike, viewerHelpful, viewerFunny bool) entity.ReactionSummary {
	result := entity.ReactionSummary{Counts: map[entity.ReactionType]int64{
		entity.ReactionLike: like, entity.ReactionHelpful: helpful, entity.ReactionFunny: funny,
	}}
	if viewerLike {
		result.ViewerReactions = append(result.ViewerReactions, entity.ReactionLike)
	}
	if viewerHelpful {
		result.ViewerReactions = append(result.ViewerReactions, entity.ReactionHelpful)
	}
	if viewerFunny {
		result.ViewerReactions = append(result.ViewerReactions, entity.ReactionFunny)
	}
	return result
}

var _ community.Store = (*Store)(nil)
