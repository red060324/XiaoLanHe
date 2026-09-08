package mysql

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

// Store persists the durable part of the flash-sale workflow in MySQL.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

const (
	reconciliationTimeout = 5 * time.Second
	upsertScopeSQL        = `
		INSERT INTO flash_sale_scope_lock(edition_id,region_code,currency)
		VALUES (?,?,?) AS incoming
		ON DUPLICATE KEY UPDATE edition_id=incoming.edition_id`
	lockScopeSQL = `
		SELECT edition_id FROM flash_sale_scope_lock
		WHERE edition_id=? AND region_code=? AND currency=? FOR UPDATE`
	insertActivitySQL = `
		INSERT INTO flash_sale_activity(
			code,edition_id,region_code,currency,sale_price_minor,total_stock,allocated_stock,status,
			starts_at,ends_at,payment_timeout_seconds,version,created_by
		) VALUES (?,?,?,?,?,?,0,'draft',?,?,?,0,?)`
	selectReleaseJobIDsSQL = `
		SELECT id FROM flash_sale_release_job
		WHERE claimable_at<=CURRENT_TIMESTAMP(6)
		ORDER BY claimable_at,id LIMIT ? FOR UPDATE SKIP LOCKED`
	lockUserSQL               = `SELECT id FROM user_account WHERE id=? FOR UPDATE`
	lockUserSkipLockedSQL     = `SELECT id FROM user_account WHERE id=? FOR UPDATE SKIP LOCKED`
	lockActivitySQL           = `SELECT id FROM flash_sale_activity WHERE id=? FOR UPDATE`
	lockActivitySkipLockedSQL = `SELECT id FROM flash_sale_activity WHERE id=? FOR UPDATE SKIP LOCKED`
	lockOrderSQL              = `SELECT id FROM purchase_order WHERE id=? FOR UPDATE`
	lockOrderSkipLockedSQL    = `SELECT id FROM purchase_order WHERE id=? FOR UPDATE SKIP LOCKED`
	lockReservationSQL        = `SELECT request_id FROM flash_sale_reservation WHERE request_id=? FOR UPDATE`
	lockReservationSkipSQL    = `SELECT request_id FROM flash_sale_reservation WHERE request_id=? FOR UPDATE SKIP LOCKED`
	selectDraftMetadataSQL    = `
		SELECT edition_id,region_code,currency,version
		FROM flash_sale_activity WHERE id=? AND status='draft'`
	lockDraftMetadataSQL = selectDraftMetadataSQL + ` FOR UPDATE`
)

func (s *Store) ListActivities(ctx context.Context, filter flashsale.ListFilter) ([]entity.Activity, error) {
	rows, err := s.db.QueryContext(ctx, activitySelect+`
		WHERE a.status IN ('active','cancelled','ended') AND (?=0 OR a.id<?)
		ORDER BY a.id DESC LIMIT ?`, filter.BeforeID, filter.BeforeID, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]entity.Activity, 0, filter.Limit)
	for rows.Next() {
		item, err := scanActivity(rows)
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

func (s *Store) CreateActivity(ctx context.Context, activity entity.Activity) (entity.Activity, error) {
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.Activity, error) {
		if err := upsertAndLockScopes(ctx, tx, activityScope(activity)); err != nil {
			return entity.Activity{}, err
		}

		result, err := tx.ExecContext(ctx, insertActivitySQL,
			activity.Code, activity.EditionID, activity.Region, activity.Currency,
			activity.SalePriceMinor, activity.TotalStock, activity.StartsAt.UTC(), activity.EndsAt.UTC(),
			int64(activity.PaymentTimeout/time.Second), activity.CreatedBy)
		if err != nil {
			return entity.Activity{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return entity.Activity{}, err
		}
		return queryActivity(ctx, tx, id)
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return result, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileActivityCreate(reconcileCtx, activity, err)
}

func (s *Store) UpdateDraft(ctx context.Context, activity entity.Activity) (entity.Activity, error) {
	if activity.ID <= 0 {
		return entity.Activity{}, flashsale.ErrInvalidInput
	}
	var expectedVersion int64
	var originalScope scopeKey
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.Activity, error) {
		observed, err := readDraftMetadata(ctx, tx, activity.ID, false)
		if err != nil {
			return entity.Activity{}, err
		}
		originalScope = observed.scopeKey

		target := scopeKey{editionID: activity.EditionID, region: activity.Region, currency: activity.Currency}
		if err := upsertAndLockScopes(ctx, tx, observed.scopeKey, target); err != nil {
			return entity.Activity{}, err
		}
		current, err := readDraftMetadata(ctx, tx, activity.ID, true)
		if err != nil {
			return entity.Activity{}, err
		}
		if current != observed {
			return entity.Activity{}, entity.ErrInvalidState
		}
		expectedVersion = current.version + 1
		result, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_activity SET code=?,edition_id=?,region_code=?,currency=?,
				sale_price_minor=?,total_stock=?,starts_at=?,ends_at=?,payment_timeout_seconds=?,
				version=version+1,updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='draft'`, activity.Code, activity.EditionID, activity.Region, activity.Currency,
			activity.SalePriceMinor, activity.TotalStock, activity.StartsAt.UTC(), activity.EndsAt.UTC(),
			int64(activity.PaymentTimeout/time.Second), activity.ID)
		if err != nil {
			return entity.Activity{}, err
		}
		if ok, err := changedOne(result); err != nil {
			return entity.Activity{}, err
		} else if !ok {
			return entity.Activity{}, entity.ErrInvalidState
		}
		return queryActivity(ctx, tx, activity.ID)
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return result, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileActivityUpdate(reconcileCtx, activity, expectedVersion, originalScope, err)
}

func (s *Store) ActivateActivity(ctx context.Context, id, version int64, _ time.Time) (entity.Activity, error) {
	var activityScope scopeKey
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.Activity, error) {
		observed, err := readDraftMetadata(ctx, tx, id, false)
		if err != nil || observed.version != version-1 {
			if err == nil {
				err = entity.ErrInvalidState
			}
			return entity.Activity{}, err
		}
		activityScope = observed.scopeKey
		if err := upsertAndLockScopes(ctx, tx, observed.scopeKey); err != nil {
			return entity.Activity{}, err
		}

		var startsAt, endsAt time.Time
		err = tx.QueryRowContext(ctx, `
			SELECT starts_at,ends_at FROM flash_sale_activity
			WHERE id=? AND status='draft' AND version=?
				AND edition_id=? AND region_code=? AND currency=?
			FOR UPDATE`, id, version-1, observed.editionID, observed.region, observed.currency).Scan(&startsAt, &endsAt)
		if errors.Is(err, sql.ErrNoRows) {
			return entity.Activity{}, entity.ErrInvalidState
		}
		if err != nil {
			return entity.Activity{}, err
		}

		var overlappingID int64
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM flash_sale_activity
			WHERE id<>? AND edition_id=? AND region_code=? AND currency=? AND status='active'
				AND starts_at<? AND ends_at>?
			ORDER BY id LIMIT 1 FOR UPDATE`,
			id, observed.editionID, observed.region, observed.currency, endsAt, startsAt).Scan(&overlappingID)
		if err == nil {
			return entity.Activity{}, entity.ErrInvalidState
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return entity.Activity{}, err
		}

		result, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_activity SET status='active',version=?,
				activated_at=CURRENT_TIMESTAMP(6),updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='draft' AND version=? AND ends_at>CURRENT_TIMESTAMP(6)
				AND edition_id=? AND region_code=? AND currency=?`,
			version, id, version-1, observed.editionID, observed.region, observed.currency)
		if err != nil {
			return entity.Activity{}, err
		}
		if ok, err := changedOne(result); err != nil {
			return entity.Activity{}, err
		} else if !ok {
			return entity.Activity{}, entity.ErrInvalidState
		}
		return queryActivity(ctx, tx, id)
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return result, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileActivityActivation(reconcileCtx, id, version, activityScope, err)
}

func (s *Store) CancelActivity(ctx context.Context, id int64, cutoff time.Time) (entity.Activity, error) {
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (entity.Activity, error) {
		updated, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_activity
			SET status='cancelled',cancelled_at=?,updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND (status='active' OR (status='cancelled' AND cancelled_at=?))`,
			cutoff.UTC(), id, cutoff.UTC())
		if err != nil {
			return entity.Activity{}, err
		}
		if ok, err := changedOne(updated); err != nil {
			return entity.Activity{}, err
		} else if !ok {
			current, readErr := queryActivity(ctx, tx, id)
			if readErr == nil && current.Status == entity.StatusCancelled && sameMySQLTime(current.CancelledAt, cutoff) {
				return current, nil
			}
			if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
				return entity.Activity{}, readErr
			}
			return entity.Activity{}, entity.ErrInvalidState
		}
		return queryActivity(ctx, tx, id)
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return result, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileActivityCancellation(reconcileCtx, id, cutoff, err)
}

