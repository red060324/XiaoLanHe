package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/order/entity"
	order "github.com/red060324/XiaoLanHe/internal/order/usecase"
	promotionentity "github.com/red060324/XiaoLanHe/internal/promotion/entity"
)

const (
	duplicateEntry        = 1062
	paymentProvider       = "sandbox"
	reconciliationTimeout = 5 * time.Second
)

const lockUserSQL = `select id from user_account where id = ? for update`

const lockActiveEntitlementSQL = `
	select source_order_id
	from game_entitlement
	where user_id = ? and edition_id = ? and status = 'active'
	for update`

const strictPaymentReplayWhere = `
	o.order_no = ? and o.user_id = ? and o.status = 'paid'
	and p.order_id = o.id and p.status = 'paid' and p.provider = ?
	and p.provider_reference = ? and p.idempotency_key = ? and p.amount_minor = o.total_minor
	and e.source_order_id = o.id`

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) FindByIdempotency(ctx context.Context, userID int64, key string) (entity.Order, error) {
	return queryOrder(ctx, s.db, `o.user_id = ? and o.idempotency_key = ?`, userID, key)
}

func (s *Store) Get(ctx context.Context, orderNo string) (entity.Order, error) {
	return queryOrder(ctx, s.db, `o.order_no = ?`, orderNo)
}

