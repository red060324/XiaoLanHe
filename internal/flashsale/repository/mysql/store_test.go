package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/red060324/XiaoLanHe/internal/adapter/mysqltx"
	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

func TestCreateActivityUpsertsAndLocksScopeBeforeInsert(t *testing.T) {
	store, mock := newMockStore(t)
	startsAt := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	endsAt := startsAt.Add(time.Hour)
	createdAt := startsAt.Add(-time.Hour)
	activity := entity.Activity{
		Code: "SALE-ONE", EditionID: 7, Region: "CN", Currency: "CNY",
		SalePriceMinor: 9900, TotalStock: 5, StartsAt: startsAt, EndsAt: endsAt,
		PaymentTimeout: 15 * time.Minute, CreatedBy: 11,
	}

	mock.ExpectBegin()
	mock.ExpectExec(upsertScopeSQL).WithArgs(int64(7), "CN", "CNY").
		WillReturnResult(newResult(0, 0))
	mock.ExpectQuery(lockScopeSQL).WithArgs(int64(7), "CN", "CNY").
		WillReturnRows(newRows("edition_id").AddRow(int64(7)))
	mock.ExpectExec(insertActivitySQL).
		WithArgs("SALE-ONE", int64(7), "CN", "CNY", int64(9900), int64(5),
			startsAt, endsAt, int64(900), int64(11)).
		WillReturnResult(newResult(41, 1))
	mock.ExpectQuery(activitySelect + ` WHERE a.id=?`).WithArgs(int64(41)).WillReturnRows(
		activityRows().AddRow(41, "SALE-ONE", "game", "Game", 7, "Deluxe", "CN", "CNY",
			9900, 5, 0, "draft", startsAt, endsAt, 900, 0, 11, nil, nil, createdAt, createdAt),
	)
	mock.ExpectCommit()

	got, err := store.CreateActivity(context.Background(), activity)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 41 || got.Status != entity.StatusDraft || got.PaymentTimeout != 15*time.Minute {
		t.Fatalf("activity = %#v", got)
	}
}