func (s *Store) GetActivity(ctx context.Context, id int64) (entity.Activity, error) {
	activity, err := queryActivity(ctx, s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Activity{}, flashsale.ErrNotFound
	}
	return activity, err
}

func (s *Store) Allocate(ctx context.Context, event flashsale.Event) (flashsale.Allocation, error) {
	result, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (flashsale.Allocation, error) {
		if err := lockUser(ctx, tx, event.UserID); err != nil {
			return flashsale.Allocation{}, err
		}
		if err := lockActivity(ctx, tx, event.ActivityID); err != nil {
			return flashsale.Allocation{}, err
		}
		activity, err := queryActivity(ctx, tx, event.ActivityID)
		if errors.Is(err, sql.ErrNoRows) {
			return flashsale.Allocation{}, flashsale.ErrNotFound
		}
		if err != nil {
			return flashsale.Allocation{}, err
		}
		if activity.Version != event.ActivityVersion || !activity.AcceptsReservationTime(event.ReservedAt) {
			return flashsale.Allocation{}, flashsale.ErrEnded
		}

		existing, err := queryAllocation(ctx, tx, event.RequestID)
		if err == nil {
			if existing.ActivityID != event.ActivityID || existing.UserID != event.UserID ||
				existing.IdempotencyDigest != event.IdempotencyDigest || !sameMySQLTime(existing.ReservedAt, event.ReservedAt) {
				return flashsale.Allocation{}, flashsale.ErrAlreadyReserved
			}
			return existing, nil
		}
		if !errors.Is(err, flashsale.ErrNotFound) {
			return flashsale.Allocation{}, err
		}

		var otherRequest string
		err = tx.QueryRowContext(ctx, `
			SELECT request_id FROM flash_sale_reservation
			WHERE activity_id=? AND user_id=?`, event.ActivityID, event.UserID).Scan(&otherRequest)
		if err == nil {
			return flashsale.Allocation{}, flashsale.ErrAlreadyReserved
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return flashsale.Allocation{}, err
		}
		if activity.AllocatedStock >= activity.TotalStock {
			return flashsale.Allocation{}, flashsale.ErrStockExhausted
		}
		digest, err := decodeDigest(event.IdempotencyDigest)
		if err != nil {
			return flashsale.Allocation{}, err
		}

		var effectiveNow time.Time
		if err := tx.QueryRowContext(ctx, `SELECT CURRENT_TIMESTAMP(6)`).Scan(&effectiveNow); err != nil {
			return flashsale.Allocation{}, err
		}
		effectiveNow = effectiveNow.UTC()
		deadline := effectiveNow.Add(activity.PaymentTimeout)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO flash_sale_reservation(
				request_id,activity_id,user_id,idempotency_digest,status,reserved_at,payment_expires_at,created_at,updated_at
			) VALUES (?,?,?,?,'reserved',?,?,?,?)`,
			event.RequestID, event.ActivityID, event.UserID, digest, event.ReservedAt.UTC(), deadline, effectiveNow, effectiveNow); err != nil {
			return flashsale.Allocation{}, err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_activity SET allocated_stock=allocated_stock+1,updated_at=?
			WHERE id=? AND allocated_stock<total_stock`, effectiveNow, activity.ID)
		if err != nil {
			return flashsale.Allocation{}, err
		}
		if ok, err := changedOne(result); err != nil {
			return flashsale.Allocation{}, err
		} else if !ok {
			return flashsale.Allocation{}, flashsale.ErrStockExhausted
		}

		activity.AllocatedStock++
		activity.UpdatedAt = effectiveNow
		return flashsale.Allocation{
			RequestID: event.RequestID, ActivityID: activity.ID, UserID: event.UserID,
			IdempotencyDigest: event.IdempotencyDigest, Status: entity.ReservationReserved,
			ReservedAt: event.ReservedAt.UTC(), PaymentExpiresAt: deadline, Activity: activity,
		}, nil
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return result, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileAllocation(reconcileCtx, event, err)
}

