package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	orderentity "github.com/red060324/XiaoLanHe/internal/order/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestReserveAuthenticatesAndReturnsStableReplay(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	store := &fakeStore{activity: activeActivity(now)}
	admission := &fakeAdmission{result: AdmissionResult{Outcome: AdmissionAccepted, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ReservedAt: now}}
	service := NewService(store, fakeCatalog{}, admission, &fakeOrders{})

	if _, err := service.Reserve(context.Background(), auth.Principal{}, 41, "reserve-key.01"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("anonymous error=%v", err)
	}
	result, err := service.Reserve(context.Background(), auth.Principal{UserID: 7, Role: auth.RoleUser}, 41, "reserve-key.01")
	if err != nil || result.RequestID != admission.result.RequestID || result.Status != RequestQueued {
		t.Fatalf("reserve=%+v err=%v", result, err)
	}
	if admission.calls != 1 || admission.last.UserID != 7 || admission.last.ActivityID != 41 || admission.last.IdempotencyDigest == "reserve-key.01" {
		t.Fatalf("admission calls=%d command=%+v", admission.calls, admission.last)
	}

	admission.result = AdmissionResult{Outcome: AdmissionReplay, RequestID: result.RequestID, ReservedAt: now}
	replay, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, 41, "reserve-key.01")
	if err != nil || !replay.Replayed || replay.RequestID != result.RequestID || admission.calls != 2 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, admission.calls, err)
	}
}

func TestReserveMapsAtomicAdmissionOutcomes(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		outcome AdmissionOutcome
		want    error
	}{
		{AdmissionNotStarted, ErrNotStarted},
		{AdmissionEnded, ErrEnded},
		{AdmissionExhausted, ErrStockExhausted},
		{AdmissionAlreadyReserved, ErrAlreadyReserved},
		{AdmissionUnavailable, ErrUnavailable},
	}
	for _, tc := range cases {
		admission := &fakeAdmission{result: AdmissionResult{Outcome: tc.outcome}}
		service := NewService(&fakeStore{activity: activeActivity(now)}, fakeCatalog{}, admission, &fakeOrders{})
		_, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, 41, "reserve-key.01")
		if !errors.Is(err, tc.want) {
			t.Fatalf("outcome=%s error=%v want=%v", tc.outcome, err, tc.want)
		}
	}
}

func TestReserveAllowsExactReplayAfterCancellation(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	activity.Status = entity.StatusCancelled
	activity.CancelledAt = now
	admission := &fakeAdmission{result: AdmissionResult{
		Outcome: AdmissionReplay, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ReservedAt: now.Add(-time.Second),
	}}
	service := NewService(&fakeStore{activity: activity}, fakeCatalog{}, admission, &fakeOrders{})

	request, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, activity.ID, "reserve-key.01")
	if err != nil || !request.Replayed || request.RequestID != admission.result.RequestID || admission.calls != 1 {
		t.Fatalf("request=%+v admission_calls=%d err=%v", request, admission.calls, err)
	}
}

func TestActivityMutationsRequireAdminAndActivationWarmsCache(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	activity.Status = entity.StatusDraft
	activity.Version = 0
	store := &fakeStore{activity: activity}
	catalog := &countingCatalog{offer: catalogentity.PurchaseOffer{EditionID: 12, AmountMinor: 1999, Currency: "USD", Region: "GLOBAL"}}
	cache := &fakeActivityCache{}
	service := NewService(store, catalog, &fakeAdmission{}, &fakeOrders{}).WithActivityCache(cache)

	if _, err := service.Activate(context.Background(), auth.Principal{UserID: 7, Role: auth.RoleUser}, activity.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin activation error=%v", err)
	}
	if cache.stageCalls != 0 || cache.enableCalls != 0 {
		t.Fatalf("unauthorized cache calls stage=%d enable=%d", cache.stageCalls, cache.enableCalls)
	}
	activated, err := service.Activate(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}, activity.ID)
	if err != nil || activated.Status != entity.StatusActive || activated.Version != 1 {
		t.Fatalf("activated=%+v err=%v", activated, err)
	}
	if cache.stageCalls != 1 || cache.enableCalls != 1 {
		t.Fatalf("cache calls stage=%d enable=%d", cache.stageCalls, cache.enableCalls)
	}
}