func TestCreateActivityRollsBackWhenScopeLockFails(t *testing.T) {
	store, mock := newMockStore(t)
	want := errors.New("scope lock")
	mock.ExpectBegin()
	mock.ExpectExec(upsertScopeSQL).WithArgs(int64(7), "CN", "CNY").
		WillReturnResult(newResult(0, 0))
	mock.ExpectQuery(lockScopeSQL).WithArgs(int64(7), "CN", "CNY").WillReturnError(want)
	mock.ExpectRollback()

	_, err := store.CreateActivity(context.Background(), entity.Activity{
		EditionID: 7, Region: "CN", Currency: "CNY",
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestClaimReleaseJobsSelectsUpdatesAndReadsInOneTransaction(t *testing.T) {
	store, mock := newMockStore(t)
	reservedAt := time.Date(2026, 9, 7, 2, 3, 4, 500000000, time.UTC)
	requestOne := "fsr_1_0123456789abcdef0123456789abcdef"
	requestTwo := "fsr_2_fedcba9876543210fedcba9876543210"

	mock.ExpectBegin()
	mock.ExpectQuery(`
		SELECT id FROM flash_sale_release_job
		WHERE claimable_at<=CURRENT_TIMESTAMP(6)
		ORDER BY claimable_at,id LIMIT ? FOR UPDATE SKIP LOCKED`).WithArgs(2).WillReturnRows(
		newRows("id").AddRow(int64(4)).AddRow(int64(9)),
	)
	mock.ExpectExec(`
			UPDATE flash_sale_release_job
			SET status='leased',attempts=attempts+1,
				lease_until=TIMESTAMPADD(MICROSECOND,?,CURRENT_TIMESTAMP(6)),updated_at=CURRENT_TIMESTAMP(6)
			WHERE id IN (?,?)`).WithArgs(int64(1500), int64(4), int64(9)).
		WillReturnResult(newResult(0, 2))
	mock.ExpectQuery(`
			SELECT id,request_id,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,reason,attempts
			FROM flash_sale_release_job WHERE id IN (?,?) ORDER BY id`).WithArgs(int64(4), int64(9)).WillReturnRows(
		newRows("id", "request_id", "activity_id", "user_id", "digest", "reserved_at", "reason", "attempts").
			AddRow(4, requestOne, 7, 11, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", reservedAt, "technical_rollback", 1).
			AddRow(9, requestTwo, 8, 12, "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210", reservedAt, "payment_expired", 3),
	)
	mock.ExpectCommit()

	jobs, err := store.ClaimReleaseJobs(context.Background(), 2, 1500*time.Microsecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != 4 || jobs[1].ID != 9 ||
		jobs[0].Attempts != 1 || jobs[0].LeaseGeneration != 1 ||
		jobs[1].Attempts != 3 || jobs[1].LeaseGeneration != 3 {
		t.Fatalf("jobs = %#v", jobs)
	}
}

func TestClaimReleaseJobsRejectsLeaseWithoutExactMicrosecondPrecision(t *testing.T) {
	store, _ := newMockStore(t)
	for _, lease := range []time.Duration{time.Nanosecond, time.Microsecond + time.Nanosecond} {
		if _, err := store.ClaimReleaseJobs(context.Background(), 1, lease); !errors.Is(err, flashsale.ErrInvalidInput) {
			t.Fatalf("lease = %s, error = %v, want invalid input", lease, err)
		}
	}
}

func TestMarkOrderReadyAcceptsExactNoOpReplay(t *testing.T) {
	store, mock := newMockStore(t)
	requestID := "fsr_1_0123456789abcdef0123456789abcdef"
	const userID int64 = 11
	const orderID int64 = 41

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT user_id FROM flash_sale_reservation WHERE request_id=?`).
		WithArgs(requestID).WillReturnRows(newRows("user_id").AddRow(userID))
	mock.ExpectQuery(lockUserSQL).WithArgs(userID).
		WillReturnRows(newRows("id").AddRow(userID))
	mock.ExpectQuery(`SELECT activity_id FROM flash_sale_reservation WHERE request_id=? AND user_id=?`).
		WithArgs(requestID, userID).WillReturnRows(newRows("activity_id").AddRow(int64(7)))
	mock.ExpectQuery(lockActivitySQL).WithArgs(int64(7)).
		WillReturnRows(newRows("id").AddRow(int64(7)))
	mock.ExpectQuery(`
			SELECT id FROM purchase_order
			WHERE order_no=? AND source_type='flash_sale' AND source_reference=? AND user_id=?
			FOR UPDATE`).WithArgs("ORD-1", requestID, userID).
		WillReturnRows(newRows("id").AddRow(orderID))
	mock.ExpectQuery(lockReservationSQL).WithArgs(requestID).
		WillReturnRows(newRows("request_id").AddRow(requestID))
	mock.ExpectExec(`
			UPDATE flash_sale_reservation
			SET status='order_ready',order_id=?,updated_at=CURRENT_TIMESTAMP(6)
			WHERE request_id=? AND user_id=?
				AND (status='reserved' OR (status='order_ready' AND order_id=?))`).
		WithArgs(orderID, requestID, userID, orderID).WillReturnResult(newResult(0, 0))
	mock.ExpectQuery(`
			SELECT r.request_id
			FROM flash_sale_reservation AS r
			JOIN purchase_order AS o ON o.id=r.order_id
			WHERE r.request_id=? AND r.status='order_ready' AND o.order_no=?
				AND o.source_type='flash_sale' AND o.source_reference=r.request_id
				AND o.user_id=r.user_id AND r.order_id=o.id`).WithArgs(requestID, "ORD-1").
		WillReturnRows(newRows("request_id").AddRow(requestID))
	mock.ExpectCommit()

	if err := store.MarkOrderReady(context.Background(), requestID, "ORD-1"); err != nil {
		t.Fatal(err)
	}
}

func TestAllocateLocksUserBeforeActivity(t *testing.T) {
	store, mock := newMockStore(t)
	want := errors.New("activity lock failed")
	event := flashsale.Event{ActivityID: 7, UserID: 11}

	mock.ExpectBegin()
	mock.ExpectQuery(lockUserSQL).WithArgs(event.UserID).
		WillReturnRows(newRows("id").AddRow(event.UserID))
	mock.ExpectQuery(lockActivitySQL).WithArgs(event.ActivityID).WillReturnError(want)
	mock.ExpectRollback()

	_, err := store.Allocate(context.Background(), event)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestFailLocksResourcesAndVerifiesExactFailedReplay(t *testing.T) {
	store, mock := newMockStore(t)
	reservedAt := time.Date(2026, 9, 7, 2, 3, 4, 500000000, time.UTC)
	createdAt := reservedAt.Add(-2 * time.Hour)
	digest := strings.Repeat("ab", 32)
	digestBytes := make([]byte, 32)
	for i := range digestBytes {
		digestBytes[i] = 0xab
	}
	event := flashsale.Event{
		RequestID: "fsr_7_abababababababababababababababab", ActivityID: 7, ActivityVersion: 3,
		UserID: 11, ReservedAt: reservedAt, IdempotencyDigest: digest,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(lockUserSQL).WithArgs(event.UserID).
		WillReturnRows(newRows("id").AddRow(event.UserID))
	mock.ExpectQuery(lockActivitySQL).WithArgs(event.ActivityID).
		WillReturnRows(newRows("id").AddRow(event.ActivityID))
	mock.ExpectQuery(activitySelect + ` WHERE a.id=?`).WithArgs(event.ActivityID).WillReturnRows(
		activityRows().AddRow(event.ActivityID, "SALE-SEVEN", "game", "Game", 5, "Deluxe", "CN", "CNY",
			9900, 5, 1, "active", reservedAt.Add(-time.Hour), reservedAt.Add(time.Hour), 900, event.ActivityVersion,
			21, nil, nil, createdAt, createdAt),
	)
	mock.ExpectQuery(`
			SELECT status,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,failure_code
			FROM flash_sale_reservation WHERE request_id=? FOR UPDATE`).WithArgs(event.RequestID).WillReturnRows(
		newRows("status", "activity_id", "user_id", "digest", "reserved_at", "failure_code").
			AddRow("failed", event.ActivityID, event.UserID, digest, reservedAt, "already_owned"),
	)
	mock.ExpectExec(`
			INSERT INTO flash_sale_release_job(request_id,activity_id,user_id,idempotency_digest,reserved_at,reason)
			VALUES (?,?,?,?,?,?) AS incoming
			ON DUPLICATE KEY UPDATE request_id=incoming.request_id`).
		WithArgs(event.RequestID, event.ActivityID, event.UserID, digestBytes, reservedAt, "final_guard").
		WillReturnResult(newResult(0, 0))
	mock.ExpectQuery(`
			SELECT activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,reason
			FROM flash_sale_release_job WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
		newRows("activity_id", "user_id", "digest", "reserved_at", "reason").
			AddRow(event.ActivityID, event.UserID, digest, reservedAt, "final_guard"),
	)
	mock.ExpectCommit()

	if err := store.Fail(context.Background(), event, "already_owned", "final_guard"); err != nil {
		t.Fatal(err)
	}
}

func TestFailureCommitAmbiguityRequiresExactDurableOutcome(t *testing.T) {
	reservedAt := time.Date(2026, 9, 7, 2, 3, 4, 500000000, time.UTC)
	digest := strings.Repeat("ab", 32)
	event := flashsale.Event{
		RequestID: "fsr_7_abababababababababababababababab", ActivityID: 7, ActivityVersion: 3,
		UserID: 11, ReservedAt: reservedAt, IdempotencyDigest: digest,
	}
	commitErr := &mysqltx.CommitOutcomeUnknownError{Err: errors.New("connection lost during commit")}

	t.Run("exact failed reservation and release job commit reconciliation", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`
			SELECT status,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at
			FROM flash_sale_reservation WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
			newRows("status", "activity_id", "user_id", "digest", "reserved_at").
				AddRow("failed", event.ActivityID, event.UserID, digest, reservedAt),
		)
		mock.ExpectQuery(`
			SELECT activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,status,COALESCE(failure_code,'')
			FROM flash_sale_reservation WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
			newRows("activity_id", "user_id", "digest", "reserved_at", "status", "failure_code").
				AddRow(event.ActivityID, event.UserID, digest, reservedAt, "failed", "already_owned"),
		)
		mock.ExpectQuery(`
			SELECT activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,reason
			FROM flash_sale_release_job WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
			newRows("activity_id", "user_id", "digest", "reserved_at", "reason").
				AddRow(event.ActivityID, event.UserID, digest, reservedAt, "final_guard"),
		)
		mock.ExpectCommit()

		if err := store.reconcileFailure(context.Background(), event, "already_owned", "final_guard", commitErr); err != nil {
			t.Fatal(err)
		}
	})

	for _, status := range []entity.ReservationStatus{entity.ReservationOrderReady, entity.ReservationExpired} {
		t.Run(string(status)+" is rejected and rolls back", func(t *testing.T) {
			store, mock := newMockStore(t)
			mock.ExpectBegin()
			mock.ExpectQuery(`
				SELECT status,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at
				FROM flash_sale_reservation WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
				newRows("status", "activity_id", "user_id", "digest", "reserved_at").
					AddRow(string(status), event.ActivityID, event.UserID, digest, reservedAt),
			)
			mock.ExpectRollback()

			err := store.reconcileFailure(context.Background(), event, "already_owned", "final_guard", commitErr)
			if !errors.Is(err, flashsale.ErrUnsupportedEvent) {
				t.Fatalf("error = %v, want unsupported event", err)
			}
		})
	}

	t.Run("missing release job preserves ambiguity and rolls back", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`
			SELECT status,activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at
			FROM flash_sale_reservation WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
			newRows("status", "activity_id", "user_id", "digest", "reserved_at").
				AddRow("failed", event.ActivityID, event.UserID, digest, reservedAt),
		)
		mock.ExpectQuery(`
			SELECT activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,status,COALESCE(failure_code,'')
			FROM flash_sale_reservation WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
			newRows("activity_id", "user_id", "digest", "reserved_at", "status", "failure_code").
				AddRow(event.ActivityID, event.UserID, digest, reservedAt, "failed", "already_owned"),
		)
		mock.ExpectQuery(`
			SELECT activity_id,user_id,LOWER(HEX(idempotency_digest)),reserved_at,reason
			FROM flash_sale_release_job WHERE request_id=? FOR SHARE`).WithArgs(event.RequestID).WillReturnRows(
			newRows("activity_id", "user_id", "digest", "reserved_at", "reason"),
		)
		mock.ExpectRollback()

		err := store.reconcileFailure(context.Background(), event, "already_owned", "final_guard", commitErr)
		if !mysqltx.IsCommitOutcomeUnknown(err) || !errors.Is(err, flashsale.ErrUnsupportedEvent) {
			t.Fatalf("error = %v, want ambiguous commit joined with unsupported event", err)
		}
	})
}

