package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/promotion/entity"
	promotion "github.com/red060324/XiaoLanHe/internal/promotion/usecase"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) List(ctx context.Context, filter promotion.ListFilter) ([]entity.Coupon, error) {
	rows, err := s.pool.Query(ctx, `
		select d.id,d.code,d.name,d.discount_type,coalesce(d.fixed_minor,0),coalesce(d.percentage_bps,0),
			d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
			coalesce(d.game_id,0),coalesce(d.edition_id,0),c.status,c.starts_at,c.ends_at,
			(select count(*) from coupon_claim cl where cl.coupon_id=d.id and cl.user_id=$1 and cl.status in ('claimed','redeemed'))
		from coupon_definition d join coupon_campaign c on c.id=d.campaign_id
		where c.status='active' and c.starts_at<=$2 and c.ends_at>$2
			and ($3::bigint=0 or d.game_id is null or d.game_id=$3)
			and ($4::bigint=0 or d.id<$4)
		order by d.id desc limit $5`, filter.ViewerID, filter.Now, filter.GameID, filter.BeforeID, filter.Limit)
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
	rows, err := s.pool.Query(ctx, `
		select cl.id,cl.coupon_id,d.code,cl.user_id,cl.status,cl.idempotency_key,cl.claimed_at
		from coupon_claim cl
		join coupon_definition d on d.id=cl.coupon_id
		join coupon_campaign c on c.id=d.campaign_id
		where cl.user_id=$1 and cl.status='claimed'
			and c.status='active' and c.starts_at<=$2 and c.ends_at>$2
			and not exists(select 1 from purchase_order o where o.coupon_claim_id=cl.id)
			and ($3::bigint=0 or cl.id<$3)
		order by cl.id desc limit $4`, filter.UserID, filter.Now, filter.BeforeID, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]entity.Claim, 0, filter.Limit)
	for rows.Next() {
		var claim entity.Claim
		if err := rows.Scan(&claim.ID, &claim.CouponID, &claim.CouponCode, &claim.UserID, &claim.Status, &claim.IdempotencyKey, &claim.ClaimedAt); err != nil {
			return nil, err
		}
		items = append(items, claim)
	}
	return items, rows.Err()
}

func (s *Store) Claim(ctx context.Context, command promotion.ClaimCommand) (result promotion.ClaimResult, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return promotion.ClaimResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, command.UserID); err != nil {
		return promotion.ClaimResult{}, err
	}
	var existing entity.Claim
	err = tx.QueryRow(ctx, `
		select cl.id,cl.coupon_id,d.code,cl.user_id,cl.status,cl.idempotency_key,cl.claimed_at
		from coupon_claim cl join coupon_definition d on d.id=cl.coupon_id
		where cl.user_id=$1 and cl.idempotency_key=$2`, command.UserID, command.IdempotencyKey).
		Scan(&existing.ID, &existing.CouponID, &existing.CouponCode, &existing.UserID, &existing.Status, &existing.IdempotencyKey, &existing.ClaimedAt)
	if err == nil {
		if existing.CouponCode != command.Code {
			return promotion.ClaimResult{}, promotion.ErrIdempotencyConflict
		}
		return promotion.ClaimResult{Claim: existing, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return promotion.ClaimResult{}, err
	}

	coupon, err := scanCoupon(tx.QueryRow(ctx, `
		select d.id,d.code,d.name,d.discount_type,coalesce(d.fixed_minor,0),coalesce(d.percentage_bps,0),
			d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
			coalesce(d.game_id,0),coalesce(d.edition_id,0),c.status,c.starts_at,c.ends_at,
			(select count(*) from coupon_claim cl where cl.coupon_id=d.id and cl.user_id=$2 and cl.status in ('claimed','redeemed'))
		from coupon_definition d join coupon_campaign c on c.id=d.campaign_id
		where upper(d.code)=upper($1)
		for update of d
		for share of c`, command.Code, command.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return promotion.ClaimResult{}, promotion.ErrNotFound
	}
	if err != nil {
		return promotion.ClaimResult{}, err
	}
	if err := coupon.ValidateClaim(command.Now); err != nil {
		return promotion.ClaimResult{}, err
	}
	if coupon.ViewerClaimCount >= coupon.PerUserLimit {
		return promotion.ClaimResult{}, promotion.ErrClaimLimit
	}

	claim := entity.Claim{CouponID: coupon.ID, CouponCode: coupon.Code, UserID: command.UserID, Status: "claimed", IdempotencyKey: command.IdempotencyKey, ClaimedAt: command.Now}
	if err := tx.QueryRow(ctx, `
		insert into coupon_claim(coupon_id,user_id,status,idempotency_key,claimed_at)
		values ($1,$2,'claimed',$3,$4) returning id`, claim.CouponID, claim.UserID, claim.IdempotencyKey, claim.ClaimedAt).Scan(&claim.ID); err != nil {
		return promotion.ClaimResult{}, err
	}
	if _, err := tx.Exec(ctx, `update coupon_definition set claimed_stock=claimed_stock+1,updated_at=$2 where id=$1`, coupon.ID, command.Now); err != nil {
		return promotion.ClaimResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return promotion.ClaimResult{}, err
	}
	return promotion.ClaimResult{Claim: claim}, nil
}

func (s *Store) FindClaimCoupon(ctx context.Context, userID, claimID int64) (entity.Claim, entity.Coupon, error) {
	var claim entity.Claim
	var coupon entity.Coupon
	err := s.pool.QueryRow(ctx, `
		select cl.id,cl.coupon_id,d.code,cl.user_id,cl.status,cl.idempotency_key,cl.claimed_at,
			d.id,d.code,d.name,d.discount_type,coalesce(d.fixed_minor,0),coalesce(d.percentage_bps,0),
			d.currency,d.minimum_minor,d.total_stock,d.claimed_stock,d.per_user_limit,
			coalesce(d.game_id,0),coalesce(d.edition_id,0),c.status,c.starts_at,c.ends_at
		from coupon_claim cl
		join coupon_definition d on d.id=cl.coupon_id
		join coupon_campaign c on c.id=d.campaign_id
		where cl.id=$1 and cl.user_id=$2`, claimID, userID).Scan(
		&claim.ID, &claim.CouponID, &claim.CouponCode, &claim.UserID, &claim.Status, &claim.IdempotencyKey, &claim.ClaimedAt,
		&coupon.ID, &coupon.Code, &coupon.Name, &coupon.DiscountType, &coupon.FixedMinor, &coupon.PercentageBps,
		&coupon.Currency, &coupon.MinimumMinor, &coupon.TotalStock, &coupon.ClaimedStock, &coupon.PerUserLimit,
		&coupon.GameID, &coupon.EditionID, &coupon.CampaignStatus, &coupon.StartsAt, &coupon.EndsAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Claim{}, entity.Coupon{}, promotion.ErrNotFound
	}
	return claim, coupon, err
}

type scanner interface{ Scan(...any) error }

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
