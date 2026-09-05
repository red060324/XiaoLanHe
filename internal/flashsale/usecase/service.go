package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	catalogentity "github.com/red060324/XiaoLanHe/internal/catalog/entity"
	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	orderentity "github.com/red060324/XiaoLanHe/internal/order/entity"
	"github.com/red060324/XiaoLanHe/internal/platform/auth"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

var (
	ErrInvalidInput     = errors.New("invalid flash sale input")
	ErrUnauthenticated  = auth.ErrUnauthenticated
	ErrForbidden        = errors.New("flash sale forbidden")
	ErrNotFound         = errors.New("flash sale not found")
	ErrNotStarted       = errors.New("flash sale not started")
	ErrEnded            = errors.New("flash sale ended")
	ErrStockExhausted   = errors.New("flash sale stock exhausted")
	ErrAlreadyReserved  = errors.New("flash sale already reserved")
	ErrAlreadyOwned     = errors.New("flash sale edition already owned")
	ErrOrderUnavailable = errors.New("flash sale order unavailable")
	ErrUnavailable      = errors.New("flash sale unavailable")
	ErrUnsupportedEvent = errors.New("unsupported flash sale event")
)

var (
	idempotencyPattern = regexp.MustCompile("^[A-Za-z0-9._:-]{8,128}$")
	requestIDPattern   = regexp.MustCompile("^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$")
)

type AdmissionOutcome string

const (
	AdmissionAccepted        AdmissionOutcome = "accepted"
	AdmissionReplay          AdmissionOutcome = "replay"
	AdmissionNotStarted      AdmissionOutcome = "not_started"
	AdmissionEnded           AdmissionOutcome = "ended"
	AdmissionExhausted       AdmissionOutcome = "exhausted"
	AdmissionAlreadyReserved AdmissionOutcome = "already_reserved"
	AdmissionUnavailable     AdmissionOutcome = "unavailable"
)

type AdmissionCommand struct {
	RequestID         string
	ActivityID        int64
	ActivityVersion   int64
	UserID            int64
	IdempotencyDigest string
	ReservedAt        time.Time
}

type AdmissionResult struct {
	Outcome    AdmissionOutcome
	RequestID  string
	ReservedAt time.Time
}

type Admission interface {
	Reserve(context.Context, AdmissionCommand) (AdmissionResult, error)
}

type AdmissionRecord struct {
	RequestID         string
	ActivityID        int64
	UserID            int64
	IdempotencyDigest string
	Status            string
	FailureCode       string
	ReservedAt        time.Time
}

type AdmissionInspector interface {
	ServerTime(context.Context) (time.Time, error)
	Lookup(context.Context, string, int64) (AdmissionRecord, bool, error)
}

type Catalog interface {
	PurchaseOffer(context.Context, int64, string, string) (catalogentity.PurchaseOffer, error)
}

type Store interface {
	ListActivities(context.Context, ListFilter) ([]entity.Activity, error)
	GetActivity(context.Context, int64) (entity.Activity, error)
	CreateActivity(context.Context, entity.Activity) (entity.Activity, error)
	UpdateDraft(context.Context, entity.Activity) (entity.Activity, error)
	ActivateActivity(context.Context, int64, int64, time.Time) (entity.Activity, error)
	CancelActivity(context.Context, int64, time.Time) (entity.Activity, error)
	Allocate(context.Context, Event) (Allocation, error)
	Fail(context.Context, Event, string, string) error
	MarkOrderReady(context.Context, string, string) error
	GetRequest(context.Context, string, int64, bool) (Request, error)
	ExpireDue(context.Context, int) (int, error)
	ClaimReleaseJobs(context.Context, int, time.Duration) ([]ReleaseJob, error)
	CompleteReleaseJob(context.Context, int64) error
	RetryReleaseJob(context.Context, int64, time.Time, string) error
}

type ActivityCache interface {
	Stage(context.Context, entity.Activity) error
	Enable(context.Context, entity.Activity) error
	Close(context.Context, entity.Activity) (time.Time, error)
	GetRequest(context.Context, string, int64, bool) (Request, error)
}

type OrderCommand struct {
	RequestID        string
	UserID           int64
	Offer            catalogentity.PurchaseOffer
	SalePriceMinor   int64
	PaymentExpiresAt time.Time
}

type OrderResult struct {
	Order    orderentity.Order
	Replayed bool
}

type Orders interface {
	CreateFromFlashSale(context.Context, OrderCommand) (OrderResult, error)
}

type Event struct {
	Version           int
	RequestID         string
	ActivityID        int64
	ActivityVersion   int64
	UserID            int64
	ReservedAt        time.Time
	IdempotencyDigest string
}

