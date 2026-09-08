package usecase

import (
	"context"
	"errors"
	"strconv"
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

func TestReserveReturnsDurableReplayAfterActivityEnded(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	activity.Status = entity.StatusEnded
	requestID := "fsr_" + strconv.FormatInt(activity.ID, 36) + "_" + idempotencyDigest(activity.ID, 7, "reserve-key.01")[:32]
	store := &fakeStore{activity: activity, request: Request{RequestID: requestID, ActivityID: activity.ID, Status: RequestOrderReady}}
	catalog := &countingCatalog{owned: true}
	admission := &fakeAdmission{}
	service := NewService(store, catalog, admission, &fakeOrders{})

	got, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, activity.ID, "reserve-key.01")
	if err != nil || got.RequestID != requestID || !got.Replayed {
		t.Fatalf("request=%+v err=%v", got, err)
	}
	if catalog.ownershipCalls != 0 || admission.calls != 0 {
		t.Fatalf("ownership calls=%d admission calls=%d, want 0, 0", catalog.ownershipCalls, admission.calls)
	}
	if store.activityCalls != 0 || store.hasReservationCalls != 0 {
		t.Fatalf("activity calls=%d reservation calls=%d, want 0, 0", store.activityCalls, store.hasReservationCalls)
	}
}

func TestReserveChecksActivityBeforeRedisReplayFallback(t *testing.T) {
	activityErr := errors.New("activity unavailable")
	cache := &fakeActivityCache{request: Request{RequestID: "fsr_15_0123456789abcdef0123456789abcdef", Status: RequestQueued}}
	service := NewService(&fakeStore{activityErr: activityErr}, fakeCatalog{}, &fakeAdmission{}, &fakeOrders{}).WithActivityCache(cache)

	_, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, 41, "reserve-key.01")
	if !errors.Is(err, activityErr) || cache.requestCalls != 0 || cache.hasReservationCalls != 0 {
		t.Fatalf("error=%v cache request calls=%d reservation calls=%d", err, cache.requestCalls, cache.hasReservationCalls)
	}
}

func TestReserveRetriesSameKeyAfterTechnicalRollback(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	cache := &fakeActivityCache{request: Request{RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: activity.ID, Status: RequestFailed, FailureCode: "technical_rollback"}}
	cache.request.RequestID = "fsr_" + strconv.FormatInt(activity.ID, 36) + "_" + idempotencyDigest(activity.ID, 7, "reserve-key.01")[:32]
	admission := &fakeAdmission{result: AdmissionResult{Outcome: AdmissionAccepted, RequestID: cache.request.RequestID, ReservedAt: now}}
	service := NewService(&fakeStore{activity: activity}, fakeCatalog{}, admission, &fakeOrders{}).WithActivityCache(cache)

	request, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, activity.ID, "reserve-key.01")
	if err != nil || request.Status != RequestQueued || request.Replayed || admission.calls != 1 {
		t.Fatalf("request=%+v admission calls=%d err=%v", request, admission.calls, err)
	}
}

func TestReserveReturnsDurableReplayBeforeOwnership(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	requestID := "fsr_" + strconv.FormatInt(activity.ID, 36) + "_" + idempotencyDigest(activity.ID, 7, "reserve-key.01")[:32]
	want := Request{RequestID: requestID, ActivityID: activity.ID, Status: RequestOrderReady, OrderNo: "ord_0123456789abcdef0123456789abcdef"}
	store := &fakeStore{activity: activity, request: want}
	catalog := &countingCatalog{owned: true}
	admission := &fakeAdmission{}
	service := NewService(store, catalog, admission, &fakeOrders{})

	got, err := service.Reserve(context.Background(), auth.Principal{UserID: 7, Role: auth.RoleAdmin}, activity.ID, "reserve-key.01")
	if err != nil || got.RequestID != want.RequestID || got.Status != want.Status || got.OrderNo != want.OrderNo || !got.Replayed {
		t.Fatalf("request=%+v err=%v", got, err)
	}
	if store.requestCalls != 1 || store.requestUserID != 7 || store.requestAdmin {
		t.Fatalf("lookup calls=%d user=%d admin=%v", store.requestCalls, store.requestUserID, store.requestAdmin)
	}
	if catalog.ownershipCalls != 0 || admission.calls != 0 {
		t.Fatalf("ownership calls=%d admission calls=%d, want 0, 0", catalog.ownershipCalls, admission.calls)
	}
}

