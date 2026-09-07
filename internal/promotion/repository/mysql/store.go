package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const promotionReconciliationTimeout = 5 * time.Second

const (
	lockClaimUserSQL = `
		select id from user_account where id=? for update`
	findClaimByIdempotencySQL = `
		select cl.id,cl.coupon_id,d.code,cl.user_id,cl.status,cl.idempotency_key,cl.claimed_at
		from coupon_claim cl join coupon_definition d on d.id=cl.coupon_id
		where cl.user_id=? and cl.idempotency_key=?`
	lockClaimCouponSQL = `
		select d.id,d.code,d.name,d.discount_type,coalesce(d.fixed_minor,0),coalesce(d.percentage_bps,0),
			d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
			coalesce(d.game_id,0),coalesce(d.edition_id,0),c.status,c.starts_at,c.ends_at,
			(select count(*) from coupon_claim cl where cl.coupon_id=d.id and cl.user_id=? and cl.status in ('claimed','redeemed'))
		from coupon_definition d join coupon_campaign c on c.id=d.campaign_id
		where d.code=?
		for update`
	databaseNowSQL = `select utc_timestamp(6)`
	insertClaimSQL = `
		insert into coupon_claim(coupon_id,user_id,status,idempotency_key,claimed_at)
		values (?,?,'claimed',?,?)`
	incrementClaimedStockSQL = `
		update coupon_definition set claimed_stock=claimed_stock+1,updated_at=? where id=?`
)

func (s *Store) List(ctx context.Context, filter promotion.ListFilter) ([]entity.Coupon, error) {
	rows, err := s.db.QueryContext(ctx, `
		select d.id,d.code,d.name,d.discount_type,coalesce(d.fixed_minor,0),coalesce(d.percentage_bps,0),
			d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
			coalesce(d.game_id,0),coalesce(d.edition_id,0),c.status,c.starts_at,c.ends_at,
			(select count(*) from coupon_claim cl where cl.coupon_id=d.id and cl.user_id=? and cl.status in ('claimed','redeemed'))
		from coupon_definition d join coupon_campaign c on c.id=d.campaign_id
		where c.status='active' and c.starts_at<=? and c.ends_at>?
			and (?=0 or d.game_id is null or d.game_id=?)
			and (?=0 or d.id<?)
		order by d.id desc limit ?`,
		filter.ViewerID, filter.Now.UTC(), filter.Now.UTC(),
		filter.GameID, filter.GameID, filter.BeforeID, filter.BeforeID, filter.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entity.Coupon, 0, filter.Limit)
	for rows.Next() {
		coupon, err := scanCoupon(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, coupon)
	}
	return items, rows.Err()
}

func (s *Store) ListClaims(ctx context.Context, filter promotion.ClaimFilter) ([]entity.Claim, error) {
	rows, err := s.db.QueryContext(ctx, `
		select cl.id,cl.coupon_id,d.code,cl.user_id,cl.status,cl.idempotency_key,cl.claimed_at
		from coupon_claim cl
		join coupon_definition d on d.id=cl.coupon_id
		join coupon_campaign c on c.id=d.campaign_id
		where cl.user_id=? and cl.status='claimed'
			and c.status='active' and c.starts_at<=? and c.ends_at>?
			and not exists(select 1 from purchase_order o where o.coupon_claim_id=cl.id)
			and (?=0 or cl.id<?)
		order by cl.id desc limit ?`,
		filter.UserID, filter.Now.UTC(), filter.Now.UTC(), filter.BeforeID, filter.BeforeID, filter.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entity.Claim, 0, filter.Limit)
	for rows.Next() {
		var claim entity.Claim
		if err := scanClaim(rows, &claim); err != nil {
			return nil, err
		}
		items = append(items, claim)
	}
	return items, rows.Err()
}

func (s *Store) Claim(ctx context.Context, command promotion.ClaimCommand) (promotion.ClaimResult, error) {
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (promotion.ClaimResult, error) {
		return claim(ctx, tx, command)
	})
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return result, err
	}

	ambiguousErr := err
	reconcileCtx, cancel := promotionReconciliationContext(ctx)
	defer cancel()
	result, err = mysqltx.Run(reconcileCtx, s.db, func(tx *sql.Tx) (promotion.ClaimResult, error) {
		return reconcileClaim(reconcileCtx, tx, command)
	})
	if errors.Is(err, errClaimNotDurable) {
		return promotion.ClaimResult{}, ambiguousErr
	}
	if err != nil {
		return promotion.ClaimResult{}, errors.Join(ambiguousErr, fmt.Errorf("reconcile ambiguous coupon claim: %w", err))
	}
	return result, nil
}

func promotionReconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), promotionReconciliationTimeout)
}

var errClaimNotDurable = errors.New("coupon claim is not durable")

func reconcileClaim(ctx context.Context, tx *sql.Tx, command promotion.ClaimCommand) (promotion.ClaimResult, error) {
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, lockClaimUserSQL, command.UserID).Scan(&lockedUserID); err != nil {
		return promotion.ClaimResult{}, err
	}

	var existing entity.Claim
	err := scanClaim(tx.QueryRowContext(ctx, findClaimByIdempotencySQL, command.UserID, command.IdempotencyKey), &existing)
	if errors.Is(err, sql.ErrNoRows) {
		return promotion.ClaimResult{}, errClaimNotDurable
	}
	if err != nil {
		return promotion.ClaimResult{}, err
	}
	if existing.CouponCode != command.Code {
		return promotion.ClaimResult{}, promotion.ErrIdempotencyConflict
	}
	return promotion.ClaimResult{Claim: existing, Replayed: true}, nil
}

