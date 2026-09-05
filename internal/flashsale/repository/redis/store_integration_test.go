package redis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/red060324/XiaoLanHe/internal/flashsale/entity"
	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

func TestRedisAdmissionLifecycleIntegration(t *testing.T) {
	store, activity, cleanup := integrationStore(t, 2)
	defer cleanup()
	ctx := context.Background()
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}

	first := admissionCommand(activity, 7, "same-key")
	accepted, err := store.Reserve(ctx, first)
	if err != nil || accepted.Outcome != flashsale.AdmissionAccepted || accepted.RequestID != first.RequestID || accepted.ReservedAt.IsZero() {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	replay, err := store.Reserve(ctx, first)
	if err != nil || replay.Outcome != flashsale.AdmissionReplay || replay.RequestID != first.RequestID || !replay.ReservedAt.Equal(accepted.ReservedAt) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflict := admissionCommand(activity, 7, "different-key")
	result, err := store.Reserve(ctx, conflict)
	if err != nil || result.Outcome != flashsale.AdmissionAlreadyReserved {
		t.Fatalf("conflict=%+v err=%v", result, err)
	}

	second := admissionCommand(activity, 8, "second-key")
	result, err = store.Reserve(ctx, second)
	if err != nil || result.Outcome != flashsale.AdmissionAccepted {
		t.Fatalf("second=%+v err=%v", result, err)
	}
	exhausted := admissionCommand(activity, 9, "third-key")
	result, err = store.Reserve(ctx, exhausted)
	if err != nil || result.Outcome != flashsale.AdmissionExhausted {
		t.Fatalf("exhausted=%+v err=%v", result, err)
	}

	released, err := store.Release(ctx, flashsale.ReleaseCommand{
		RequestID: first.RequestID, ActivityID: activity.ID, UserID: first.UserID, IdempotencyDigest: first.IdempotencyDigest,
		ReservedAt: accepted.ReservedAt, Reason: "technical_rollback", RemoveBuyer: true,
	})
	if err != nil || !released {
		t.Fatalf("released=%v err=%v", released, err)
	}
	released, err = store.Release(ctx, flashsale.ReleaseCommand{
		RequestID: first.RequestID, ActivityID: activity.ID, UserID: first.UserID, IdempotencyDigest: first.IdempotencyDigest,
		ReservedAt: accepted.ReservedAt, Reason: "technical_rollback", RemoveBuyer: true,
	})
	if err != nil || released {
		t.Fatalf("replayed release=%v err=%v", released, err)
	}
	remaining, err := store.Remaining(ctx, activity.ID)
	if err != nil || remaining != 1 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
}

func TestRedisStageCanReplaceUnusedDisabledDraftState(t *testing.T) {
	store, activity, cleanup := integrationStore(t, 2)
	defer cleanup()
	ctx := context.Background()
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	activity.StartsAt = activity.StartsAt.Add(time.Second)
	activity.EndsAt = activity.EndsAt.Add(time.Minute)
	activity.TotalStock = 3
	activity.Version++
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatalf("replace unused disabled state: %v", err)
	}
	remaining, err := store.Remaining(ctx, activity.ID)
	if err != nil || remaining != 3 {
		t.Fatalf("remaining=%d err=%v", remaining, err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}
	activity.TotalStock = 4
	if err := store.Stage(ctx, activity); !errors.Is(err, flashsale.ErrUnavailable) {
		t.Fatalf("enabled state mutation error=%v", err)
	}
}

func TestRedisAdmissionHighContentionIntegration(t *testing.T) {
	const stock int64 = 17
	const users = 96
	store, activity, cleanup := integrationStore(t, stock)
	defer cleanup()
	ctx := context.Background()
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var accepted atomic.Int64
	var exhausted atomic.Int64
	var unexpected atomic.Int64
	var wg sync.WaitGroup
	for userID := int64(1); userID <= users; userID++ {
		wg.Add(1)
		go func(userID int64) {
			defer wg.Done()
			<-start
			result, err := store.Reserve(ctx, admissionCommand(activity, userID, fmt.Sprintf("key-%d", userID)))
			if err != nil {
				unexpected.Add(1)
				return
			}
			switch result.Outcome {
			case flashsale.AdmissionAccepted:
				accepted.Add(1)
			case flashsale.AdmissionExhausted:
				exhausted.Add(1)
			default:
				unexpected.Add(1)
			}
		}(userID)
	}
	close(start)
	wg.Wait()
	remaining, err := store.Remaining(ctx, activity.ID)
	if err != nil || accepted.Load() != stock || exhausted.Load() != users-stock || unexpected.Load() != 0 || remaining != 0 {
		t.Fatalf("accepted=%d exhausted=%d unexpected=%d remaining=%d err=%v", accepted.Load(), exhausted.Load(), unexpected.Load(), remaining, err)
	}
}