func TestCreateActivityRejectsMismatchedCatalogOffer(t *testing.T) {
	now := time.Now().UTC()
	draft := activeActivity(now)
	draft.Status = entity.StatusDraft
	principal := auth.Principal{UserID: 1, Role: auth.RoleAdmin}
	for name, offer := range map[string]catalogentity.PurchaseOffer{
		"edition":  {EditionID: 13, AmountMinor: 1999, Currency: "USD", Region: "GLOBAL"},
		"currency": {EditionID: 12, AmountMinor: 1999, Currency: "CNY", Region: "GLOBAL"},
		"region":   {EditionID: 12, AmountMinor: 1999, Currency: "USD", Region: "CN"},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}
			service := NewService(store, &countingCatalog{offer: offer}, &fakeAdmission{}, &fakeOrders{})
			if _, err := service.CreateActivity(context.Background(), principal, draft); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v", err)
			}
			if store.activity.ID != 0 {
				t.Fatalf("mismatched offer reached store: %+v", store.activity)
			}
		})
	}
}

func TestPublicActivityDetailHidesDraft(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	activity.Status = entity.StatusDraft
	service := NewService(&fakeStore{activity: activity}, fakeCatalog{}, &fakeAdmission{}, &fakeOrders{})

	if _, err := service.GetActivity(context.Background(), activity.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft detail error=%v", err)
	}
}

func TestActivationCanRepairRedisAfterDurableCommit(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	store := &fakeStore{activity: activity}
	cache := &fakeActivityCache{}
	catalog := &countingCatalog{}
	service := NewService(store, catalog, &fakeAdmission{}, &fakeOrders{}).WithActivityCache(cache)

	repaired, err := service.Activate(context.Background(), auth.Principal{UserID: 1, Role: auth.RoleAdmin}, activity.ID)
	if err != nil || repaired != activity || cache.stageCalls != 1 || cache.enableCalls != 1 || store.activateCalls != 0 || catalog.calls != 0 {
		t.Fatalf("repaired=%+v stage=%d enable=%d durable=%d catalog=%d err=%v", repaired, cache.stageCalls, cache.enableCalls, store.activateCalls, catalog.calls, err)
	}
}

