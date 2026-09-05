package redis

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

const defaultRecoveryGrace = 24 * time.Hour
const maxReservationClockAge = 30 * time.Second

var (
	prefixPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	requestIDPattern = regexp.MustCompile(`^fsr_[1-9a-z][0-9a-z]{0,12}_[a-f0-9]{32}$`)
	digestPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

//go:embed scripts/stage.lua
var stageLua string

//go:embed scripts/enable.lua
var enableLua string

//go:embed scripts/close.lua
var closeLua string

//go:embed scripts/admit.lua
var admitLua string

//go:embed scripts/release.lua
var releaseLua string

//go:embed scripts/claim_pending.lua
var claimPendingLua string

//go:embed scripts/complete_pending.lua
var completePendingLua string

type Store struct {
	client          goredis.UniversalClient
	prefix          string
	recoveryGrace   time.Duration
	stage           *goredis.Script
	enable          *goredis.Script
	close           *goredis.Script
	admit           *goredis.Script
	release         *goredis.Script
	claimPending    *goredis.Script
	completePending *goredis.Script
}

func NewStore(client goredis.UniversalClient, prefix string, recoveryGrace time.Duration) (*Store, error) {
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), ":")
	if client == nil || !prefixPattern.MatchString(prefix) {
		return nil, errors.New("invalid flash sale Redis configuration")
	}
	if recoveryGrace == 0 {
		recoveryGrace = defaultRecoveryGrace
	}
	if recoveryGrace < time.Minute || recoveryGrace > 7*24*time.Hour {
		return nil, errors.New("invalid flash sale Redis recovery grace")
	}
	return &Store{
		client: client, prefix: prefix, recoveryGrace: recoveryGrace,
		stage: goredis.NewScript(stageLua), enable: goredis.NewScript(enableLua), close: goredis.NewScript(closeLua),
		admit: goredis.NewScript(admitLua), release: goredis.NewScript(releaseLua), claimPending: goredis.NewScript(claimPendingLua),
		completePending: goredis.NewScript(completePendingLua),
	}, nil
}

