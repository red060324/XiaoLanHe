package entry

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
	knowledge "github.com/red060324/XiaoLanHe/internal/knowledge/usecase"
	"github.com/red060324/XiaoLanHe/internal/platform/httpx"
)

func TestSearchCapacityAllowsBurstAndRefillsDeterministically(t *testing.T) {
	clock := &searchTestClock{value: time.Unix(1_700_000_000, 0)}
	guard := newSearchAdmission(time.Second, 2, 2, clock.Now)

	for attempt := 0; attempt < 2; attempt++ {
		release, retryAfter, err := guard.acquire(context.Background())
		if err != nil || retryAfter != 0 || release == nil {
			t.Fatalf("burst attempt %d: release=%v retryAfter=%s err=%v", attempt+1, release != nil, retryAfter, err)
		}
		release()
	}

	if release, retryAfter, err := guard.acquire(context.Background()); !errors.Is(err, entity.ErrCapacity) || release != nil || retryAfter != time.Second {
		t.Fatalf("empty bucket: release=%v retryAfter=%s err=%v", release != nil, retryAfter, err)
	}
	clock.Advance(500 * time.Millisecond)
	if release, retryAfter, err := guard.acquire(context.Background()); !errors.Is(err, entity.ErrCapacity) || release != nil || retryAfter != 500*time.Millisecond {
		t.Fatalf("partial refill: release=%v retryAfter=%s err=%v", release != nil, retryAfter, err)
	}
	clock.Advance(500 * time.Millisecond)
	release, retryAfter, err := guard.acquire(context.Background())
	if err != nil || retryAfter != 0 || release == nil {
		t.Fatalf("full refill: release=%v retryAfter=%s err=%v", release != nil, retryAfter, err)
	}
	release()
}

func TestKnowledgeSearchCapacityDefaultsAreConservativeAndBounded(t *testing.T) {
	clock := &searchTestClock{value: time.Unix(1_700_000_000, 0)}
	guard := newSearchAdmission(defaultSearchRefillEvery, defaultSearchBurst, defaultSearchConcurrency, clock.Now)
	if guard.refillEvery != time.Second || guard.burst != 5 || cap(guard.inFlight) != 4 {
		t.Fatalf("defaults refill=%s burst=%v concurrency=%d", guard.refillEvery, guard.burst, cap(guard.inFlight))
	}
	for attempt := 0; attempt < defaultSearchBurst; attempt++ {
		release, _, err := guard.acquire(context.Background())
		if err != nil {
			t.Fatalf("default burst attempt %d: %v", attempt+1, err)
		}
		release()
	}
	if release, _, err := guard.acquire(context.Background()); !errors.Is(err, entity.ErrCapacity) || release != nil {
		t.Fatalf("request after default burst: release=%v err=%v", release != nil, err)
	}
}

func TestSearchCapacityBoundsConcurrencyAndReleasesExactlyOnce(t *testing.T) {
	guard := newSearchAdmission(time.Hour, 3, 1, time.Now)
	firstRelease, _, err := guard.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if release, retryAfter, err := guard.acquire(context.Background()); !errors.Is(err, entity.ErrCapacity) || release != nil || retryAfter != concurrencyRetryAfter {
		t.Fatalf("concurrency rejection: release=%v retryAfter=%s err=%v", release != nil, retryAfter, err)
	}
	firstRelease()
	firstRelease()

	nextRelease, _, err := guard.acquire(context.Background())
	if err != nil {
		t.Fatalf("slot was not released: %v", err)
	}
	nextRelease()
}