func (s *Store) List(ctx context.Context, filter order.ListFilter) ([]entity.Order, error) {
	query := orderSelect + `
		where o.user_id = ?
		  and (? is null or o.created_at < ? or (o.created_at = ? and o.id < ?))
		order by o.created_at desc, o.id desc
		limit ?`
	cursor := nullableTime(filter.Cursor.CreatedAt)
	rows, err := s.db.QueryContext(ctx, query, filter.UserID, cursor, cursor, cursor, filter.Cursor.ID, filter.Limit)
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

func (s *Store) Create(ctx context.Context, command order.CreateCommand) (order.CreateResult, error) {
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (order.CreateResult, error) {
		if err := lockUser(ctx, tx, command.UserID); err != nil {
			return order.CreateResult{}, err
		}

		existing, err := queryOrder(ctx, tx, `o.user_id = ? and o.idempotency_key = ?`, command.UserID, command.IdempotencyKey)
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
		if err := tx.QueryRowContext(ctx, `
			select exists(
				select 1 from game_entitlement
				where user_id = ? and edition_id = ? and status = 'active'
			)`, command.UserID, command.Offer.EditionID).Scan(&owned); err != nil {
			return order.CreateResult{}, err
		}
		if owned {
			return order.CreateResult{}, order.ErrAlreadyOwned
		}

		effectiveNow, err := databaseNow(ctx, tx)
		if err != nil {
			return order.CreateResult{}, err
		}

		var offer catalogSnapshot
		if err := tx.QueryRowContext(ctx, `
			select e.id, e.game_id, g.slug, g.name, e.code, e.name
			from game_edition e
			join game g on g.id = e.game_id
			where e.id = ? and e.status = 'active' and g.status = 'active'
			for share`, command.Offer.EditionID).Scan(
			&offer.EditionID, &offer.GameID, &offer.GameSlug, &offer.GameName, &offer.EditionCode, &offer.EditionName,
		); errors.Is(err, sql.ErrNoRows) {
			return order.CreateResult{}, order.ErrPriceUnavailable
		} else if err != nil {
			return order.CreateResult{}, err
		}

		var currentAmount int64
		var priceActiveFrom time.Time
		var priceActiveUntil sql.NullTime
		err = tx.QueryRowContext(ctx, `
			select amount_minor, active_from, active_until
			from game_price
			where edition_id = ? and currency = ? and region_code in (?, 'GLOBAL')
			  and active_from <= ? and (active_until is null or active_until > ?)
			order by case when region_code = ? then 0 else 1 end, active_from desc
			limit 1
			for share`, offer.EditionID, command.Offer.Currency, command.Offer.Region, effectiveNow, effectiveNow, command.Offer.Region).Scan(
			&currentAmount, &priceActiveFrom, &priceActiveUntil,
		)
		if errors.Is(err, sql.ErrNoRows) || err == nil && currentAmount != command.Offer.AmountMinor {
			return order.CreateResult{}, order.ErrPriceUnavailable
		}
		if err != nil {
			return order.CreateResult{}, err
		}

		currentDiscount := int64(0)
		var coupon promotionentity.Coupon
		if command.Quote.ClaimID > 0 {
			var claimUserID int64
			var status string
			if err := tx.QueryRowContext(ctx, `
				select cl.user_id, cl.status,
				       d.id, d.code, d.name, d.discount_type, coalesce(d.fixed_minor, 0), coalesce(d.percentage_bps, 0),
				       d.currency, d.minimum_minor, d.total_stock, d.claimed_stock, d.per_user_limit,
				       coalesce(d.game_id, 0), coalesce(d.edition_id, 0), c.status, c.starts_at, c.ends_at
				from coupon_claim cl
				join coupon_definition d on d.id = cl.coupon_id
				join coupon_campaign c on c.id = d.campaign_id
				where cl.id = ? and cl.coupon_id = ?
				for update`, command.Quote.ClaimID, command.Quote.CouponID).Scan(
				&claimUserID, &status,
				&coupon.ID, &coupon.Code, &coupon.Name, &coupon.DiscountType, &coupon.FixedMinor, &coupon.PercentageBps,
				&coupon.Currency, &coupon.MinimumMinor, &coupon.TotalStock, &coupon.ClaimedStock, &coupon.PerUserLimit,
				&coupon.GameID, &coupon.EditionID, &coupon.CampaignStatus, &coupon.StartsAt, &coupon.EndsAt,
			); errors.Is(err, sql.ErrNoRows) {
				return order.CreateResult{}, order.ErrCouponIneligible
			} else if err != nil {
				return order.CreateResult{}, err
			}
			if claimUserID != command.UserID || status != "claimed" {
				return order.CreateResult{}, order.ErrCouponIneligible
			}
			var used bool
			if err := tx.QueryRowContext(ctx, `select exists(select 1 from purchase_order where coupon_claim_id = ?)`, command.Quote.ClaimID).Scan(&used); err != nil {
				return order.CreateResult{}, err
			}
			if used {
				return order.CreateResult{}, order.ErrCouponIneligible
			}
		}

		if effectiveNow.Before(priceActiveFrom) || priceActiveUntil.Valid && !effectiveNow.Before(priceActiveUntil.Time) {
			return order.CreateResult{}, order.ErrPriceUnavailable
		}
		if command.Quote.ClaimID > 0 {
			if err := coupon.ValidateUse(effectiveNow); err != nil {
				return order.CreateResult{}, order.ErrCouponIneligible
			}
			currentDiscount, err = coupon.Discount(currentAmount, command.Offer.Currency, offer.GameID, offer.EditionID)
			if err != nil {
				return order.CreateResult{}, order.ErrCouponIneligible
			}
		}
		currentTotal, err := entity.CalculateTotals(currentAmount, currentDiscount)
		if err != nil {
			if command.Quote.ClaimID > 0 {
				return order.CreateResult{}, order.ErrCouponIneligible
			}
			return order.CreateResult{}, order.ErrPriceUnavailable
		}
		if currentDiscount != command.Quote.DiscountMinor || currentTotal != command.TotalMinor {
			if command.Quote.ClaimID > 0 {
				return order.CreateResult{}, order.ErrCouponIneligible
			}
			return order.CreateResult{}, order.ErrPriceUnavailable
		}

		inserted, err := tx.ExecContext(ctx, `
			insert into purchase_order(
				order_no, user_id, status, currency, region_code, subtotal_minor, discount_minor, total_minor,
				coupon_claim_id, idempotency_key, source_type, source_reference, payment_expires_at, created_at, updated_at
			) values (?, ?, 'pending_payment', ?, ?, ?, ?, ?, nullif(?, 0), ?, 'standard', null, null, ?, ?)`,
			command.OrderNo, command.UserID, command.Offer.Currency, command.Offer.Region, currentAmount, currentDiscount, currentTotal,
			command.Quote.ClaimID, command.IdempotencyKey, effectiveNow, effectiveNow,
		)
		if err != nil {
			return order.CreateResult{}, err
		}
		orderID, err := inserted.LastInsertId()
		if err != nil {
			return order.CreateResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into purchase_order_item(
				order_id, edition_id, game_id, game_slug_snapshot, game_name_snapshot, edition_code_snapshot,
				edition_name_snapshot, unit_price_minor, quantity
			) values (?, ?, ?, ?, ?, ?, ?, ?, 1)`, orderID, offer.EditionID, offer.GameID, offer.GameSlug, offer.GameName,
			offer.EditionCode, offer.EditionName, currentAmount); err != nil {
			return order.CreateResult{}, err
		}
		created, err := queryOrder(ctx, tx, `o.id = ?`, orderID)
		return order.CreateResult{Order: created}, err
	})
	if err == nil {
		return result, nil
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return order.CreateResult{}, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileCreate(reconcileCtx, command, err)
}

func (s *Store) CreateFromFlashSale(ctx context.Context, command order.FlashSaleCreateCommand) (order.CreateResult, error) {
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (order.CreateResult, error) {
		if err := lockUser(ctx, tx, command.UserID); err != nil {
			return order.CreateResult{}, err
		}

		existing, err := queryOrder(ctx, tx, `o.source_type = 'flash_sale' and o.source_reference = ?`, command.RequestID)
		if err == nil {
			if !matchesFlashSaleCommand(existing, command) {
				return order.CreateResult{}, order.ErrIdempotencyConflict
			}
			return order.CreateResult{Order: existing, Replayed: true}, nil
		}
		if !errors.Is(err, order.ErrNotFound) {
			return order.CreateResult{}, err
		}

		var owned bool
		if err := tx.QueryRowContext(ctx, `
			select exists(
				select 1 from game_entitlement
				where user_id = ? and edition_id = ? and status = 'active'
			)`, command.UserID, command.Offer.EditionID).Scan(&owned); err != nil {
			return order.CreateResult{}, err
		}
		if owned {
			return order.CreateResult{}, order.ErrAlreadyOwned
		}

		var offer catalogSnapshot
		if err := tx.QueryRowContext(ctx, `
			select e.id, e.game_id, g.slug, g.name, e.code, e.name
			from game_edition e
			join game g on g.id = e.game_id
			where e.id = ? and e.status = 'active' and g.status = 'active'
			for share`, command.Offer.EditionID).Scan(
			&offer.EditionID, &offer.GameID, &offer.GameSlug, &offer.GameName, &offer.EditionCode, &offer.EditionName,
		); errors.Is(err, sql.ErrNoRows) {
			return order.CreateResult{}, order.ErrPriceUnavailable
		} else if err != nil {
			return order.CreateResult{}, err
		}

		effectiveNow, err := databaseNow(ctx, tx)
		if err != nil {
			return order.CreateResult{}, err
		}
		if command.SalePriceMinor < 0 || !effectiveNow.Before(command.PaymentExpiresAt) {
			return order.CreateResult{}, order.ErrPriceUnavailable
		}

		inserted, err := tx.ExecContext(ctx, `
			insert into purchase_order(
				order_no, user_id, status, currency, region_code, subtotal_minor, discount_minor, total_minor,
				coupon_claim_id, idempotency_key, source_type, source_reference, payment_expires_at, created_at, updated_at
			) values (?, ?, 'pending_payment', ?, ?, ?, 0, ?, null, ?, 'flash_sale', ?, ?, ?, ?)`,
			command.OrderNo, command.UserID, command.Offer.Currency, command.Offer.Region, command.SalePriceMinor,
			command.SalePriceMinor, command.RequestID, command.RequestID, command.PaymentExpiresAt, effectiveNow, effectiveNow,
		)
		if err != nil {
			return order.CreateResult{}, err
		}
		orderID, err := inserted.LastInsertId()
		if err != nil {
			return order.CreateResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into purchase_order_item(
				order_id, edition_id, game_id, game_slug_snapshot, game_name_snapshot, edition_code_snapshot,
				edition_name_snapshot, unit_price_minor, quantity
			) values (?, ?, ?, ?, ?, ?, ?, ?, 1)`, orderID, offer.EditionID, offer.GameID, offer.GameSlug, offer.GameName,
			offer.EditionCode, offer.EditionName, command.SalePriceMinor); err != nil {
			return order.CreateResult{}, err
		}
		created, err := queryOrder(ctx, tx, `o.id = ?`, orderID)
		return order.CreateResult{Order: created}, err
	})
	if err == nil {
		return result, nil
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return order.CreateResult{}, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileFlashSaleCreate(reconcileCtx, command, err)
}

func (s *Store) Pay(ctx context.Context, command order.PayCommand) (order.PayResult, error) {
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (order.PayResult, error) {
		if err := lockUser(ctx, tx, command.UserID); err != nil {
			return order.PayResult{}, err
		}

		existing, err := queryStrictPaymentReplay(ctx, tx, command)
		if err == nil {
			return order.PayResult{Order: existing, Replayed: true}, nil
		}
		if !errors.Is(err, order.ErrNotFound) {
			return order.PayResult{}, err
		}

		current, err := queryOrderForUpdate(ctx, tx, command.OrderNo)
		if err != nil {
			return order.PayResult{}, err
		}
		if current.UserID != command.UserID {
			return order.PayResult{}, order.ErrForbidden
		}
		entitlementOrderID, hasEntitlement, err := lockActiveEntitlement(ctx, tx, current.UserID, current.Item.EditionID)
		if err != nil {
			return order.PayResult{}, err
		}
		if hasEntitlement {
			if !sameEntitlementOrder(entitlementOrderID, current.ID) {
				return order.PayResult{}, order.ErrAlreadyOwned
			}
			// An exact paid-order replay returned above. Reaching this branch means
			// that the same-order entitlement has no matching durable payment identity.
			return order.PayResult{}, order.ErrIdempotencyConflict
		}
		if current.Status == entity.StatusPaid {
			return order.PayResult{}, order.ErrIdempotencyConflict
		}

		effectiveNow, err := databaseNow(ctx, tx)
		if err != nil {
			return order.PayResult{}, err
		}
		if err := current.ValidatePayAt(effectiveNow); err != nil {
			return order.PayResult{}, err
		}

		_, err = tx.ExecContext(ctx, `
			insert into game_entitlement(user_id, edition_id, source_order_id, status, granted_at)
			values (?, ?, ?, 'active', ?)`, current.UserID, current.Item.EditionID, current.ID, effectiveNow)
		if err != nil {
			if isDuplicate(err) {
				// The earlier locked read makes a duplicate here an invariant race
				// or a conflicting unique key. Never convert it into a replay.
				return order.PayResult{}, order.ErrAlreadyOwned
			}
			return order.PayResult{}, err
		}

		if current.CouponClaimID > 0 {
			updated, err := tx.ExecContext(ctx, `
				update coupon_claim
				set status = 'redeemed', redeemed_order_id = ?, updated_at = ?
				where id = ? and user_id = ? and status = 'claimed'`, current.ID, effectiveNow, current.CouponClaimID, command.UserID)
			if err != nil {
				return order.PayResult{}, err
			}
			affected, err := updated.RowsAffected()
			if err != nil {
				return order.PayResult{}, err
			}
			if affected != 1 {
				return order.PayResult{}, order.ErrCouponIneligible
			}
		}

		insertedPayment, err := tx.ExecContext(ctx, `
			insert into payment_record(
				order_id, provider, provider_reference, status, amount_minor, idempotency_key, metadata, created_at, updated_at
			) values (?, ?, ?, 'paid', ?, ?, json_object(), ?, ?)`, current.ID, paymentProvider, command.ProviderReference,
			current.TotalMinor, command.IdempotencyKey, effectiveNow, effectiveNow)
		if err != nil {
			if isDuplicate(err) {
				return order.PayResult{}, order.ErrIdempotencyConflict
			}
			return order.PayResult{}, err
		}
		if _, err := insertedPayment.LastInsertId(); err != nil {
			return order.PayResult{}, err
		}

		updatedOrder, err := tx.ExecContext(ctx, `
			update purchase_order
			set status = 'paid', updated_at = ?
			where id = ? and status = 'pending_payment'`, effectiveNow, current.ID)
		if err != nil {
			return order.PayResult{}, err
		}
		affected, err := updatedOrder.RowsAffected()
		if err != nil {
			return order.PayResult{}, err
		}
		if affected != 1 {
			return order.PayResult{}, entity.ErrInvalidState
		}

		paid, err := queryOrder(ctx, tx, `o.id = ?`, current.ID)
		return order.PayResult{Order: paid}, err
	})
	if err == nil {
		return result, nil
	}
	if !mysqltx.IsCommitOutcomeUnknown(err) {
		return order.PayResult{}, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcilePay(reconcileCtx, command, err)
}

type catalogSnapshot struct {
	EditionID, GameID                            int64
	GameSlug, GameName, EditionCode, EditionName string
}

const orderSelect = `
	select o.id, o.order_no, o.user_id, o.status, o.currency, o.subtotal_minor, o.discount_minor, o.total_minor, coalesce(o.coupon_claim_id, 0),
	       i.edition_id, i.game_id, i.game_slug_snapshot, i.game_name_snapshot, i.edition_code_snapshot, i.edition_name_snapshot, i.unit_price_minor, o.region_code,
	       p.id, p.provider, p.provider_reference, p.status, p.amount_minor, p.created_at,
	       o.source_type, coalesce(o.source_reference, ''), o.payment_expires_at,
	       o.created_at, o.updated_at
	from purchase_order o
	join purchase_order_item i on i.order_id = o.id
	left join payment_record p on p.order_id = o.id and p.paid_payment = 1`

const strictPaymentReplaySelect = `
	select o.id, o.order_no, o.user_id, o.status, o.currency, o.subtotal_minor, o.discount_minor, o.total_minor, coalesce(o.coupon_claim_id, 0),
	       i.edition_id, i.game_id, i.game_slug_snapshot, i.game_name_snapshot, i.edition_code_snapshot, i.edition_name_snapshot, i.unit_price_minor, o.region_code,
	       p.id, p.provider, p.provider_reference, p.status, p.amount_minor, p.created_at,
	       o.source_type, coalesce(o.source_reference, ''), o.payment_expires_at,
	       o.created_at, o.updated_at
	from purchase_order o
	join purchase_order_item i on i.order_id = o.id
	join payment_record p on p.order_id = o.id and p.paid_payment = 1
	join game_entitlement e on e.user_id = o.user_id and e.edition_id = i.edition_id and e.status = 'active'`

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryOrder(ctx context.Context, q queryer, where string, args ...any) (entity.Order, error) {
	return scanOrderRow(q.QueryRowContext(ctx, orderSelect+" where "+where, args...))
}

func queryOrderForUpdate(ctx context.Context, tx *sql.Tx, orderNo string) (entity.Order, error) {
	return scanOrderRow(tx.QueryRowContext(ctx, `
		select o.id, o.order_no, o.user_id, o.status, o.currency, o.subtotal_minor, o.discount_minor, o.total_minor, coalesce(o.coupon_claim_id, 0),
		       i.edition_id, i.game_id, i.game_slug_snapshot, i.game_name_snapshot, i.edition_code_snapshot, i.edition_name_snapshot, i.unit_price_minor, o.region_code,
		       null, null, null, null, null, null,
		       o.source_type, coalesce(o.source_reference, ''), o.payment_expires_at,
		       o.created_at, o.updated_at
		from purchase_order o
		join purchase_order_item i on i.order_id = o.id
		where o.order_no = ?
		for update`, orderNo))
}

func queryStrictPaymentReplay(ctx context.Context, q queryer, command order.PayCommand) (entity.Order, error) {
	return scanOrderRow(q.QueryRowContext(ctx, strictPaymentReplaySelect+" where "+strictPaymentReplayWhere+" for update",
		command.OrderNo, command.UserID, paymentProvider, command.ProviderReference, command.IdempotencyKey))
}

func lockActiveEntitlement(ctx context.Context, tx *sql.Tx, userID, editionID int64) (sql.NullInt64, bool, error) {
	var sourceOrderID sql.NullInt64
	err := tx.QueryRowContext(ctx, lockActiveEntitlementSQL, userID, editionID).Scan(&sourceOrderID)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullInt64{}, false, nil
	}
	if err != nil {
		return sql.NullInt64{}, false, err
	}
	return sourceOrderID, true, nil
}