func TestRedisAdmissionWindowAndCloseIntegration(t *testing.T) {
	store, activity, cleanup := integrationStore(t, 1)
	defer cleanup()
	ctx := context.Background()
	activity.StartsAt = time.Now().UTC().Add(time.Minute)
	activity.EndsAt = activity.StartsAt.Add(time.Minute)
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}
	result, err := store.Reserve(ctx, admissionCommand(activity, 7, "not-started"))
	if err != nil || result.Outcome != flashsale.AdmissionNotStarted {
		t.Fatalf("before start=%+v err=%v", result, err)
	}
	cutoff, err := store.Close(ctx, activity)
	if err != nil || cutoff.IsZero() {
		t.Fatalf("cutoff=%s err=%v", cutoff, err)
	}
	result, err = store.Reserve(ctx, admissionCommand(activity, 7, "closed"))
	if err != nil || result.Outcome != flashsale.AdmissionEnded {
		t.Fatalf("closed=%+v err=%v", result, err)
	}
}

func TestRedisAdmissionFailsClosedAndReloadsScriptIntegration(t *testing.T) {
	store, activity, cleanup := integrationStore(t, 2)
	defer cleanup()
	ctx := context.Background()
	command := admissionCommand(activity, 7, "dependency-key")

	result, err := store.Reserve(ctx, command)
	if err != nil || result.Outcome != flashsale.AdmissionUnavailable {
		t.Fatalf("missing state=%+v err=%v", result, err)
	}
	keys := store.keys(activity.ID)
	if err := store.client.HSet(ctx, keys.meta, "version", activity.Version, "active", "1").Err(); err != nil {
		t.Fatal(err)
	}
	result, err = store.Reserve(ctx, command)
	if err != nil || result.Outcome != flashsale.AdmissionUnavailable {
		t.Fatalf("malformed state=%+v err=%v", result, err)
	}
	if err := store.client.Unlink(ctx, keys.all()...).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.client.ScriptFlush(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	result, err = store.Reserve(ctx, command)
	if err != nil || result.Outcome != flashsale.AdmissionAccepted {
		t.Fatalf("NOSCRIPT reload result=%+v err=%v", result, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.Reserve(cancelled, admissionCommand(activity, 8, "cancelled-key"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error=%v", err)
	}
}

func TestRedisAdmissionExactEndIntegration(t *testing.T) {
	store, activity, cleanup := integrationStore(t, 1)
	defer cleanup()
	ctx := context.Background()
	activity.StartsAt = time.Now().UTC().Add(-time.Minute)
	activity.EndsAt = time.Now().UTC().Add(100 * time.Millisecond)
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}
	for {
		now, err := store.ServerTime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !now.Before(activity.EndsAt) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := store.Reserve(ctx, admissionCommand(activity, 7, "exact-end-key"))
	if err != nil || result.Outcome != flashsale.AdmissionEnded {
		t.Fatalf("exact end=%+v err=%v", result, err)
	}
}

func TestRedisAdmissionOneUserContentionIntegration(t *testing.T) {
	t.Run("same key replays", func(t *testing.T) {
		store, activity, cleanup := integrationStore(t, 10)
		defer cleanup()
		ctx := context.Background()
		if err := store.Stage(ctx, activity); err != nil {
			t.Fatal(err)
		}
		if err := store.Enable(ctx, activity); err != nil {
			t.Fatal(err)
		}
		command := admissionCommand(activity, 7, "same-race-key")
		outcomes := raceAdmissions(ctx, store, 32, func(int) flashsale.AdmissionCommand { return command })
		remaining, err := store.Remaining(ctx, activity.ID)
		if err != nil || outcomes[flashsale.AdmissionAccepted] != 1 || outcomes[flashsale.AdmissionReplay] != 31 || remaining != 9 {
			t.Fatalf("outcomes=%v remaining=%d err=%v", outcomes, remaining, err)
		}
	})

	t.Run("different keys conflict", func(t *testing.T) {
		store, activity, cleanup := integrationStore(t, 10)
		defer cleanup()
		ctx := context.Background()
		if err := store.Stage(ctx, activity); err != nil {
			t.Fatal(err)
		}
		if err := store.Enable(ctx, activity); err != nil {
			t.Fatal(err)
		}
		outcomes := raceAdmissions(ctx, store, 32, func(index int) flashsale.AdmissionCommand {
			return admissionCommand(activity, 7, fmt.Sprintf("different-race-key-%d", index))
		})
		remaining, err := store.Remaining(ctx, activity.ID)
		if err != nil || outcomes[flashsale.AdmissionAccepted] != 1 || outcomes[flashsale.AdmissionAlreadyReserved] != 31 || remaining != 9 {
			t.Fatalf("outcomes=%v remaining=%d err=%v", outcomes, remaining, err)
		}
	})
}

func TestRedisPendingRecoveryLeaseAndCompletionIntegration(t *testing.T) {
	store, activity, cleanup := integrationStore(t, 1)
	defer cleanup()
	ctx := context.Background()
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}
	command := admissionCommand(activity, 7, "recover-key")
	accepted, err := store.Reserve(ctx, command)
	if err != nil || accepted.Outcome != flashsale.AdmissionAccepted {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	command.ReservedAt = accepted.ReservedAt
	if err := store.client.ZAdd(ctx, store.keys(activity.ID).pending, goredis.Z{Score: float64(time.Now().Add(-time.Minute).UnixMilli()), Member: command.RequestID}).Err(); err != nil {
		t.Fatal(err)
	}

	events, err := store.ClaimStale(ctx, activity.ID, 30*time.Second, time.Minute, 10)
	if err != nil || len(events) != 1 || events[0].RequestID != command.RequestID || events[0].UserID != command.UserID || events[0].IdempotencyDigest != command.IdempotencyDigest || !events[0].ReservedAt.Equal(command.ReservedAt) {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	again, err := store.ClaimStale(ctx, activity.ID, 30*time.Second, time.Minute, 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("leased events=%+v err=%v", again, err)
	}
	if err := store.CompletePending(ctx, events[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.CompletePending(ctx, events[0]); err != nil {
		t.Fatalf("idempotent completion: %v", err)
	}
	if score, err := store.client.ZScore(ctx, store.keys(activity.ID).pending, command.RequestID).Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("pending score=%f err=%v", score, err)
	}
}

func TestRedisPendingCompletionAcceptsExactReleasedMarkerIntegration(t *testing.T) {
	store, activity, cleanup := integrationStore(t, 1)
	defer cleanup()
	ctx := context.Background()
	if err := store.Stage(ctx, activity); err != nil {
		t.Fatal(err)
	}
	if err := store.Enable(ctx, activity); err != nil {
		t.Fatal(err)
	}
	command := admissionCommand(activity, 7, "completion-release-race")
	accepted, err := store.Reserve(ctx, command)
	if err != nil || accepted.Outcome != flashsale.AdmissionAccepted {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	event := flashsale.Event{
		Version: 1, RequestID: command.RequestID, ActivityID: activity.ID, ActivityVersion: activity.Version, UserID: command.UserID,
		ReservedAt: accepted.ReservedAt, IdempotencyDigest: command.IdempotencyDigest,
	}
	released, err := store.Release(ctx, flashsale.ReleaseCommand{
		RequestID: event.RequestID, ActivityID: event.ActivityID, UserID: event.UserID, IdempotencyDigest: event.IdempotencyDigest,
		ReservedAt: event.ReservedAt, Reason: "final_guard", RemoveBuyer: false,
	})
	if err != nil || !released {
		t.Fatalf("released=%v err=%v", released, err)
	}
	if err := store.CompletePending(ctx, event); err != nil {
		t.Fatalf("complete released pending marker: %v", err)
	}
	replay, err := store.Reserve(ctx, command)
	if err != nil || replay.Outcome != flashsale.AdmissionReplay || replay.RequestID != command.RequestID || !replay.ReservedAt.Equal(accepted.ReservedAt) {
		t.Fatalf("released replay=%+v err=%v", replay, err)
	}
	request, err := store.GetRequest(ctx, command.RequestID, command.UserID, false)
	if err != nil || request.Status != flashsale.RequestFailed || request.FailureCode != "final_guard" {
		t.Fatalf("released request=%+v err=%v", request, err)
	}
}

func integrationStore(t *testing.T, stock int64) (*Store, entity.Activity, func()) {
	t.Helper()
	redisURL := os.Getenv("XLH_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("XLH_TEST_REDIS_URL is not set")
	}
	options, err := goredis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse XLH_TEST_REDIS_URL: %v", err)
	}
	client := goredis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("ping test Redis: %v", err)
	}
	prefix := fmt.Sprintf("xlh-it-%d", time.Now().UnixNano())
	store, err := NewStore(client, prefix, time.Hour)
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	activity := entity.Activity{ID: 41, Version: 1, TotalStock: stock, Status: entity.StatusActive, StartsAt: time.Now().UTC().Add(-time.Minute), EndsAt: time.Now().UTC().Add(time.Hour)}
	cleanup := func() {
		keys := store.keys(activity.ID).all()
		_ = client.Unlink(context.Background(), keys...).Err()
		_ = client.Close()
	}
	return store, activity, cleanup
}

func admissionCommand(activity entity.Activity, userID int64, key string) flashsale.AdmissionCommand {
	digestBytes := sha256.Sum256([]byte(strconv.FormatInt(activity.ID, 10) + ":" + strconv.FormatInt(userID, 10) + ":" + key))
	digest := hex.EncodeToString(digestBytes[:])
	return flashsale.AdmissionCommand{
		RequestID: "fsr_" + strconv.FormatInt(activity.ID, 36) + "_" + digest[:32], ActivityID: activity.ID,
		ActivityVersion: activity.Version, UserID: userID, IdempotencyDigest: digest,
	}
}

func raceAdmissions(ctx context.Context, store *Store, count int, command func(int) flashsale.AdmissionCommand) map[flashsale.AdmissionOutcome]int {
	start := make(chan struct{})
	results := make(chan flashsale.AdmissionOutcome, count)
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := store.Reserve(ctx, command(index))
			if err != nil {
				results <- flashsale.AdmissionUnavailable
				return
			}
			results <- result.Outcome
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)
	outcomes := make(map[flashsale.AdmissionOutcome]int)
	for outcome := range results {
		outcomes[outcome]++
	}
	return outcomes
}