func TestSearchCapacityRejectsCanceledContextWithoutConsumingCapacity(t *testing.T) {
	guard := newSearchAdmission(time.Hour, 1, 1, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if release, retryAfter, err := guard.acquire(ctx); !errors.Is(err, context.Canceled) || release != nil || retryAfter != 0 {
		t.Fatalf("canceled acquire: release=%v retryAfter=%s err=%v", release != nil, retryAfter, err)
	}
	release, _, err := guard.acquire(context.Background())
	if err != nil {
		t.Fatalf("canceled request consumed capacity: %v", err)
	}
	release()
}

func TestKnowledgeSearchCapacityRateRejectsBeforeProviderAndIgnoresForwardedAddress(t *testing.T) {
	provider := &countingSearchProvider{}
	guard := newSearchAdmission(time.Hour, 1, 2, time.Now)
	router := searchCapacityRouter(provider, guard)

	allowed := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=guide", nil, ut.Header{Key: "X-Forwarded-For", Value: "192.0.2.1"})
	if allowed.Code != 200 {
		t.Fatalf("allowed status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	rejected := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=guide", nil, ut.Header{Key: "X-Forwarded-For", Value: "198.51.100.2"})
	if rejected.Code != 429 || !strings.Contains(rejected.Body.String(), `"code":"capacity_exceeded"`) {
		t.Fatalf("rejected status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	if retryAfter := string(rejected.Header().Peek("Retry-After")); retryAfter != strconv.FormatInt(retryAfterSeconds(time.Hour), 10) {
		t.Fatalf("Retry-After=%q", retryAfter)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider calls=%d, want 1", calls)
	}
}

func TestKnowledgeSearchCapacityInvalidRequestDoesNotConsumeToken(t *testing.T) {
	provider := &countingSearchProvider{}
	guard := newSearchAdmission(time.Hour, 1, 1, time.Now)
	router := searchCapacityRouter(provider, guard)

	invalid := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=&mode=unknown", nil)
	if invalid.Code != 400 || provider.calls.Load() != 0 {
		t.Fatalf("invalid status=%d calls=%d body=%s", invalid.Code, provider.calls.Load(), invalid.Body.String())
	}
	valid := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=guide", nil)
	if valid.Code != 200 || provider.calls.Load() != 1 {
		t.Fatalf("valid status=%d calls=%d body=%s", valid.Code, provider.calls.Load(), valid.Body.String())
	}
}

func TestKnowledgeSearchCapacityConcurrencyRejectsBeforeProvider(t *testing.T) {
	provider := newBlockingSearchProvider()
	guard := newSearchAdmission(time.Hour, 3, 1, time.Now)
	router := searchCapacityRouter(provider, guard)
	firstDone := make(chan *ut.ResponseRecorder, 1)
	go func() {
		firstDone <- ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=first", nil)
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first provider call did not start")
	}

	rejected := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=second", nil)
	if rejected.Code != 429 || string(rejected.Header().Peek("Retry-After")) != "1" {
		t.Fatalf("rejected status=%d retry-after=%q body=%s", rejected.Code, rejected.Header().Peek("Retry-After"), rejected.Body.String())
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("provider calls after rejection=%d, want 1", calls)
	}

	close(provider.unblock)
	select {
	case response := <-firstDone:
		if response.Code != 200 {
			t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first search did not finish")
	}

	afterRelease := ut.PerformRequest(router.Engine, "GET", "/api/knowledge/search?query=third", nil)
	if afterRelease.Code != 200 || provider.calls.Load() != 2 {
		t.Fatalf("after release status=%d calls=%d body=%s", afterRelease.Code, provider.calls.Load(), afterRelease.Body.String())
	}
}

func TestKnowledgeSearchCapacityCancellationDoesNotLeakSlot(t *testing.T) {
	provider := newBlockingSearchProvider()
	guard := newSearchAdmission(time.Hour, 2, 1, time.Now)
	handler := newHTTPWithSearchAdmission(knowledge.NewService(provider), knowledgeAuthenticator{}, "https://play.example", guard)

	preCanceled, cancelPre := context.WithCancel(context.Background())
	cancelPre()
	preCanceledResponse := newSearchRequestContext("canceled-before-admission")
	handler.search(preCanceled, preCanceledResponse)
	if preCanceledResponse.Response.StatusCode() != 408 || provider.calls.Load() != 0 {
		t.Fatalf("pre-canceled status=%d calls=%d body=%s", preCanceledResponse.Response.StatusCode(), provider.calls.Load(), preCanceledResponse.Response.Body())
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceledResponse := newSearchRequestContext("canceled-in-flight")
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.search(ctx, canceledResponse)
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider call did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled search did not finish")
	}
	if canceledResponse.Response.StatusCode() != 503 || provider.calls.Load() != 1 {
		t.Fatalf("in-flight canceled status=%d calls=%d body=%s", canceledResponse.Response.StatusCode(), provider.calls.Load(), canceledResponse.Response.Body())
	}

	close(provider.unblock)
	afterCancellation := newSearchRequestContext("after-cancellation")
	handler.search(context.Background(), afterCancellation)
	if afterCancellation.Response.StatusCode() != 200 || provider.calls.Load() != 2 {
		t.Fatalf("after cancellation status=%d calls=%d body=%s", afterCancellation.Response.StatusCode(), provider.calls.Load(), afterCancellation.Response.Body())
	}
}

func searchCapacityRouter(provider knowledge.Provider, guard *searchAdmission) *server.Hertz {
	router := server.Default()
	router.Use(httpx.RequestIDMiddleware)
	newHTTPWithSearchAdmission(knowledge.NewService(provider), knowledgeAuthenticator{}, "https://play.example", guard).Register(router)
	return router
}

func newSearchRequestContext(query string) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.SetRequestURI("/api/knowledge/search?query=" + query)
	return c
}

type searchTestClock struct {
	mu    sync.Mutex
	value time.Time
}

func (c *searchTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *searchTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.value = c.value.Add(delta)
	c.mu.Unlock()
}

type countingSearchProvider struct {
	providerFake
	calls atomic.Int64
}

func (p *countingSearchProvider) Search(_ context.Context, input entity.SearchInput) (entity.SearchResult, error) {
	p.calls.Add(1)
	return entity.SearchResult{Query: input.Query, Provider: "lightrag", Mode: input.Mode, Items: []entity.Evidence{}}, nil
}

type blockingSearchProvider struct {
	providerFake
	calls   atomic.Int64
	started chan struct{}
	unblock chan struct{}
	once    sync.Once
}

func newBlockingSearchProvider() *blockingSearchProvider {
	return &blockingSearchProvider{started: make(chan struct{}), unblock: make(chan struct{})}
}

func (p *blockingSearchProvider) Search(ctx context.Context, input entity.SearchInput) (entity.SearchResult, error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.started) })
	select {
	case <-ctx.Done():
		return entity.SearchResult{}, ctx.Err()
	case <-p.unblock:
		return entity.SearchResult{Query: input.Query, Provider: "lightrag", Mode: input.Mode, Items: []entity.Evidence{}}, nil
	}
}