func (s *Store) reconcileCreate(ctx context.Context, command order.CreateCommand, commitErr error) (order.CreateResult, error) {
	tx, err := s.beginReconciliation(ctx, command.UserID)
	if err != nil {
		return order.CreateResult{}, errors.Join(commitErr, fmt.Errorf("begin create reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := queryOrder(ctx, tx, `o.user_id = ? and o.idempotency_key = ? for update`, command.UserID, command.IdempotencyKey)
	if err == nil {
		if !matchesCommand(existing, command) {
			return order.CreateResult{}, order.ErrIdempotencyConflict
		}
		return order.CreateResult{Order: existing, Replayed: true}, nil
	}
	if errors.Is(err, order.ErrNotFound) {
		return order.CreateResult{}, commitErr
	}
	return order.CreateResult{}, errors.Join(commitErr, fmt.Errorf("reconcile create: %w", err))
}

func (s *Store) reconcileFlashSaleCreate(ctx context.Context, command order.FlashSaleCreateCommand, commitErr error) (order.CreateResult, error) {
	tx, err := s.beginReconciliation(ctx, command.UserID)
	if err != nil {
		return order.CreateResult{}, errors.Join(commitErr, fmt.Errorf("begin flash-sale create reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := queryOrder(ctx, tx, `o.source_type = 'flash_sale' and o.source_reference = ? for update`, command.RequestID)
	if err == nil {
		if !matchesFlashSaleCommand(existing, command) {
			return order.CreateResult{}, order.ErrIdempotencyConflict
		}
		return order.CreateResult{Order: existing, Replayed: true}, nil
	}
	if errors.Is(err, order.ErrNotFound) {
		return order.CreateResult{}, commitErr
	}
	return order.CreateResult{}, errors.Join(commitErr, fmt.Errorf("reconcile flash-sale create: %w", err))
}

func (s *Store) reconcilePay(ctx context.Context, command order.PayCommand, commitErr error) (order.PayResult, error) {
	tx, err := s.beginReconciliation(ctx, command.UserID)
	if err != nil {
		return order.PayResult{}, errors.Join(commitErr, fmt.Errorf("begin payment reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	replayed, err := queryStrictPaymentReplay(ctx, tx, command)
	if err == nil {
		return order.PayResult{Order: replayed, Replayed: true}, nil
	}
	if errors.Is(err, order.ErrNotFound) {
		return order.PayResult{}, commitErr
	}
	return order.PayResult{}, errors.Join(commitErr, fmt.Errorf("reconcile payment: %w", err))
}

func (s *Store) beginReconciliation(ctx context.Context, userID int64) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	if err := lockUser(ctx, tx, userID); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return nil, errors.Join(err, fmt.Errorf("rollback reconciliation: %w", rollbackErr))
		}
		return nil, err
	}
	return tx, nil
}

func reconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), reconciliationTimeout)
}

func lockUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var lockedID int64
	err := tx.QueryRowContext(ctx, lockUserSQL, userID).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return order.ErrForbidden
	}
	return err
}

func databaseNow(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRowContext(ctx, `select utc_timestamp(6)`).Scan(&now)
	return now.UTC(), err
}

type scanner interface{ Scan(...any) error }

func scanOrderRow(row scanner) (entity.Order, error) {
	item, err := scanOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Order{}, order.ErrNotFound
	}
	return item, err
}

