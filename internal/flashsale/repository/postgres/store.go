package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) ListActivities(ctx context.Context, filter flashsale.ListFilter) ([]entity.Activity, error) {
	rows, err := s.pool.Query(ctx, activitySelect+`
		where a.status in ('active','cancelled','ended') and ($1::bigint=0 or a.id<$1)
		order by a.id desc limit $2`, filter.BeforeID, filter.Limit)
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
	return items, rows.Err()
}

func (s *Store) CreateActivity(ctx context.Context, activity entity.Activity) (entity.Activity, error) {
	var id int64
	var createdAt time.Time
	err := s.pool.QueryRow(ctx, `
		insert into flash_sale_activity(
			code,edition_id,region_code,currency,sale_price_minor,total_stock,allocated_stock,status,
			starts_at,ends_at,payment_timeout_seconds,version,created_by
		) values ($1,$2,$3,$4,$5,$6,0,'draft',$7,$8,$9,0,$10)
		returning id,created_at`, activity.Code, activity.EditionID, activity.Region, activity.Currency,
		activity.SalePriceMinor, activity.TotalStock, activity.StartsAt, activity.EndsAt,
		int64(activity.PaymentTimeout/time.Second), activity.CreatedBy).Scan(&id, &createdAt)
	if err != nil {
		return entity.Activity{}, err
	}
	activity.ID = id
	activity.Status = entity.StatusDraft
	activity.CreatedAt = createdAt
	activity.UpdatedAt = createdAt
	return s.GetActivity(ctx, id)
}

func (s *Store) UpdateDraft(ctx context.Context, activity entity.Activity) (entity.Activity, error) {
	if activity.ID <= 0 {
		return entity.Activity{}, flashsale.ErrInvalidInput
	}
	tag, err := s.pool.Exec(ctx, `
		update flash_sale_activity set code=$2,edition_id=$3,region_code=$4,currency=$5,
			sale_price_minor=$6,total_stock=$7,starts_at=$8,ends_at=$9,payment_timeout_seconds=$10,
			version=version+1,updated_at=statement_timestamp()
		where id=$1 and status='draft'`, activity.ID, activity.Code, activity.EditionID,
		activity.Region, activity.Currency, activity.SalePriceMinor, activity.TotalStock,
		activity.StartsAt, activity.EndsAt, int64(activity.PaymentTimeout/time.Second))
	if err != nil {
		return entity.Activity{}, err
	}
	if tag.RowsAffected() != 1 {
		return entity.Activity{}, entity.ErrInvalidState
	}
	return s.GetActivity(ctx, activity.ID)
}

func (s *Store) ActivateActivity(ctx context.Context, id, version int64, _ time.Time) (entity.Activity, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return entity.Activity{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var editionID int64
	var region, currency string
	var startsAt, endsAt time.Time
	err = tx.QueryRow(ctx, `
		select edition_id,region_code,currency,starts_at,ends_at
		from flash_sale_activity where id=$1 and status='draft' and version=$2-1 for update`, id, version).Scan(&editionID, &region, &currency, &startsAt, &endsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Activity{}, entity.ErrInvalidState
	}
	if err != nil {
		return entity.Activity{}, err
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1::bigint::text||':'||$2::text||':'||$3::text,0))`, editionID, region, currency); err != nil {
		return entity.Activity{}, err
	}
	var overlapping bool
	if err := tx.QueryRow(ctx, `
		select exists(select 1 from flash_sale_activity
		where id<>$1 and edition_id=$2 and region_code=$3 and currency=$4 and status='active'
			and starts_at<$6 and ends_at>$5)`,
		id, editionID, region, currency, startsAt, endsAt).Scan(&overlapping); err != nil {
		return entity.Activity{}, err
	}
	if overlapping {
		return entity.Activity{}, entity.ErrInvalidState
	}
	tag, err := tx.Exec(ctx, `
		update flash_sale_activity set status='active',version=$2,
			activated_at=statement_timestamp(),updated_at=statement_timestamp()
		where id=$1 and status='draft' and version=$2-1 and ends_at>statement_timestamp()`, id, version)
	if err != nil {
		return entity.Activity{}, err
	}
	if tag.RowsAffected() != 1 {
		return entity.Activity{}, entity.ErrInvalidState
	}
	if err := tx.Commit(ctx); err != nil {
		return entity.Activity{}, err
	}
	return s.GetActivity(ctx, id)
}

func (s *Store) CancelActivity(ctx context.Context, id int64, cutoff time.Time) (entity.Activity, error) {
	tag, err := s.pool.Exec(ctx, `
		update flash_sale_activity set status='cancelled',cancelled_at=$2,updated_at=statement_timestamp()
		where id=$1 and (status='active' or (status='cancelled' and cancelled_at=$2))`, id, cutoff.UTC())
	if err != nil {
		return entity.Activity{}, err
	}
	if tag.RowsAffected() != 1 {
		return entity.Activity{}, entity.ErrInvalidState
	}
	return s.GetActivity(ctx, id)
}

func (s *Store) GetActivity(ctx context.Context, id int64) (entity.Activity, error) {
	activity, err := scanActivity(s.pool.QueryRow(ctx, activitySelect+` where a.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.Activity{}, flashsale.ErrNotFound
	}
	return activity, err
}