func (s *Store) MarkOrderReady(ctx context.Context, requestID, orderNo string) error {
	err := run(ctx, s.db, func(tx *sql.Tx) error {
		var userID int64
		readErr := tx.QueryRowContext(ctx, `
			SELECT user_id FROM flash_sale_reservation WHERE request_id=?`, requestID).Scan(&userID)
		if errors.Is(readErr, sql.ErrNoRows) {
			return flashsale.ErrNotFound
		}
		if readErr != nil {
			return readErr
		}
		if err := lockUser(ctx, tx, userID); err != nil {
			return err
		}
		var activityID int64
		readErr = tx.QueryRowContext(ctx, `
			SELECT activity_id FROM flash_sale_reservation WHERE request_id=? AND user_id=?`, requestID, userID).Scan(&activityID)
		if errors.Is(readErr, sql.ErrNoRows) {
			return flashsale.ErrNotFound
		}
		if readErr != nil {
			return readErr
		}
		if err := lockActivity(ctx, tx, activityID); err != nil {
			return err
		}
		var orderID int64
		readErr = tx.QueryRowContext(ctx, `
			SELECT id FROM purchase_order
			WHERE order_no=? AND source_type='flash_sale' AND source_reference=? AND user_id=?
			FOR UPDATE`, orderNo, requestID, userID).Scan(&orderID)
		if errors.Is(readErr, sql.ErrNoRows) {
			return flashsale.ErrNotFound
		}
		if readErr != nil {
			return readErr
		}
		if err := lockReservation(ctx, tx, requestID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_reservation
			SET status='order_ready',order_id=?,updated_at=CURRENT_TIMESTAMP(6)
			WHERE request_id=? AND user_id=?
				AND (status='reserved' OR (status='order_ready' AND order_id=?))`,
			orderID, requestID, userID, orderID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
		if affected != 0 {
			return flashsale.ErrNotFound
		}

		// With clientFoundRows disabled, an exact no-op replay may report zero
		// affected rows. Prove the complete durable binding before accepting it.
		var replayedRequestID string
		err = tx.QueryRowContext(ctx, `
			SELECT r.request_id
			FROM flash_sale_reservation AS r
			JOIN purchase_order AS o ON o.id=r.order_id
			WHERE r.request_id=? AND r.status='order_ready' AND o.order_no=?
				AND o.source_type='flash_sale' AND o.source_reference=r.request_id
				AND o.user_id=r.user_id AND r.order_id=o.id`, requestID, orderNo).Scan(&replayedRequestID)
		if errors.Is(err, sql.ErrNoRows) {
			return flashsale.ErrNotFound
		}
		return err
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileOrderReady(reconcileCtx, requestID, orderNo, err)
}

func (s *Store) Fail(ctx context.Context, event flashsale.Event, failureCode, reason string) error {
	err := run(ctx, s.db, func(tx *sql.Tx) error {
		if err := lockUser(ctx, tx, event.UserID); err != nil {
			return err
		}
		if err := lockActivity(ctx, tx, event.ActivityID); err != nil {
			return err
		}
		activity, err := queryActivity(ctx, tx, event.ActivityID)
		if errors.Is(err, sql.ErrNoRows) {
			return flashsale.ErrNotFound
		}
		if err != nil {
			return err
		}
		digest, err := decodeDigest(event.IdempotencyDigest)
		if err != nil {
			return err
		}

		var status entity.ReservationStatus
		var existingFailure sql.NullString
		var activityID, userID int64
		var digestHex string
		var reservedAt time.Time
		err = tx.QueryRowContext(ctx, `
			SELECT status,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,failure_code
			FROM flash_sale_reservation WHERE request_id=? FOR UPDATE`, event.RequestID).Scan(
			&status, &activityID, &userID, &digestHex, &reservedAt, &existingFailure,
		)
		if err == nil && !matchesEvent(activityID, userID, digestHex, reservedAt, event) {
			return flashsale.ErrUnsupportedEvent
		}

		switch {
		case err == nil && (status == entity.ReservationOrderReady || status == entity.ReservationExpired):
			return flashsale.ErrUnsupportedEvent
		case err == nil && status == entity.ReservationReserved:
			result, updateErr := tx.ExecContext(ctx, `
				UPDATE flash_sale_reservation
				SET status='failed',failure_code=?,updated_at=CURRENT_TIMESTAMP(6)
				WHERE request_id=? AND status='reserved'`, failureCode, event.RequestID)
			if updateErr != nil {
				return updateErr
			}
			changed, updateErr := changedOne(result)
			if updateErr != nil {
				return updateErr
			}
			if changed {
				stockResult, updateErr := tx.ExecContext(ctx, `
					UPDATE flash_sale_activity
					SET allocated_stock=allocated_stock-1,updated_at=CURRENT_TIMESTAMP(6)
					WHERE id=? AND allocated_stock>0`, event.ActivityID)
				if updateErr != nil {
					return updateErr
				}
				stockChanged, updateErr := changedOne(stockResult)
				if updateErr != nil {
					return updateErr
				}
				if !stockChanged {
					return flashsale.ErrUnavailable
				}
			}
		case err == nil && status == entity.ReservationFailed:
			if !existingFailure.Valid || existingFailure.String != failureCode {
				return flashsale.ErrUnsupportedEvent
			}
		case errors.Is(err, sql.ErrNoRows):
			deadline := event.ReservedAt.UTC().Add(activity.PaymentTimeout)
			if _, insertErr := tx.ExecContext(ctx, `
				INSERT INTO flash_sale_reservation(
					request_id,activity_id,user_id,idempotency_digest,status,failure_code,reserved_at,payment_expires_at
				) VALUES (?,?,?,?,'failed',?,?,?) AS incoming
				ON DUPLICATE KEY UPDATE request_id=flash_sale_reservation.request_id`,
				event.RequestID, event.ActivityID, event.UserID, digest, failureCode, event.ReservedAt.UTC(), deadline); insertErr != nil {
				return insertErr
			}
			if err := verifyReservationFailure(ctx, tx, event, failureCode); err != nil {
				return err
			}
		default:
			return err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO flash_sale_release_job(request_id,activity_id,user_id,idempotency_digest,reserved_at,reason)
			VALUES (?,?,?,?,?,?) AS incoming
			ON DUPLICATE KEY UPDATE request_id=incoming.request_id`,
			event.RequestID, event.ActivityID, event.UserID, digest, event.ReservedAt.UTC(), reason)
		if err != nil {
			return err
		}
		return verifyReleaseJob(ctx, tx, event, reason)
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileFailure(reconcileCtx, event, failureCode, reason, err)
}

func (s *Store) ExpireDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > maxBatchSize {
		return 0, flashsale.ErrInvalidInput
	}

	var processed []dueReservation
	count, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) (int, error) {
		processed = nil
		rows, err := tx.QueryContext(ctx, `
			SELECT r.request_id,r.activity_id,r.user_id,o.id,r.idempotency_digest,r.reserved_at
			FROM flash_sale_reservation AS r
			JOIN purchase_order AS o ON o.id=r.order_id
			WHERE r.status='order_ready' AND o.status='pending_payment'
				AND o.source_type='flash_sale' AND o.payment_expires_at<=CURRENT_TIMESTAMP(6)
			ORDER BY o.payment_expires_at,o.id LIMIT ?`, limit)
		if err != nil {
			return 0, err
		}
		items := make([]dueReservation, 0, limit)
		for rows.Next() {
			var item dueReservation
			if err := rows.Scan(&item.requestID, &item.activityID, &item.userID, &item.orderID, &item.digest, &item.reservedAt); err != nil {
				rows.Close()
				return 0, err
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}

		items, err = lockExpiryResources(ctx, tx, items)
		if err != nil {
			return 0, err
		}

		for _, item := range items {
			var eligible int
			err = tx.QueryRowContext(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM flash_sale_reservation AS r
					JOIN purchase_order AS o ON o.id=r.order_id
					WHERE r.request_id=? AND r.activity_id=? AND r.user_id=? AND r.idempotency_digest=?
						AND r.reserved_at=? AND r.status='order_ready' AND o.id=?
						AND o.user_id=? AND o.source_type='flash_sale' AND o.source_reference=r.request_id
						AND o.status='pending_payment' AND o.payment_expires_at<=CURRENT_TIMESTAMP(6)
				)`, item.requestID, item.activityID, item.userID, item.digest, item.reservedAt.UTC(),
				item.orderID, item.userID).Scan(&eligible)
			if err != nil {
				return 0, err
			}
			if eligible != 1 {
				continue
			}

			orderResult, err := tx.ExecContext(ctx, `
				UPDATE purchase_order SET status='expired',updated_at=CURRENT_TIMESTAMP(6)
				WHERE id=? AND user_id=? AND status='pending_payment'`, item.orderID, item.userID)
			if err != nil {
				return 0, err
			}
			reservationResult, err := tx.ExecContext(ctx, `
				UPDATE flash_sale_reservation SET status='expired',updated_at=CURRENT_TIMESTAMP(6)
				WHERE request_id=? AND user_id=? AND activity_id=? AND status='order_ready'`,
				item.requestID, item.userID, item.activityID)
			if err != nil {
				return 0, err
			}
			activityResult, err := tx.ExecContext(ctx, `
				UPDATE flash_sale_activity SET allocated_stock=allocated_stock-1,updated_at=CURRENT_TIMESTAMP(6)
				WHERE id=? AND allocated_stock>0`, item.activityID)
			if err != nil {
				return 0, err
			}
			if err := requireOneEach(orderResult, reservationResult, activityResult); err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO flash_sale_release_job(request_id,activity_id,user_id,idempotency_digest,reserved_at,reason)
				VALUES (?,?,?,?,?,'payment_expired') AS incoming
				ON DUPLICATE KEY UPDATE request_id=incoming.request_id`,
				item.requestID, item.activityID, item.userID, item.digest, item.reservedAt.UTC()); err != nil {
				return 0, err
			}
			if err := verifyReleaseJobDigest(ctx, tx, item, "payment_expired"); err != nil {
				return 0, err
			}
			processed = append(processed, item)
		}
		return len(processed), nil
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return count, err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileExpiry(reconcileCtx, processed, err)
}

func (s *Store) ClaimReleaseJobs(ctx context.Context, limit int, lease time.Duration) ([]flashsale.ReleaseJob, error) {
	leaseMicros := lease.Microseconds()
	if limit < 1 || limit > maxBatchSize || lease <= 0 || lease%time.Microsecond != 0 {
		return nil, flashsale.ErrInvalidInput
	}

	jobs, err := mysqltx.Run(ctx, s.db, func(tx *sql.Tx) ([]flashsale.ReleaseJob, error) {
		rows, err := tx.QueryContext(ctx, selectReleaseJobIDsSQL, limit)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, 0, limit)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []flashsale.ReleaseJob{}, nil
		}

		args := make([]any, 0, len(ids)+1)
		args = append(args, leaseMicros)
		for _, id := range ids {
			args = append(args, id)
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_release_job
			SET status='leased',attempts=attempts+1,
				lease_until=TIMESTAMPADD(MICROSECOND,?,CURRENT_TIMESTAMP(6)),updated_at=CURRENT_TIMESTAMP(6)
			WHERE id IN (`+placeholders(len(ids))+`)`, args...)
		if err != nil {
			return nil, err
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected != int64(len(ids)) {
			return nil, flashsale.ErrUnavailable
		}

		queryArgs := make([]any, len(ids))
		for i, id := range ids {
			queryArgs[i] = id
		}
		rows, err = tx.QueryContext(ctx, `
			SELECT id,request_id,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,reason,attempts
			FROM flash_sale_release_job WHERE id IN (`+placeholders(len(ids))+`) ORDER BY id`, queryArgs...)
		if err != nil {
			return nil, err
		}
		jobs := make([]flashsale.ReleaseJob, 0, len(ids))
		for rows.Next() {
			var job flashsale.ReleaseJob
			if err := rows.Scan(&job.ID, &job.RequestID, &job.ActivityID, &job.UserID,
				&job.IdempotencyDigest, &job.ReservedAt, &job.Reason, &job.Attempts); err != nil {
				rows.Close()
				return nil, err
			}
			job.ReservedAt = job.ReservedAt.UTC()
			job.LeaseGeneration = job.Attempts
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		return jobs, nil
	})
	// A generation counter fences stale completion/retry, but cannot prove which
	// claimant owns a generation after an ambiguous commit. Fail closed and let
	// the lease expire instead of returning work that another worker may own.
	return jobs, err
}

func (s *Store) CompleteReleaseJob(ctx context.Context, id int64, leaseGeneration int) error {
	if id <= 0 || leaseGeneration <= 0 {
		return flashsale.ErrInvalidInput
	}
	err := run(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_release_job
			SET status='done',lease_until=NULL,completed_at=CURRENT_TIMESTAMP(6),updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='leased' AND attempts=?`, id, leaseGeneration)
		if err != nil {
			return err
		}
		if ok, err := changedOne(result); err != nil {
			return err
		} else if !ok {
			return flashsale.ErrNotFound
		}
		return nil
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileReleaseCompletion(reconcileCtx, id, leaseGeneration, err)
}

func (s *Store) RetryReleaseJob(ctx context.Context, id int64, leaseGeneration int, next time.Time, code string) error {
	if id <= 0 || leaseGeneration <= 0 {
		return flashsale.ErrInvalidInput
	}
	err := run(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE flash_sale_release_job
			SET status='pending',lease_until=NULL,next_attempt_at=?,last_error_code=?,updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='leased' AND attempts=?`, next.UTC(), code, id, leaseGeneration)
		if err != nil {
			return err
		}
		if ok, err := changedOne(result); err != nil {
			return err
		} else if !ok {
			return flashsale.ErrNotFound
		}
		return nil
	})
	if err == nil || !mysqltx.IsCommitOutcomeUnknown(err) {
		return err
	}
	reconcileCtx, cancel := reconciliationContext(ctx)
	defer cancel()
	return s.reconcileReleaseRetry(reconcileCtx, id, leaseGeneration, next, code, err)
}

func (s *Store) GetRequest(ctx context.Context, requestID string, userID int64, admin bool) (flashsale.Request, error) {
	var request flashsale.Request
	var durableStatus entity.ReservationStatus
	var orderNo, failureCode sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT r.request_id,r.activity_id,r.status,o.order_no,r.failure_code,r.payment_expires_at
		FROM flash_sale_reservation AS r LEFT JOIN purchase_order AS o ON o.id=r.order_id
		WHERE r.request_id=? AND (? OR r.user_id=?)`, requestID, admin, userID).Scan(
		&request.RequestID, &request.ActivityID, &durableStatus, &orderNo, &failureCode, &request.PaymentExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.Request{}, flashsale.ErrNotFound
	}
	if err != nil {
		return flashsale.Request{}, err
	}
	if orderNo.Valid {
		request.OrderNo = orderNo.String
	}
	if failureCode.Valid {
		request.FailureCode = failureCode.String
	}
	request.PaymentExpiresAt = request.PaymentExpiresAt.UTC()
	switch durableStatus {
	case entity.ReservationReserved:
		request.Status = flashsale.RequestProcessing
	case entity.ReservationOrderReady:
		request.Status = flashsale.RequestOrderReady
	case entity.ReservationFailed:
		request.Status = flashsale.RequestFailed
	case entity.ReservationExpired:
		request.Status = flashsale.RequestExpired
	default:
		return flashsale.Request{}, flashsale.ErrUnavailable
	}
	return request, nil
}

type scopeKey struct {
	editionID        int64
	region, currency string
}

type draftMetadata struct {
	scopeKey
	version int64
}

type dueReservation struct {
	requestID                   string
	activityID, userID, orderID int64
	digest                      []byte
	reservedAt                  time.Time
}

func readDraftMetadata(ctx context.Context, q queryer, id int64, locked bool) (draftMetadata, error) {
	query := selectDraftMetadataSQL
	if locked {
		query = lockDraftMetadataSQL
	}
	var value draftMetadata
	err := q.QueryRowContext(ctx, query, id).Scan(&value.editionID, &value.region, &value.currency, &value.version)
	if errors.Is(err, sql.ErrNoRows) {
		return draftMetadata{}, entity.ErrInvalidState
	}
	return value, err
}

func upsertAndLockScopes(ctx context.Context, tx *sql.Tx, scopes ...scopeKey) error {
	unique := make(map[scopeKey]struct{}, len(scopes))
	ordered := make([]scopeKey, 0, len(scopes))
	for _, scope := range scopes {
		if _, exists := unique[scope]; exists {
			continue
		}
		unique[scope] = struct{}{}
		ordered = append(ordered, scope)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].editionID != ordered[j].editionID {
			return ordered[i].editionID < ordered[j].editionID
		}
		if ordered[i].region != ordered[j].region {
			return ordered[i].region < ordered[j].region
		}
		return ordered[i].currency < ordered[j].currency
	})
	for _, scope := range ordered {
		if err := upsertScope(ctx, tx, scope.editionID, scope.region, scope.currency); err != nil {
			return err
		}
		if err := lockScope(ctx, tx, scope.editionID, scope.region, scope.currency); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reconcileActivityCreate(ctx context.Context, expected entity.Activity, commitErr error) (entity.Activity, error) {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("begin activity-create reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	current, err := scanActivity(tx.QueryRowContext(ctx, activitySelect+` WHERE a.code=?`, expected.Code))
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Activity{}, commitErr
	}
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("reconcile activity create: %w", err))
	}
	if !matchesCreatedActivity(current, expected) {
		return entity.Activity{}, errors.Join(commitErr, errors.New("activity create durable identity mismatch"))
	}
	return current, nil
}