func TestReserveReturnsCachedReplayBeforeOwnership(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	requestID := "fsr_" + strconv.FormatInt(activity.ID, 36) + "_" + idempotencyDigest(activity.ID, 7, "reserve-key.01")[:32]
	catalog := &countingCatalog{owned: true}
	cache := &fakeActivityCache{request: Request{RequestID: requestID, ActivityID: activity.ID, Status: RequestQueued}}
	admission := &fakeAdmission{}
	service := NewService(&fakeStore{activity: activity}, catalog, admission, &fakeOrders{}).WithActivityCache(cache)

	got, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, activity.ID, "reserve-key.01")
	if err != nil || got.RequestID != requestID || got.Status != RequestQueued || !got.Replayed {
		t.Fatalf("request=%+v err=%v", got, err)
	}
	if cache.requestCalls != 1 || cache.requestUserID != 7 || cache.requestAdmin || catalog.ownershipCalls != 0 || admission.calls != 0 {
		t.Fatalf("cache calls=%d user=%d admin=%v ownership=%d admission=%d",
			cache.requestCalls, cache.requestUserID, cache.requestAdmin, catalog.ownershipCalls, admission.calls)
	}
}

func TestReserveReturnsAlreadyReservedBeforeOwnershipForDifferentKey(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	store := &fakeStore{activity: activity, hasReservation: true}
	catalog := &countingCatalog{owned: true}
	admission := &fakeAdmission{result: AdmissionResult{Outcome: AdmissionAccepted}}
	service := NewService(store, catalog, admission, &fakeOrders{})

	_, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, activity.ID, "different-key.02")
	if !errors.Is(err, ErrAlreadyReserved) {
		t.Fatalf("error=%v, want already reserved", err)
	}
	if store.hasReservationCalls != 1 || store.reservationActivityID != activity.ID || store.reservationUserID != 7 {
		t.Fatalf("reservation calls=%d activity=%d user=%d", store.hasReservationCalls, store.reservationActivityID, store.reservationUserID)
	}
	if catalog.ownershipCalls != 0 || admission.calls != 0 {
		t.Fatalf("ownership calls=%d admission calls=%d, want 0, 0", catalog.ownershipCalls, admission.calls)
	}
}

func TestReserveReturnsRedisOnlyAlreadyReservedBeforeOwnershipForDifferentKey(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	cache := &fakeActivityCache{hasReservation: true}
	catalog := &countingCatalog{owned: true}
	admission := &fakeAdmission{result: AdmissionResult{Outcome: AdmissionAccepted}}
	service := NewService(&fakeStore{activity: activity}, catalog, admission, &fakeOrders{}).WithActivityCache(cache)

	_, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, activity.ID, "different-key.02")
	if !errors.Is(err, ErrAlreadyReserved) {
		t.Fatalf("error=%v, want already reserved", err)
	}
	if cache.hasReservationCalls != 1 || cache.reservationActivityID != activity.ID || cache.reservationUserID != 7 {
		t.Fatalf("cache reservation lookup calls=%d activity=%d user=%d", cache.hasReservationCalls, cache.reservationActivityID, cache.reservationUserID)
	}
	if catalog.ownershipCalls != 0 || admission.calls != 0 {
		t.Fatalf("ownership calls=%d admission calls=%d, want 0, 0", catalog.ownershipCalls, admission.calls)
	}
}

