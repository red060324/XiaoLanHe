package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/community/entity"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const postColumns = `
	p.id,p.author_id,u.user_name,coalesce(u.display_name,''),p.game_id,g.slug,g.name,
	p.title,p.content,p.status,p.created_at,p.updated_at,
	(select count(*) from community_comment c where c.post_id=p.id and c.status='published'),
	(select count(*) from community_reaction r where r.post_id=p.id and r.reaction_type='like'),
	(select count(*) from community_reaction r where r.post_id=p.id and r.reaction_type='helpful'),
	(select count(*) from community_reaction r where r.post_id=p.id and r.reaction_type='funny'),
	exists(select 1 from community_reaction r where r.post_id=p.id and r.user_id=$1 and r.reaction_type='like'),
	exists(select 1 from community_reaction r where r.post_id=p.id and r.user_id=$1 and r.reaction_type='helpful'),
	exists(select 1 from community_reaction r where r.post_id=p.id and r.user_id=$1 and r.reaction_type='funny')`

func (s *Store) ListPosts(ctx context.Context, filter community.PostFilter) ([]entity.Post, error) {
	rows, err := s.pool.Query(ctx, `select `+postColumns+`
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.status='published' and ($2::bigint=0 or p.game_id=$2)
			and ($3::bigint=0 or (p.created_at,p.id)<($4,$3))
		order by p.created_at desc,p.id desc limit $5`, filter.ViewerID, filter.GameID, filter.Cursor.ID, filter.Cursor.CreatedAt, filter.Limit)
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
	return items, rows.Err()
}

func (s *Store) GetPost(ctx context.Context, id, viewerID int64, includeNonPublic bool) (entity.Post, error) {
	row := s.pool.QueryRow(ctx, `select `+postColumns+`
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.id=$2 and p.status<>'deleted' and ($3 or p.status='published')`, viewerID, id, includeNonPublic)
	post, err := scanPost(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Post{}, community.ErrNotFound
	}
	return post, err
}

func (s *Store) CreatePost(ctx context.Context, authorID int64, draft entity.PostDraft) (entity.Post, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		insert into community_post(author_id,game_id,title,content)
		values ($1,nullif($2,0),$3,$4) returning id`, authorID, draft.GameID, draft.Title, draft.Content).Scan(&id)
	if err != nil {
		return entity.Post{}, err
	}
	return s.GetPost(ctx, id, authorID, true)
}

func (s *Store) UpdatePost(ctx context.Context, id, viewerID int64, draft entity.PostDraft) (entity.Post, error) {
	tag, err := s.pool.Exec(ctx, `
		update community_post set game_id=nullif($2,0),title=$3,content=$4,updated_at=now()
		where id=$1 and status<>'deleted'`, id, draft.GameID, draft.Title, draft.Content)
	if err != nil {
		return entity.Post{}, err
	}
	if tag.RowsAffected() == 0 {
		return entity.Post{}, community.ErrNotFound
	}
	return s.GetPost(ctx, id, viewerID, true)
}

func (s *Store) DeletePost(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `update community_post set status='deleted',deleted_at=now(),updated_at=now() where id=$1 and status<>'deleted'`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return community.ErrNotFound
	}
	return err
}

func (s *Store) ModeratePost(ctx context.Context, id int64, status entity.Status) (entity.Post, error) {
	tag, err := s.pool.Exec(ctx, `update community_post set status=$2,updated_at=now() where id=$1 and status<>'deleted'`, id, status)
	if err != nil {
		return entity.Post{}, err
	}
	if tag.RowsAffected() == 0 {
		return entity.Post{}, community.ErrNotFound
	}
	return s.GetPost(ctx, id, 0, true)
}

func (s *Store) ListComments(ctx context.Context, filter community.CommentFilter) ([]entity.Comment, error) {
	rows, err := s.pool.Query(ctx, `
		select c.id,c.post_id,c.author_id,u.user_name,coalesce(u.display_name,''),c.content,c.status,c.created_at,c.updated_at
		from community_comment c join user_account u on u.id=c.author_id
		where c.post_id=$1 and c.status='published'
			and ($2::bigint=0 or (c.created_at,c.id)>($3,$2))
		order by c.created_at,c.id limit $4`, filter.PostID, filter.Cursor.ID, filter.Cursor.CreatedAt, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]entity.Comment, 0, filter.Limit)
	for rows.Next() {
		item, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetComment(ctx context.Context, id int64, includeNonPublic bool) (entity.Comment, error) {
	row := s.pool.QueryRow(ctx, `
		select c.id,c.post_id,c.author_id,u.user_name,coalesce(u.display_name,''),c.content,c.status,c.created_at,c.updated_at
		from community_comment c join user_account u on u.id=c.author_id
		where c.id=$1 and c.status<>'deleted' and ($2 or c.status='published')`, id, includeNonPublic)
	comment, err := scanComment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Comment{}, community.ErrNotFound
	}
	return comment, err
}

