package entry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

type Runner func(context.Context) (int, error)

type Background struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func StartBackground(parent context.Context, recoveryInterval time.Duration, recovery Runner, expiryInterval time.Duration, expiry Runner, releaseInterval time.Duration, release Runner) *Background {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	background := &Background{cancel: cancel, done: done}
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(3)
		go runPeriodic(ctx, &wg, "flash_sale_recovery", recoveryInterval, recovery)
		go runPeriodic(ctx, &wg, "flash_sale_expiry", expiryInterval, expiry)
		go runPeriodic(ctx, &wg, "flash_sale_release", releaseInterval, release)
		wg.Wait()
	}()
	return background
}

func (b *Background) Shutdown(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.cancel()
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runPeriodic(ctx context.Context, wg *sync.WaitGroup, operation string, interval time.Duration, runner Runner) {
	defer wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processed, err := runner(ctx)
			if err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "flash sale worker failed", "operation", operation, "outcome", "dependency_error")
			} else if processed > 0 {
				slog.InfoContext(ctx, "flash sale worker completed", "operation", operation, "processed", processed)
			}
		}
	}
}

func ExpiryRunner(worker *flashsale.ExpiryReaper) Runner         { return worker.RunOnce }
func ReleaseRunner(worker *flashsale.ReleaseWorker) Runner       { return worker.RunOnce }
func RecoveryRunner(worker *flashsale.RecoveryDispatcher) Runner { return worker.RunOnce }
