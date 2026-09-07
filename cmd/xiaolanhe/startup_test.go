package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMigrationStartupTimeoutIncludesLockAndDDL(t *testing.T) {
	lockTimeout := 45 * time.Second
	if got, want := migrationStartupTimeout(lockTimeout), lockTimeout+migrationDDLTimeout; got != want {
		t.Fatalf("migrationStartupTimeout() = %s, want %s", got, want)
	}
	if got := migrationStartupTimeout(lockTimeout); got <= lockTimeout {
		t.Fatalf("migration startup timeout %s does not leave a DDL budget after lock timeout %s", got, lockTimeout)
	}
}

func TestRunStartupPhaseCancelsContextOnReturn(t *testing.T) {
	var phaseContext context.Context
	if err := runStartupPhase(time.Minute, func(ctx context.Context) error {
		phaseContext = ctx
		return nil
	}); err != nil {
		t.Fatalf("runStartupPhase() error = %v", err)
	}
	if err := phaseContext.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("phase context error after return = %v, want context.Canceled", err)
	}
}

func TestRunStartupPhaseUsesIndependentContext(t *testing.T) {
	firstContext := context.Background()
	if err := runStartupPhase(time.Minute, func(ctx context.Context) error {
		firstContext = ctx
		return nil
	}); err != nil {
		t.Fatalf("first runStartupPhase() error = %v", err)
	}

	if err := runStartupPhase(time.Minute, func(ctx context.Context) error {
		if err := firstContext.Err(); !errors.Is(err, context.Canceled) {
			t.Fatalf("first phase context error = %v, want context.Canceled", err)
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("second phase inherited cancellation: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("second runStartupPhase() error = %v", err)
	}
}

func TestRunStartupPhaseRejectsNonPositiveTimeout(t *testing.T) {
	called := false
	err := runStartupPhase(0, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, errInvalidStartupTimeout) {
		t.Fatalf("runStartupPhase() error = %v, want %v", err, errInvalidStartupTimeout)
	}
	if called {
		t.Fatal("runStartupPhase called phase with an invalid timeout")
	}
}

func TestStartRocketMQClientRunsBoundedPreflightBeforeSynchronousStart(t *testing.T) {
	steps := make([]string, 0, 2)
	var deadlineCalls atomic.Int32
	const timeout = 20 * time.Millisecond
	err := startRocketMQClient(timeout, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("preflight context has no deadline")
		}
		steps = append(steps, "ready")
		return nil
	}, func() error {
		steps = append(steps, "start")
		return nil
	}, func() { deadlineCalls.Add(1) })
	if err != nil {
		t.Fatalf("startRocketMQClient() error = %v", err)
	}
	if got, want := len(steps), 2; got != want || steps[0] != "ready" || steps[1] != "start" {
		t.Fatalf("steps = %v, want [ready start]", steps)
	}
	time.Sleep(3 * timeout)
	if got := deadlineCalls.Load(); got != 0 {
		t.Fatalf("deadline callback calls = %d, want 0", got)
	}
}

func TestStartRocketMQClientDoesNotStartAfterFailedOrTimedOutPreflight(t *testing.T) {
	for name, ready := range map[string]func(context.Context) error{
		"failure": func(context.Context) error { return errors.New("route unavailable") },
		"timeout": func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	} {
		t.Run(name, func(t *testing.T) {
			var startCalls, deadlineCalls atomic.Int32
			timeout := time.Second
			if name == "timeout" {
				timeout = time.Millisecond
			}
			err := startRocketMQClient(timeout, ready, func() error {
				startCalls.Add(1)
				return nil
			}, func() { deadlineCalls.Add(1) })
			if err == nil {
				t.Fatal("expected preflight error")
			}
			if startCalls.Load() != 0 || deadlineCalls.Load() != 0 {
				t.Fatalf("callbacks after failed preflight: start=%d deadline=%d", startCalls.Load(), deadlineCalls.Load())
			}
		})
	}
}

func TestStartRocketMQClientRejectsMissingCallbacks(t *testing.T) {
	if err := startRocketMQClient(time.Second, nil, func() error { return nil }, func() {}); err == nil {
		t.Fatal("expected missing readiness callback error")
	}
	if err := startRocketMQClient(time.Second, func(context.Context) error { return nil }, nil, func() {}); err == nil {
		t.Fatal("expected missing start callback error")
	}
	if err := startRocketMQClient(time.Second, func(context.Context) error { return nil }, func() error { return nil }, nil); err == nil {
		t.Fatal("expected missing deadline callback error")
	}
}

func TestStartRocketMQClientRejectsNonPositiveTimeoutBeforeCallbacks(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		var readyCalls, startCalls, deadlineCalls atomic.Int32
		err := startRocketMQClient(timeout, func(context.Context) error {
			readyCalls.Add(1)
			return nil
		}, func() error {
			startCalls.Add(1)
			return nil
		}, func() { deadlineCalls.Add(1) })
		if !errors.Is(err, errInvalidStartupTimeout) {
			t.Fatalf("timeout %s error = %v, want %v", timeout, err, errInvalidStartupTimeout)
		}
		if readyCalls.Load() != 0 || startCalls.Load() != 0 || deadlineCalls.Load() != 0 {
			t.Fatalf("timeout %s callbacks: ready=%d start=%d deadline=%d", timeout, readyCalls.Load(), startCalls.Load(), deadlineCalls.Load())
		}
	}
}

