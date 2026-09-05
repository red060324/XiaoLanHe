package rocketmq

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestTransactionAdmissionCommitsAndReturnsLuaResult(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 123000000, time.UTC)
	admission := &fakeAdmission{result: flashsale.AdmissionResult{Outcome: flashsale.AdmissionAccepted, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ReservedAt: now}}
	inspector := &fakeInspector{now: now}
	listener := &transactionListener{admission: admission, inspector: inspector, timeout: time.Second}
	client := &fakeTransactionProducer{listener: listener}
	producer := &Producer{client: client, recovery: &fakeSyncProducer{}, topic: "XLH_FLASH_SALE_V1", inspector: inspector, listener: listener, timeout: time.Second}

	result, err := producer.Reserve(context.Background(), testAdmissionCommand())
	if err != nil || result.Outcome != flashsale.AdmissionAccepted || result.RequestID != admission.result.RequestID {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if client.message == nil || client.message.Topic != "XLH_FLASH_SALE_V1" || client.message.GetTags() != reservationTagV1 || admission.last.ReservedAt != now {
		t.Fatalf("message=%+v command=%+v", client.message, admission.last)
	}
}

func TestTransactionAdmissionReturnsBusinessRollback(t *testing.T) {
	now := time.Now().UTC()
	admission := &fakeAdmission{result: flashsale.AdmissionResult{Outcome: flashsale.AdmissionExhausted}}
	inspector := &fakeInspector{now: now}
	listener := &transactionListener{admission: admission, inspector: inspector, timeout: time.Second}
	producer := &Producer{client: &fakeTransactionProducer{listener: listener}, recovery: &fakeSyncProducer{}, topic: "topic", inspector: inspector, listener: listener, timeout: time.Second}
	result, err := producer.Reserve(context.Background(), testAdmissionCommand())
	if err != nil || result.Outcome != flashsale.AdmissionExhausted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTransactionAdmissionRollsBackReplayHalfMessage(t *testing.T) {
	now := time.Now().UTC()
	admission := &fakeAdmission{result: flashsale.AdmissionResult{
		Outcome: flashsale.AdmissionReplay, RequestID: testAdmissionCommand().RequestID, ReservedAt: now.Add(-time.Second),
	}}
	inspector := &fakeInspector{now: now}
	listener := &transactionListener{admission: admission, inspector: inspector, timeout: time.Second}
	client := &fakeTransactionProducer{listener: listener}
	producer := &Producer{client: client, recovery: &fakeSyncProducer{}, topic: "topic", inspector: inspector, listener: listener, timeout: time.Second}
	result, err := producer.Reserve(context.Background(), testAdmissionCommand())
	if err != nil || result.Outcome != flashsale.AdmissionReplay || client.state != primitive.RollbackMessageState {
		t.Fatalf("result=%+v state=%v err=%v", result, client.state, err)
	}
}

func TestTransactionAdmissionCorrelatesConcurrentSameRequestResults(t *testing.T) {
	now := time.Now().UTC()
	admission := &sequencedAdmission{results: []flashsale.AdmissionResult{
		{Outcome: flashsale.AdmissionAccepted, RequestID: testAdmissionCommand().RequestID, ReservedAt: now},
		{Outcome: flashsale.AdmissionReplay, RequestID: testAdmissionCommand().RequestID, ReservedAt: now},
	}}
	inspector := &fakeInspector{now: now}
	listener := &transactionListener{admission: admission, inspector: inspector, timeout: time.Second}
	client := &concurrentTransactionProducer{listener: listener, ready: make(chan struct{}, 2), release: make(chan struct{})}
	producer := &Producer{client: client, recovery: &fakeSyncProducer{}, topic: "topic", inspector: inspector, listener: listener, timeout: time.Second}

	type reservationResult struct {
		result flashsale.AdmissionResult
		err    error
	}
	results := make(chan reservationResult, 2)
	for index := 0; index < 2; index++ {
		go func() {
			result, err := producer.Reserve(context.Background(), testAdmissionCommand())
			results <- reservationResult{result: result, err: err}
		}()
	}
	<-client.ready
	<-client.ready
	close(client.release)

	outcomes := map[flashsale.AdmissionOutcome]int{}
	for index := 0; index < 2; index++ {
		reservation := <-results
		if reservation.err != nil {
			t.Fatalf("reservation %d: %v", index, reservation.err)
		}
		outcomes[reservation.result.Outcome]++
	}
	if outcomes[flashsale.AdmissionAccepted] != 1 || outcomes[flashsale.AdmissionReplay] != 1 {
		t.Fatalf("outcomes=%v", outcomes)
	}
}

func TestTransactionAdmissionFailsClosedOnUncertainResult(t *testing.T) {
	now := time.Now().UTC()
	admission := &fakeAdmission{err: errors.New("redis timeout")}
	inspector := &fakeInspector{now: now}
	listener := &transactionListener{admission: admission, inspector: inspector, timeout: time.Second}
	producer := &Producer{client: &fakeTransactionProducer{listener: listener}, recovery: &fakeSyncProducer{}, topic: "topic", inspector: inspector, listener: listener, timeout: time.Second}
	if _, err := producer.Reserve(context.Background(), testAdmissionCommand()); !errors.Is(err, flashsale.ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestTransactionCheckerMapsPresentAbsentAndUncertain(t *testing.T) {
	event := testEvent(time.Now().UTC())
	payload, err := encodeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	message := &primitive.MessageExt{Message: *primitive.NewMessage("topic", payload)}
	inspector := &fakeInspector{record: flashsale.AdmissionRecord{
		RequestID: event.RequestID, ActivityID: event.ActivityID, UserID: event.UserID,
		IdempotencyDigest: event.IdempotencyDigest, Status: "queued", ReservedAt: event.ReservedAt,
	}, found: true}
	listener := &transactionListener{admission: &fakeAdmission{}, inspector: inspector, timeout: time.Second}
	if state := listener.CheckLocalTransaction(message); state != primitive.CommitMessageState {
		t.Fatalf("present state=%v", state)
	}
	inspector.record.ReservedAt = event.ReservedAt.Add(time.Millisecond)
	if state := listener.CheckLocalTransaction(message); state != primitive.RollbackMessageState {
		t.Fatalf("mismatched time state=%v", state)
	}
	inspector.record.ReservedAt = event.ReservedAt
	inspector.found = false
	if state := listener.CheckLocalTransaction(message); state != primitive.RollbackMessageState {
		t.Fatalf("absent state=%v", state)
	}
	inspector.err = errors.New("redis down")
	if state := listener.CheckLocalTransaction(message); state != primitive.UnknowState {
		t.Fatalf("uncertain state=%v", state)
	}
}

func TestCodecRejectsUnknownAndOversizedPayload(t *testing.T) {
	if _, err := decodeEvent([]byte(`{"version":1,"unknown":true}`)); !errors.Is(err, flashsale.ErrUnsupportedEvent) {
		t.Fatalf("unknown field err=%v", err)
	}
	if _, err := decodeEvent(make([]byte, maxEventBytes+1)); !errors.Is(err, flashsale.ErrUnsupportedEvent) {
		t.Fatalf("oversized err=%v", err)
	}
}

func TestRecoveryPublishUsesOriginalValidatedEvent(t *testing.T) {
	event := testEvent(time.Now().UTC())
	recovery := &fakeSyncProducer{}
	producer := &Producer{recovery: recovery, topic: "XLH_FLASH_SALE_V1"}
	if err := producer.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if recovery.message == nil || recovery.message.Topic != "XLH_FLASH_SALE_V1" || recovery.message.GetTags() != reservationTagV1 {
		t.Fatalf("message=%+v", recovery.message)
	}
	decoded, err := decodeEvent(recovery.message.Body)
	if err != nil || decoded != event {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

func TestProducerStartCleansUpTransactionProducerWhenRecoveryStartFails(t *testing.T) {
	transaction := &fakeTransactionProducer{}
	recovery := &fakeSyncProducer{startErr: errors.New("recovery start failed")}
	producer := &Producer{client: transaction, recovery: recovery}

	if err := producer.Start(); err == nil {
		t.Fatal("expected recovery start error")
	}
	if transaction.startCalls != 1 || transaction.shutdownCalls != 1 || recovery.startCalls != 1 {
		t.Fatalf("transaction start=%d shutdown=%d recovery start=%d", transaction.startCalls, transaction.shutdownCalls, recovery.startCalls)
	}
}

func TestProducerShutdownAttemptsBothClients(t *testing.T) {
	transaction := &fakeTransactionProducer{shutdownErr: errors.New("transaction shutdown failed")}
	recovery := &fakeSyncProducer{shutdownErr: errors.New("recovery shutdown failed")}
	producer := &Producer{client: transaction, recovery: recovery}

	if err := producer.Shutdown(); !errors.Is(err, recovery.shutdownErr) {
		t.Fatalf("err=%v", err)
	}
	if transaction.shutdownCalls != 1 || recovery.shutdownCalls != 1 {
		t.Fatalf("transaction shutdown=%d recovery shutdown=%d", transaction.shutdownCalls, recovery.shutdownCalls)
	}
}

func TestReadinessProbeRequiresWritableTopicRoute(t *testing.T) {
	config := testConfig()
	t.Run("ready", func(t *testing.T) {
		admin := &fakeTopicRouteAdmin{queues: []*primitive.MessageQueue{{Topic: config.Topic, BrokerName: "broker-a"}}}
		probe := newReadinessProbe(config, func(received Config) (topicRouteAdmin, error) {
			if received.Topic != config.Topic || received.AccessKey != config.AccessKey {
				t.Fatalf("config=%+v", received)
			}
			return admin, nil
		})
		if err := probe.Ready(context.Background()); err != nil || admin.topic != config.Topic || admin.closeCalls != 1 {
			t.Fatalf("topic=%q closes=%d err=%v", admin.topic, admin.closeCalls, err)
		}
	})
	t.Run("missing route", func(t *testing.T) {
		probe := newReadinessProbe(config, func(Config) (topicRouteAdmin, error) { return &fakeTopicRouteAdmin{}, nil })
		if err := probe.Ready(context.Background()); err == nil {
			t.Fatal("expected empty topic route to fail")
		}
	})
	t.Run("admin failure", func(t *testing.T) {
		want := errors.New("admin unavailable")
		probe := newReadinessProbe(config, func(Config) (topicRouteAdmin, error) { return nil, want })
		if err := probe.Ready(context.Background()); !errors.Is(err, want) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("close failure", func(t *testing.T) {
		want := errors.New("close failed")
		admin := &fakeTopicRouteAdmin{queues: []*primitive.MessageQueue{{Topic: config.Topic}}, closeErr: want}
		probe := newReadinessProbe(config, func(Config) (topicRouteAdmin, error) { return admin, nil })
		if err := probe.Ready(context.Background()); !errors.Is(err, want) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("bounded concurrent probes", func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{}, 1)
		admin := &fakeTopicRouteAdmin{queues: []*primitive.MessageQueue{{Topic: config.Topic}}, started: started, release: release}
		probe := newReadinessProbe(config, func(Config) (topicRouteAdmin, error) { return admin, nil })
		first := make(chan error, 1)
		go func() { first <- probe.Ready(context.Background()) }()
		<-started
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if err := probe.Ready(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second probe err=%v", err)
		}
		close(release)
		if err := <-first; err != nil {
			t.Fatalf("first probe err=%v", err)
		}
	})
}

func TestRocketMQConfigRejectsUnsafeNames(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"topic":            func(config *Config) { config.Topic = "bad topic" },
		"producer group":   func(config *Config) { config.ProducerGroup = strings.Repeat("p", 247) },
		"consumer group":   func(config *Config) { config.ConsumerGroup = "bad.group" },
		"name server":      func(config *Config) { config.NameServers = []string{"rmq:9876\nother"} },
		"name server port": func(config *Config) { config.NameServers = []string{"rmq:70000"} },
		"access key":       func(config *Config) { config.AccessKey = "bad.access" },
		"secret key":       func(config *Config) { config.SecretKey = "secret\nvalue" },
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig()
			mutate(&config)
			if _, err := NewReadinessProbe(config); err == nil {
				t.Fatal("expected invalid configuration")
			}
		})
	}
}

func TestConsumerRetriesPoisonTransientAndPendingCompletionFailures(t *testing.T) {
	event := testEvent(time.Now().UTC())
	payload, err := encodeEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	message := &primitive.MessageExt{Message: *primitive.NewMessage("topic", payload)}
	handler := &fakeHandler{}
	completer := &fakePendingCompleter{}
	callback := consumeCallback(handler, completer, 2)

	if result, _ := callback(context.Background(), &primitive.MessageExt{Message: *primitive.NewMessage("topic", []byte("invalid"))}); result != consumer.ConsumeRetryLater {
		t.Fatalf("poison result=%v", result)
	}
	handler.err = errors.New("postgres unavailable")
	if result, _ := callback(context.Background(), message); result != consumer.ConsumeRetryLater || completer.calls != 0 {
		t.Fatalf("handler result=%v completion calls=%d", result, completer.calls)
	}
	handler.err = nil
	completer.err = errors.New("redis unavailable")
	if result, _ := callback(context.Background(), message); result != consumer.ConsumeRetryLater || completer.calls != 1 {
		t.Fatalf("completion result=%v calls=%d", result, completer.calls)
	}
	completer.err = nil
	if result, _ := callback(context.Background(), message); result != consumer.ConsumeSuccess || handler.calls != 3 || completer.calls != 2 {
		t.Fatalf("success result=%v handler=%d completion=%d", result, handler.calls, completer.calls)
	}
	message.ReconsumeTimes = 2
	handler.err = errors.New("still unavailable")
	if result, _ := callback(context.Background(), message); result != consumer.ConsumeRetryLater {
		t.Fatalf("exhausted retry result=%v", result)
	}
}

func TestTransactionLogsExposeStateWithoutProviderErrors(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	secret := "redis://:super-secret@private-redis:6379/0"
	inspector := &fakeInspector{err: errors.New(secret)}
	listener := &transactionListener{admission: &fakeAdmission{}, inspector: inspector, timeout: time.Second}
	payload, err := encodeEvent(testEvent(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	message := &primitive.MessageExt{Message: *primitive.NewMessage("topic", payload)}
	if state := listener.CheckLocalTransaction(message); state != primitive.UnknowState {
		t.Fatalf("state=%v", state)
	}
	logs := output.String()
	if strings.Contains(logs, secret) || !strings.Contains(logs, "transaction_state=unknown") || !strings.Contains(logs, "outcome=dependency_unavailable") {
		t.Fatalf("logs=%q", logs)
	}
}

type fakeAdmission struct {
	result flashsale.AdmissionResult
	err    error
	last   flashsale.AdmissionCommand
}

type sequencedAdmission struct {
	results []flashsale.AdmissionResult
	mu      sync.Mutex
}

func (a *sequencedAdmission) Reserve(context.Context, flashsale.AdmissionCommand) (flashsale.AdmissionResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := a.results[0]
	a.results = a.results[1:]
	return result, nil
}

type concurrentTransactionProducer struct {
	listener *transactionListener
	ready    chan struct{}
	release  chan struct{}
}

func (*concurrentTransactionProducer) Start() error    { return nil }
func (*concurrentTransactionProducer) Shutdown() error { return nil }
func (p *concurrentTransactionProducer) SendMessageInTransaction(_ context.Context, message *primitive.Message) (*primitive.TransactionSendResult, error) {
	state := p.listener.ExecuteLocalTransaction(message)
	p.ready <- struct{}{}
	<-p.release
	return &primitive.TransactionSendResult{SendResult: &primitive.SendResult{Status: primitive.SendOK}, State: state}, nil
}

func (f *fakeAdmission) Reserve(_ context.Context, command flashsale.AdmissionCommand) (flashsale.AdmissionResult, error) {
	f.last = command
	return f.result, f.err
}

type fakeInspector struct {
	now    time.Time
	record flashsale.AdmissionRecord
	found  bool
	err    error
}

func (f *fakeInspector) ServerTime(context.Context) (time.Time, error) { return f.now, f.err }
func (f *fakeInspector) Lookup(context.Context, string, int64) (flashsale.AdmissionRecord, bool, error) {
	return f.record, f.found, f.err
}

type fakeTransactionProducer struct {
	listener      *transactionListener
	message       *primitive.Message
	state         primitive.LocalTransactionState
	err           error
	startErr      error
	shutdownErr   error
	startCalls    int
	shutdownCalls int
}

type fakeSyncProducer struct {
	message       *primitive.Message
	err           error
	startErr      error
	shutdownErr   error
	startCalls    int
	shutdownCalls int
}

type fakeHandler struct {
	calls int
	err   error
}

func (f *fakeHandler) Fulfil(context.Context, flashsale.Event) error { f.calls++; return f.err }

type fakePendingCompleter struct {
	calls int
	err   error
}

type fakeTopicRouteAdmin struct {
	queues     []*primitive.MessageQueue
	err        error
	closeErr   error
	topic      string
	closeCalls int
	started    chan struct{}
	release    chan struct{}
}

func (f *fakeTopicRouteAdmin) FetchPublishMessageQueues(ctx context.Context, topic string) ([]*primitive.MessageQueue, error) {
	f.topic = topic
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.queues, f.err
}

func (f *fakeTopicRouteAdmin) Close() error {
	f.closeCalls++
	return f.closeErr
}

func (f *fakePendingCompleter) CompletePending(context.Context, flashsale.Event) error {
	f.calls++
	return f.err
}

func (f *fakeSyncProducer) Start() error {
	f.startCalls++
	return f.startErr
}
func (f *fakeSyncProducer) Shutdown() error {
	f.shutdownCalls++
	return f.shutdownErr
}
func (f *fakeSyncProducer) SendSync(_ context.Context, messages ...*primitive.Message) (*primitive.SendResult, error) {
	if len(messages) > 0 {
		f.message = messages[0]
	}
	if f.err != nil {
		return nil, f.err
	}
	return &primitive.SendResult{Status: primitive.SendOK}, nil
}

func (f *fakeTransactionProducer) Start() error {
	f.startCalls++
	return f.startErr
}
func (f *fakeTransactionProducer) Shutdown() error {
	f.shutdownCalls++
	return f.shutdownErr
}
func (f *fakeTransactionProducer) SendMessageInTransaction(_ context.Context, message *primitive.Message) (*primitive.TransactionSendResult, error) {
	f.message = message
	if f.err != nil {
		return nil, f.err
	}
	f.state = f.listener.ExecuteLocalTransaction(message)
	return &primitive.TransactionSendResult{SendResult: &primitive.SendResult{Status: primitive.SendOK}, State: f.state}, nil
}

func testAdmissionCommand() flashsale.AdmissionCommand {
	return flashsale.AdmissionCommand{RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, ActivityVersion: 1, UserID: 7, IdempotencyDigest: testDigest}
}

func testEvent(reservedAt time.Time) flashsale.Event {
	return flashsale.Event{Version: 1, RequestID: "fsr_15_0123456789abcdef0123456789abcdef", ActivityID: 41, ActivityVersion: 1, UserID: 7, ReservedAt: reservedAt, IdempotencyDigest: testDigest}
}

func testConfig() Config {
	return Config{
		NameServers: []string{"rmq:9876"}, AccessKey: "access-key", SecretKey: "secret-key",
		Topic: "XLH_FLASH_SALE_V1", ProducerGroup: "xlh-flash-sale-producer-v1", ConsumerGroup: "xlh-flash-sale-consumer-v1",
		SendTimeout: time.Second, ConsumeTimeout: time.Second, ConsumerConcurrency: 1, RetryLimit: 1,
	}
}