func (s *Store) Allocate(ctx context.Context, event flashsale.Event) (result flashsale.Allocation, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return flashsale.Allocation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	activity, err := scanActivity(tx.QueryRow(ctx, activitySelect+` where a.id=$1 for update of a`, event.ActivityID))
	if errors.Is(err, pgx.ErrNoRows) {
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
			existing.IdempotencyDigest != event.IdempotencyDigest || !existing.ReservedAt.Equal(event.ReservedAt) {
			return flashsale.Allocation{}, flashsale.ErrAlreadyReserved
		}
		return existing, nil
	}
	if !errors.Is(err, flashsale.ErrNotFound) {
		return flashsale.Allocation{}, err
	}
	var otherRequest string
	err = tx.QueryRow(ctx, `select request_id from flash_sale_reservation where activity_id=$1 and user_id=$2`, event.ActivityID, event.UserID).Scan(&otherRequest)
	if err == nil {
		return flashsale.Allocation{}, flashsale.ErrAlreadyReserved
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return flashsale.Allocation{}, err
	}
	if activity.AllocatedStock >= activity.TotalStock {
		return flashsale.Allocation{}, flashsale.ErrStockExhausted
	}
	digest, err := hex.DecodeString(event.IdempotencyDigest)
	if err != nil || len(digest) != sha256Size {
		return flashsale.Allocation{}, flashsale.ErrUnsupportedEvent
	}
	var effectiveNow time.Time
	if err := tx.QueryRow(ctx, `select statement_timestamp()`).Scan(&effectiveNow); err != nil {
		return flashsale.Allocation{}, err
	}
	deadline := effectiveNow.Add(activity.PaymentTimeout)
	if _, err := tx.Exec(ctx, `
		insert into flash_sale_reservation(
			request_id,activity_id,user_id,idempotency_digest,status,reserved_at,payment_expires_at,created_at,updated_at
		) values ($1,$2,$3,$4,'reserved',$5,$6,$7,$7)`,
		event.RequestID, event.ActivityID, event.UserID, digest, event.ReservedAt.UTC(), deadline, effectiveNow); err != nil {
		return flashsale.Allocation{}, err
	}
	tag, err := tx.Exec(ctx, `
		update flash_sale_activity set allocated_stock=allocated_stock+1,updated_at=$2
		where id=$1 and allocated_stock<total_stock`, activity.ID, effectiveNow)
	if err != nil {
		return flashsale.Allocation{}, err
	}
	if tag.RowsAffected() != 1 {
		return flashsale.Allocation{}, flashsale.ErrStockExhausted
	}
	if err := tx.Commit(ctx); err != nil {
		return flashsale.Allocation{}, err
	}
	activity.AllocatedStock++
	return flashsale.Allocation{
		RequestID: event.RequestID, ActivityID: activity.ID, UserID: event.UserID,
		IdempotencyDigest: event.IdempotencyDigest, Status: entity.ReservationReserved,
		ReservedAt: event.ReservedAt.UTC(), PaymentExpiresAt: deadline, Activity: activity,
	}, nil
}

