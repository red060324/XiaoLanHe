package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/community/entity"
	community "github.com/red060324/XiaoLanHe/internal/community/usecase"
)

func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	return NewStore(db), mock
}

func TestListPostsBindsRepeatedParametersAndScansNullableGame(t *testing.T) {
	store, mock := newMockStore(t)
	created := time.Date(2026, 9, 7, 2, 3, 4, 5000, time.UTC)
	filter := community.PostFilter{
		GameID: 9, ViewerID: 7, Query: "%guide", Limit: 11,
		Cursor: community.Cursor{ID: 5, CreatedAt: created},
	}
	query := `select ` + postColumns + `
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.status='published' and (?=0 or p.game_id=?)
			and (?=0 or (p.created_at,p.id)<(?,?))
			and (?='' or instr(lower(p.title),lower(?))>0 or instr(lower(p.content),lower(?))>0)
		order by p.created_at desc,p.id desc limit ?`
	mock.ExpectQuery(query).
		WithArgs(int64(7), int64(7), int64(7), int64(9), int64(9), int64(5), created, int64(5), "%guide", "%guide", "%guide", 11).
		WillReturnRows(sqlmock.NewRows(postColumnNames()).
			AddRow(12, 3, "author", "Author", nil, nil, nil, "title", "body", "published", created, created, 2, 1, 2, 3, true, false, true))

	posts, err := store.ListPosts(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Game != nil {
		t.Fatalf("posts=%+v", posts)
	}
	if posts[0].Reactions.Counts[entity.ReactionHelpful] != 2 || len(posts[0].Reactions.ViewerReactions) != 2 {
		t.Fatalf("reactions=%+v", posts[0].Reactions)
	}
}

func TestListCommentsDistinguishesEmptyParentFromMissingParent(t *testing.T) {
	query := `
		select p.id,c.id,c.post_id,c.author_id,u.user_name,u.display_name,c.content,c.status,c.created_at,c.updated_at
		from community_post p
		left join community_comment c on c.post_id=p.id and c.status='published'
			and (?=0 or (c.created_at,c.id)>(?,?))
		left join user_account u on u.id=c.author_id
		where p.id=? and p.status='published'
		order by c.created_at,c.id limit ?`

	t.Run("empty published post", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectQuery(query).
			WithArgs(int64(0), time.Unix(0, 0).UTC(), int64(0), int64(22), 10).
			WillReturnRows(sqlmock.NewRows([]string{"parent_id", "id", "post_id", "author_id", "user_name", "display_name", "content", "status", "created_at", "updated_at"}).
				AddRow(22, nil, nil, nil, nil, nil, nil, nil, nil, nil))
		comments, err := store.ListComments(context.Background(), community.CommentFilter{PostID: 22, Limit: 10})
		if err != nil || len(comments) != 0 || comments == nil {
			t.Fatalf("comments=%+v err=%v", comments, err)
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectQuery(query).
			WithArgs(int64(0), time.Unix(0, 0).UTC(), int64(0), int64(23), 10).
			WillReturnRows(sqlmock.NewRows([]string{"parent_id", "id", "post_id", "author_id", "user_name", "display_name", "content", "status", "created_at", "updated_at"}))
		_, err := store.ListComments(context.Background(), community.CommentFilter{PostID: 23, Limit: 10})
		if !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestCreateCommentCommitsAndUsesInsertResultID(t *testing.T) {
	store, mock := newMockStore(t)
	now := time.Date(2026, 9, 7, 4, 5, 6, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`select id from community_post where id=? and status='published' for update`).
		WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8))
	mock.ExpectExec(`insert into community_comment(post_id,author_id,content) values (?,?,?)`).
		WithArgs(int64(8), int64(4), "hello").WillReturnResult(sqlmock.NewResult(41, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`
		select c.id,c.post_id,c.author_id,u.user_name,coalesce(u.display_name,''),c.content,c.status,c.created_at,c.updated_at
		from community_comment c join user_account u on u.id=c.author_id
		where c.id=? and c.status<>'deleted' and (? or c.status='published')`).
		WithArgs(int64(41), true).
		WillReturnRows(sqlmock.NewRows([]string{"id", "post_id", "author_id", "user_name", "display_name", "content", "status", "created_at", "updated_at"}).
			AddRow(41, 8, 4, "user", "", "hello", "published", now, now))

	comment, err := store.CreateComment(context.Background(), 8, 4, "hello")
	if err != nil || comment.ID != 41 {
		t.Fatalf("comment=%+v err=%v", comment, err)
	}
}

func TestCreateCommentRollsBackWhenPostIsNotPublished(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`select id from community_post where id=? and status='published' for update`).
		WithArgs(int64(8)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := store.CreateComment(context.Background(), 8, 4, "hello")
	if !errors.Is(err, community.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateCommentReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		rows       *sqlmock.Rows
		wantOK     bool
		wantReason string
	}{
		{
			name:   "committed exact identity",
			rows:   commentRows().AddRow(41, 8, 4, "user", "", "hello", "published", time.Now(), time.Now()),
			wantOK: true,
		},
		{name: "not committed", rows: commentRows(), wantReason: "connection lost during commit"},
		{
			name:       "mismatch",
			rows:       commentRows().AddRow(41, 9, 4, "user", "", "hello", "published", time.Now(), time.Now()),
			wantReason: "durable identity mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			commitErr := errors.New("connection lost during commit")
			mock.ExpectBegin()
			mock.ExpectQuery(`select id from community_post where id=? and status='published' for update`).WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8))
			mock.ExpectExec(`insert into community_comment(post_id,author_id,content) values (?,?,?)`).WithArgs(int64(8), int64(4), "hello").WillReturnResult(sqlmock.NewResult(41, 1))
			mock.ExpectCommit().WillReturnError(commitErr)
			mock.ExpectBegin()
			mock.ExpectQuery(commentQuery()).WithArgs(int64(41), true).WillReturnRows(test.rows)
			mock.ExpectRollback()

			comment, err := store.CreateComment(context.Background(), 8, 4, "hello")
			if test.wantOK {
				if err != nil || comment.ID != 41 {
					t.Fatalf("CreateComment() = (%+v, %v), want reconciled comment", comment, err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("CreateComment() error = %v, want unknown outcome with %q", err, test.wantReason)
			}
		})
	}
}

func TestCreatePostPropagatesLastInsertIDError(t *testing.T) {
	store, mock := newMockStore(t)
	want := errors.New("last insert id")
	mock.ExpectBegin()
	mock.ExpectExec(`
		insert into community_post(author_id,game_id,title,content)
		values (?,nullif(?,0),?,?)`).
		WithArgs(int64(3), int64(0), "title", "body").
		WillReturnResult(errorResult{lastInsertIDErr: want})
	mock.ExpectRollback()

	_, err := store.CreatePost(context.Background(), 3, entity.PostDraft{Title: "title", Content: "body"})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestCreatePostReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		rows       *sqlmock.Rows
		wantOK     bool
		wantReason string
	}{
		{
			name:   "committed exact identity",
			rows:   sqlmock.NewRows(postColumnNames()).AddRow(61, 3, "author", "Author", nil, nil, nil, "title", "body", "published", time.Now(), time.Now(), 0, 0, 0, 0, false, false, false),
			wantOK: true,
		},
		{name: "not committed", rows: sqlmock.NewRows(postColumnNames()), wantReason: "connection lost during commit"},
		{
			name:       "mismatch",
			rows:       sqlmock.NewRows(postColumnNames()).AddRow(61, 4, "other", "Other", nil, nil, nil, "title", "body", "published", time.Now(), time.Now(), 0, 0, 0, 0, false, false, false),
			wantReason: "durable identity mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			commitErr := errors.New("connection lost during commit")
			mock.ExpectBegin()
			mock.ExpectExec(`
			insert into community_post(author_id,game_id,title,content)
			values (?,nullif(?,0),?,?)`).WithArgs(int64(3), int64(0), "title", "body").WillReturnResult(sqlmock.NewResult(61, 1))
			mock.ExpectCommit().WillReturnError(commitErr)
			mock.ExpectBegin()
			mock.ExpectQuery(postByIDQuery()).WithArgs(int64(3), int64(3), int64(3), int64(61), true).WillReturnRows(test.rows)
			mock.ExpectRollback()

			post, err := store.CreatePost(context.Background(), 3, entity.PostDraft{Title: "title", Content: "body"})
			if test.wantOK {
				if err != nil || post.ID != 61 {
					t.Fatalf("CreatePost() = (%+v, %v), want reconciled post", post, err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("CreatePost() error = %v, want unknown outcome with %q", err, test.wantReason)
			}
		})
	}
}

func TestDeletePostChecksRowsAffected(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectExec(`update community_post set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`).
			WithArgs(int64(19)).WillReturnResult(sqlmock.NewResult(0, 0))
		if err := store.DeletePost(context.Background(), 19); !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("result error", func(t *testing.T) {
		store, mock := newMockStore(t)
		want := errors.New("rows affected")
		mock.ExpectExec(`update community_post set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`).
			WithArgs(int64(20)).WillReturnResult(errorResult{rowsAffectedErr: want})
		if err := store.DeletePost(context.Background(), 20); !errors.Is(err, want) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestUpdatePostMapsMissingAndAllowsExistingNoOp(t *testing.T) {
	query := `
		update community_post set game_id=nullif(?,0),title=?,content=?,updated_at=current_timestamp(6)
		where id=? and status<>'deleted'`

	t.Run("missing", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectExec(query).WithArgs(int64(0), "title", "body", int64(31)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`select exists(select 1 from community_post where id=? and status<>'deleted')`).
			WithArgs(int64(31)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

		_, err := store.UpdatePost(context.Background(), 31, 9, entity.PostDraft{Title: "title", Content: "body"})
		if !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("UpdatePost() error = %v, want not found", err)
		}
	})

	t.Run("existing no-op", func(t *testing.T) {
		store, mock := newMockStore(t)
		now := time.Date(2026, 9, 7, 5, 6, 7, 0, time.UTC)
		mock.ExpectExec(query).WithArgs(int64(0), "title", "body", int64(32)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`select exists(select 1 from community_post where id=? and status<>'deleted')`).
			WithArgs(int64(32)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery(`select `+postColumns+`
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.id=? and p.status<>'deleted' and (? or p.status='published')`).
			WithArgs(int64(9), int64(9), int64(9), int64(32), true).
			WillReturnRows(sqlmock.NewRows(postColumnNames()).
				AddRow(32, 9, "author", "Author", nil, nil, nil, "title", "body", "published", now, now, 0, 0, 0, 0, false, false, false))

		post, err := store.UpdatePost(context.Background(), 32, 9, entity.PostDraft{Title: "title", Content: "body"})
		if err != nil || post.ID != 32 {
			t.Fatalf("UpdatePost() = (%+v, %v)", post, err)
		}
	})
}

func TestUpdateCommentMapsMissingAndAllowsExistingNoOp(t *testing.T) {
	const updateQuery = `update community_comment set content=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`
	const existsQuery = `select exists(select 1 from community_comment where id=? and status<>'deleted')`

	t.Run("missing", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectExec(updateQuery).WithArgs("body", int64(41)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(existsQuery).WithArgs(int64(41)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

		_, err := store.UpdateComment(context.Background(), 41, "body")
		if !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("UpdateComment() error = %v, want not found", err)
		}
	})

	t.Run("existing no-op", func(t *testing.T) {
		store, mock := newMockStore(t)
		now := time.Date(2026, 9, 7, 5, 6, 7, 0, time.UTC)
		mock.ExpectExec(updateQuery).WithArgs("body", int64(42)).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(existsQuery).WithArgs(int64(42)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery(`
		select c.id,c.post_id,c.author_id,u.user_name,coalesce(u.display_name,''),c.content,c.status,c.created_at,c.updated_at
		from community_comment c join user_account u on u.id=c.author_id
		where c.id=? and c.status<>'deleted' and (? or c.status='published')`).
			WithArgs(int64(42), true).
			WillReturnRows(sqlmock.NewRows([]string{"id", "post_id", "author_id", "user_name", "display_name", "content", "status", "created_at", "updated_at"}).
				AddRow(42, 8, 4, "user", "", "body", "published", now, now))

		comment, err := store.UpdateComment(context.Background(), 42, "body")
		if err != nil || comment.ID != 42 {
			t.Fatalf("UpdateComment() = (%+v, %v)", comment, err)
		}
	})
}

func TestModerationMapsMissingAndAllowsExistingNoOp(t *testing.T) {
	tests := []struct {
		name        string
		updateQuery string
		existsQuery string
		moderate    func(*Store, context.Context, int64) error
	}{
		{
			name:        "post",
			updateQuery: `update community_post set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			existsQuery: `select exists(select 1 from community_post where id=? and status<>'deleted')`,
			moderate: func(store *Store, ctx context.Context, id int64) error {
				_, err := store.ModeratePost(ctx, id, entity.StatusHidden)
				return err
			},
		},
		{
			name:        "comment",
			updateQuery: `update community_comment set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			existsQuery: `select exists(select 1 from community_comment where id=? and status<>'deleted')`,
			moderate: func(store *Store, ctx context.Context, id int64) error {
				_, err := store.ModerateComment(ctx, id, entity.StatusHidden)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name+" missing", func(t *testing.T) {
			store, mock := newMockStore(t)
			mock.ExpectExec(test.updateQuery).WithArgs(entity.StatusHidden, int64(51)).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(test.existsQuery).WithArgs(int64(51)).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

			if err := test.moderate(store, context.Background(), 51); !errors.Is(err, community.ErrNotFound) {
				t.Fatalf("moderate error = %v, want not found", err)
			}
		})

		t.Run(test.name+" existing no-op", func(t *testing.T) {
			store, mock := newMockStore(t)
			mock.ExpectExec(test.updateQuery).WithArgs(entity.StatusHidden, int64(52)).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(test.existsQuery).WithArgs(int64(52)).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			if test.name == "post" {
				mock.ExpectQuery(`select `+postColumns+`
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.id=? and p.status<>'deleted' and (? or p.status='published')`).
					WithArgs(int64(0), int64(0), int64(0), int64(52), true).
					WillReturnRows(sqlmock.NewRows(postColumnNames()).AddRow(52, 4, "author", "Author", nil, nil, nil, "title", "body", "hidden", time.Now(), time.Now(), 0, 0, 0, 0, false, false, false))
			} else {
				mock.ExpectQuery(`
		select c.id,c.post_id,c.author_id,u.user_name,coalesce(u.display_name,''),c.content,c.status,c.created_at,c.updated_at
		from community_comment c join user_account u on u.id=c.author_id
		where c.id=? and c.status<>'deleted' and (? or c.status='published')`).
					WithArgs(int64(52), true).
					WillReturnRows(sqlmock.NewRows([]string{"id", "post_id", "author_id", "user_name", "display_name", "content", "status", "created_at", "updated_at"}).
						AddRow(52, 8, 4, "author", "Author", "body", "hidden", time.Now(), time.Now()))
			}

			if err := test.moderate(store, context.Background(), 52); err != nil {
				t.Fatalf("moderate error = %v", err)
			}
		})
	}
}

func TestSetReactionCommitsThenReadsPortableAggregate(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(`select id from community_post where id=? and status='published' for update`).
		WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8))
	mock.ExpectExec(`insert into community_reaction(post_id,user_id,reaction_type) values (?,?,?) on duplicate key update id=community_reaction.id`).
		WithArgs(int64(8), int64(4), entity.ReactionHelpful).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery(reactionSummaryQuery()).
		WithArgs(int64(4), int64(4), int64(4), int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"likes", "helpful", "funny", "viewer_like", "viewer_helpful", "viewer_funny"}).
			AddRow(2, 3, 1, 0, 1, 0))

	summary, err := store.SetReaction(context.Background(), 8, 4, entity.ReactionHelpful, true)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Counts[entity.ReactionHelpful] != 3 || len(summary.ViewerReactions) != 1 || summary.ViewerReactions[0] != entity.ReactionHelpful {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestSetReactionRollsBackOnWriteError(t *testing.T) {
	store, mock := newMockStore(t)
	want := errors.New("write")
	mock.ExpectBegin()
	mock.ExpectQuery(`select id from community_post where id=? and status='published' for update`).
		WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8))
	mock.ExpectExec(`delete from community_reaction where post_id=? and user_id=? and reaction_type=?`).
		WithArgs(int64(8), int64(4), entity.ReactionFunny).WillReturnError(want)
	mock.ExpectRollback()

	_, err := store.SetReaction(context.Background(), 8, 4, entity.ReactionFunny, false)
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestSetReactionReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		exists     bool
		wantOK     bool
		wantReason string
	}{
		{name: "committed exact state", exists: true, wantOK: true},
		{name: "not committed", exists: false, wantReason: "durable state mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			commitErr := errors.New("connection lost during commit")
			mock.ExpectBegin()
			mock.ExpectQuery(`select id from community_post where id=? and status='published' for update`).WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8))
			mock.ExpectExec(`insert into community_reaction(post_id,user_id,reaction_type) values (?,?,?) on duplicate key update id=community_reaction.id`).WithArgs(int64(8), int64(4), entity.ReactionLike).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit().WillReturnError(commitErr)
			mock.ExpectBegin()
			mock.ExpectQuery(`select exists(select 1 from community_reaction where post_id=? and user_id=? and reaction_type=?)`).WithArgs(int64(8), int64(4), entity.ReactionLike).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(test.exists))
			if test.wantOK {
				mock.ExpectQuery(reactionSummaryQuery()).WithArgs(int64(4), int64(4), int64(4), int64(8)).WillReturnRows(sqlmock.NewRows([]string{"likes", "helpful", "funny", "viewer_like", "viewer_helpful", "viewer_funny"}).AddRow(1, 0, 0, 1, 0, 0))
			}
			mock.ExpectRollback()

			summary, err := store.SetReaction(context.Background(), 8, 4, entity.ReactionLike, true)
			if test.wantOK {
				if err != nil || summary.Counts[entity.ReactionLike] != 1 {
					t.Fatalf("SetReaction() = (%+v, %v), want reconciled summary", summary, err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("SetReaction() error = %v, want unknown outcome with %q", err, test.wantReason)
			}
		})
	}
}

func TestUnsetReactionReconcilesAmbiguousCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		exists     bool
		wantOK     bool
		wantReason string
	}{
		{name: "committed exact state", exists: false, wantOK: true},
		{name: "not committed", exists: true, wantReason: "durable state mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			commitErr := errors.New("connection lost during commit")
			mock.ExpectBegin()
			mock.ExpectQuery(`select id from community_post where id=? and status='published' for update`).WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(8))
			mock.ExpectExec(`delete from community_reaction where post_id=? and user_id=? and reaction_type=?`).WithArgs(int64(8), int64(4), entity.ReactionLike).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit().WillReturnError(commitErr)
			mock.ExpectBegin()
			mock.ExpectQuery(`select exists(select 1 from community_reaction where post_id=? and user_id=? and reaction_type=?)`).WithArgs(int64(8), int64(4), entity.ReactionLike).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(test.exists))
			if test.wantOK {
				mock.ExpectQuery(reactionSummaryQuery()).WithArgs(int64(4), int64(4), int64(4), int64(8)).WillReturnRows(sqlmock.NewRows([]string{"likes", "helpful", "funny", "viewer_like", "viewer_helpful", "viewer_funny"}).AddRow(0, 0, 0, 0, 0, 0))
			}
			mock.ExpectRollback()

			summary, err := store.SetReaction(context.Background(), 8, 4, entity.ReactionLike, false)
			if test.wantOK {
				if err != nil || summary.Counts[entity.ReactionLike] != 0 || len(summary.ViewerReactions) != 0 {
					t.Fatalf("SetReaction() = (%+v, %v), want reconciled removal", summary, err)
				}
			} else if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, commitErr) || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("SetReaction() error = %v, want unknown outcome with %q", err, test.wantReason)
			}
		})
	}
}