func scanOrder(row scanner) (entity.Order, error) {
	var item entity.Order
	var paymentID sql.NullInt64
	var provider, reference, paymentStatus sql.NullString
	var paymentAmount sql.NullInt64
	var paymentCreated, paymentExpiresAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.OrderNo, &item.UserID, &item.Status, &item.Currency, &item.SubtotalMinor, &item.DiscountMinor, &item.TotalMinor, &item.CouponClaimID,
		&item.Item.EditionID, &item.Item.GameID, &item.Item.GameSlug, &item.Item.GameName, &item.Item.EditionCode, &item.Item.EditionName, &item.Item.UnitPriceMinor, &item.Item.Region,
		&paymentID, &provider, &reference, &paymentStatus, &paymentAmount, &paymentCreated,
		&item.SourceType, &item.SourceReference, &paymentExpiresAt,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return entity.Order{}, err
	}
	if paymentExpiresAt.Valid {
		item.PaymentExpiresAt = paymentExpiresAt.Time.UTC()
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	if paymentID.Valid {
		if !provider.Valid || !reference.Valid || !paymentStatus.Valid || !paymentAmount.Valid || !paymentCreated.Valid {
			return entity.Order{}, errors.New("incomplete paid payment record")
		}
		item.Payment = &entity.Payment{
			ID: paymentID.Int64, Provider: provider.String, ProviderReference: reference.String,
			Status: paymentStatus.String, AmountMinor: paymentAmount.Int64, CreatedAt: paymentCreated.Time.UTC(),
		}
	}
	return item, nil
}