func TestCommitAmbiguityReconciliationsUseReadCommittedTransactions(t *testing.T) {
	commitErr := &mysqltx.CommitOutcomeUnknownError{Err: errors.New("connection lost during commit")}
	reservedAt := time.Date(2026, 9, 7, 2, 3, 4, 500000000, time.UTC)
	requestID := "fsr_7_abababababababababababababababab"
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = 0xab
	}

	t.Run("order ready exact state commits", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`
			SELECT r.request_id
			FROM flash_sale_reservation AS r
			JOIN purchase_order AS o ON o.id=r.order_id
			WHERE r.request_id=? AND r.status='order_ready' AND o.order_no=?
				AND o.source_type='flash_sale' AND o.source_reference=r.request_id
				AND o.user_id=r.user_id FOR SHARE`).WithArgs(requestID, "ORD-1").WillReturnRows(
			newRows("request_id").AddRow(requestID),
		)
		mock.ExpectCommit()

		if err := store.reconcileOrderReady(context.Background(), requestID, "ORD-1", commitErr); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("order ready absent state rolls back", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`
			SELECT r.request_id
			FROM flash_sale_reservation AS r
			JOIN purchase_order AS o ON o.id=r.order_id
			WHERE r.request_id=? AND r.status='order_ready' AND o.order_no=?
				AND o.source_type='flash_sale' AND o.source_reference=r.request_id
				AND o.user_id=r.user_id FOR SHARE`).WithArgs(requestID, "ORD-1").WillReturnRows(
			newRows("request_id"),
		)
		mock.ExpectRollback()

		if err := store.reconcileOrderReady(context.Background(), requestID, "ORD-1", commitErr); err != commitErr {
			t.Fatalf("error = %v, want original ambiguous commit", err)
		}
	})

	t.Run("expiry exact state and release job commit", func(t *testing.T) {
		store, mock := newMockStore(t)
		item := dueReservation{requestID: requestID, activityID: 7, userID: 11, orderID: 41, digest: digest, reservedAt: reservedAt}
		mock.ExpectBegin()
		mock.ExpectQuery(`
			SELECT r.request_id FROM flash_sale_reservation AS r
			JOIN purchase_order AS o ON o.id=r.order_id
			WHERE r.request_id=? AND r.status='expired' AND r.activity_id=? AND r.user_id=?
				AND r.idempotency_digest=? AND r.reserved_at=? AND o.id=? AND o.status='expired'
				AND o.user_id=r.user_id AND o.source_type='flash_sale' AND o.source_reference=r.request_id
			FOR SHARE`).WithArgs(requestID, int64(7), int64(11), digest, reservedAt, int64(41)).WillReturnRows(
			newRows("request_id").AddRow(requestID),
		)
		mock.ExpectQuery(`
			SELECT activity_id,user_id,idempotency_digest,reserved_at,reason
			FROM flash_sale_release_job WHERE request_id=? FOR SHARE`).WithArgs(requestID).WillReturnRows(
			newRows("activity_id", "user_id", "digest", "reserved_at", "reason").
				AddRow(int64(7), int64(11), digest, reservedAt, "payment_expired"),
		)
		mock.ExpectCommit()

		if count, err := store.reconcileExpiry(context.Background(), []dueReservation{item}, commitErr); err != nil || count != 1 {
			t.Fatalf("reconcileExpiry() = %d, %v, want 1, nil", count, err)
		}
	})

	t.Run("release completion exact generation commits", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT status,attempts,lease_until,completed_at FROM flash_sale_release_job WHERE id=? FOR SHARE`).WithArgs(int64(41)).
			WillReturnRows(newRows("status", "attempts", "lease_until", "completed_at").AddRow("done", 7, nil, reservedAt))
		mock.ExpectCommit()

		if err := store.reconcileReleaseCompletion(context.Background(), 41, 7, commitErr); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("release completion stale generation rolls back", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT status,attempts,lease_until,completed_at FROM flash_sale_release_job WHERE id=? FOR SHARE`).WithArgs(int64(41)).
			WillReturnRows(newRows("status", "attempts", "lease_until", "completed_at").AddRow("done", 8, nil, reservedAt))
		mock.ExpectRollback()

		if err := store.reconcileReleaseCompletion(context.Background(), 41, 7, commitErr); err != commitErr {
			t.Fatalf("error = %v, want original ambiguous commit", err)
		}
	})

	t.Run("release retry exact generation commits", func(t *testing.T) {
		store, mock := newMockStore(t)
		next := reservedAt.Add(time.Minute)
		mock.ExpectBegin()
		mock.ExpectQuery(`
			SELECT status,attempts,next_attempt_at,COALESCE(last_error_code,''),lease_until
			FROM flash_sale_release_job WHERE id=? FOR SHARE`).WithArgs(int64(52)).WillReturnRows(
			newRows("status", "attempts", "next_attempt_at", "last_error_code", "lease_until").
				AddRow("pending", 9, next, "redis_unavailable", nil),
		)
		mock.ExpectCommit()

		if err := store.reconcileReleaseRetry(context.Background(), 52, 9, next, "redis_unavailable", commitErr); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCompleteReleaseJobUsesLeaseGenerationCAS(t *testing.T) {
	t.Run("matching generation", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`
			UPDATE flash_sale_release_job
			SET status='done',lease_until=NULL,completed_at=CURRENT_TIMESTAMP(6),updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='leased' AND attempts=?`).WithArgs(int64(41), 7).
			WillReturnResult(newResult(0, 1))
		mock.ExpectCommit()

		if err := store.CompleteReleaseJob(context.Background(), 41, 7); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stale generation", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`
			UPDATE flash_sale_release_job
			SET status='done',lease_until=NULL,completed_at=CURRENT_TIMESTAMP(6),updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='leased' AND attempts=?`).WithArgs(int64(41), 6).
			WillReturnResult(newResult(0, 0))
		mock.ExpectRollback()

		if err := store.CompleteReleaseJob(context.Background(), 41, 6); !errors.Is(err, flashsale.ErrNotFound) {
			t.Fatalf("error = %v, want not found", err)
		}
	})

	t.Run("invalid generation", func(t *testing.T) {
		store, _ := newMockStore(t)
		if err := store.CompleteReleaseJob(context.Background(), 41, 0); !errors.Is(err, flashsale.ErrInvalidInput) {
			t.Fatalf("error = %v, want invalid input", err)
		}
	})
}

func TestRetryReleaseJobUsesLeaseGenerationCAS(t *testing.T) {
	next := time.Date(2026, 9, 7, 12, 0, 0, 123456000, time.FixedZone("test", 8*60*60))

	t.Run("matching generation", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`
			UPDATE flash_sale_release_job
			SET status='pending',lease_until=NULL,next_attempt_at=?,last_error_code=?,updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='leased' AND attempts=?`).
			WithArgs(next.UTC(), "redis_unavailable", int64(52), 9).WillReturnResult(newResult(0, 1))
		mock.ExpectCommit()

		if err := store.RetryReleaseJob(context.Background(), 52, 9, next, "redis_unavailable"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stale generation", func(t *testing.T) {
		store, mock := newMockStore(t)
		mock.ExpectBegin()
		mock.ExpectExec(`
			UPDATE flash_sale_release_job
			SET status='pending',lease_until=NULL,next_attempt_at=?,last_error_code=?,updated_at=CURRENT_TIMESTAMP(6)
			WHERE id=? AND status='leased' AND attempts=?`).
			WithArgs(next.UTC(), "redis_unavailable", int64(52), 8).WillReturnResult(newResult(0, 0))
		mock.ExpectRollback()

		if err := store.RetryReleaseJob(context.Background(), 52, 8, next, "redis_unavailable"); !errors.Is(err, flashsale.ErrNotFound) {
			t.Fatalf("error = %v, want not found", err)
		}
	})

	t.Run("invalid generation", func(t *testing.T) {
		store, _ := newMockStore(t)
		if err := store.RetryReleaseJob(context.Background(), 52, 0, next, "redis_unavailable"); !errors.Is(err, flashsale.ErrInvalidInput) {
			t.Fatalf("error = %v, want invalid input", err)
		}
	})
}

func TestUpdateDraftLocksOldAndNewScopesInDeterministicOrder(t *testing.T) {
	store, mock := newMockStore(t)
	want := errors.New("stop after scope locks")
	activity := entity.Activity{ID: 41, EditionID: 7, Region: "CN", Currency: "CNY"}

	mock.ExpectBegin()
	mock.ExpectQuery(selectDraftMetadataSQL).WithArgs(activity.ID).WillReturnRows(
		newRows("edition_id", "region_code", "currency", "version").AddRow(int64(9), "US", "USD", int64(3)),
	)
	mock.ExpectExec(upsertScopeSQL).WithArgs(int64(7), "CN", "CNY").WillReturnResult(newResult(0, 0))
	mock.ExpectQuery(lockScopeSQL).WithArgs(int64(7), "CN", "CNY").
		WillReturnRows(newRows("edition_id").AddRow(int64(7)))
	mock.ExpectExec(upsertScopeSQL).WithArgs(int64(9), "US", "USD").WillReturnResult(newResult(0, 0))
	mock.ExpectQuery(lockScopeSQL).WithArgs(int64(9), "US", "USD").
		WillReturnRows(newRows("edition_id").AddRow(int64(9)))
	mock.ExpectQuery(lockDraftMetadataSQL).WithArgs(activity.ID).WillReturnError(want)
	mock.ExpectRollback()

	if _, err := store.UpdateDraft(context.Background(), activity); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func newMockStore(t *testing.T) (*Store, *testSQLMock) {
	t.Helper()
	mock := &testSQLMock{}
	db := sql.OpenDB(testConnector{mock: mock})
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if err := mock.expectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return NewStore(db), mock
}

func activityRows() *testRows {
	return newRows(
		"id", "code", "slug", "game_name", "edition_id", "edition_name", "region_code", "currency",
		"sale_price_minor", "total_stock", "allocated_stock", "status", "starts_at", "ends_at",
		"payment_timeout_seconds", "version", "created_by", "activated_at", "cancelled_at", "created_at", "updated_at",
	)
}

type expectedOperation struct {
	kind      string
	query     string
	args      []driver.Value
	result    driver.Result
	rows      driver.Rows
	err       error
	isolation driver.IsolationLevel
}

type testSQLMock struct {
	mu         sync.Mutex
	operations []*expectedOperation
	next       int
}

func (m *testSQLMock) ExpectBegin() {
	m.operations = append(m.operations, &expectedOperation{
		kind: "begin", isolation: driver.IsolationLevel(sql.LevelReadCommitted),
	})
}

func (m *testSQLMock) ExpectExec(query string) *expectedOperation {
	op := &expectedOperation{kind: "exec", query: normalizeSQL(query)}
	m.operations = append(m.operations, op)
	return op
}

func (m *testSQLMock) ExpectQuery(query string) *expectedOperation {
	op := &expectedOperation{kind: "query", query: normalizeSQL(query)}
	m.operations = append(m.operations, op)
	return op
}

func (m *testSQLMock) ExpectCommit() {
	m.operations = append(m.operations, &expectedOperation{kind: "commit"})
}

func (m *testSQLMock) ExpectRollback() {
	m.operations = append(m.operations, &expectedOperation{kind: "rollback"})
}

func (m *testSQLMock) take(kind, query string, args []driver.NamedValue, isolation driver.IsolationLevel) (*expectedOperation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.next >= len(m.operations) {
		return nil, fmt.Errorf("unexpected %s %q", kind, normalizeSQL(query))
	}
	op := m.operations[m.next]
	m.next++
	if op.kind != kind {
		return nil, fmt.Errorf("operation %d: got %s, want %s", m.next, kind, op.kind)
	}
	if op.query != normalizeSQL(query) {
		return nil, fmt.Errorf("operation %d query: got %q, want %q", m.next, normalizeSQL(query), op.query)
	}
	if kind == "begin" && op.isolation != isolation {
		return nil, fmt.Errorf("transaction isolation = %d, want %d", isolation, op.isolation)
	}
	var gotArgs []driver.Value
	if len(args) > 0 {
		gotArgs = make([]driver.Value, len(args))
	}
	for i := range args {
		gotArgs[i] = args[i].Value
	}
	if !reflect.DeepEqual(gotArgs, op.args) {
		return nil, fmt.Errorf("operation %d args = %#v, want %#v", m.next, gotArgs, op.args)
	}
	return op, nil
}

func (m *testSQLMock) expectationsWereMet() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.next != len(m.operations) {
		return fmt.Errorf("%d of %d database operations executed", m.next, len(m.operations))
	}
	return nil
}

func (op *expectedOperation) WithArgs(args ...any) *expectedOperation {
	op.args = make([]driver.Value, len(args))
	for i, arg := range args {
		value, err := driver.DefaultParameterConverter.ConvertValue(arg)
		if err != nil {
			panic(err)
		}
		op.args[i] = value
	}
	return op
}

func (op *expectedOperation) WillReturnResult(result driver.Result) *expectedOperation {
	op.result = result
	return op
}

func (op *expectedOperation) WillReturnRows(rows driver.Rows) *expectedOperation {
	op.rows = rows
	return op
}

func (op *expectedOperation) WillReturnError(err error) *expectedOperation {
	op.err = err
	return op
}

type testConnector struct{ mock *testSQLMock }

func (c testConnector) Connect(context.Context) (driver.Conn, error) {
	return &testConn{mock: c.mock}, nil
}
func (c testConnector) Driver() driver.Driver { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type testConn struct{ mock *testSQLMock }

func (*testConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not implemented")
}
func (*testConn) Close() error { return nil }
func (c *testConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *testConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	op, err := c.mock.take("begin", "", nil, options.Isolation)
	if err != nil {
		return nil, err
	}
	if op.err != nil {
		return nil, op.err
	}
	return &testTx{mock: c.mock}, nil
}
func (c *testConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	op, err := c.mock.take("exec", query, args, 0)
	if err != nil {
		return nil, err
	}
	if op.err != nil {
		return nil, op.err
	}
	return op.result, nil
}
func (c *testConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	op, err := c.mock.take("query", query, args, 0)
	if err != nil {
		return nil, err
	}
	if op.err != nil {
		return nil, op.err
	}
	return op.rows, nil
}

type testTx struct{ mock *testSQLMock }

func (tx *testTx) Commit() error {
	op, err := tx.mock.take("commit", "", nil, 0)
	if err != nil {
		return err
	}
	return op.err
}
func (tx *testTx) Rollback() error {
	op, err := tx.mock.take("rollback", "", nil, 0)
	if err != nil {
		return err
	}
	return op.err
}

type testResult struct{ lastInsertID, rowsAffected int64 }

func newResult(lastInsertID, rowsAffected int64) testResult {
	return testResult{lastInsertID: lastInsertID, rowsAffected: rowsAffected}
}
func (r testResult) LastInsertId() (int64, error) { return r.lastInsertID, nil }
func (r testResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }

type testRows struct {
	columns []string
	values  [][]driver.Value
	next    int
}

func newRows(columns ...string) *testRows { return &testRows{columns: columns} }
func (r *testRows) AddRow(values ...any) *testRows {
	row := make([]driver.Value, len(values))
	for i, value := range values {
		converted, err := driver.DefaultParameterConverter.ConvertValue(value)
		if err != nil {
			panic(err)
		}
		row[i] = converted
	}
	r.values = append(r.values, row)
	return r
}
func (r *testRows) Columns() []string { return r.columns }
func (*testRows) Close() error        { return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.next >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.next])
	r.next++
	return nil
}

func normalizeSQL(query string) string { return strings.Join(strings.Fields(query), " ") }