type Allocation struct {
	RequestID         string
	ActivityID        int64
	UserID            int64
	IdempotencyDigest string
	Status            entity.ReservationStatus
	ReservedAt        time.Time
	PaymentExpiresAt  time.Time
	Activity          entity.Activity
}

type RequestStatus string

const (
	RequestQueued     RequestStatus = "queued"
	RequestProcessing RequestStatus = "processing"
	RequestOrderReady RequestStatus = "order_ready"
	RequestFailed     RequestStatus = "failed"
	RequestExpired    RequestStatus = "expired"
)

type Request struct {
	RequestID        string
	ActivityID       int64
	Status           RequestStatus
	OrderNo          string
	FailureCode      string
	PaymentExpiresAt time.Time
	Replayed         bool
}

type ReleaseJob struct {
	ID                int64
	RequestID         string
	ActivityID        int64
	UserID            int64
	IdempotencyDigest string
	ReservedAt        time.Time
	Reason            string
	Attempts          int
}

type ReleaseCommand struct {
	RequestID         string
	ActivityID        int64
	UserID            int64
	IdempotencyDigest string
	ReservedAt        time.Time
	Reason            string
	RemoveBuyer       bool
}

type Compensator interface {
	Release(context.Context, ReleaseCommand) (bool, error)
}

type PendingRecovery interface {
	ClaimStale(context.Context, int64, time.Duration, time.Duration, int) ([]Event, error)
}

type PendingCompleter interface {
	CompletePending(context.Context, Event) error
}

type EventPublisher interface {
	Publish(context.Context, Event) error
}

type Service struct {
	store     Store
	catalog   Catalog
	admission Admission
	orders    Orders
	cache     ActivityCache
}

func NewService(store Store, catalog Catalog, admission Admission, orders Orders) *Service {
	return &Service{store: store, catalog: catalog, admission: admission, orders: orders}
}

func (s *Service) WithActivityCache(cache ActivityCache) *Service {
	s.cache = cache
	return s
}

type ListFilter struct {
	BeforeID int64
	Limit    int
}

func (s *Service) ListActivities(ctx context.Context, cursor string, limit int) ([]entity.Activity, string, error) {
	beforeID, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", ErrInvalidInput
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return nil, "", ErrInvalidInput
	}
	items, err := s.store.ListActivities(ctx, ListFilter{BeforeID: beforeID, Limit: limit + 1})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	return items, next, nil
}

func (s *Service) GetActivity(ctx context.Context, id int64) (entity.Activity, error) {
	if id <= 0 {
		return entity.Activity{}, ErrNotFound
	}
	activity, err := s.store.GetActivity(ctx, id)
	if err != nil {
		return entity.Activity{}, err
	}
	if activity.Status == entity.StatusDraft {
		return entity.Activity{}, ErrNotFound
	}
	return activity, nil
}

func (s *Service) CreateActivity(ctx context.Context, principal auth.Principal, draft entity.Activity) (entity.Activity, error) {
	if !principal.IsAdmin() {
		return entity.Activity{}, ErrForbidden
	}
	draft.Normalize()
	offer, err := s.catalog.PurchaseOffer(ctx, draft.EditionID, draft.Region, draft.Currency)
	if err != nil || !matchesOffer(draft, offer) || draft.ValidateDraft(time.Now().UTC(), offer.AmountMinor) != nil {
		return entity.Activity{}, ErrInvalidInput
	}
	draft.Status = entity.StatusDraft
	draft.CreatedBy = principal.UserID
	return s.store.CreateActivity(ctx, draft)
}

func (s *Service) UpdateActivity(ctx context.Context, principal auth.Principal, draft entity.Activity) (entity.Activity, error) {
	if !principal.IsAdmin() {
		return entity.Activity{}, ErrForbidden
	}
	draft.Normalize()
	offer, err := s.catalog.PurchaseOffer(ctx, draft.EditionID, draft.Region, draft.Currency)
	if err != nil || !matchesOffer(draft, offer) || draft.ValidateDraft(time.Now().UTC(), offer.AmountMinor) != nil {
		return entity.Activity{}, ErrInvalidInput
	}
	return s.store.UpdateDraft(ctx, draft)
}

