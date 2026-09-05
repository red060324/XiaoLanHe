package entry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackgroundRunsAllWorkersAndShutsDown(t *testing.T) {
	var recovery, expiry, release atomic.Int64
	runner := func(counter *atomic.Int64) Runner {
		return func(context.Context) (int, error) {
			counter.Add(1)
			return 0, nil
		}
	}
	background := StartBackground(context.Background(), time.Millisecond, runner(&recovery), time.Millisecond, runner(&expiry), time.Millisecond, runner(&release))
	deadline := time.Now().Add(time.Second)
	for (recovery.Load() == 0 || expiry.Load() == 0 || release.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := background.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if recovery.Load() == 0 || expiry.Load() == 0 || release.Load() == 0 {
		t.Fatalf("recovery=%d expiry=%d release=%d", recovery.Load(), expiry.Load(), release.Load())
	}
}

func TestWorkerLogsDoNotIncludeProviderErrors(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	done := make(chan struct{}, 1)
	secret := "redis://:super-secret@private-redis:6379/0"
	handled := make(chan struct{}, 1)
	slog.SetDefault(slog.New(&notifyingHandler{Handler: slog.NewTextHandler(&output, nil), handled: handled}))
	background := StartBackground(context.Background(), time.Millisecond, func(context.Context) (int, error) {
		select {
		case done <- struct{}{}:
		default:
		}
		return 0, errors.New(secret)
	}, time.Hour, func(context.Context) (int, error) { return 0, nil }, time.Hour, func(context.Context) (int, error) { return 0, nil })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not run")
	}
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("worker failure was not logged")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := background.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if logs := output.String(); strings.Contains(logs, secret) || !strings.Contains(logs, "dependency_error") {
		t.Fatalf("logs=%q", logs)
	}
}

type notifyingHandler struct {
	slog.Handler
	handled chan struct{}
}

func (h *notifyingHandler) Handle(ctx context.Context, record slog.Record) error {
	err := h.Handler.Handle(ctx, record)
	select {
	case h.handled <- struct{}{}:
	default:
	}
	return err
}

func (h *notifyingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &notifyingHandler{Handler: h.Handler.WithAttrs(attrs), handled: h.handled}
}

func (h *notifyingHandler) WithGroup(name string) slog.Handler {
	return &notifyingHandler{Handler: h.Handler.WithGroup(name), handled: h.handled}
}