func TestStartRocketMQClientStopsWatchdogAfterFastError(t *testing.T) {
	want := errors.New("start failed")
	var deadlineCalls atomic.Int32
	err := startRocketMQClient(50*time.Millisecond, func(context.Context) error { return nil }, func() error { return want }, func() { deadlineCalls.Add(1) })
	if !errors.Is(err, want) {
		t.Fatalf("start error = %v, want %v", err, want)
	}
	time.Sleep(100 * time.Millisecond)
	if got := deadlineCalls.Load(); got != 0 {
		t.Fatalf("deadline callback calls = %d, want 0", got)
	}
}

func TestStartRocketMQClientTriggersFailStopWatchdog(t *testing.T) {
	release := make(chan struct{})
	deadline := make(chan struct{})
	done := make(chan error, 1)
	var deadlineCalls atomic.Int32
	go func() {
		done <- startRocketMQClient(10*time.Millisecond, func(context.Context) error { return nil }, func() error {
			<-release
			return nil
		}, func() {
			deadlineCalls.Add(1)
			close(deadline)
		})
	}()
	select {
	case <-deadline:
	case <-time.After(time.Second):
		t.Fatal("start watchdog did not fire")
	}
	select {
	case err := <-done:
		t.Fatalf("helper returned while Start was still blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("start returned error after test watchdog release: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := deadlineCalls.Load(); got != 1 {
		t.Fatalf("deadline callback calls = %d, want 1", got)
	}
}

func TestRocketMQStartDeadlineSubprocess(t *testing.T) {
	if os.Getenv("XLH_TEST_ROCKETMQ_START_DEADLINE") == "1" {
		_ = startRocketMQClient(20*time.Millisecond, func(context.Context) error { return nil }, func() error {
			select {}
		}, rocketMQStartDeadline("consumer"))
		_, _ = os.Stderr.WriteString("helper returned unexpectedly\n")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRocketMQStartDeadlineSubprocess$")
	command.Env = append(os.Environ(), "XLH_TEST_ROCKETMQ_START_DEADLINE=1")
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("subprocess error = %v, output=%s", err, output)
	}
	if ctx.Err() != nil {
		t.Fatalf("subprocess did not fail-stop within deadline: %v", ctx.Err())
	}
	text := string(output)
	for _, marker := range []string{"component=consumer", "outcome=startup_timeout"} {
		if !strings.Contains(text, marker) {
			t.Fatalf("subprocess output missing %q: %s", marker, text)
		}
	}
	if strings.Contains(text, "helper returned unexpectedly") {
		t.Fatalf("fail-stop callback returned: %s", text)
	}
}