func (s *Service) Activate(ctx context.Context, principal auth.Principal, id int64) (entity.Activity, error) {
	if !principal.IsAdmin() {
		return entity.Activity{}, ErrForbidden
	}
	activity, err := s.store.GetActivity(ctx, id)
	if err != nil {
		return entity.Activity{}, err
	}
	if s.cache == nil {
		return entity.Activity{}, ErrUnavailable
	}
	if activity.Status == entity.StatusActive {
		if err := s.cache.Stage(ctx, activity); err != nil {
			return entity.Activity{}, ErrUnavailable
		}
		if err := s.cache.Enable(ctx, activity); err != nil {
			return entity.Activity{}, ErrUnavailable
		}
		return activity, nil
	}
	if activity.Status != entity.StatusDraft {
		return entity.Activity{}, entity.ErrInvalidState
	}
	offer, err := s.catalog.PurchaseOffer(ctx, activity.EditionID, activity.Region, activity.Currency)
	validationActivity := activity
	validationActivity.AllocatedStock = 0
	if err != nil || !matchesOffer(validationActivity, offer) || validationActivity.ValidateDraft(time.Now().UTC(), offer.AmountMinor) != nil {
		return entity.Activity{}, ErrInvalidInput
	}
	now := time.Now().UTC()
	if err := activity.Activate(now); err != nil {
		return entity.Activity{}, err
	}
	if err := s.cache.Stage(ctx, activity); err != nil {
		return entity.Activity{}, ErrUnavailable
	}
	activity, err = s.store.ActivateActivity(ctx, id, activity.Version, now)
	if err != nil {
		return entity.Activity{}, err
	}
	if err := s.cache.Enable(ctx, activity); err != nil {
		return entity.Activity{}, ErrUnavailable
	}
	return activity, nil
}

func (s *Service) Cancel(ctx context.Context, principal auth.Principal, id int64) (entity.Activity, error) {
	if !principal.IsAdmin() {
		return entity.Activity{}, ErrForbidden
	}
	activity, err := s.store.GetActivity(ctx, id)
	if err != nil {
		return entity.Activity{}, err
	}
	if s.cache == nil {
		return entity.Activity{}, ErrUnavailable
	}
	cutoff, err := s.cache.Close(ctx, activity)
	if err != nil {
		return entity.Activity{}, ErrUnavailable
	}
	return s.store.CancelActivity(ctx, id, cutoff)
}

func (s *Service) Reserve(ctx context.Context, principal auth.Principal, activityID int64, idempotencyKey string) (Request, error) {
	if principal.UserID <= 0 {
		return Request{}, ErrUnauthenticated
	}
	if activityID <= 0 || !idempotencyPattern.MatchString(idempotencyKey) {
		return Request{}, ErrInvalidInput
	}
	activity, err := s.store.GetActivity(ctx, activityID)
	if err != nil {
		return Request{}, err
	}
	if activity.Status != entity.StatusActive && activity.Status != entity.StatusCancelled {
		return Request{}, ErrEnded
	}
	digest := idempotencyDigest(activityID, principal.UserID, idempotencyKey)
	requestID := "fsr_" + strconv.FormatInt(activityID, 36) + "_" + digest[:32]
	result, err := s.admission.Reserve(ctx, AdmissionCommand{
		RequestID: requestID, ActivityID: activity.ID, ActivityVersion: activity.Version,
		UserID: principal.UserID, IdempotencyDigest: digest,
	})
	if err != nil {
		return Request{}, err
	}
	request := Request{RequestID: result.RequestID, ActivityID: activity.ID, Status: RequestQueued, Replayed: result.Outcome == AdmissionReplay}
	if request.RequestID == "" {
		request.RequestID = requestID
	}
	switch result.Outcome {
	case AdmissionAccepted, AdmissionReplay:
		return request, nil
	case AdmissionNotStarted:
		return Request{}, ErrNotStarted
	case AdmissionEnded:
		return Request{}, ErrEnded
	case AdmissionExhausted:
		return Request{}, ErrStockExhausted
	case AdmissionAlreadyReserved:
		return Request{}, ErrAlreadyReserved
	default:
		return Request{}, ErrUnavailable
	}
}