func (s *Store) MarkOrderReady(ctx context.Context, requestID, orderNo string) error {
	tag, err := s.pool.Exec(ctx, `
		update flash_sale_reservation r
		set status='order_ready',order_id=o.id,updated_at=statement_timestamp()
		from purchase_order o
		where r.request_id=$1 and o.order_no=$2 and o.source_type='flash_sale'
			and o.source_reference=r.request_id and o.user_id=r.user_id
			and (r.status='reserved' or (r.status='order_ready' and r.order_id=o.id))`, requestID, orderNo)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return flashsale.ErrNotFound
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, event flashsale.Event, failureCode, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	activity, err := scanActivity(tx.QueryRow(ctx, activitySelect+` where a.id=$1 for update of a`, event.ActivityID))
	if errors.Is(err, pgx.ErrNoRows) {
		return flashsale.ErrNotFound
	}
	if err != nil {
		return err
	}
	digest, err := hex.DecodeString(event.IdempotencyDigest)
	if err != nil || len(digest) != sha256Size {
		return flashsale.ErrUnsupportedEvent
	}
	var status entity.ReservationStatus
	var activityID, userID int64
	var digestHex string
	var reservedAt time.Time
	err = tx.QueryRow(ctx, `
		select status,activity_id,user_id,encode(idempotency_digest,'hex'),reserved_at
		from flash_sale_reservation where request_id=$1 for update`, event.RequestID).Scan(
		&status, &activityID, &userID, &digestHex, &reservedAt,
	)
	if err == nil && (activityID != event.ActivityID || userID != event.UserID || digestHex != event.IdempotencyDigest || !reservedAt.Equal(event.ReservedAt)) {
		return flashsale.ErrUnsupportedEvent
	}
	switch {
	case err == nil && (status == entity.ReservationOrderReady || status == entity.ReservationExpired):
		return tx.Commit(ctx)
	case err == nil && status == entity.ReservationReserved:
		tag, updateErr := tx.Exec(ctx, `
			update flash_sale_reservation set status='failed',failure_code=$2,updated_at=statement_timestamp()
			where request_id=$1 and status='reserved'`, event.RequestID, failureCode)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() == 1 {
			if _, updateErr := tx.Exec(ctx, `update flash_sale_activity set allocated_stock=allocated_stock-1,updated_at=statement_timestamp() where id=$1 and allocated_stock>0`, event.ActivityID); updateErr != nil {
				return updateErr
			}
		}
	case err == nil && status == entity.ReservationFailed:
	case errors.Is(err, pgx.ErrNoRows):
		deadline := event.ReservedAt.UTC().Add(activity.PaymentTimeout)
		if _, insertErr := tx.Exec(ctx, `
			insert into flash_sale_reservation(
				request_id,activity_id,user_id,idempotency_digest,status,failure_code,reserved_at,payment_expires_at
			) values ($1,$2,$3,$4,'failed',$5,$6,$7)
			on conflict do nothing`, event.RequestID, event.ActivityID, event.UserID, digest, failureCode, event.ReservedAt.UTC(), deadline); insertErr != nil {
			return insertErr
		}
	default:
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into flash_sale_release_job(request_id,activity_id,user_id,idempotency_digest,reserved_at,reason)
		values ($1,$2,$3,$4,$5,$6) on conflict (request_id) do nothing`,
		event.RequestID, event.ActivityID, event.UserID, digest, event.ReservedAt.UTC(), reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ExpireDue(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, flashsale.ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		select r.request_id,r.activity_id,r.user_id,o.id
		from flash_sale_reservation r join purchase_order o on o.id=r.order_id
		where r.status='order_ready' and o.status='pending_payment'
			and o.source_type='flash_sale' and o.payment_expires_at<=statement_timestamp()
		order by o.payment_expires_at,o.id for update of r,o skip locked limit $1`, limit)
	if err != nil {
		return 0, err
	}
	type due struct {
		requestID                   string
		activityID, userID, orderID int64
	}
	items := make([]due, 0, limit)
	for rows.Next() {
		var item due
		if err := rows.Scan(&item.requestID, &item.activityID, &item.userID, &item.orderID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, item := range items {
		if _, err := tx.Exec(ctx, `update purchase_order set status='expired',updated_at=statement_timestamp() where id=$1 and status='pending_payment'`, item.orderID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `update flash_sale_reservation set status='expired',updated_at=statement_timestamp() where request_id=$1 and status='order_ready'`, item.requestID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `update flash_sale_activity set allocated_stock=allocated_stock-1,updated_at=statement_timestamp() where id=$1 and allocated_stock>0`, item.activityID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			insert into flash_sale_release_job(request_id,activity_id,user_id,idempotency_digest,reserved_at,reason)
			select request_id,activity_id,user_id,idempotency_digest,reserved_at,'payment_expired'
			from flash_sale_reservation where request_id=$1 on conflict (request_id) do nothing`, item.requestID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(items), nil
}

func (s *Store) ClaimReleaseJobs(ctx context.Context, limit int, lease time.Duration) ([]flashsale.ReleaseJob, error) {
	if limit < 1 || limit > 1000 || lease <= 0 {
		return nil, flashsale.ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		with due as (
			select j.id from flash_sale_release_job j
			where (j.status='pending' and j.next_attempt_at<=statement_timestamp())
				or (j.status='leased' and j.lease_until<statement_timestamp())
			order by j.next_attempt_at,j.id for update skip locked limit $1
		), claimed as (
			update flash_sale_release_job j set status='leased',attempts=attempts+1,
				lease_until=statement_timestamp()+($2::bigint * interval '1 millisecond'),updated_at=statement_timestamp()
			from due where j.id=due.id
			returning j.id,j.request_id,j.activity_id,j.user_id,j.reason,j.attempts
		)
		select c.id,c.request_id,c.activity_id,c.user_id,encode(c.idempotency_digest,'hex'),c.reserved_at,c.reason,c.attempts
		from claimed c order by c.id`, limit, lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	jobs := make([]flashsale.ReleaseJob, 0, limit)
	for rows.Next() {
		var job flashsale.ReleaseJob
		if err := rows.Scan(&job.ID, &job.RequestID, &job.ActivityID, &job.UserID, &job.IdempotencyDigest, &job.ReservedAt, &job.Reason, &job.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) CompleteReleaseJob(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `update flash_sale_release_job set status='done',lease_until=null,completed_at=statement_timestamp(),updated_at=statement_timestamp() where id=$1 and status='leased'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return flashsale.ErrNotFound
	}
	return nil
}

func (s *Store) RetryReleaseJob(ctx context.Context, id int64, next time.Time, code string) error {
	tag, err := s.pool.Exec(ctx, `update flash_sale_release_job set status='pending',lease_until=null,next_attempt_at=$2,last_error_code=$3,updated_at=statement_timestamp() where id=$1 and status='leased'`, id, next.UTC(), code)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return flashsale.ErrNotFound
	}
	return nil
}

func (s *Store) GetRequest(ctx context.Context, requestID string, userID int64, admin bool) (flashsale.Request, error) {
	var request flashsale.Request
	var durableStatus entity.ReservationStatus
	var orderNo *string
	var failureCode *string
	err := s.pool.QueryRow(ctx, `
		select r.request_id,r.activity_id,r.status,o.order_no,r.failure_code,r.payment_expires_at
		from flash_sale_reservation r left join purchase_order o on o.id=r.order_id
		where r.request_id=$1 and ($3 or r.user_id=$2)`, requestID, userID, admin).Scan(
		&request.RequestID, &request.ActivityID, &durableStatus, &orderNo, &failureCode, &request.PaymentExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return flashsale.Request{}, flashsale.ErrNotFound
	}
	if orderNo != nil {
		request.OrderNo = *orderNo
	}
	if failureCode != nil {
		request.FailureCode = *failureCode
	}
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
	return request, err
}

func queryAllocation(ctx context.Context, q queryer, requestID string) (flashsale.Allocation, error) {
	var result flashsale.Allocation
	var activity entity.Activity
	var paymentTimeoutSeconds int64
	err := q.QueryRow(ctx, `
		select r.request_id,r.activity_id,r.user_id,encode(r.idempotency_digest,'hex'),r.status,r.reserved_at,r.payment_expires_at,
			a.code,g.slug,g.name,a.edition_id,e.name,a.region_code,a.currency,a.sale_price_minor,a.total_stock,a.allocated_stock,
			a.status,a.starts_at,a.ends_at,a.payment_timeout_seconds,a.version,a.created_by,
			a.activated_at,a.cancelled_at,a.created_at,a.updated_at
		from flash_sale_reservation r join flash_sale_activity a on a.id=r.activity_id
		join game_edition e on e.id=a.edition_id join game g on g.id=e.game_id
		where r.request_id=$1`, requestID).Scan(
		&result.RequestID, &result.ActivityID, &result.UserID, &result.IdempotencyDigest, &result.Status, &result.ReservedAt, &result.PaymentExpiresAt,
		&activity.Code, &activity.GameSlug, &activity.GameName, &activity.EditionID, &activity.EditionName, &activity.Region, &activity.Currency, &activity.SalePriceMinor,
		&activity.TotalStock, &activity.AllocatedStock, &activity.Status, &activity.StartsAt, &activity.EndsAt,
		&paymentTimeoutSeconds, &activity.Version, &activity.CreatedBy,
		nullableTimeScanner{value: &activity.ActivatedAt}, nullableTimeScanner{value: &activity.CancelledAt},
		&activity.CreatedAt, &activity.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return flashsale.Allocation{}, flashsale.ErrNotFound
	}
	activity.ID = result.ActivityID
	activity.PaymentTimeout = time.Duration(paymentTimeoutSeconds) * time.Second
	result.Activity = activity
	return result, err
}

const activitySelect = `
	select a.id,a.code,g.slug,g.name,a.edition_id,e.name,a.region_code,a.currency,a.sale_price_minor,a.total_stock,a.allocated_stock,
		a.status,a.starts_at,a.ends_at,a.payment_timeout_seconds,a.version,a.created_by,
		a.activated_at,a.cancelled_at,a.created_at,a.updated_at
	from flash_sale_activity a join game_edition e on e.id=a.edition_id join game g on g.id=e.game_id`

type scanner interface{ Scan(...any) error }
type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanActivity(row scanner) (entity.Activity, error) {
	var activity entity.Activity
	var paymentTimeoutSeconds int64
	err := row.Scan(
		&activity.ID, &activity.Code, &activity.GameSlug, &activity.GameName, &activity.EditionID, &activity.EditionName, &activity.Region, &activity.Currency,
		&activity.SalePriceMinor, &activity.TotalStock, &activity.AllocatedStock, &activity.Status,
		&activity.StartsAt, &activity.EndsAt, &paymentTimeoutSeconds, &activity.Version, &activity.CreatedBy,
		nullableTimeScanner{value: &activity.ActivatedAt}, nullableTimeScanner{value: &activity.CancelledAt},
		&activity.CreatedAt, &activity.UpdatedAt,
	)
	activity.PaymentTimeout = time.Duration(paymentTimeoutSeconds) * time.Second
	return activity, err
}

type nullableTimeScanner struct{ value *time.Time }

func (s nullableTimeScanner) Scan(src any) error {
	if src == nil {
		*s.value = time.Time{}
		return nil
	}
	value, ok := src.(time.Time)
	if !ok {
		return errors.New("invalid nullable timestamp")
	}
	*s.value = value
	return nil
}

const sha256Size = 32

var _ flashsale.Store = (*Store)(nil)