func (s *Store) reconcileActivityUpdate(ctx context.Context, expected entity.Activity, expectedVersion int64, original scopeKey, commitErr error) (entity.Activity, error) {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("begin activity-update reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	current, err := queryActivity(ctx, tx, expected.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Activity{}, commitErr
	}
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("reconcile activity update: %w", err))
	}
	if current.Status != entity.StatusDraft || current.Version != expectedVersion || !matchesDraftPayload(current, expected) {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("activity update durable identity mismatch from scope %d/%s/%s", original.editionID, original.region, original.currency))
	}
	return current, nil
}

func (s *Store) reconcileActivityActivation(ctx context.Context, id, version int64, expectedScope scopeKey, commitErr error) (entity.Activity, error) {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("begin activity-activation reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	current, err := queryActivity(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Activity{}, commitErr
	}
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("reconcile activity activation: %w", err))
	}
	if current.Status != entity.StatusActive || current.Version != version || activityScope(current) != expectedScope || current.ActivatedAt.IsZero() {
		return entity.Activity{}, errors.Join(commitErr, errors.New("activity activation durable identity mismatch"))
	}
	return current, nil
}

func (s *Store) reconcileActivityCancellation(ctx context.Context, id int64, cutoff time.Time, commitErr error) (entity.Activity, error) {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("begin activity-cancellation reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	current, err := queryActivity(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return entity.Activity{}, commitErr
	}
	if err != nil {
		return entity.Activity{}, errors.Join(commitErr, fmt.Errorf("reconcile activity cancellation: %w", err))
	}
	if current.Status != entity.StatusCancelled || !sameMySQLTime(current.CancelledAt, cutoff) {
		return entity.Activity{}, errors.Join(commitErr, errors.New("activity cancellation durable identity mismatch"))
	}
	return current, nil
}

