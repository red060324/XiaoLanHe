package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/order/entity"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) FindByIdempotency(ctx context.Context, userID int64, key string) (entity.Order, error) {
	return queryOrder(ctx, s.pool, `o.user_id=$1 and o.idempotency_key=$2`, userID, key)
}

func (s *Store) Get(ctx context.Context, orderNo string) (entity.Order, error) {
	return queryOrder(ctx, s.pool, `o.order_no=$1`, orderNo)
}

func (s *Store) List(ctx context.Context, filter order.ListFilter) ([]entity.Order, error) {
	rows, err := s.pool.Query(ctx, orderSelect+`
		where o.user_id=$1 and ($2::timestamptz is null or (o.created_at,o.id)<($2,$3))
		order by o.created_at desc,o.id desc limit $4`, filter.UserID, nullableTime(filter.Cursor.CreatedAt), filter.Cursor.ID, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]entity.Order, 0, filter.Limit)
	for rows.Next() {
		item, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) Create(ctx context.Context, command order.CreateCommand) (result order.CreateResult, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return order.CreateResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, command.UserID); err != nil {
		return order.CreateResult{}, err
	}
	existing, err := queryOrder(ctx, tx, `o.user_id=$1 and o.idempotency_key=$2`, command.UserID, command.IdempotencyKey)
	if err == nil {
		if !matchesCommand(existing, command) {
			return order.CreateResult{}, order.ErrIdempotencyConflict
		}
		return order.CreateResult{Order: existing, Replayed: true}, nil
	}
	if !errors.Is(err, order.ErrNotFound) {
		return order.CreateResult{}, err
	}
	var owned bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from game_entitlement where user_id=$1 and edition_id=$2 and status='active')`, command.UserID, command.Offer.EditionID).Scan(&owned); err != nil {
		return order.CreateResult{}, err
	}
	if owned {
		return order.CreateResult{}, order.ErrAlreadyOwned
	}
	if command.Quote.ClaimID > 0 {
		var claimUserID int64
		var status string
		if err := tx.QueryRow(ctx, `select user_id,status from coupon_claim where id=$1 for update`, command.Quote.ClaimID).Scan(&claimUserID, &status); errors.Is(err, pgx.ErrNoRows) {
			return order.CreateResult{}, order.ErrCouponIneligible
		} else if err != nil {
			return order.CreateResult{}, err
		}
		if claimUserID != command.UserID || status != "claimed" {
			return order.CreateResult{}, order.ErrCouponIneligible
		}
		var used bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from purchase_order where coupon_claim_id=$1)`, command.Quote.ClaimID).Scan(&used); err != nil {
			return order.CreateResult{}, err
		}
		if used {
			return order.CreateResult{}, order.ErrCouponIneligible
		}
	}
	var orderID int64
	if err := tx.QueryRow(ctx, `
		insert into purchase_order(order_no,user_id,status,currency,region_code,subtotal_minor,discount_minor,total_minor,coupon_claim_id,idempotency_key,created_at,updated_at)
		values ($1,$2,'pending_payment',$3,$4,$5,$6,$7,nullif($8,0),$9,$10,$10) returning id`,
		command.OrderNo, command.UserID, command.Offer.Currency, command.Offer.Region, command.Offer.AmountMinor, command.Quote.DiscountMinor, command.TotalMinor, command.Quote.ClaimID, command.IdempotencyKey, command.Now).Scan(&orderID); err != nil {
		return order.CreateResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into purchase_order_item(order_id,edition_id,game_id,game_slug_snapshot,game_name_snapshot,edition_code_snapshot,edition_name_snapshot,unit_price_minor,quantity)
		values ($1,$2,$3,$4,$5,$6,$7,$8,1)`, orderID, command.Offer.EditionID, command.Offer.GameID, command.Offer.GameSlug, command.Offer.GameName, command.Offer.EditionCode, command.Offer.EditionName, command.Offer.AmountMinor); err != nil {
		return order.CreateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return order.CreateResult{}, err
	}
	created, err := s.Get(ctx, command.OrderNo)
	return order.CreateResult{Order: created}, err
}

func (s *Store) Pay(ctx context.Context, command order.PayCommand) (result order.PayResult, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return order.PayResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, command.UserID); err != nil {
		return order.PayResult{}, err
	}
	existing, err := queryOrderWithPaymentKey(ctx, tx, command.OrderNo, command.UserID, command.IdempotencyKey)
	if err == nil {
		return order.PayResult{Order: existing, Replayed: true}, nil
	}
	if !errors.Is(err, order.ErrNotFound) {
		return order.PayResult{}, err
	}
	var current entity.Order
	err = tx.QueryRow(ctx, `
		select o.id,o.order_no,o.user_id,o.status,o.currency,o.subtotal_minor,o.discount_minor,o.total_minor,coalesce(o.coupon_claim_id,0),
			i.edition_id,i.game_id,i.game_slug_snapshot,i.game_name_snapshot,i.edition_code_snapshot,i.edition_name_snapshot,i.unit_price_minor,o.region_code,
			o.created_at,o.updated_at
		from purchase_order o join purchase_order_item i on i.order_id=o.id
		where o.order_no=$1 for update of o`, command.OrderNo).Scan(
		&current.ID, &current.OrderNo, &current.UserID, &current.Status, &current.Currency, &current.SubtotalMinor, &current.DiscountMinor, &current.TotalMinor, &current.CouponClaimID,
		&current.Item.EditionID, &current.Item.GameID, &current.Item.GameSlug, &current.Item.GameName, &current.Item.EditionCode, &current.Item.EditionName, &current.Item.UnitPriceMinor, &current.Item.Region,
		&current.CreatedAt, &current.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return order.PayResult{}, order.ErrNotFound
	}
	if err != nil {
		return order.PayResult{}, err
	}
	if current.UserID != command.UserID {
		return order.PayResult{}, order.ErrForbidden
	}
	if current.Status == entity.StatusPaid {
		replayed, replayErr := queryOrderWithPaymentKey(ctx, tx, command.OrderNo, command.UserID, command.IdempotencyKey)
		if replayErr == nil {
			return order.PayResult{Order: replayed, Replayed: true}, nil
		}
		if !errors.Is(replayErr, order.ErrNotFound) {
			return order.PayResult{}, replayErr
		}
	}
	if err := current.ValidatePay(); err != nil {
		return order.PayResult{}, err
	}
	if current.CouponClaimID > 0 {
		tag, err := tx.Exec(ctx, `update coupon_claim set status='redeemed',redeemed_order_id=$2,updated_at=$3 where id=$1 and user_id=$4 and status='claimed'`, current.CouponClaimID, current.ID, command.Now, command.UserID)
		if err != nil {
			return order.PayResult{}, err
		}
		if tag.RowsAffected() != 1 {
			return order.PayResult{}, order.ErrCouponIneligible
		}
	}
	var paymentID int64
	if err := tx.QueryRow(ctx, `
		insert into payment_record(order_id,provider,provider_reference,status,amount_minor,idempotency_key,metadata,created_at,updated_at)
		values ($1,'sandbox',$2,'paid',$3,$4,'{}'::jsonb,$5,$5) returning id`, current.ID, command.ProviderReference, current.TotalMinor, command.IdempotencyKey, command.Now).Scan(&paymentID); err != nil {
		return order.PayResult{}, err
	}
	if _, err := tx.Exec(ctx, `update purchase_order set status='paid',updated_at=$2 where id=$1`, current.ID, command.Now); err != nil {
		return order.PayResult{}, err
	}
	tag, err := tx.Exec(ctx, `
		insert into game_entitlement(user_id,edition_id,source_order_id,status,granted_at)
		values ($1,$2,$3,'active',$4)
		on conflict (user_id,edition_id) where status='active' do nothing`, current.UserID, current.Item.EditionID, current.ID, command.Now)
	if err != nil {
		return order.PayResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return order.PayResult{}, order.ErrAlreadyOwned
	}
	if err := tx.Commit(ctx); err != nil {
		return order.PayResult{}, err
	}
	paid, err := s.Get(ctx, command.OrderNo)
	return order.PayResult{Order: paid}, err
}

const orderSelect = `
	select o.id,o.order_no,o.user_id,o.status,o.currency,o.subtotal_minor,o.discount_minor,o.total_minor,coalesce(o.coupon_claim_id,0),
		i.edition_id,i.game_id,i.game_slug_snapshot,i.game_name_snapshot,i.edition_code_snapshot,i.edition_name_snapshot,i.unit_price_minor,o.region_code,
		p.id,p.provider,p.provider_reference,p.status,p.amount_minor,p.created_at,
		o.created_at,o.updated_at
	from purchase_order o
	join purchase_order_item i on i.order_id=o.id
	left join payment_record p on p.order_id=o.id and p.status='paid'`

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func queryOrder(ctx context.Context, q queryer, where string, args ...any) (entity.Order, error) {
	return scanOrderRow(q.QueryRow(ctx, orderSelect+" where "+where, args...))
}

func queryOrderWithPaymentKey(ctx context.Context, q queryer, orderNo string, userID int64, key string) (entity.Order, error) {
	return scanOrderRow(q.QueryRow(ctx, orderSelect+` where o.order_no=$1 and o.user_id=$2 and p.idempotency_key=$3`, orderNo, userID, key))
}

type scanner interface{ Scan(...any) error }

func scanOrderRow(row scanner) (entity.Order, error) {
	item, err := scanOrder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Order{}, order.ErrNotFound
	}
	return item, err
}

func scanOrder(row scanner) (entity.Order, error) {
	var item entity.Order
	var paymentID *int64
	var provider, reference, paymentStatus *string
	var paymentAmount *int64
	var paymentCreated *time.Time
	err := row.Scan(
		&item.ID, &item.OrderNo, &item.UserID, &item.Status, &item.Currency, &item.SubtotalMinor, &item.DiscountMinor, &item.TotalMinor, &item.CouponClaimID,
		&item.Item.EditionID, &item.Item.GameID, &item.Item.GameSlug, &item.Item.GameName, &item.Item.EditionCode, &item.Item.EditionName, &item.Item.UnitPriceMinor, &item.Item.Region,
		&paymentID, &provider, &reference, &paymentStatus, &paymentAmount, &paymentCreated,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err == nil && paymentID != nil {
		item.Payment = &entity.Payment{ID: *paymentID, Provider: *provider, ProviderReference: *reference, Status: *paymentStatus, AmountMinor: *paymentAmount, CreatedAt: *paymentCreated}
	}
	return item, err
}

func matchesCommand(existing entity.Order, command order.CreateCommand) bool {
	return existing.Item.EditionID == command.Offer.EditionID && existing.CouponClaimID == command.Quote.ClaimID && existing.Item.Region == command.Offer.Region && existing.Currency == command.Offer.Currency
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

var _ order.Store = (*Store)(nil)