func (s *Store) CreateComment(ctx context.Context, postID, authorID int64, content string) (entity.Comment, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `insert into community_comment(post_id,author_id,content) values ($1,$2,$3) returning id`, postID, authorID, content).Scan(&id)
	if err != nil {
		return entity.Comment{}, err
	}
	return s.GetComment(ctx, id, true)
}

func (s *Store) UpdateComment(ctx context.Context, id int64, content string) (entity.Comment, error) {
	tag, err := s.pool.Exec(ctx, `update community_comment set content=$2,updated_at=now() where id=$1 and status<>'deleted'`, id, content)
	if err != nil {
		return entity.Comment{}, err
	}
	if tag.RowsAffected() == 0 {
		return entity.Comment{}, community.ErrNotFound
	}
	return s.GetComment(ctx, id, true)
}

func (s *Store) DeleteComment(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `update community_comment set status='deleted',deleted_at=now(),updated_at=now() where id=$1 and status<>'deleted'`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return community.ErrNotFound
	}
	return err
}

func (s *Store) ModerateComment(ctx context.Context, id int64, status entity.Status) (entity.Comment, error) {
	tag, err := s.pool.Exec(ctx, `update community_comment set status=$2,updated_at=now() where id=$1 and status<>'deleted'`, id, status)
	if err != nil {
		return entity.Comment{}, err
	}
	if tag.RowsAffected() == 0 {
		return entity.Comment{}, community.ErrNotFound
	}
	return s.GetComment(ctx, id, true)
}

func (s *Store) SetReaction(ctx context.Context, postID, userID int64, reaction entity.ReactionType, active bool) (entity.ReactionSummary, error) {
	var err error
	if active {
		_, err = s.pool.Exec(ctx, `insert into community_reaction(post_id,user_id,reaction_type) values ($1,$2,$3) on conflict do nothing`, postID, userID, reaction)
	} else {
		_, err = s.pool.Exec(ctx, `delete from community_reaction where post_id=$1 and user_id=$2 and reaction_type=$3`, postID, userID, reaction)
	}
	if err != nil {
		return entity.ReactionSummary{}, err
	}
	return s.reactionSummary(ctx, postID, userID)
}

func (s *Store) reactionSummary(ctx context.Context, postID, viewerID int64) (entity.ReactionSummary, error) {
	var like, helpful, funny int64
	var viewerLike, viewerHelpful, viewerFunny bool
	err := s.pool.QueryRow(ctx, `
		select
			count(*) filter (where reaction_type='like'),
			count(*) filter (where reaction_type='helpful'),
			count(*) filter (where reaction_type='funny'),
			coalesce(bool_or(user_id=$2 and reaction_type='like'),false),
			coalesce(bool_or(user_id=$2 and reaction_type='helpful'),false),
			coalesce(bool_or(user_id=$2 and reaction_type='funny'),false)
		from community_reaction where post_id=$1`, postID, viewerID).
		Scan(&like, &helpful, &funny, &viewerLike, &viewerHelpful, &viewerFunny)
	if err != nil {
		return entity.ReactionSummary{}, err
	}
	return newReactionSummary(like, helpful, funny, viewerLike, viewerHelpful, viewerFunny), nil
}

type scanner interface{ Scan(...any) error }

func scanPost(row scanner) (entity.Post, error) {
	var post entity.Post
	var gameID *int64
	var gameSlug, gameName *string
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
	if gameID != nil {
		post.Game = &entity.Game{ID: *gameID, Slug: *gameSlug, Name: *gameName}
	}
	post.Reactions = newReactionSummary(like, helpful, funny, viewerLike, viewerHelpful, viewerFunny)
	return post, nil
}

func scanComment(row scanner) (entity.Comment, error) {
	var comment entity.Comment
	var status string
	err := row.Scan(&comment.ID, &comment.PostID, &comment.Author.ID, &comment.Author.Username, &comment.Author.DisplayName, &comment.Content, &status, &comment.CreatedAt, &comment.UpdatedAt)
	comment.Status = entity.Status(status)
	return comment, err
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