func (s *Store) reconcileAllocation(ctx context.Context, event flashsale.Event, commitErr error) (flashsale.Allocation, error) {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return flashsale.Allocation{}, errors.Join(commitErr, fmt.Errorf("begin allocation reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	current, err := queryAllocation(ctx, tx, event.RequestID)
	if errors.Is(err, flashsale.ErrNotFound) {
		return flashsale.Allocation{}, commitErr
	}
	if err != nil {
		return flashsale.Allocation{}, errors.Join(commitErr, fmt.Errorf("reconcile allocation: %w", err))
	}
	if !matchesEvent(current.ActivityID, current.UserID, current.IdempotencyDigest, current.ReservedAt, event) {
		return flashsale.Allocation{}, flashsale.ErrAlreadyReserved
	}
	return current, nil
}

func (s *Store) beginReconciliation(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

func reconciliationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), reconciliationTimeout)
}

func matchesCreatedActivity(current, expected entity.Activity) bool {
	return current.Status == entity.StatusDraft && current.Version == 0 && current.AllocatedStock == 0 &&
		current.CreatedBy == expected.CreatedBy && matchesDraftPayload(current, expected)
}

func matchesDraftPayload(current, expected entity.Activity) bool {
	return (current.ID == expected.ID || expected.ID == 0) &&
		current.Code == expected.Code && current.EditionID == expected.EditionID && current.Region == expected.Region &&
		current.Currency == expected.Currency && current.SalePriceMinor == expected.SalePriceMinor &&
		current.TotalStock == expected.TotalStock && sameMySQLTime(current.StartsAt, expected.StartsAt) &&
		sameMySQLTime(current.EndsAt, expected.EndsAt) &&
		current.PaymentTimeout/time.Second == expected.PaymentTimeout/time.Second
}