func (s *Store) LoadScripts(ctx context.Context) error {
	for _, script := range []*goredis.Script{s.stage, s.enable, s.close, s.admit, s.release, s.claimPending, s.completePending} {
		if err := script.Load(ctx, s.client).Err(); err != nil {
			return fmt.Errorf("load flash sale Redis script: %w", err)
		}
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *Store) ServerTime(ctx context.Context) (time.Time, error) {
	value, err := s.client.Time(ctx).Result()
	if err != nil {
		return time.Time{}, fmt.Errorf("read Redis server time: %w", err)
	}
	return value.UTC(), nil
}

func (s *Store) Stage(ctx context.Context, activity entity.Activity) error {
	if !validActivity(activity) {
		return flashsale.ErrInvalidInput
	}
	keys := s.keys(activity.ID)
	result, err := s.stage.Run(ctx, s.client, keys.all(),
		activity.Version, activity.StartsAt.UnixMilli(), activity.EndsAt.UnixMilli(),
		activity.TotalStock, activity.TotalStock-activity.AllocatedStock, s.expiresAt(activity).UnixMilli()).Int64()
	if err != nil {
		return fmt.Errorf("stage flash sale Redis state: %w", err)
	}
	if result < 0 {
		return flashsale.ErrUnavailable
	}
	return nil
}

func (s *Store) Enable(ctx context.Context, activity entity.Activity) error {
	if activity.ID <= 0 || activity.Version <= 0 {
		return flashsale.ErrInvalidInput
	}
	result, err := s.enable.Run(ctx, s.client, []string{s.keys(activity.ID).meta}, activity.Version).Int64()
	if err != nil {
		return fmt.Errorf("enable flash sale Redis state: %w", err)
	}
	if result != 1 {
		return flashsale.ErrUnavailable
	}
	return nil
}

func (s *Store) Close(ctx context.Context, activity entity.Activity) (time.Time, error) {
	if activity.ID <= 0 || activity.Version <= 0 {
		return time.Time{}, flashsale.ErrInvalidInput
	}
	values, err := s.close.Run(ctx, s.client, []string{s.keys(activity.ID).meta}, activity.Version).Slice()
	if err != nil {
		return time.Time{}, fmt.Errorf("close flash sale Redis state: %w", err)
	}
	if len(values) != 2 {
		return time.Time{}, flashsale.ErrUnavailable
	}
	code, ok := redisInteger(values[0])
	millis, millisOK := redisInteger(values[1])
	if !ok || !millisOK || code < 1 || millis <= 0 {
		return time.Time{}, flashsale.ErrUnavailable
	}
	return time.UnixMilli(millis).UTC(), nil
}

func (s *Store) Reserve(ctx context.Context, command flashsale.AdmissionCommand) (result flashsale.AdmissionResult, resultErr error) {
	started := time.Now()
	metricOutcome := string(flashsale.AdmissionUnavailable)
	defer func() {
		if result.Outcome != "" {
			metricOutcome = string(result.Outcome)
		}
		if resultErr != nil && (errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded)) {
			metricOutcome = contextOutcome(resultErr)
		}
		platformmetrics.Default().ObserveFlashSale("lua_admission", metricOutcome, time.Since(started), 1, 0)
	}()
	if command.ReservedAt.IsZero() {
		serverTime, err := s.ServerTime(ctx)
		if err != nil {
			return flashsale.AdmissionResult{}, err
		}
		command.ReservedAt = serverTime
	}
	if !validAdmission(command) {
		return flashsale.AdmissionResult{}, flashsale.ErrInvalidInput
	}
	keys := s.keys(command.ActivityID)
	values, err := s.admit.Run(ctx, s.client, keys.all(), command.RequestID, command.ActivityVersion,
		command.UserID, command.IdempotencyDigest, command.ReservedAt.UTC().UnixMilli(), maxReservationClockAge.Milliseconds()).Slice()
	if err != nil {
		return flashsale.AdmissionResult{}, fmt.Errorf("reserve flash sale Redis stock: %w", err)
	}
	if len(values) != 3 {
		return flashsale.AdmissionResult{}, flashsale.ErrUnavailable
	}
	code, ok := redisInteger(values[0])
	reservedMillis, timeOK := redisInteger(values[2])
	if !ok || !timeOK {
		return flashsale.AdmissionResult{}, flashsale.ErrUnavailable
	}
	outcome := mapAdmissionOutcome(code)
	result = flashsale.AdmissionResult{Outcome: outcome}
	if outcome == flashsale.AdmissionAccepted || outcome == flashsale.AdmissionReplay {
		requestID, stringOK := values[1].(string)
		if !stringOK || requestID != command.RequestID || reservedMillis <= 0 {
			return flashsale.AdmissionResult{}, flashsale.ErrUnavailable
		}
		result.RequestID = requestID
		result.ReservedAt = time.UnixMilli(reservedMillis).UTC()
	}
	return result, nil
}

func (s *Store) Lookup(ctx context.Context, requestID string, activityID int64) (flashsale.AdmissionRecord, bool, error) {
	if activityID <= 0 || requestID == "" {
		return flashsale.AdmissionRecord{}, false, flashsale.ErrInvalidInput
	}
	value, err := s.client.HGet(ctx, s.keys(activityID).requests, requestID).Result()
	if errors.Is(err, goredis.Nil) {
		return flashsale.AdmissionRecord{}, false, nil
	}
	if err != nil {
		return flashsale.AdmissionRecord{}, false, fmt.Errorf("read flash sale Redis request: %w", err)
	}
	marker, err := parseMarker(requestID, activityID, value)
	if err != nil {
		return flashsale.AdmissionRecord{}, false, flashsale.ErrUnavailable
	}
	return marker, true, nil
}

func (s *Store) GetRequest(ctx context.Context, requestID string, userID int64, admin bool) (flashsale.Request, error) {
	activityID, err := ActivityIDFromRequestID(requestID)
	if err != nil {
		return flashsale.Request{}, flashsale.ErrNotFound
	}
	marker, found, err := s.Lookup(ctx, requestID, activityID)
	if err != nil {
		return flashsale.Request{}, err
	}
	if !found || (!admin && marker.UserID != userID) {
		return flashsale.Request{}, flashsale.ErrNotFound
	}
	switch marker.Status {
	case "queued":
		return flashsale.Request{RequestID: requestID, ActivityID: activityID, Status: flashsale.RequestQueued}, nil
	case "released":
		return flashsale.Request{RequestID: requestID, ActivityID: activityID, Status: flashsale.RequestFailed, FailureCode: marker.FailureCode}, nil
	default:
		return flashsale.Request{}, flashsale.ErrNotFound
	}
}

