package entry

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	orderentity "github.com/red060324/XiaoLanHe/internal/order/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpauth"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

const httpRequestID = "fsr_15_0123456789abcdef0123456789abcdef"

func TestPublicFlashSaleHTTP(t *testing.T) {
	now := time.Now().UTC()
	store := &httpStore{activity: httpActivity(now), request: flashsale.Request{RequestID: httpRequestID, ActivityID: 41, Status: flashsale.RequestQueued}}
	service := flashsale.NewService(store, httpCatalog{}, &httpAdmission{result: flashsale.AdmissionResult{Outcome: flashsale.AdmissionAccepted, RequestID: httpRequestID, ReservedAt: now}}, httpOrders{})
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(service, httpAuthenticator{}, "https://play.example").Register(router)

	list := ut.PerformRequest(router.Engine, "GET", "/api/flash-sales", nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"gameSlug":"demo"`) || strings.Contains(list.Body.String(), "totalStock") {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	reserve := ut.PerformRequest(router.Engine, "POST", "/api/flash-sales/41/reservations", nil,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"},
		ut.Header{Key: "Origin", Value: "https://play.example"},
		ut.Header{Key: "Idempotency-Key", Value: "reserve-key.01"})
	if reserve.Code != 202 || !strings.Contains(reserve.Body.String(), httpRequestID) || !strings.Contains(reserve.Body.String(), `"status":"queued"`) {
		t.Fatalf("reserve status=%d body=%s", reserve.Code, reserve.Body.String())
	}
	poll := ut.PerformRequest(router.Engine, "GET", "/api/flash-sale-requests/"+httpRequestID, nil, ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"})
	if poll.Code != 200 || !strings.Contains(poll.Body.String(), httpRequestID) {
		t.Fatalf("poll status=%d body=%s", poll.Code, poll.Body.String())
	}
}

func TestFlashSaleHTTPAuthOriginAndErrors(t *testing.T) {
	now := time.Now().UTC()
	admission := &httpAdmission{result: flashsale.AdmissionResult{Outcome: flashsale.AdmissionExhausted}}
	service := flashsale.NewService(&httpStore{activity: httpActivity(now)}, httpCatalog{}, admission, httpOrders{})
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(service, httpAuthenticator{}, "https://play.example").Register(router)

	anonymous := ut.PerformRequest(router.Engine, "POST", "/api/flash-sales/41/reservations", nil, ut.Header{Key: "Idempotency-Key", Value: "reserve-key.01"})
	if anonymous.Code != 401 {
		t.Fatalf("anonymous status=%d body=%s", anonymous.Code, anonymous.Body.String())
	}
	crossSite := ut.PerformRequest(router.Engine, "POST", "/api/flash-sales/41/reservations", nil,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"}, ut.Header{Key: "Origin", Value: "https://evil.example"},
		ut.Header{Key: "Idempotency-Key", Value: "reserve-key.01"})
	if crossSite.Code != 403 {
		t.Fatalf("cross site status=%d body=%s", crossSite.Code, crossSite.Body.String())
	}
	exhausted := ut.PerformRequest(router.Engine, "POST", "/api/flash-sales/41/reservations", nil,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=user"}, ut.Header{Key: "Origin", Value: "https://play.example"},
		ut.Header{Key: "Idempotency-Key", Value: "reserve-key.01"})
	if exhausted.Code != 409 || !strings.Contains(exhausted.Body.String(), `"code":"stock_exhausted"`) {
		t.Fatalf("exhausted status=%d body=%s", exhausted.Code, exhausted.Body.String())
	}
}

func TestAdminFlashSaleHTTP(t *testing.T) {
	now := time.Now().UTC()
	store := &httpStore{activity: httpActivity(now)}
	service := flashsale.NewService(store, httpCatalog{}, &httpAdmission{}, httpOrders{}).WithActivityCache(httpActivityCache{})
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	NewHTTP(service, httpAuthenticator{}, "https://play.example").Register(router)
	body := fmt.Sprintf("{\"code\":\"AUTUMN-DELUXE\",\"editionId\":\"12\",\"region\":\"GLOBAL\",\"currency\":\"USD\",\"salePriceMinor\":999,\"totalStock\":10,\"startsAt\":%q,\"endsAt\":%q,\"paymentTimeoutSeconds\":900}", now.Add(time.Minute).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	created := ut.PerformRequest(router.Engine, "POST", "/api/admin/flash-sales", &ut.Body{Body: strings.NewReader(body), Len: -1},
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"}, ut.Header{Key: "Origin", Value: "https://play.example"})
	if created.Code != 201 || !store.created || !strings.Contains(created.Body.String(), `"totalStock":10`) {
		t.Fatalf("created status=%d body=%s saved=%v", created.Code, created.Body.String(), store.created)
	}
	updated := ut.PerformRequest(router.Engine, "PUT", "/api/admin/flash-sales/41", &ut.Body{Body: strings.NewReader(body), Len: -1},
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"}, ut.Header{Key: "Origin", Value: "https://play.example"})
	if updated.Code != 200 || !store.updated {
		t.Fatalf("updated status=%d body=%s saved=%v", updated.Code, updated.Body.String(), store.updated)
	}
	activated := ut.PerformRequest(router.Engine, "POST", "/api/admin/flash-sales/41/activate", nil,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"}, ut.Header{Key: "Origin", Value: "https://play.example"})
	if activated.Code != 200 || !store.activated {
		t.Fatalf("activated status=%d body=%s activated=%v", activated.Code, activated.Body.String(), store.activated)
	}
	cancelled := ut.PerformRequest(router.Engine, "POST", "/api/admin/flash-sales/41/cancel", nil,
		ut.Header{Key: "Cookie", Value: httpauth.CookieName + "=admin"}, ut.Header{Key: "Origin", Value: "https://play.example"})
	if cancelled.Code != 200 || !store.cancelled {
		t.Fatalf("cancelled status=%d body=%s cancelled=%v", cancelled.Code, cancelled.Body.String(), store.cancelled)
	}
}

type httpStore struct {
	activity  entity.Activity
	request   flashsale.Request
	created   bool
	updated   bool
	activated bool
	cancelled bool
}

func (s *httpStore) ListActivities(context.Context, flashsale.ListFilter) ([]entity.Activity, error) {
	return []entity.Activity{s.activity}, nil
}
func (s *httpStore) GetActivity(context.Context, int64) (entity.Activity, error) {
	return s.activity, nil
}
func (s *httpStore) CreateActivity(_ context.Context, activity entity.Activity) (entity.Activity, error) {
	s.created = true
	activity.ID = 41
	activity.GameSlug = "demo"
	activity.GameName = "Demo"
	activity.EditionName = "Standard"
	s.activity = activity
	return activity, nil
}
func (s *httpStore) UpdateDraft(_ context.Context, activity entity.Activity) (entity.Activity, error) {
	s.updated = true
	activity.Status = entity.StatusDraft
	s.activity = activity
	return activity, nil
}
func (s *httpStore) ActivateActivity(_ context.Context, _ int64, version int64, at time.Time) (entity.Activity, error) {
	s.activated = true
	s.activity.Status = entity.StatusActive
	s.activity.Version = version
	s.activity.ActivatedAt = at
	return s.activity, nil
}
func (s *httpStore) CancelActivity(_ context.Context, _ int64, cutoff time.Time) (entity.Activity, error) {
	s.cancelled = true
	s.activity.Status = entity.StatusCancelled
	s.activity.CancelledAt = cutoff
	return s.activity, nil
}
func (s *httpStore) Allocate(context.Context, flashsale.Event) (flashsale.Allocation, error) {
	return flashsale.Allocation{}, nil
}
func (s *httpStore) Fail(context.Context, flashsale.Event, string, string) error { return nil }
func (s *httpStore) MarkOrderReady(context.Context, string, string) error        { return nil }
func (s *httpStore) GetRequest(context.Context, string, int64, bool) (flashsale.Request, error) {
	if s.request.RequestID == "" {
		return flashsale.Request{}, flashsale.ErrNotFound
	}
	return s.request, nil
}
func (s *httpStore) ExpireDue(context.Context, int) (int, error) { return 0, nil }
func (s *httpStore) ClaimReleaseJobs(context.Context, int, time.Duration) ([]flashsale.ReleaseJob, error) {
	return nil, nil
}
func (s *httpStore) CompleteReleaseJob(context.Context, int64) error                 { return nil }
func (s *httpStore) RetryReleaseJob(context.Context, int64, time.Time, string) error { return nil }

type httpCatalog struct{}

func (httpCatalog) PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error) {
	return catalogentity.PurchaseOffer{GameID: 3, GameSlug: "demo", GameName: "Demo", EditionID: 12, EditionCode: "standard", EditionName: "Standard", AmountMinor: 1999, Currency: "USD", Region: "GLOBAL"}, nil
}

type httpAdmission struct{ result flashsale.AdmissionResult }

func (a *httpAdmission) Reserve(context.Context, flashsale.AdmissionCommand) (flashsale.AdmissionResult, error) {
	return a.result, nil
}

type httpActivityCache struct{}

func (httpActivityCache) Stage(context.Context, entity.Activity) error  { return nil }
func (httpActivityCache) Enable(context.Context, entity.Activity) error { return nil }
func (httpActivityCache) Close(context.Context, entity.Activity) (time.Time, error) {
	return time.Now().UTC(), nil
}
func (httpActivityCache) GetRequest(context.Context, string, int64, bool) (flashsale.Request, error) {
	return flashsale.Request{}, flashsale.ErrNotFound
}

type httpOrders struct{}

func (httpOrders) CreateFromFlashSale(context.Context, flashsale.OrderCommand) (flashsale.OrderResult, error) {
	return flashsale.OrderResult{Order: orderentity.Order{}}, nil
}

type httpAuthenticator struct{}

func (httpAuthenticator) Authenticate(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "user":
		return auth.Principal{UserID: 7, Role: auth.RoleUser}, nil
	case "admin":
		return auth.Principal{UserID: 1, Role: auth.RoleAdmin}, nil
	default:
		return auth.Principal{}, auth.ErrUnauthenticated
	}
}

func httpActivity(now time.Time) entity.Activity {
	return entity.Activity{ID: 41, Code: "AUTUMN-DELUXE", GameSlug: "demo", GameName: "Demo", EditionID: 12, EditionName: "Standard", Region: "GLOBAL", Currency: "USD", SalePriceMinor: 999, TotalStock: 10, Status: entity.StatusActive, Version: 1, StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Hour), PaymentTimeout: 15 * time.Minute}
}