func activityScope(activity entity.Activity) scopeKey {
	return scopeKey{editionID: activity.EditionID, region: activity.Region, currency: activity.Currency}
}

func sameMySQLTime(left, right time.Time) bool {
	return left.UTC().Truncate(time.Microsecond).Equal(right.UTC().Truncate(time.Microsecond))
}

func lockUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	var lockedID int64
	err := tx.QueryRowContext(ctx, lockUserSQL, userID).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.ErrNotFound
	}
	return err
}

func lockActivity(ctx context.Context, tx *sql.Tx, activityID int64) error {
	var lockedID int64
	err := tx.QueryRowContext(ctx, lockActivitySQL, activityID).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.ErrNotFound
	}
	return err
}

func lockReservation(ctx context.Context, tx *sql.Tx, requestID string) error {
	var lockedRequestID string
	err := tx.QueryRowContext(ctx, lockReservationSQL, requestID).Scan(&lockedRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.ErrNotFound
	}
	return err
}

func verifyReservationFailure(ctx context.Context, q queryer, event flashsale.Event, failureCode string) error {
	var activityID, userID int64
	var digestHex, status, storedFailure string
	var reservedAt time.Time
	err := q.QueryRowContext(ctx, `
		SELECT activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,status,COALESCE(failure_code,'')
		FROM flash_sale_reservation WHERE request_id=? FOR SHARE`, event.RequestID).Scan(
		&activityID, &userID, &digestHex, &reservedAt, &status, &storedFailure,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.ErrUnsupportedEvent
	}
	if err != nil {
		return err
	}
	if status != string(entity.ReservationFailed) || storedFailure != failureCode ||
		!matchesEvent(activityID, userID, digestHex, reservedAt, event) {
		return flashsale.ErrUnsupportedEvent
	}
	return nil
}

func verifyReleaseJob(ctx context.Context, q queryer, event flashsale.Event, reason string) error {
	var activityID, userID int64
	var digestHex, storedReason string
	var reservedAt time.Time
	err := q.QueryRowContext(ctx, `
		SELECT activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,reason
		FROM flash_sale_release_job WHERE request_id=? FOR SHARE`, event.RequestID).Scan(
		&activityID, &userID, &digestHex, &reservedAt, &storedReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.ErrUnsupportedEvent
	}
	if err != nil {
		return err
	}
	if storedReason != reason || !matchesEvent(activityID, userID, digestHex, reservedAt, event) {
		return flashsale.ErrUnsupportedEvent
	}
	return nil
}

func matchesEvent(activityID, userID int64, digest string, reservedAt time.Time, event flashsale.Event) bool {
	return activityID == event.ActivityID && userID == event.UserID && digest == event.IdempotencyDigest &&
		sameMySQLTime(reservedAt, event.ReservedAt)
}

func (s *Store) reconcileOrderReady(ctx context.Context, requestID, orderNo string, commitErr error) error {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("begin order-ready reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	var durableRequestID string
	err = tx.QueryRowContext(ctx, `
		SELECT r.request_id
		FROM flash_sale_reservation AS r
		JOIN purchase_order AS o ON o.id=r.order_id
		WHERE r.request_id=? AND r.status='order_ready' AND o.order_no=?
			AND o.source_type='flash_sale' AND o.source_reference=r.request_id
			AND o.user_id=r.user_id FOR SHARE`, requestID, orderNo).Scan(&durableRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return commitErr
	}
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile order ready: %w", err))
	}
	return commitReconciliation(tx, commitErr, "order ready")
}