func TestFulfilIsIdempotentAcrossOrderReplay(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	requestID := "fsr_15_0123456789abcdef0123456789abcdef"
	store := &fakeStore{activity: activeActivity(now), allocation: Allocation{
		RequestID: requestID, ActivityID: 41, UserID: 7, Status: entity.ReservationReserved,
		PaymentExpiresAt: now.Add(15 * time.Minute), Activity: activeActivity(now),
	}}
	orders := &fakeOrders{result: OrderResult{Order: orderentity.Order{OrderNo: "ord_0123456789abcdef0123456789abcdef"}}}
	catalog := &countingCatalog{}
	service := NewService(store, catalog, &fakeAdmission{}, orders)
	event := Event{Version: 1, RequestID: requestID, ActivityID: 41, ActivityVersion: 1, UserID: 7, ReservedAt: now, IdempotencyDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}

	if err := service.Fulfil(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if store.markedOrderNo != orders.result.Order.OrderNo || orders.calls != 1 {
		t.Fatalf("marked=%q order calls=%d", store.markedOrderNo, orders.calls)
	}
	if catalog.calls != 0 {
		t.Fatalf("fulfil revalidated mutable catalog price %d times", catalog.calls)
	}
	orders.result.Replayed = true
	if err := service.Fulfil(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if orders.calls != 2 || store.markCalls != 2 {
		t.Fatalf("replay order calls=%d marks=%d", orders.calls, store.markCalls)
	}
}

func TestFulfilDoesNotReopenTerminalReservation(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	for _, status := range []entity.ReservationStatus{entity.ReservationFailed, entity.ReservationExpired, entity.ReservationOrderReady} {
		t.Run(string(status), func(t *testing.T) {
			store := &fakeStore{allocation: Allocation{Status: status, Activity: activeActivity(now)}}
			orders := &fakeOrders{}
			service := NewService(store, fakeCatalog{}, &fakeAdmission{}, orders)
			event := Event{Version: 1, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, ActivityVersion: 1, UserID: 7, ReservedAt: now, IdempotencyDigest: testDigest}
			if err := service.Fulfil(context.Background(), event); err != nil || orders.calls != 0 || store.markCalls != 0 {
				t.Fatalf("status=%s order calls=%d marks=%d err=%v", status, orders.calls, store.markCalls, err)
			}
		})
	}
}

func TestFulfilMarksAlreadyOwnedAsTerminalFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStore{allocation: Allocation{
		RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, UserID: 7,
		Status: entity.ReservationReserved, PaymentExpiresAt: now.Add(time.Minute), Activity: activeActivity(now),
	}}
	orders := &fakeOrders{err: ErrAlreadyOwned}
	service := NewService(store, &countingCatalog{}, &fakeAdmission{}, orders)
	event := Event{Version: 1, RequestID: store.allocation.RequestID, ActivityID: 41, ActivityVersion: 1, UserID: 7, ReservedAt: now, IdempotencyDigest: testDigest}

	if err := service.Fulfil(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "already_owned" || orders.calls != 1 || store.markCalls != 0 {
		t.Fatalf("failure=%q order calls=%d mark calls=%d", store.failedCode, orders.calls, store.markCalls)
	}
}

func TestFulfilMarksPermanentOrderFailureAsTerminal(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStore{allocation: Allocation{
		RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, UserID: 7,
		Status: entity.ReservationReserved, PaymentExpiresAt: now.Add(time.Minute), Activity: activeActivity(now),
	}}
	orders := &fakeOrders{err: ErrOrderUnavailable}
	service := NewService(store, &countingCatalog{}, &fakeAdmission{}, orders)
	event := Event{Version: 1, RequestID: store.allocation.RequestID, ActivityID: 41, ActivityVersion: 1, UserID: 7, ReservedAt: now, IdempotencyDigest: testDigest}

	if err := service.Fulfil(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "order_unavailable" || orders.calls != 1 || store.markCalls != 0 {
		t.Fatalf("failure=%q order calls=%d mark calls=%d", store.failedCode, orders.calls, store.markCalls)
	}
}

func TestEventRejectsRequestDigestMismatch(t *testing.T) {
	event := Event{
		Version: 1, RequestID: "fsr_15_ffffffffffffffffffffffffffffffff", ActivityID: 41, ActivityVersion: 1, UserID: 7,
		ReservedAt: time.Now().UTC(), IdempotencyDigest: testDigest,
	}
	if !errors.Is(event.Validate(), ErrUnsupportedEvent) {
		t.Fatalf("mismatched event was accepted: %+v", event)
	}
}

func activeActivity(now time.Time) entity.Activity {
	return entity.Activity{
		ID: 41, Code: "AUTUMN-DELUXE", EditionID: 12, Region: "GLOBAL", Currency: "USD",
		SalePriceMinor: 999, TotalStock: 10, Status: entity.StatusActive, Version: 1,
		StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), PaymentTimeout: 15 * time.Minute,
	}
}

type fakeCatalog struct{}

func (fakeCatalog) PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error) {
	return catalogentity.PurchaseOffer{GameID: 3, GameSlug: "demo", GameName: "Demo", EditionID: 12, EditionCode: "standard", EditionName: "Standard", AmountMinor: 1999, Currency: "USD", Region: "GLOBAL"}, nil
}

type countingCatalog struct {
	calls int
	offer catalogentity.PurchaseOffer
}

func (c *countingCatalog) PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error) {
	c.calls++
	if c.offer.EditionID == 0 {
		return catalogentity.PurchaseOffer{}, errors.New("catalog price changed after activation")
	}
	return c.offer, nil
}

type fakeActivityCache struct {
	stageCalls, enableCalls, closeCalls int
	cutoff                              time.Time
}