func TestAmbiguousAutocommitWriteClassification(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{err: io.ErrUnexpectedEOF, want: true},
		{err: io.ErrShortWrite, want: true},
		{err: &temporaryNetworkError{}, want: true},
		{err: context.DeadlineExceeded, want: true},
		{err: context.Canceled, want: true},
		{err: drivermysql.ErrInvalidConn, want: true},
		{err: drivermysql.ErrPktSync, want: true},
		{err: drivermysql.ErrPktSyncMul, want: true},
		{err: drivermysql.ErrMalformPkt, want: true},
		{err: driver.ErrBadConn, want: false},
		{err: &drivermysql.MySQLError{Number: 1062}, want: false},
		{err: errors.New("validation"), want: false},
	}
	for _, test := range tests {
		if got := isAmbiguousAutocommitWriteError(test.err); got != test.want {
			t.Errorf("isAmbiguousAutocommitWriteError(%T) = %v, want %v", test.err, got, test.want)
		}
	}
}

func TestUpdatePostReturnsUnknownForAmbiguousAutocommitErrorWithoutReplay(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(`
		update community_post set game_id=nullif(?,0),title=?,content=?,updated_at=current_timestamp(6)
		where id=? and status<>'deleted'`).
		WithArgs(int64(0), "title", "body", int64(31)).
		WillReturnError(io.ErrUnexpectedEOF)

	_, err := store.UpdatePost(context.Background(), 31, 9, entity.PostDraft{Title: "title", Content: "body"})
	if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("UpdatePost() error = %v, want explicit unknown outcome wrapping transport error", err)
	}
}

