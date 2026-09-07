package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"
)

const (
	databaseStartupTimeout       = 30 * time.Second
	componentConstructionTimeout = 10 * time.Second
	dependencyStartupTimeout     = 10 * time.Second
	migrationDDLTimeout          = 2 * time.Minute
)

var errInvalidStartupTimeout = errors.New("startup phase timeout must be positive")

// runStartupPhase gives each startup dependency an independent deadline and
// releases its timer as soon as that phase completes. Runtime components must
// use request or lifecycle contexts instead of retaining this context.
func runStartupPhase(timeout time.Duration, run func(context.Context) error) error {
	if timeout <= 0 {
		return errInvalidStartupTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return run(ctx)
}

func migrationStartupTimeout(lockTimeout time.Duration) time.Duration {
	return lockTimeout + migrationDDLTimeout
}

// startRocketMQClient proves the configured topic route through a bounded-return
// preflight before invoking the pinned SDK's synchronous Start method. It is
// intentionally not implemented by racing Start against a timer: Start has no
// context or abort contract, so doing that would leak a live client goroutine.
//
// In rocketmq-client-go v2.1.2 producer Start performs no synchronous network
// operation. Consumer Start uses fixed six-second route requests per subscribed
// topic/name server and fixed three-second heartbeats per discovered broker
// address, but exposes no total Start deadline or cancellation hook. The SDK
// call therefore remains on the startup goroutine while a hard-deadline
// watchdog fail-stops the process. In production deadlineExceeded must not
// return; terminating the process is the only way to guarantee that an
// uncooperative Start cannot survive as an orphan.
func startRocketMQClient(timeout time.Duration, ready func(context.Context) error, start func() error, deadlineExceeded func()) error {
	if ready == nil || start == nil || deadlineExceeded == nil {
		return errors.New("RocketMQ startup callbacks are required")
	}
	if err := runStartupPhase(timeout, ready); err != nil {
		return err
	}
	watchdog := time.AfterFunc(timeout, deadlineExceeded)
	defer watchdog.Stop()
	return start()
}

func rocketMQStartDeadline(component string) func() {
	return func() {
		slog.Error("RocketMQ client start exceeded hard deadline", "component", component, "outcome", "startup_timeout")
		os.Exit(1)
	}
}