func (s *Store) Release(ctx context.Context, command flashsale.ReleaseCommand) (released bool, resultErr error) {
	started := time.Now()
	defer func() {
		outcome := "dependency"
		switch {
		case resultErr == nil && released:
			outcome = "released"
		case resultErr == nil:
			outcome = "replay"
		case errors.Is(resultErr, context.Canceled), errors.Is(resultErr, context.DeadlineExceeded):
			outcome = contextOutcome(resultErr)
		case errors.Is(resultErr, flashsale.ErrInvalidInput):
			outcome = "invalid"
		}
		platformmetrics.Default().ObserveFlashSale("lua_release", outcome, time.Since(started), 1, pendingAge(command.ReservedAt))
	}()
	if command.ActivityID <= 0 || command.UserID <= 0 || !requestIDPattern.MatchString(command.RequestID) ||
		!digestPattern.MatchString(command.IdempotencyDigest) || command.ReservedAt.IsZero() || !validReleaseReason(command.Reason) {
		return false, flashsale.ErrInvalidInput
	}
	removeBuyer := "0"
	if command.RemoveBuyer {
		removeBuyer = "1"
	}
	result, err := s.release.Run(ctx, s.client, s.keys(command.ActivityID).all(), command.RequestID, command.UserID,
		command.IdempotencyDigest, command.ReservedAt.UTC().UnixMilli(), command.Reason, removeBuyer).Int64()
	if err != nil {
		return false, fmt.Errorf("release flash sale Redis stock: %w", err)
	}
	if result < 0 {
		return false, flashsale.ErrUnavailable
	}
	return result == 1, nil
}

