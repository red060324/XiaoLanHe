package entry

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/red060324/XiaoLanHe/internal/knowledge/entity"
)

const (
	defaultSearchRefillEvery = time.Second
	defaultSearchBurst       = 5
	defaultSearchConcurrency = 4
	concurrencyRetryAfter    = time.Second
)

// searchAdmission is shared by every public search handled by one HTTP entry. It is
// deliberately global rather than client-keyed: no untrusted address header can
// bypass it and its memory use is constant. Tokens refill lazily, so it owns no
// goroutine or cleanup lifecycle.
type searchAdmission struct {
	mu          sync.Mutex
	refillEvery time.Duration
	burst       float64
	tokens      float64
	lastRefill  time.Time
	now         func() time.Time
	inFlight    chan struct{}
}

func newDefaultSearchAdmission() *searchAdmission {
	return newSearchAdmission(defaultSearchRefillEvery, defaultSearchBurst, defaultSearchConcurrency, time.Now)
}

func newSearchAdmission(refillEvery time.Duration, burst, maxConcurrent int, now func() time.Time) *searchAdmission {
	if refillEvery <= 0 || burst <= 0 || maxConcurrent <= 0 || now == nil {
		panic("knowledge search admission requires positive bounds and a clock")
	}
	return &searchAdmission{
		refillEvery: refillEvery,
		burst:       float64(burst),
		tokens:      float64(burst),
		lastRefill:  now(),
		now:         now,
		inFlight:    make(chan struct{}, maxConcurrent),
	}
}

// acquire never queues for capacity: a full semaphore or empty bucket fails fast.
// The returned release function must be deferred by the caller.
func (a *searchAdmission) acquire(ctx context.Context) (release func(), retryAfter time.Duration, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case a.inFlight <- struct{}{}:
	default:
		return nil, concurrencyRetryAfter, entity.ErrCapacity
	}

	var once sync.Once
	release = func() {
		once.Do(func() { <-a.inFlight })
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, 0, err
	}

	allowed, retryAfter := a.takeToken()
	if !allowed {
		release()
		return nil, retryAfter, entity.ErrCapacity
	}
	if err := ctx.Err(); err != nil {
		a.refundToken()
		release()
		return nil, 0, err
	}
	return release, 0, nil
}

func (a *searchAdmission) takeToken() (bool, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.refillLocked(a.now())
	if a.tokens >= 1 {
		a.tokens--
		return true, 0
	}
	missing := 1 - a.tokens
	retryAfter := time.Duration(math.Ceil(missing * float64(a.refillEvery)))
	if retryAfter <= 0 {
		retryAfter = time.Nanosecond
	}
	return false, retryAfter
}

func (a *searchAdmission) refundToken() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refillLocked(a.now())
	a.tokens = math.Min(a.burst, a.tokens+1)
}

func (a *searchAdmission) refillLocked(now time.Time) {
	elapsed := now.Sub(a.lastRefill)
	if elapsed <= 0 {
		return
	}
	a.tokens = math.Min(a.burst, a.tokens+float64(elapsed)/float64(a.refillEvery))
	a.lastRefill = now
}

func retryAfterSeconds(delay time.Duration) int64 {
	seconds := int64(delay / time.Second)
	if delay%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	return seconds
}