func (s *Store) reconcileFailure(ctx context.Context, event flashsale.Event, failureCode, reason string, commitErr error) error {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("begin failure reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	var status entity.ReservationStatus
	var activityID, userID int64
	var digestHex string
	var reservedAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT status,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at
		FROM flash_sale_reservation WHERE request_id=? FOR SHARE`, event.RequestID).Scan(
		&status, &activityID, &userID, &digestHex, &reservedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return commitErr
	}
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile failure reservation: %w", err))
	}
	if !matchesEvent(activityID, userID, digestHex, reservedAt, event) {
		return flashsale.ErrUnsupportedEvent
	}
	if status == entity.ReservationOrderReady || status == entity.ReservationExpired {
		return flashsale.ErrUnsupportedEvent
	}
	if err := verifyReservationFailure(ctx, tx, event, failureCode); err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile failure state: %w", err))
	}
	if err := verifyReleaseJob(ctx, tx, event, reason); err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile release job: %w", err))
	}
	return commitReconciliation(tx, commitErr, "failure")
}

func lockExpiryResources(ctx context.Context, tx *sql.Tx, items []dueReservation) ([]dueReservation, error) {
	active := make([]bool, len(items))
	for i := range active {
		active[i] = true
	}
	intLocks := []struct {
		query string
		key   func(dueReservation) int64
	}{
		{lockUserSkipLockedSQL, func(item dueReservation) int64 { return item.userID }},
		{lockActivitySkipLockedSQL, func(item dueReservation) int64 { return item.activityID }},
		{lockOrderSkipLockedSQL, func(item dueReservation) int64 { return item.orderID }},
	}
	for _, lock := range intLocks {
		ordered := sortedUniqueInt64(items, active, lock.key)
		for _, key := range ordered {
			var lockedID int64
			err := tx.QueryRowContext(ctx, lock.query, key).Scan(&lockedID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			if errors.Is(err, sql.ErrNoRows) {
				for i, item := range items {
					if lock.key(item) == key {
						active[i] = false
					}
				}
			}
		}
	}

	requests := make([]string, 0, len(items))
	for i, item := range items {
		if active[i] {
			requests = append(requests, item.requestID)
		}
	}
	sort.Strings(requests)
	for _, requestID := range requests {
		var lockedRequestID string
		err := tx.QueryRowContext(ctx, lockReservationSkipSQL, requestID).Scan(&lockedRequestID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			for i, item := range items {
				if item.requestID == requestID {
					active[i] = false
				}
			}
		}
	}

	locked := make([]dueReservation, 0, len(items))
	for i, item := range items {
		if active[i] {
			locked = append(locked, item)
		}
	}
	return locked, nil
}

func sortedUniqueInt64(items []dueReservation, active []bool, key func(dueReservation) int64) []int64 {
	unique := make(map[int64]struct{}, len(items))
	for i, item := range items {
		if active[i] {
			unique[key(item)] = struct{}{}
		}
	}
	ordered := make([]int64, 0, len(unique))
	for value := range unique {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered
}

func requireOneEach(results ...sql.Result) error {
	for _, result := range results {
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return flashsale.ErrUnavailable
		}
	}
	return nil
}

func verifyReleaseJobDigest(ctx context.Context, q queryer, item dueReservation, reason string) error {
	var activityID, userID int64
	var digest []byte
	var reservedAt time.Time
	var storedReason string
	err := q.QueryRowContext(ctx, `
		SELECT activity_id,user_id,idempotency_digest,reserved_at,reason
		FROM flash_sale_release_job WHERE request_id=? FOR SHARE`, item.requestID).Scan(
		&activityID, &userID, &digest, &reservedAt, &storedReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.ErrUnavailable
	}
	if err != nil {
		return err
	}
	if activityID != item.activityID || userID != item.userID || !equalBytes(digest, item.digest) ||
		!sameMySQLTime(reservedAt, item.reservedAt) || storedReason != reason {
		return flashsale.ErrUnsupportedEvent
	}
	return nil
}

func (s *Store) reconcileExpiry(ctx context.Context, items []dueReservation, commitErr error) (int, error) {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return 0, errors.Join(commitErr, fmt.Errorf("begin expiry reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	for _, item := range items {
		var requestID string
		err = tx.QueryRowContext(ctx, `
			SELECT r.request_id FROM flash_sale_reservation AS r
			JOIN purchase_order AS o ON o.id=r.order_id
			WHERE r.request_id=? AND r.status='expired' AND r.activity_id=? AND r.user_id=?
				AND r.idempotency_digest=? AND r.reserved_at=? AND o.id=? AND o.status='expired'
				AND o.user_id=r.user_id AND o.source_type='flash_sale' AND o.source_reference=r.request_id
			FOR SHARE`,
			item.requestID, item.activityID, item.userID, item.digest, item.reservedAt.UTC(), item.orderID).Scan(&requestID)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, commitErr
		}
		if err != nil {
			return 0, errors.Join(commitErr, fmt.Errorf("reconcile expiry state: %w", err))
		}
		if err := verifyReleaseJobDigest(ctx, tx, item, "payment_expired"); err != nil {
			return 0, errors.Join(commitErr, fmt.Errorf("reconcile expiry release job: %w", err))
		}
	}
	if err := commitReconciliation(tx, commitErr, "expiry"); err != nil {
		return 0, err
	}
	return len(items), nil
}

func (s *Store) reconcileReleaseCompletion(ctx context.Context, id int64, leaseGeneration int, commitErr error) error {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("begin release completion reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	var attempts int
	var leaseUntil, completedAt sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT status,attempts,lease_until,completed_at FROM flash_sale_release_job WHERE id=? FOR SHARE`, id).Scan(
		&status, &attempts, &leaseUntil, &completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return commitErr
	}
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile release completion: %w", err))
	}
	if status == "done" && attempts == leaseGeneration && !leaseUntil.Valid && completedAt.Valid {
		return commitReconciliation(tx, commitErr, "release completion")
	}
	return commitErr
}