func contextOutcome(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	return "dependency"
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

func (s *Store) ClaimStale(ctx context.Context, activityID int64, stale, lease time.Duration, limit int) ([]flashsale.Event, error) {
	if activityID <= 0 || stale <= 0 || lease <= 0 || limit < 1 || limit > 1000 {
		return nil, flashsale.ErrInvalidInput
	}
	keys := s.keys(activityID)
	values, err := s.claimPending.Run(ctx, s.client, []string{keys.meta, keys.requests, keys.pending},
		stale.Milliseconds(), lease.Milliseconds(), limit).Slice()
	if err != nil {
		return nil, fmt.Errorf("claim stale flash sale requests: %w", err)
	}
	if len(values) < 1 || (len(values)-1)%4 != 0 {
		return nil, flashsale.ErrUnavailable
	}
	version, ok := redisInteger(values[0])
	if !ok || version <= 0 {
		return nil, flashsale.ErrUnavailable
	}
	events := make([]flashsale.Event, 0, (len(values)-1)/4)
	for index := 1; index < len(values); index += 4 {
		requestID, requestOK := values[index].(string)
		userID, userOK := redisInteger(values[index+1])
		digest, digestOK := values[index+2].(string)
		reservedMillis, reservedOK := redisInteger(values[index+3])
		event := flashsale.Event{
			Version: 1, RequestID: requestID, ActivityID: activityID, ActivityVersion: version, UserID: userID,
			IdempotencyDigest: digest, ReservedAt: time.UnixMilli(reservedMillis).UTC(),
		}
		if !requestOK || !userOK || !digestOK || !reservedOK || event.Validate() != nil {
			return nil, flashsale.ErrUnavailable
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *Store) CompletePending(ctx context.Context, event flashsale.Event) error {
	if event.Validate() != nil {
		return flashsale.ErrInvalidInput
	}
	keys := s.keys(event.ActivityID)
	result, err := s.completePending.Run(ctx, s.client, []string{keys.requests, keys.pending}, event.RequestID, event.UserID,
		event.IdempotencyDigest, event.ReservedAt.UTC().UnixMilli()).Int64()
	if err != nil {
		return fmt.Errorf("complete flash sale pending request: %w", err)
	}
	if result < 0 {
		return flashsale.ErrUnavailable
	}
	return nil
}

func ActivityIDFromRequestID(requestID string) (int64, error) {
	if !requestIDPattern.MatchString(requestID) {
		return 0, flashsale.ErrInvalidInput
	}
	parts := strings.Split(requestID, "_")
	if len(parts) != 3 || parts[0] != "fsr" {
		return 0, flashsale.ErrInvalidInput
	}
	activityID, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil || activityID <= 0 {
		return 0, flashsale.ErrInvalidInput
	}
	return activityID, nil
}

func (s *Store) Remaining(ctx context.Context, activityID int64) (int64, error) {
	return s.client.Get(ctx, s.keys(activityID).stock).Int64()
}

type activityKeys struct{ meta, stock, buyers, requests, pending string }

func (k activityKeys) all() []string {
	return []string{k.meta, k.stock, k.buyers, k.requests, k.pending}
}

func (s *Store) keys(activityID int64) activityKeys {
	base := s.prefix + ":fs:{" + strconv.FormatInt(activityID, 10) + "}:"
	return activityKeys{meta: base + "meta", stock: base + "stock", buyers: base + "buyers", requests: base + "requests", pending: base + "pending"}
}

func (s *Store) expiresAt(activity entity.Activity) time.Time {
	return activity.EndsAt.UTC().Add(s.recoveryGrace)
}

func validActivity(activity entity.Activity) bool {
	return activity.ID > 0 && activity.Version > 0 && activity.TotalStock > 0 && activity.AllocatedStock >= 0 && activity.AllocatedStock <= activity.TotalStock &&
		!activity.StartsAt.IsZero() && activity.StartsAt.Before(activity.EndsAt)
}

func validAdmission(command flashsale.AdmissionCommand) bool {
	requestActivityID, err := ActivityIDFromRequestID(command.RequestID)
	return err == nil && requestActivityID == command.ActivityID && command.ActivityID > 0 && command.ActivityVersion > 0 && command.UserID > 0 &&
		requestIDPattern.MatchString(command.RequestID) && digestPattern.MatchString(command.IdempotencyDigest) &&
		strings.Split(command.RequestID, "_")[2] == command.IdempotencyDigest[:32] && !command.ReservedAt.IsZero()
}

func validReleaseReason(value string) bool {
	switch value {
	case "technical_rollback", "final_guard", "payment_expired", "admin_repair":
		return true
	default:
		return false
	}
}

func mapAdmissionOutcome(code int64) flashsale.AdmissionOutcome {
	switch code {
	case 1:
		return flashsale.AdmissionAccepted
	case 2:
		return flashsale.AdmissionReplay
	case -1:
		return flashsale.AdmissionNotStarted
	case -2:
		return flashsale.AdmissionEnded
	case -3:
		return flashsale.AdmissionExhausted
	case -4:
		return flashsale.AdmissionAlreadyReserved
	default:
		return flashsale.AdmissionUnavailable
	}
}

func redisInteger(value any) (int64, bool) {
	switch value := value.(type) {
	case int64:
		return value, true
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func parseMarker(requestID string, activityID int64, value string) (flashsale.AdmissionRecord, error) {
	parts := strings.Split(value, "|")
	requestParts := strings.Split(requestID, "_")
	if len(parts) < 4 || len(parts) > 5 || len(requestParts) != 3 || !digestPattern.MatchString(parts[1]) || requestParts[2] != parts[1][:32] {
		return flashsale.AdmissionRecord{}, errors.New("invalid marker")
	}
	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || userID <= 0 {
		return flashsale.AdmissionRecord{}, errors.New("invalid marker user")
	}
	reservedAt, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || reservedAt <= 0 {
		return flashsale.AdmissionRecord{}, errors.New("invalid marker time")
	}
	record := flashsale.AdmissionRecord{RequestID: requestID, ActivityID: activityID, UserID: userID, IdempotencyDigest: parts[1], Status: parts[2], ReservedAt: time.UnixMilli(reservedAt).UTC()}
	if record.Status == "released" {
		if len(parts) != 5 || !validReleaseReason(parts[4]) {
			return flashsale.AdmissionRecord{}, errors.New("invalid released marker")
		}
		record.FailureCode = parts[4]
	} else if record.Status != "queued" || len(parts) != 4 {
		return flashsale.AdmissionRecord{}, errors.New("invalid marker status")
	}
	return record, nil
}

var (
	_ flashsale.Admission          = (*Store)(nil)
	_ flashsale.AdmissionInspector = (*Store)(nil)
	_ flashsale.ActivityCache      = (*Store)(nil)
	_ flashsale.Compensator        = (*Store)(nil)
	_ flashsale.PendingRecovery    = (*Store)(nil)
	_ flashsale.PendingCompleter   = (*Store)(nil)
)