func TestUpdatePostPreservesDeterministicWriteError(t *testing.T) {
	store, mock := newMockStore(t)
	want := &drivermysql.MySQLError{Number: 1452, Message: "foreign key constraint"}
	mock.ExpectExec(`
		update community_post set game_id=nullif(?,0),title=?,content=?,updated_at=current_timestamp(6)
		where id=? and status<>'deleted'`).
		WithArgs(int64(9), "title", "body", int64(31)).
		WillReturnError(want)

	_, err := store.UpdatePost(context.Background(), 31, 9, entity.PostDraft{GameID: 9, Title: "title", Content: "body"})
	if err != want || mysqltx.IsCommitOutcomeUnknown(err) {
		t.Fatalf("UpdatePost() error = %v, want deterministic MySQL error", err)
	}
}

func TestAutocommitMutationsReturnUnknownForAmbiguousErrorsWithoutReplay(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		args   []driver.Value
		mutate func(*Store) error
	}{
		{
			name:  "delete post",
			query: `update community_post set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{int64(31)},
			mutate: func(store *Store) error {
				return store.DeletePost(context.Background(), 31)
			},
		},
		{
			name:  "moderate post",
			query: `update community_post set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{entity.StatusHidden, int64(31)},
			mutate: func(store *Store) error {
				_, err := store.ModeratePost(context.Background(), 31, entity.StatusHidden)
				return err
			},
		},
		{
			name:  "update comment",
			query: `update community_comment set content=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{"body", int64(41)},
			mutate: func(store *Store) error {
				_, err := store.UpdateComment(context.Background(), 41, "body")
				return err
			},
		},
		{
			name:  "delete comment",
			query: `update community_comment set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{int64(41)},
			mutate: func(store *Store) error {
				return store.DeleteComment(context.Background(), 41)
			},
		},
		{
			name:  "moderate comment",
			query: `update community_comment set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{entity.StatusHidden, int64(41)},
			mutate: func(store *Store) error {
				_, err := store.ModerateComment(context.Background(), 41, entity.StatusHidden)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			mock.ExpectExec(test.query).WithArgs(test.args...).WillReturnError(drivermysql.ErrInvalidConn)

			err := test.mutate(store)
			if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, drivermysql.ErrInvalidConn) {
				t.Fatalf("mutation error = %v, want explicit unknown outcome wrapping driver error", err)
			}
		})
	}
}

func TestAutocommitMutationsPreserveDeterministicErrors(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		args   []driver.Value
		mutate func(*Store) error
	}{
		{
			name:  "delete post",
			query: `update community_post set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{int64(31)},
			mutate: func(store *Store) error {
				return store.DeletePost(context.Background(), 31)
			},
		},
		{
			name:  "moderate post",
			query: `update community_post set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{entity.StatusHidden, int64(31)},
			mutate: func(store *Store) error {
				_, err := store.ModeratePost(context.Background(), 31, entity.StatusHidden)
				return err
			},
		},
		{
			name:  "update comment",
			query: `update community_comment set content=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{"body", int64(41)},
			mutate: func(store *Store) error {
				_, err := store.UpdateComment(context.Background(), 41, "body")
				return err
			},
		},
		{
			name:  "delete comment",
			query: `update community_comment set status='deleted',deleted_at=current_timestamp(6),updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{int64(41)},
			mutate: func(store *Store) error {
				return store.DeleteComment(context.Background(), 41)
			},
		},
		{
			name:  "moderate comment",
			query: `update community_comment set status=?,updated_at=current_timestamp(6) where id=? and status<>'deleted'`,
			args:  []driver.Value{entity.StatusHidden, int64(41)},
			mutate: func(store *Store) error {
				_, err := store.ModerateComment(context.Background(), 41, entity.StatusHidden)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newMockStore(t)
			want := &drivermysql.MySQLError{Number: 1452, Message: "foreign key constraint"}
			mock.ExpectExec(test.query).WithArgs(test.args...).WillReturnError(want)

			err := test.mutate(store)
			if err != want || mysqltx.IsCommitOutcomeUnknown(err) {
				t.Fatalf("mutation error = %v, want deterministic MySQL error", err)
			}
		})
	}
}

type temporaryNetworkError struct{}

func (*temporaryNetworkError) Error() string   { return "network reset" }
func (*temporaryNetworkError) Timeout() bool   { return false }
func (*temporaryNetworkError) Temporary() bool { return true }

func TestRequireExistingAffectedDistinguishesNoOpFromMissing(t *testing.T) {
	t.Run("existing no-op is successful", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectQuery(`select exists(select 1 from community_post where id=? and status<>'deleted')`).
			WithArgs(int64(17)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		if err := store.requirePostAffected(context.Background(), sqlmock.NewResult(0, 0), 17); err != nil {
			t.Fatalf("requirePostAffected() error = %v", err)
		}
	})

	t.Run("missing row maps to domain error", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectQuery(`select exists(select 1 from community_comment where id=? and status<>'deleted')`).
			WithArgs(int64(18)).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

		if err := store.requireCommentAffected(context.Background(), sqlmock.NewResult(0, 0), 18); !errors.Is(err, community.ErrNotFound) {
			t.Fatalf("requireCommentAffected() error = %v, want not found", err)
		}
	})
}

func postColumnNames() []string {
	return []string{
		"id", "author_id", "user_name", "display_name", "game_id", "slug", "game_name",
		"title", "content", "status", "created_at", "updated_at", "comment_count",
		"likes", "helpful", "funny", "viewer_like", "viewer_helpful", "viewer_funny",
	}
}

func postByIDQuery() string {
	return `select ` + postColumns + `
		from community_post p
		join user_account u on u.id=p.author_id
		left join game g on g.id=p.game_id
		where p.id=? and p.status<>'deleted' and (? or p.status='published')`
}

func reactionSummaryQuery() string {
	return `
		select
			count(case when reaction_type='like' then 1 end),
			count(case when reaction_type='helpful' then 1 end),
			count(case when reaction_type='funny' then 1 end),
			coalesce(max(case when user_id=? and reaction_type='like' then 1 else 0 end),0),
			coalesce(max(case when user_id=? and reaction_type='helpful' then 1 else 0 end),0),
			coalesce(max(case when user_id=? and reaction_type='funny' then 1 else 0 end),0)
		from community_reaction where post_id=?`
}

func commentQuery() string {
	return `
		select c.id,c.post_id,c.author_id,u.user_name,coalesce(u.display_name,''),c.content,c.status,c.created_at,c.updated_at
		from community_comment c join user_account u on u.id=c.author_id
		where c.id=? and c.status<>'deleted' and (? or c.status='published')`
}

func commentRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "post_id", "author_id", "user_name", "display_name", "content", "status", "created_at", "updated_at"})
}

type errorResult struct {
	lastInsertIDErr error
	rowsAffectedErr error
}

func (r errorResult) LastInsertId() (int64, error) { return 0, r.lastInsertIDErr }
func (r errorResult) RowsAffected() (int64, error) { return 0, r.rowsAffectedErr }