func claim(ctx context.Context, tx *sql.Tx, command promotion.ClaimCommand) (promotion.ClaimResult, error) {
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, lockClaimUserSQL, command.UserID).Scan(&lockedUserID); err != nil {
		return promotion.ClaimResult{}, err
	}

	var existing entity.Claim
	err := scanClaim(tx.QueryRowContext(ctx, findClaimByIdempotencySQL, command.UserID, command.IdempotencyKey), &existing)
	if err == nil {
		if existing.CouponCode != command.Code {
			return promotion.ClaimResult{}, promotion.ErrIdempotencyConflict
		}
		return promotion.ClaimResult{Claim: existing, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return promotion.ClaimResult{}, err
	}

	coupon, err := scanCoupon(tx.QueryRowContext(ctx, lockClaimCouponSQL, command.UserID, command.Code))
	if errors.Is(err, sql.ErrNoRows) {
		return promotion.ClaimResult{}, promotion.ErrNotFound
	}
	if err != nil {
		return promotion.ClaimResult{}, err
	}

	// Read the database clock only after the campaign and coupon locks have been
	// acquired. A transaction that waited on either row must validate the newly
	// committed campaign window, not the time at which its locking read began.
	var effectiveNow time.Time
	if err := tx.QueryRowContext(ctx, databaseNowSQL).Scan(&effectiveNow); err != nil {
		return promotion.ClaimResult{}, err
	}
	effectiveNow = effectiveNow.UTC()
	if err := coupon.ValidateClaim(effectiveNow); err != nil {
		return promotion.ClaimResult{}, err
	}
	if coupon.ViewerClaimCount >= coupon.PerUserLimit {
		return promotion.ClaimResult{}, promotion.ErrClaimLimit
	}

	claim := entity.Claim{
		CouponID:       coupon.ID,
		CouponCode:     coupon.Code,
		UserID:         command.UserID,
		Status:         "claimed",
		IdempotencyKey: command.IdempotencyKey,
		ClaimedAt:      effectiveNow,
	}
	insertResult, err := tx.ExecContext(ctx, insertClaimSQL, claim.CouponID, claim.UserID, claim.IdempotencyKey, claim.ClaimedAt)
	if err != nil {
		return promotion.ClaimResult{}, err
	}
	claim.ID, err = insertResult.LastInsertId()
	if err != nil {
		return promotion.ClaimResult{}, err
	}

	updateResult, err := tx.ExecContext(ctx, incrementClaimedStockSQL,
		effectiveNow, coupon.ID,
	)
	if err != nil {
		return promotion.ClaimResult{}, err
	}
	updated, err := updateResult.RowsAffected()
	if err != nil {
		return promotion.ClaimResult{}, err
	}
	if updated != 1 {
		return promotion.ClaimResult{}, fmt.Errorf("update coupon stock: affected %d rows", updated)
	}
	return promotion.ClaimResult{Claim: claim}, nil
}

func (s *Store) FindClaimCoupon(ctx context.Context, userID, claimID int64) (entity.Claim, entity.Coupon, error) {
	var claim entity.Claim
	var coupon entity.Coupon
	err := s.db.QueryRowContext(ctx, `
		select cl.id,cl.coupon_id,d.code,cl.user_id,cl.status,cl.idempotency_key,cl.claimed_at,
			d.id,d.code,d.name,d.discount_type,coalesce(d.fixed_minor,0),coalesce(d.percentage_bps,0),
			d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
			coalesce(d.game_id,0),coalesce(d.edition_id,0),c.status,c.starts_at,c.ends_at
		from coupon_claim cl
		join coupon_definition d on d.id=cl.coupon_id
		join coupon_campaign c on c.id=d.campaign_id
		where cl.id=? and cl.user_id=?`, claimID, userID).Scan(
		&claim.ID, &claim.CouponID, &claim.CouponCode, &claim.UserID, &claim.Status, &claim.IdempotencyKey, &claim.ClaimedAt,
		&coupon.ID, &coupon.Code, &coupon.Name, &coupon.DiscountType, &coupon.FixedMinor, &coupon.PercentageBps,
		&coupon.Currency, &coupon.MinimumMinor, &coupon.TotalStock, &coupon.ClaimedStock, &coupon.PerUserLimit,
		&coupon.GameID, &coupon.EditionID, &coupon.CampaignStatus, &coupon.StartsAt, &coupon.EndsAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Claim{}, entity.Coupon{}, promotion.ErrNotFound
	}
	return claim, coupon, err
}

type scanner interface{ Scan(...any) error }

func scanClaim(row scanner, claim *entity.Claim) error {
	return row.Scan(
		&claim.ID, &claim.CouponID, &claim.CouponCode, &claim.UserID,
		&claim.Status, &claim.IdempotencyKey, &claim.ClaimedAt,
	)
}

func scanCoupon(row scanner) (entity.Coupon, error) {
	var coupon entity.Coupon
	err := row.Scan(
		&coupon.ID, &coupon.Code, &coupon.Name, &coupon.DiscountType, &coupon.FixedMinor, &coupon.PercentageBps,
		&coupon.Currency, &coupon.MinimumMinor, &coupon.TotalStock, &coupon.ClaimedStock, &coupon.PerUserLimit,
		&coupon.GameID, &coupon.EditionID, &coupon.CampaignStatus, &coupon.StartsAt, &coupon.EndsAt, &coupon.ViewerClaimCount,
	)
	return coupon, err
}

var _ promotion.Store = (*Store)(nil)