func (c *fakeActivityCache) Stage(context.Context, entity.Activity) error { c.stageCalls++; return nil }
func (c *fakeActivityCache) Enable(context.Context, entity.Activity) error {
	c.enableCalls++
	return nil
}
func (c *fakeActivityCache) Close(context.Context, entity.Activity) (time.Time, error) {
	c.closeCalls++
	return c.cutoff, nil
}
func (*fakeActivityCache) GetRequest(context.Context, string, int64, bool) (Request, error) {
	return Request{}, ErrNotFound
}

type fakeStore struct {
	activity             entity.Activity
	allocation           Allocation
	markedOrderNo        string
	markCalls            int
	failedCode           string
	releaseJobs          []ReleaseJob
	completedReleaseJobs []int64
	retriedReleaseJobID  int64
	retriedAt            time.Time
	retryCode            string
	expiredCount         int
	expiryBatch          int
	request              Request
	requestErr           error
	activateCalls        int
}

func (s *fakeStore) ListActivities(context.Context, ListFilter) ([]entity.Activity, error) {
	return []entity.Activity{s.activity}, nil
}
func (s *fakeStore) CreateActivity(_ context.Context, activity entity.Activity) (entity.Activity, error) {
	s.activity = activity
	return activity, nil
}
func (s *fakeStore) UpdateDraft(_ context.Context, activity entity.Activity) (entity.Activity, error) {
	s.activity = activity
	return activity, nil
}
func (s *fakeStore) ActivateActivity(_ context.Context, _ int64, version int64, now time.Time) (entity.Activity, error) {
	s.activateCalls++
	s.activity.Status = entity.StatusActive
	s.activity.Version = version
	s.activity.ActivatedAt = now
	return s.activity, nil
}
func (s *fakeStore) CancelActivity(_ context.Context, _ int64, now time.Time) (entity.Activity, error) {
	s.activity.Status = entity.StatusCancelled
	s.activity.CancelledAt = now
	return s.activity, nil
}

func (s *fakeStore) GetActivity(context.Context, int64) (entity.Activity, error) {
	return s.activity, nil
}
func (s *fakeStore) Allocate(context.Context, Event) (Allocation, error) { return s.allocation, nil }
func (s *fakeStore) Fail(_ context.Context, _ Event, code, _ string) error {
	s.failedCode = code
	return nil
}
func (s *fakeStore) MarkOrderReady(_ context.Context, _ string, orderNo string) error {
	s.markedOrderNo = orderNo
	s.markCalls++
	return nil
}
func (s *fakeStore) GetRequest(context.Context, string, int64, bool) (Request, error) {
	if s.requestErr != nil {
		return Request{}, s.requestErr
	}
	if s.request.RequestID == "" {
		return Request{}, ErrNotFound
	}
	return s.request, nil
}
func (s *fakeStore) ExpireDue(_ context.Context, batch int) (int, error) {
	s.expiryBatch = batch
	return s.expiredCount, nil
}
func (s *fakeStore) ClaimReleaseJobs(context.Context, int, time.Duration) ([]ReleaseJob, error) {
	return s.releaseJobs, nil
}
func (s *fakeStore) CompleteReleaseJob(_ context.Context, id int64) error {
	s.completedReleaseJobs = append(s.completedReleaseJobs, id)
	return nil
}
func (s *fakeStore) RetryReleaseJob(_ context.Context, id int64, next time.Time, code string) error {
	s.retriedReleaseJobID = id
	s.retriedAt = next
	s.retryCode = code
	return nil
}

type fakeAdmission struct {
	result AdmissionResult
	last   AdmissionCommand
	calls  int
}

func (a *fakeAdmission) Reserve(_ context.Context, command AdmissionCommand) (AdmissionResult, error) {
	a.calls++
	a.last = command
	return a.result, nil
}

type fakeOrders struct {
	result OrderResult
	calls  int
	err    error
}

func (o *fakeOrders) CreateFromFlashSale(context.Context, OrderCommand) (OrderResult, error) {
	o.calls++
	return o.result, o.err
}