func TestReserveReturnsAlreadyOwnedBeforeAdmission(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	catalog := &countingCatalog{owned: true}
	admission := &fakeAdmission{result: AdmissionResult{Outcome: AdmissionAccepted}}
	service := NewService(&fakeStore{activity: activity}, catalog, admission, &fakeOrders{})

	_, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, activity.ID, "reserve-key.01")
	if !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("error=%v, want already owned", err)
	}
	if catalog.ownershipCalls != 1 || catalog.ownershipUserID != 7 || catalog.ownershipEditionID != activity.EditionID || admission.calls != 0 {
		t.Fatalf("ownership calls=%d user=%d edition=%d admission=%d",
			catalog.ownershipCalls, catalog.ownershipUserID, catalog.ownershipEditionID, admission.calls)
	}
}

func TestReserveFailsClosedBeforeAdmissionOnLookupError(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name            string
		storeErr        error
		reservationErr  error
		ownershipErr    error
		wantReservation int
		wantOwnership   int
	}{
		{name: "durable lookup", storeErr: errors.New("request lookup unavailable")},
		{name: "reservation lookup", reservationErr: errors.New("reservation lookup unavailable"), wantReservation: 1},
		{name: "ownership lookup", ownershipErr: errors.New("ownership unavailable"), wantReservation: 1, wantOwnership: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := &countingCatalog{ownershipErr: tc.ownershipErr}
			admission := &fakeAdmission{result: AdmissionResult{Outcome: AdmissionAccepted}}
			store := &fakeStore{activity: activeActivity(now), requestErr: tc.storeErr, hasReservationErr: tc.reservationErr}
			service := NewService(store, catalog, admission, &fakeOrders{})
			want := tc.storeErr
			if want == nil {
				want = tc.reservationErr
			}
			if want == nil {
				want = tc.ownershipErr
			}

			if _, err := service.Reserve(context.Background(), auth.Principal{UserID: 7}, 41, "reserve-key.01"); !errors.Is(err, want) {
				t.Fatalf("error=%v, want %v", err, want)
			}
			if admission.calls != 0 || store.hasReservationCalls != tc.wantReservation || catalog.ownershipCalls != tc.wantOwnership {
				t.Fatalf("admission calls=%d reservation calls=%d ownership calls=%d", admission.calls, store.hasReservationCalls, catalog.ownershipCalls)
			}
		})
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