func matchesCommand(existing entity.Order, command order.CreateCommand) bool {
	return existing.OrderNo == command.OrderNo &&
		existing.UserID == command.UserID &&
		existing.Status == entity.StatusPendingPayment &&
		existing.Item.EditionID == command.Offer.EditionID &&
		existing.Item.GameID == command.Offer.GameID &&
		existing.Item.GameSlug == command.Offer.GameSlug &&
		existing.Item.GameName == command.Offer.GameName &&
		existing.Item.EditionCode == command.Offer.EditionCode &&
		existing.Item.EditionName == command.Offer.EditionName &&
		existing.CouponClaimID == command.Quote.ClaimID &&
		existing.Item.Region == command.Offer.Region &&
		existing.Currency == command.Offer.Currency &&
		existing.SubtotalMinor == command.Offer.AmountMinor &&
		existing.DiscountMinor == command.Quote.DiscountMinor &&
		existing.TotalMinor == command.TotalMinor &&
		existing.Item.UnitPriceMinor == command.Offer.AmountMinor &&
		existing.SourceType == "standard" &&
		existing.SourceReference == "" &&
		existing.PaymentExpiresAt.IsZero() &&
		existing.Payment == nil
}

func matchesFlashSaleCommand(existing entity.Order, command order.FlashSaleCreateCommand) bool {
	return existing.OrderNo == command.OrderNo &&
		existing.UserID == command.UserID &&
		existing.Status == entity.StatusPendingPayment &&
		existing.Item.EditionID == command.Offer.EditionID &&
		existing.Item.GameID == command.Offer.GameID &&
		existing.Item.GameSlug == command.Offer.GameSlug &&
		existing.Item.GameName == command.Offer.GameName &&
		existing.Item.EditionCode == command.Offer.EditionCode &&
		existing.Item.EditionName == command.Offer.EditionName &&
		existing.Currency == command.Offer.Currency &&
		existing.Item.Region == command.Offer.Region &&
		existing.SubtotalMinor == command.SalePriceMinor &&
		existing.DiscountMinor == 0 &&
		existing.TotalMinor == command.SalePriceMinor &&
		existing.Item.UnitPriceMinor == command.SalePriceMinor &&
		existing.CouponClaimID == 0 &&
		existing.SourceType == "flash_sale" &&
		existing.SourceReference == command.RequestID &&
		sameMySQLTime(existing.PaymentExpiresAt, command.PaymentExpiresAt) &&
		existing.Payment == nil
}

func sameMySQLTime(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func isDuplicate(err error) bool {
	var mysqlErr *drivermysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == duplicateEntry
}

func sameEntitlementOrder(sourceOrderID sql.NullInt64, currentOrderID int64) bool {
	return sourceOrderID.Valid && sourceOrderID.Int64 == currentOrderID
}

var _ order.Store = (*Store)(nil)