func (s *Service) Fulfil(ctx context.Context, event Event) (resultErr error) {
	started := time.Now()
	outcome := "success"
	defer func() {
		if resultErr != nil {
			outcome = flashSaleErrorOutcome(resultErr)
		}
		platformmetrics.Default().ObserveFlashSale("fulfil", outcome, time.Since(started), 1, pendingAge(event.ReservedAt))
	}()
	if err := event.Validate(); err != nil {
		outcome = "invalid"
		return ErrUnsupportedEvent
	}
	allocation, err := s.store.Allocate(ctx, event)
	if err != nil {
		if errors.Is(err, ErrStockExhausted) || errors.Is(err, ErrAlreadyReserved) || errors.Is(err, ErrEnded) || errors.Is(err, ErrNotFound) {
			platformmetrics.Default().ObserveFlashSale("final_guard", "rejected", time.Since(started), 1, pendingAge(event.ReservedAt))
			if failErr := s.store.Fail(ctx, event, failureCode(err), "final_guard"); failErr != nil {
				return failErr
			}
			outcome = "terminal"
			return nil
		}
		return err
	}
	switch allocation.Status {
	case entity.ReservationFailed, entity.ReservationExpired, entity.ReservationOrderReady:
		return nil
	case entity.ReservationReserved:
	default:
		return ErrUnavailable
	}
	activity := allocation.Activity
	offer := catalogentity.PurchaseOffer{EditionID: activity.EditionID, Currency: activity.Currency, Region: activity.Region}
	result, err := s.orders.CreateFromFlashSale(ctx, OrderCommand{
		RequestID: event.RequestID, UserID: event.UserID, Offer: offer,
		SalePriceMinor: activity.SalePriceMinor, PaymentExpiresAt: allocation.PaymentExpiresAt,
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyOwned) {
			platformmetrics.Default().ObserveFlashSale("final_guard", "rejected", time.Since(started), 1, pendingAge(event.ReservedAt))
			if failErr := s.store.Fail(ctx, event, "already_owned", "final_guard"); failErr != nil {
				return failErr
			}
			outcome = "terminal"
			return nil
		}
		if errors.Is(err, ErrOrderUnavailable) {
			platformmetrics.Default().ObserveFlashSale("final_guard", "rejected", time.Since(started), 1, pendingAge(event.ReservedAt))
			if failErr := s.store.Fail(ctx, event, "order_unavailable", "final_guard"); failErr != nil {
				return failErr
			}
			outcome = "terminal"
			return nil
		}
		return err
	}
	return s.store.MarkOrderReady(ctx, event.RequestID, result.Order.OrderNo)
}

func flashSaleErrorOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrNotStarted):
		return "not_started"
	case errors.Is(err, ErrEnded):
		return "ended"
	case errors.Is(err, ErrStockExhausted):
		return "exhausted"
	case errors.Is(err, ErrAlreadyReserved), errors.Is(err, ErrAlreadyOwned):
		return "already_reserved"
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrUnsupportedEvent), errors.Is(err, ErrUnauthenticated), errors.Is(err, ErrForbidden):
		return "invalid"
	default:
		return "dependency"
	}
}

func pendingAge(timestamp time.Time) time.Duration {
	if timestamp.IsZero() {
		return 0
	}
	age := time.Since(timestamp)
	if age < 0 {
		return 0
	}
	return age
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrStockExhausted):
		return "final_stock_exhausted"
	case errors.Is(err, ErrAlreadyReserved):
		return "already_reserved"
	case errors.Is(err, ErrEnded):
		return "activity_ended"
	default:
		return "activity_unavailable"
	}
}

func (e Event) Validate() error {
	if e.Version != 1 || !requestIDPattern.MatchString(e.RequestID) || e.ActivityID <= 0 || e.ActivityVersion <= 0 || e.UserID <= 0 ||
		e.ReservedAt.IsZero() || !isDigest(e.IdempotencyDigest) {
		return ErrUnsupportedEvent
	}
	parts := strings.Split(e.RequestID, "_")
	activityID, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil || activityID != e.ActivityID || parts[2] != e.IdempotencyDigest[:32] {
		return ErrUnsupportedEvent
	}
	return nil
}

func isDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func matchesOffer(activity entity.Activity, offer catalogentity.PurchaseOffer) bool {
	return offer.EditionID == activity.EditionID && offer.Currency == activity.Currency &&
		(offer.Region == activity.Region || offer.Region == "GLOBAL")
}

func (s *Service) GetRequest(ctx context.Context, principal auth.Principal, requestID string) (Request, error) {
	if principal.UserID <= 0 {
		return Request{}, ErrUnauthenticated
	}
	if !requestIDPattern.MatchString(requestID) {
		return Request{}, ErrNotFound
	}
	request, err := s.store.GetRequest(ctx, requestID, principal.UserID, principal.IsAdmin())
	if err == nil || !errors.Is(err, ErrNotFound) || s.cache == nil {
		return request, err
	}
	return s.cache.GetRequest(ctx, requestID, principal.UserID, principal.IsAdmin())
}

func idempotencyDigest(activityID, userID int64, key string) string {
	value := strconv.FormatInt(activityID, 10) + ":" + strconv.FormatInt(userID, 10) + ":" + key
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func decodeCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalidInput
	}
	return id, nil
}
