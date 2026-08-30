package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/account/entity"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Register(ctx context.Context, username, displayName, passwordHash, tokenHash string, expiresAt time.Time) (user entity.User, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return entity.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		insert into user_account(user_name,display_name,password_hash,role,status)
		values ($1,$2,$3,'user','active')
		returning id,user_name,coalesce(display_name,''),role,status`, username, displayName, passwordHash).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Status)
	if err != nil {
		if uniqueViolation(err) {
			return entity.User{}, account.ErrConflict
		}
		return entity.User{}, err
	}
	if _, err = tx.Exec(ctx, `insert into user_session(user_id,token_hash,expires_at) values ($1,$2,$3)`, user.ID, tokenHash, expiresAt); err != nil {
		return entity.User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return entity.User{}, err
	}
	return user, nil
}

func (s *Store) FindCredential(ctx context.Context, username string) (user entity.User, passwordHash string, err error) {
	err = s.pool.QueryRow(ctx, `
		select id,user_name,coalesce(display_name,''),role,status,coalesce(password_hash,'')
		from user_account where lower(user_name)=lower($1)`, username).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Status, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) || passwordHash == "" {
		return entity.User{}, "", account.ErrInvalidCredentials
	}
	return user, passwordHash, err
}

func (s *Store) ReplaceSession(ctx context.Context, userID int64, currentTokenHash, newTokenHash string, expiresAt time.Time) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if currentTokenHash != "" {
		if _, err = tx.Exec(ctx, `update user_session set revoked_at=coalesce(revoked_at,now()) where token_hash=$1`, currentTokenHash); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `insert into user_session(user_id,token_hash,expires_at) values ($1,$2,$3)`, userID, newTokenHash, expiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FindSession(ctx context.Context, tokenHash string, now time.Time) (principal auth.Principal, err error) {
	err = s.pool.QueryRow(ctx, `
		select u.id,u.user_name,coalesce(u.display_name,''),u.role
		from user_session s join user_account u on u.id=s.user_id
		where s.token_hash=$1 and s.revoked_at is null and s.expires_at>$2 and u.status='active'`, tokenHash, now).
		Scan(&principal.UserID, &principal.Username, &principal.DisplayName, &principal.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Principal{}, account.ErrUnauthenticated
	}
	return principal, err
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `update user_session set revoked_at=coalesce(revoked_at,now()) where token_hash=$1`, tokenHash)
	return err
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ account.Store = (*Store)(nil)
