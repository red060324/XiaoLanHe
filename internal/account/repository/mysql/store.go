package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/account/entity"
	account "github.com/red060324/XiaoLanHe/internal/account/usecase"
	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

const (
	sha256HexLength       = 64
	reconciliationTimeout = 5 * time.Second
)

var errDurableStateMismatch = errors.New("mysql durable state does not match attempted account write")

type registrationIdentity struct {
	user      entity.User
	sessionID int64
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Register(ctx context.Context, username, displayName, passwordHash, tokenHash string, expiresAt time.Time) (entity.User, error) {
	tokenDigest, err := decodeTokenHash(tokenHash)
	if err != nil {
		return entity.User{}, err
	}
	expiresAt = expiresAt.UTC()

	var attempted registrationIdentity
	user, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.User, error) {
		result, err := tx.ExecContext(ctx, `
			insert into user_account(user_name,display_name,password_hash,role,status)
			values (?,?,?,'user','active')`, username, displayName, passwordHash)
		if err != nil {
			if duplicateKey(err) {
				return entity.User{}, account.ErrConflict
			}
			return entity.User{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return entity.User{}, err
		}
		if err := requireOneAffected(result, "insert user account"); err != nil {
			return entity.User{}, err
		}
		created := entity.User{
			ID:          id,
			Username:    username,
			DisplayName: displayName,
			Role:        auth.RoleUser,
			Status:      "active",
		}
		sessionResult, err := tx.ExecContext(ctx, `insert into user_session(user_id,token_hash,expires_at) values (?,?,?)`, created.ID, tokenDigest, expiresAt)
		if err != nil {
			return entity.User{}, err
		}
		sessionID, err := sessionResult.LastInsertId()
		if err != nil {
			return entity.User{}, err
		}
		if err := requireOneAffected(sessionResult, "insert registration session"); err != nil {
			return entity.User{}, err
		}
		attempted = registrationIdentity{user: created, sessionID: sessionID}
		return created, nil
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return user, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileRegistration(reconcileCtx, attempted, passwordHash, tokenDigest, expiresAt, err)
}

func (s *Store) reconcileRegistration(ctx context.Context, attempted registrationIdentity, passwordHash string, tokenDigest []byte, expiresAt time.Time, commitErr error) (entity.User, error) {
	var persisted entity.User
	var persistedPassword string
	var persistedSessionID, persistedSessionUserID sql.NullInt64
	var persistedToken []byte
	var persistedExpiry sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		select u.id,u.user_name,coalesce(u.display_name,''),coalesce(u.password_hash,''),u.role,u.status,
		       s.id,s.user_id,s.token_hash,s.expires_at
		from user_account u
		left join user_session s on s.id=? and s.revoked_at is null
		where u.id=?`, attempted.sessionID, attempted.user.ID).Scan(
		&persisted.ID, &persisted.Username, &persisted.DisplayName, &persistedPassword, &persisted.Role, &persisted.Status,
		&persistedSessionID, &persistedSessionUserID, &persistedToken, &persistedExpiry,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.User{}, commitErr
	}
	if err != nil {
		return entity.User{}, errors.Join(commitErr, fmt.Errorf("reconcile registration: %w", err))
	}
	if persisted != attempted.user || persistedPassword != passwordHash ||
		!persistedSessionID.Valid || persistedSessionID.Int64 != attempted.sessionID ||
		!persistedSessionUserID.Valid || persistedSessionUserID.Int64 != attempted.user.ID ||
		!equalDigest(persistedToken, tokenDigest) || !persistedExpiry.Valid || !sameMySQLTime(persistedExpiry.Time, expiresAt) {
		return entity.User{}, errors.Join(commitErr, errDurableStateMismatch)
	}
	return persisted, nil
}

func (s *Store) FindCredential(ctx context.Context, username string) (user entity.User, passwordHash string, err error) {
	err = s.db.QueryRowContext(ctx, `
		select id,user_name,coalesce(display_name,''),role,status,coalesce(password_hash,'')
		from user_account where user_name=?`, username).
		Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Status, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) || err == nil && passwordHash == "" {
		return entity.User{}, "", account.ErrInvalidCredentials
	}
	return user, passwordHash, err
}

func (s *Store) ReplaceSession(ctx context.Context, userID int64, currentTokenHash, newTokenHash string, expiresAt time.Time) (err error) {
	var currentTokenDigest []byte
	if currentTokenHash != "" {
		currentTokenDigest, err = decodeTokenHash(currentTokenHash)
		if err != nil {
			return err
		}
	}
	newTokenDigest, err := decodeTokenHash(newTokenHash)
	if err != nil {
		return err
	}

	expiresAt = expiresAt.UTC()
	var attemptedSessionID int64
	_, err = mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		if currentTokenHash != "" {
			result, err := tx.ExecContext(ctx, `update user_session set revoked_at=coalesce(revoked_at,current_timestamp(6)) where token_hash=?`, currentTokenDigest)
			if err != nil {
				return struct{}{}, err
			}
			if err := requireAtMostOneAffected(result, "revoke replaced session"); err != nil {
				return struct{}{}, err
			}
		}
		result, err := tx.ExecContext(ctx, `insert into user_session(user_id,token_hash,expires_at) values (?,?,?)`, userID, newTokenDigest, expiresAt)
		if err != nil {
			return struct{}{}, err
		}
		attemptedSessionID, err = result.LastInsertId()
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, requireOneAffected(result, "insert replacement session")
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileReplacementSession(reconcileCtx, attemptedSessionID, userID, currentTokenDigest, newTokenDigest, expiresAt, err)
}

func (s *Store) reconcileReplacementSession(ctx context.Context, sessionID, userID int64, currentTokenDigest, newTokenDigest []byte, expiresAt time.Time, commitErr error) error {
	var persistedUserID int64
	var persistedSessionID int64
	var persistedToken []byte
	var persistedExpiry time.Time
	var replacementActive bool
	var priorTokenInactive bool
	err := s.db.QueryRowContext(ctx, `
		select id,user_id,token_hash,expires_at,revoked_at is null,
		       (? or not exists(
		           select 1 from user_session prior where prior.token_hash=? and prior.revoked_at is null
		       ))
		from user_session where id=?`, len(currentTokenDigest) == 0, currentTokenDigest, sessionID).Scan(
		&persistedSessionID, &persistedUserID, &persistedToken, &persistedExpiry, &replacementActive, &priorTokenInactive,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return commitErr
	}
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile replacement session: %w", err))
	}
	if persistedSessionID != sessionID || persistedUserID != userID || !equalDigest(persistedToken, newTokenDigest) ||
		!sameMySQLTime(persistedExpiry, expiresAt) || !replacementActive || !priorTokenInactive {
		return errors.Join(commitErr, errDurableStateMismatch)
	}
	return nil
}

func (s *Store) FindSession(ctx context.Context, tokenHash string, now time.Time) (principal auth.Principal, err error) {
	tokenDigest, err := decodeTokenHash(tokenHash)
	if err != nil {
		return auth.Principal{}, err
	}
	err = s.db.QueryRowContext(ctx, `
		select u.id,u.user_name,coalesce(u.display_name,''),u.role
		from user_session s join user_account u on u.id=s.user_id
		where s.token_hash=? and s.revoked_at is null and s.expires_at>? and u.status='active'`, tokenDigest, now.UTC()).
		Scan(&principal.UserID, &principal.Username, &principal.DisplayName, &principal.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Principal{}, account.ErrUnauthenticated
	}
	return principal, err
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	tokenDigest, err := decodeTokenHash(tokenHash)
	if err != nil {
		return err
	}
	_, err = mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (struct{}, error) {
		result, err := tx.ExecContext(ctx, `update user_session set revoked_at=coalesce(revoked_at,current_timestamp(6)) where token_hash=?`, tokenDigest)
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, requireAtMostOneAffected(result, "revoke session")
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return err
	}

	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	var revokedAt sql.NullTime
	reconcileErr := s.db.QueryRowContext(reconcileCtx, `select revoked_at from user_session where token_hash=?`, tokenDigest).Scan(&revokedAt)
	switch {
	case errors.Is(reconcileErr, sql.ErrNoRows), reconcileErr == nil && revokedAt.Valid:
		return nil
	case reconcileErr != nil:
		return errors.Join(err, fmt.Errorf("reconcile session revocation: %w", reconcileErr))
	default:
		return err
	}
}

func reconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), reconciliationTimeout)
}

func requireOneAffected(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%s affected %d rows, want 1", operation, affected)
	}
	return nil
}

func requireAtMostOneAffected(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", operation, err)
	}
	if affected > 1 {
		return fmt.Errorf("%s affected %d rows, want at most 1", operation, affected)
	}
	return nil
}

func equalDigest(left, right []byte) bool {
	return len(left) == len(right) && string(left) == string(right)
}

func sameMySQLTime(left, right time.Time) bool {
	delta := left.UTC().Sub(right.UTC())
	if delta < 0 {
		delta = -delta
	}
	return delta < time.Microsecond
}

func decodeTokenHash(value string) ([]byte, error) {
	if len(value) != sha256HexLength {
		return nil, fmt.Errorf("invalid token hash length: got %d, want %d hexadecimal characters", len(value), sha256HexLength)
	}
	digest, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode token hash: %w", err)
	}
	return digest, nil
}

func duplicateKey(err error) bool {
	var mysqlErr *drivermysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

var _ account.Store = (*Store)(nil)