func TestAdminActivityReadsIncludeDraft(t *testing.T) {
	now := time.Now().UTC()
	activity := activeActivity(now)
	activity.Status = entity.StatusDraft
	store := &fakeStore{activity: activity}
	service := NewService(store, fakeCatalog{}, &fakeAdmission{}, &fakeOrders{})

	if _, _, err := service.ListActivities(context.Background(), "", 20); err != nil {
		t.Fatal(err)
	}
	if store.listFilter.IncludeDraft {
		t.Fatal("public list requested draft activities")
	}

	principal := auth.Principal{UserID: 1, Role: auth.RoleAdmin}
	items, _, err := service.ListAdminActivities(context.Background(), principal, "", 20)
	if err != nil || len(items) != 1 || items[0].Status != entity.StatusDraft {
		t.Fatalf("admin items=%+v err=%v", items, err)
	}
	if !store.listFilter.IncludeDraft {
		t.Fatal("admin list did not request draft activities")
	}
	got, err := service.GetAdminActivity(context.Background(), principal, activity.ID)
	if err != nil || got.Status != entity.StatusDraft {
		t.Fatalf("admin activity=%+v err=%v", got, err)
	}

	listCalls := store.listCalls
	if _, _, err := service.ListAdminActivities(context.Background(), auth.Principal{UserID: 7, Role: auth.RoleUser}, "", 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin list error=%v", err)
	}
	if store.listCalls != listCalls {
		t.Fatal("unauthorized admin list reached store")
	}
	if _, _, err := service.ListAdminActivities(context.Background(), auth.Principal{}, "", 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous admin list error=%v", err)
	}
	if _, err := service.GetAdminActivity(context.Background(), auth.Principal{UserID: 7, Role: auth.RoleUser}, activity.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin detail error=%v", err)
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
func (fakeCatalog) OwnsEdition(context.Context, int64, int64) (bool, error) { return false, nil }

type countingCatalog struct {
	calls              int
	offer              catalogentity.PurchaseOffer
	ownershipCalls     int
	ownershipUserID    int64
	ownershipEditionID int64
	owned              bool
	ownershipErr       error
}

func (c *countingCatalog) PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error) {
	c.calls++
	if c.offer.EditionID == 0 {
		return catalogentity.PurchaseOffer{}, errors.New("catalog price changed after activation")
	}
	return c.offer, nil
}
func (c *countingCatalog) OwnsEdition(_ context.Context, userID, editionID int64) (bool, error) {
	c.ownershipCalls++
	c.ownershipUserID = userID
	c.ownershipEditionID = editionID
	return c.owned, c.ownershipErr
}

type fakeActivityCache struct {
	stageCalls, enableCalls, closeCalls int
	cutoff                              time.Time
	request                             Request
	requestErr                          error
	requestCalls                        int
	requestUserID                       int64
	requestAdmin                        bool
	hasReservation                      bool
	hasReservationErr                   error
	hasReservationCalls                 int
	reservationActivityID               int64
	reservationUserID                   int64
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
func (c *fakeActivityCache) GetRequest(_ context.Context, _ string, userID int64, admin bool) (Request, error) {
	c.requestCalls++
	c.requestUserID = userID
	c.requestAdmin = admin
	if c.requestErr != nil {
		return Request{}, c.requestErr
	}
	if c.request.RequestID == "" {
		return Request{}, ErrNotFound
	}
	return c.request, nil
}
func (c *fakeActivityCache) HasActivityReservation(_ context.Context, activityID, userID int64) (bool, error) {
	c.hasReservationCalls++
	c.reservationActivityID = activityID
	c.reservationUserID = userID
	return c.hasReservation, c.hasReservationErr
}

type fakeStore struct {
	activity              entity.Activity
	activityErr           error
	activityCalls         int
	listFilter            ListFilter
	listCalls             int
	allocation            Allocation
	markedOrderNo         string
	markCalls             int
	failedCode            string
	releaseJobs           []ReleaseJob
	completedReleaseJobs  []int64
	completedGenerations  []int
	retriedReleaseJobID   int64
	retriedGeneration     int
	retriedAt             time.Time
	retryCode             string
	expiredCount          int
	expiryBatch           int
	request               Request
	requestErr            error
	requestCalls          int
	requestUserID         int64
	requestAdmin          bool
	hasReservation        bool
	hasReservationErr     error
	hasReservationCalls   int
	reservationActivityID int64
	reservationUserID     int64
	activateCalls         int
}

func (s *fakeStore) ListActivities(_ context.Context, filter ListFilter) ([]entity.Activity, error) {
	s.listCalls++
	s.listFilter = filter
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
	s.activityCalls++
	return s.activity, s.activityErr
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
func (s *fakeStore) GetRequest(_ context.Context, _ string, userID int64, admin bool) (Request, error) {
	s.requestCalls++
	s.requestUserID = userID
	s.requestAdmin = admin
	if s.requestErr != nil {
		return Request{}, s.requestErr
	}
	if s.request.RequestID == "" {
		return Request{}, ErrNotFound
	}
	return s.request, nil
}
func (s *fakeStore) HasActivityReservation(_ context.Context, activityID, userID int64) (bool, error) {
	s.hasReservationCalls++
	s.reservationActivityID = activityID
	s.reservationUserID = userID
	return s.hasReservation, s.hasReservationErr
}
func (s *fakeStore) ExpireDue(_ context.Context, batch int) (int, error) {
	s.expiryBatch = batch
	return s.expiredCount, nil
}
func (s *fakeStore) ClaimReleaseJobs(context.Context, int, time.Duration) ([]ReleaseJob, error) {
	return s.releaseJobs, nil
}
func (s *fakeStore) CompleteReleaseJob(_ context.Context, id int64, leaseGeneration int) error {
	s.completedReleaseJobs = append(s.completedReleaseJobs, id)
	s.completedGenerations = append(s.completedGenerations, leaseGeneration)
	return nil
}
func (s *fakeStore) RetryReleaseJob(_ context.Context, id int64, leaseGeneration int, next time.Time, code string) error {
	s.retriedReleaseJobID = id
	s.retriedGeneration = leaseGeneration
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