func (s *Store) reconcileReleaseRetry(ctx context.Context, id int64, leaseGeneration int, next time.Time, code string, commitErr error) error {
	tx, err := s.beginReconciliation(ctx)
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("begin release retry reconciliation: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	var status, lastError string
	var attempts int
	var nextAttempt time.Time
	var leaseUntil sql.NullTime
	err = tx.QueryRowContext(ctx, `
		SELECT status,attempts,next_attempt_at,COALESCE(last_error_code,''),lease_until
		FROM flash_sale_release_job WHERE id=? FOR SHARE`, id).Scan(&status, &attempts, &nextAttempt, &lastError, &leaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return commitErr
	}
	if err != nil {
		return errors.Join(commitErr, fmt.Errorf("reconcile release retry: %w", err))
	}
	if status == "pending" && attempts == leaseGeneration && lastError == code &&
		!leaseUntil.Valid && sameMySQLTime(nextAttempt, next) {
		return commitReconciliation(tx, commitErr, "release retry")
	}
	return commitErr
}

func commitReconciliation(tx *sql.Tx, commitErr error, operation string) error {
	if err := tx.Commit(); err != nil {
		return errors.Join(commitErr, fmt.Errorf("commit %s reconciliation: %w", operation, err))
	}
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func upsertScope(ctx context.Context, tx *sql.Tx, editionID int64, region, currency string) error {
	_, err := tx.ExecContext(ctx, upsertScopeSQL, editionID, region, currency)
	return err
}

func lockScope(ctx context.Context, tx *sql.Tx, editionID int64, region, currency string) error {
	var lockedEditionID int64
	return tx.QueryRowContext(ctx, lockScopeSQL, editionID, region, currency).Scan(&lockedEditionID)
}

func run(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	_, err := mysqltx.Run(ctx, db, func(tx *sql.Tx) (struct{}, error) {
		return struct{}{}, fn(tx)
	})
	return err
}

func queryActivity(ctx context.Context, q queryer, id int64) (entity.Activity, error) {
	return scanActivity(q.QueryRowContext(ctx, activitySelect+` WHERE a.id=?`, id))
}

func queryAllocation(ctx context.Context, q queryer, requestID string) (flashsale.Allocation, error) {
	var result flashsale.Allocation
	var activity entity.Activity
	var paymentTimeoutSeconds int64
	var activatedAt, cancelledAt sql.NullTime
	err := q.QueryRowContext(ctx, `
		SELECT r.request_id,r.activity_id,r.user_id,LOWER(HEX(r.idempotency_digest)),r.status,r.reserved_at,r.payment_expires_at,
			a.code,g.slug,g.name,a.edition_id,e.name,a.region_code,a.currency,a.sale_price_minor,a.total_stock,a.allocated_stock,
			a.status,a.starts_at,a.ends_at,a.payment_timeout_seconds,a.version,a.created_by,
			a.activated_at,a.cancelled_at,a.created_at,a.updated_at
		FROM flash_sale_reservation AS r JOIN flash_sale_activity AS a ON a.id=r.activity_id
		JOIN game_edition AS e ON e.id=a.edition_id JOIN game AS g ON g.id=e.game_id
		WHERE r.request_id=?`, requestID).Scan(
		&result.RequestID, &result.ActivityID, &result.UserID, &result.IdempotencyDigest, &result.Status,
		&result.ReservedAt, &result.PaymentExpiresAt,
		&activity.Code, &activity.GameSlug, &activity.GameName, &activity.EditionID, &activity.EditionName,
		&activity.Region, &activity.Currency, &activity.SalePriceMinor, &activity.TotalStock, &activity.AllocatedStock,
		&activity.Status, &activity.StartsAt, &activity.EndsAt, &paymentTimeoutSeconds, &activity.Version, &activity.CreatedBy,
		&activatedAt, &cancelledAt, &activity.CreatedAt, &activity.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return flashsale.Allocation{}, flashsale.ErrNotFound
	}
	if err != nil {
		return flashsale.Allocation{}, err
	}
	activity.ID = result.ActivityID
	activity.PaymentTimeout = time.Duration(paymentTimeoutSeconds) * time.Second
	setActivityTimes(&activity, activatedAt, cancelledAt)
	result.ReservedAt = result.ReservedAt.UTC()
	result.PaymentExpiresAt = result.PaymentExpiresAt.UTC()
	result.Activity = activity
	return result, nil
}

const activitySelect = `
	SELECT a.id,a.code,g.slug,g.name,a.edition_id,e.name,a.region_code,a.currency,a.sale_price_minor,a.total_stock,a.allocated_stock,
		a.status,a.starts_at,a.ends_at,a.payment_timeout_seconds,a.version,a.created_by,
		a.activated_at,a.cancelled_at,a.created_at,a.updated_at
	FROM flash_sale_activity AS a JOIN game_edition AS e ON e.id=a.edition_id JOIN game AS g ON g.id=e.game_id`

type scanner interface {
	Scan(...any) error
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanActivity(row scanner) (entity.Activity, error) {
	var activity entity.Activity
	var paymentTimeoutSeconds int64
	var activatedAt, cancelledAt sql.NullTime
	err := row.Scan(
		&activity.ID, &activity.Code, &activity.GameSlug, &activity.GameName, &activity.EditionID, &activity.EditionName,
		&activity.Region, &activity.Currency, &activity.SalePriceMinor, &activity.TotalStock, &activity.AllocatedStock,
		&activity.Status, &activity.StartsAt, &activity.EndsAt, &paymentTimeoutSeconds, &activity.Version, &activity.CreatedBy,
		&activatedAt, &cancelledAt, &activity.CreatedAt, &activity.UpdatedAt,
	)
	if err != nil {
		return entity.Activity{}, err
	}
	activity.PaymentTimeout = time.Duration(paymentTimeoutSeconds) * time.Second
	setActivityTimes(&activity, activatedAt, cancelledAt)
	return activity, nil
}

func setActivityTimes(activity *entity.Activity, activatedAt, cancelledAt sql.NullTime) {
	activity.StartsAt = activity.StartsAt.UTC()
	activity.EndsAt = activity.EndsAt.UTC()
	activity.CreatedAt = activity.CreatedAt.UTC()
	activity.UpdatedAt = activity.UpdatedAt.UTC()
	if activatedAt.Valid {
		activity.ActivatedAt = activatedAt.Time.UTC()
	}
	if cancelledAt.Valid {
		activity.CancelledAt = cancelledAt.Time.UTC()
	}
}

func decodeDigest(value string) ([]byte, error) {
	digest, err := hex.DecodeString(value)
	if err != nil || len(digest) != sha256Size {
		return nil, flashsale.ErrUnsupportedEvent
	}
	return digest, nil
}

func changedOne(result sql.Result) (bool, error) {
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

const (
	sha256Size   = 32
	maxBatchSize = 1000
)

var _ flashsale.Store = (*Store)(nil)
