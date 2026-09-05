package rocketmq

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apache/rocketmq-client-go/v2/admin"
	"github.com/apache/rocketmq-client-go/v2/primitive"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
)

func TestRocketMQTransactionCommitAndRollbackIntegration(t *testing.T) {
	nameServers := splitAddresses(os.Getenv("XLH_TEST_ROCKETMQ_NAMESERVERS"))
	brokerAddress := strings.TrimSpace(os.Getenv("XLH_TEST_ROCKETMQ_BROKER_ADDR"))
	if len(nameServers) == 0 || brokerAddress == "" {
		t.Skip("XLH_TEST_ROCKETMQ_NAMESERVERS and XLH_TEST_ROCKETMQ_BROKER_ADDR are not set")
	}

	suffix := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	topic := "XLH_FLASH_SALE_IT_" + suffix
	config := Config{
		NameServers: nameServers, Topic: topic,
		ProducerGroup: "xlh-flash-sale-it-producer-" + suffix,
		ConsumerGroup: "xlh-flash-sale-it-consumer-" + suffix,
		SendTimeout:   5 * time.Second, ConsumeTimeout: 30 * time.Second,
		ConsumerConcurrency: 2, RetryLimit: 2,
	}

	adminClient, err := admin.NewAdmin(admin.WithResolver(primitive.NewPassthroughResolver(nameServers)))
	if err != nil {
		t.Fatalf("create RocketMQ admin: %v", err)
	}
	cleanupTopic := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := adminClient.DeleteTopic(cleanupCtx, admin.WithTopicDelete(topic), admin.WithBrokerAddrDelete(brokerAddress), admin.WithNameSrvAddr(nameServers)); err != nil {
			t.Logf("delete integration topic %s: %v", topic, err)
		}
		if err := adminClient.Close(); err != nil {
			t.Logf("close RocketMQ admin: %v", err)
		}
	}
	t.Cleanup(cleanupTopic)
	createCtx, cancelCreate := context.WithTimeout(context.Background(), 10*time.Second)
	err = adminClient.CreateTopic(createCtx, admin.WithTopicCreate(topic), admin.WithBrokerAddrCreate(brokerAddress), admin.WithReadQueueNums(2), admin.WithWriteQueueNums(2))
	cancelCreate()
	if err != nil {
		t.Fatalf("create integration topic: %v", err)
	}
	routeDeadline := time.Now().Add(30 * time.Second)
	for {
		queues, routeErr := adminClient.FetchPublishMessageQueues(context.Background(), topic)
		if routeErr == nil && len(queues) > 0 {
			break
		}
		if time.Now().After(routeDeadline) {
			t.Fatalf("topic route did not become ready: queues=%d err=%v", len(queues), routeErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	probe, err := NewReadinessProbe(config)
	if err != nil {
		t.Fatalf("create readiness probe: %v", err)
	}
	readinessCtx, cancelReadiness := context.WithTimeout(context.Background(), 10*time.Second)
	err = probe.Ready(readinessCtx)
	cancelReadiness()
	if err != nil {
		t.Fatalf("verify topic-route readiness: %v", err)
	}

	now := time.Now().UTC()
	admission := &liveAdmission{acceptedUserID: 7}
	inspector := &fakeInspector{now: now}
	producer, err := NewProducer(config, admission, inspector)
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.Start(); err != nil {
		t.Fatalf("start integration producer: %v", err)
	}
	t.Cleanup(func() {
		if err := producer.Shutdown(); err != nil {
			t.Logf("shutdown integration producer: %v", err)
		}
	})

	handler := &liveHandler{events: make(chan flashsale.Event, 2)}
	completer := &liveCompleter{events: make(chan flashsale.Event, 2)}
	consumerClient, err := NewConsumer(config, handler, completer)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumerClient.Start(); err != nil {
		t.Fatalf("start integration consumer: %v", err)
	}
	t.Cleanup(func() {
		if err := consumerClient.Shutdown(); err != nil {
			t.Logf("shutdown integration consumer: %v", err)
		}
	})

	// Give the new consumer group one rebalance cycle before publishing.
	time.Sleep(2 * time.Second)
	acceptedCommand := testAdmissionCommand()
	acceptedCommand.UserID = 7
	accepted, err := producer.Reserve(context.Background(), acceptedCommand)
	if err != nil || accepted.Outcome != flashsale.AdmissionAccepted {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}

	select {
	case event := <-completer.events:
		if event.RequestID != acceptedCommand.RequestID || event.UserID != acceptedCommand.UserID {
			t.Fatalf("committed event=%+v", event)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for committed transaction message")
	}
	select {
	case event := <-handler.events:
		if event.RequestID != acceptedCommand.RequestID {
			t.Fatalf("handler committed event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("committed event was completed without reaching handler")
	}

	rolledBackCommand := testAdmissionCommand()
	rolledBackCommand.RequestID = "fsr_15_fedcba9876543210fedcba9876543210"
	rolledBackCommand.IdempotencyDigest = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	rolledBackCommand.UserID = 8
	rolledBack, err := producer.Reserve(context.Background(), rolledBackCommand)
	if err != nil || rolledBack.Outcome != flashsale.AdmissionExhausted {
		t.Fatalf("rolled back=%+v err=%v", rolledBack, err)
	}

	select {
	case event := <-handler.events:
		t.Fatalf("rolled-back event was consumed: %+v", event)
	case <-time.After(5 * time.Second):
	}
}

type liveAdmission struct {
	acceptedUserID int64
}

func (a *liveAdmission) Reserve(_ context.Context, command flashsale.AdmissionCommand) (flashsale.AdmissionResult, error) {
	if command.UserID == a.acceptedUserID {
		return flashsale.AdmissionResult{Outcome: flashsale.AdmissionAccepted, RequestID: command.RequestID, ReservedAt: command.ReservedAt}, nil
	}
	return flashsale.AdmissionResult{Outcome: flashsale.AdmissionExhausted}, nil
}

type liveHandler struct {
	events chan flashsale.Event
}

func (h *liveHandler) Fulfil(ctx context.Context, event flashsale.Event) error {
	select {
	case h.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type liveCompleter struct {
	events chan flashsale.Event
}

func (c *liveCompleter) CompletePending(ctx context.Context, event flashsale.Event) error {
	select {
	case c.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func splitAddresses(value string) []string {
	parts := strings.Split(value, ",")
	addresses := make([]string, 0, len(parts))
	for _, part := range parts {
		if address := strings.TrimSpace(part); address != "" {
			addresses = append(addresses, address)
		}
	}
	return addresses
}
