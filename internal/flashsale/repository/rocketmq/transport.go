package rocketmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	rmq "github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/admin"
	"github.com/apache/rocketmq-client-go/v2/consumer"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"

	flashsale "github.com/red060324/XiaoLanHe/internal/flashsale/usecase"
	platformmetrics "github.com/red060324/XiaoLanHe/internal/platform/metrics"
)

const reservationTagV1 = "RESERVED_V1"

var rocketNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,126}$`)

type Config struct {
	NameServers         []string
	AccessKey           string
	SecretKey           string
	Topic               string
	ProducerGroup       string
	ConsumerGroup       string
	SendTimeout         time.Duration
	ConsumeTimeout      time.Duration
	ConsumerConcurrency int
	RetryLimit          int32
}

type Producer struct {
	client    rmq.TransactionProducer
	recovery  syncProducer
	topic     string
	inspector flashsale.AdmissionInspector
	listener  *transactionListener
	timeout   time.Duration
}

type syncProducer interface {
	Start() error
	Shutdown() error
	SendSync(context.Context, ...*primitive.Message) (*primitive.SendResult, error)
}

func NewProducer(config Config, admission flashsale.Admission, inspector flashsale.AdmissionInspector) (*Producer, error) {
	if err := config.validateProducer(); err != nil || admission == nil || inspector == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("RocketMQ admission dependencies are required")
	}
	listener := &transactionListener{admission: admission, inspector: inspector, timeout: config.SendTimeout}
	options := []producer.Option{
		producer.WithGroupName(config.ProducerGroup),
		producer.WithNsResolver(primitive.NewPassthroughResolver(config.NameServers)),
		producer.WithSendMsgTimeout(config.SendTimeout), producer.WithRetry(1),
	}
	if config.AccessKey != "" {
		options = append(options, producer.WithCredentials(primitive.Credentials{AccessKey: config.AccessKey, SecretKey: config.SecretKey}))
	}
	client, err := rmq.NewTransactionProducer(listener, options...)
	if err != nil {
		return nil, err
	}
	recoveryOptions := []producer.Option{
		producer.WithGroupName(config.ProducerGroup + "-recovery"),
		producer.WithNsResolver(primitive.NewPassthroughResolver(config.NameServers)),
		producer.WithSendMsgTimeout(config.SendTimeout), producer.WithRetry(1),
	}
	if config.AccessKey != "" {
		recoveryOptions = append(recoveryOptions, producer.WithCredentials(primitive.Credentials{AccessKey: config.AccessKey, SecretKey: config.SecretKey}))
	}
	recovery, err := rmq.NewProducer(recoveryOptions...)
	if err != nil {
		return nil, err
	}
	return &Producer{client: client, recovery: recovery, topic: config.Topic, inspector: inspector, listener: listener, timeout: config.SendTimeout}, nil
}

func (p *Producer) Start() error {
	if err := p.client.Start(); err != nil {
		return err
	}
	if err := p.recovery.Start(); err != nil {
		_ = p.client.Shutdown()
		return err
	}
	return nil
}

func (p *Producer) Shutdown() error {
	recoveryErr := p.recovery.Shutdown()
	transactionErr := p.client.Shutdown()
	if recoveryErr != nil {
		return recoveryErr
	}
	return transactionErr
}

func (p *Producer) Reserve(ctx context.Context, command flashsale.AdmissionCommand) (flashsale.AdmissionResult, error) {
	started := time.Now()
	transactionOutcome := "unknown"
	defer func() {
		platformmetrics.Default().ObserveFlashSale("transaction", transactionOutcome, time.Since(started), 1, 0)
	}()
	serverCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	reservedAt, err := p.inspector.ServerTime(serverCtx)
	if err != nil {
		slog.WarnContext(ctx, "flash sale transaction unavailable", "request_id", command.RequestID, "activity_id", command.ActivityID, "transaction_state", "unknown", "outcome", "dependency_unavailable")
		return flashsale.AdmissionResult{}, flashsale.ErrUnavailable
	}
	command.ReservedAt = reservedAt
	event := flashsale.Event{
		Version: 1, RequestID: command.RequestID, ActivityID: command.ActivityID, ActivityVersion: command.ActivityVersion,
		UserID: command.UserID, ReservedAt: reservedAt, IdempotencyDigest: command.IdempotencyDigest,
	}
	payload, err := encodeEvent(event)
	if err != nil {
		transactionOutcome = "invalid"
		return flashsale.AdmissionResult{}, err
	}
	message := primitive.NewMessage(p.topic, payload).WithTag(reservationTagV1).WithKeys([]string{command.RequestID})
	message.WithProperty(primitive.PropertyUniqueClientMessageIdKeyIndex, primitive.CreateUniqID())
	result, sendErr := p.client.SendMessageInTransaction(ctx, message)
	localResult, hasLocalResult := p.listener.takeResult(transactionResultKey(message))
	if sendErr != nil || result == nil || result.SendResult == nil || result.Status != primitive.SendOK {
		slog.WarnContext(ctx, "flash sale transaction unavailable", "request_id", command.RequestID, "activity_id", command.ActivityID, "transaction_state", "unknown", "outcome", "dependency_unavailable")
		return flashsale.AdmissionResult{}, flashsale.ErrUnavailable
	}
	transactionOutcome = transactionStateName(result.State)
	switch result.State {
	case primitive.CommitMessageState, primitive.RollbackMessageState:
		if hasLocalResult {
			slog.InfoContext(ctx, "flash sale transaction resolved", "request_id", command.RequestID, "activity_id", command.ActivityID, "transaction_state", transactionStateName(result.State), "outcome", localResult.Outcome)
			return localResult, nil
		}
	}
	slog.WarnContext(ctx, "flash sale transaction unavailable", "request_id", command.RequestID, "activity_id", command.ActivityID, "transaction_state", transactionStateName(result.State), "outcome", "result_unavailable")
	return flashsale.AdmissionResult{}, flashsale.ErrUnavailable
}

func transactionStateName(state primitive.LocalTransactionState) string {
	switch state {
	case primitive.CommitMessageState:
		return "commit"
	case primitive.RollbackMessageState:
		return "rollback"
	default:
		return "unknown"
	}
}

func (p *Producer) Publish(ctx context.Context, event flashsale.Event) error {
	payload, err := encodeEvent(event)
	if err != nil {
		return err
	}
	message := primitive.NewMessage(p.topic, payload).WithTag(reservationTagV1).WithKeys([]string{event.RequestID})
	result, err := p.recovery.SendSync(ctx, message)
	if err != nil || result == nil || result.Status != primitive.SendOK {
		return flashsale.ErrUnavailable
	}
	return nil
}

type Handler interface {
	Fulfil(context.Context, flashsale.Event) error
}

type Consumer struct {
	client rmq.PushConsumer
}

type topicRouteAdmin interface {
	FetchPublishMessageQueues(context.Context, string) ([]*primitive.MessageQueue, error)
	Close() error
}

type routeAdminFactory func(Config) (topicRouteAdmin, error)

// ReadinessProbe verifies that the configured topic currently has a writable
// route. A one-slot gate prevents public readiness polling from creating an
// unbounded number of RocketMQ admin clients while an upstream call is slow.
type ReadinessProbe struct {
	config   Config
	factory  routeAdminFactory
	inflight chan struct{}
}

func NewConsumer(config Config, handler Handler, completer flashsale.PendingCompleter) (*Consumer, error) {
	if err := config.validateConsumer(); err != nil || handler == nil || completer == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("RocketMQ handler is required")
	}
	options := []consumer.Option{
		consumer.WithGroupName(config.ConsumerGroup),
		consumer.WithNsResolver(primitive.NewPassthroughResolver(config.NameServers)),
		consumer.WithConsumerModel(consumer.Clustering), consumer.WithConsumeMessageBatchMaxSize(1),
		consumer.WithConsumeGoroutineNums(config.ConsumerConcurrency), consumer.WithMaxReconsumeTimes(config.RetryLimit),
		consumer.WithConsumeTimeout(config.ConsumeTimeout),
	}
	if config.AccessKey != "" {
		options = append(options, consumer.WithCredentials(primitive.Credentials{AccessKey: config.AccessKey, SecretKey: config.SecretKey}))
	}
	client, err := rmq.NewPushConsumer(options...)
	if err != nil {
		return nil, err
	}
	if err := client.Subscribe(config.Topic, consumer.MessageSelector{Type: consumer.TAG, Expression: reservationTagV1}, consumeCallback(handler, completer, config.RetryLimit)); err != nil {
		return nil, err
	}
	return &Consumer{client: client}, nil
}

func consumeCallback(handler Handler, completer flashsale.PendingCompleter, retryLimit int32) func(context.Context, ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
	return func(ctx context.Context, messages ...*primitive.MessageExt) (consumer.ConsumeResult, error) {
		for _, message := range messages {
			started := time.Now()
			event, err := decodeEvent(message.Body)
			if err != nil {
				platformmetrics.Default().ObserveFlashSale("consume", retryOutcome(message.ReconsumeTimes, retryLimit), time.Since(started), 1, 0)
				slog.WarnContext(ctx, "flash sale message rejected", "outcome", "invalid_event", "reconsume_times", message.ReconsumeTimes)
				return consumer.ConsumeRetryLater, nil
			}
			if err := handler.Fulfil(ctx, event); err != nil {
				platformmetrics.Default().ObserveFlashSale("consume", retryOutcome(message.ReconsumeTimes, retryLimit), time.Since(started), 1, pendingAge(event.ReservedAt))
				slog.WarnContext(ctx, "flash sale consume retry", "request_id", event.RequestID, "activity_id", event.ActivityID, "reconsume_times", message.ReconsumeTimes)
				return consumer.ConsumeRetryLater, nil
			}
			if err := completer.CompletePending(ctx, event); err != nil {
				platformmetrics.Default().ObserveFlashSale("consume", retryOutcome(message.ReconsumeTimes, retryLimit), time.Since(started), 1, pendingAge(event.ReservedAt))
				slog.WarnContext(ctx, "flash sale pending completion retry", "request_id", event.RequestID, "activity_id", event.ActivityID, "reconsume_times", message.ReconsumeTimes)
				return consumer.ConsumeRetryLater, nil
			}
			platformmetrics.Default().ObserveFlashSale("consume", "success", time.Since(started), 1, pendingAge(event.ReservedAt))
			slog.InfoContext(ctx, "flash sale message consumed", "request_id", event.RequestID, "activity_id", event.ActivityID, "outcome", "success")
		}
		return consumer.ConsumeSuccess, nil
	}
}

func retryOutcome(reconsumeTimes, retryLimit int32) string {
	if retryLimit >= 0 && reconsumeTimes >= retryLimit {
		return "retry_exhausted"
	}
	return "retry"
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

func (c *Consumer) Start() error    { return c.client.Start() }
func (c *Consumer) Shutdown() error { return c.client.Shutdown() }

func NewReadinessProbe(config Config) (*ReadinessProbe, error) {
	if err := config.validateProducer(); err != nil {
		return nil, err
	}
	if err := config.validateConsumer(); err != nil {
		return nil, err
	}
	return newReadinessProbe(config, newRouteAdmin), nil
}

func newReadinessProbe(config Config, factory routeAdminFactory) *ReadinessProbe {
	return &ReadinessProbe{config: config, factory: factory, inflight: make(chan struct{}, 1)}
}

func newRouteAdmin(config Config) (topicRouteAdmin, error) {
	options := []admin.AdminOption{admin.WithResolver(primitive.NewPassthroughResolver(config.NameServers))}
	if config.AccessKey != "" {
		options = append(options, admin.WithCredentials(primitive.Credentials{AccessKey: config.AccessKey, SecretKey: config.SecretKey}))
	}
	return admin.NewAdmin(options...)
}

func (p *ReadinessProbe) Ready(ctx context.Context) error {
	if p == nil || p.factory == nil {
		return errors.New("RocketMQ readiness probe is not configured")
	}
	select {
	case p.inflight <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		defer func() { <-p.inflight }()
		client, err := p.factory(p.config)
		if err != nil {
			done <- err
			return
		}
		var probeErr error
		queues, err := client.FetchPublishMessageQueues(ctx, p.config.Topic)
		if err != nil {
			probeErr = fmt.Errorf("fetch RocketMQ topic route: %w", err)
		} else if len(queues) == 0 {
			probeErr = errors.New("RocketMQ topic has no writable queues")
		}
		if err := client.Close(); err != nil {
			probeErr = errors.Join(probeErr, fmt.Errorf("close RocketMQ admin client: %w", err))
		}
		done <- probeErr
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c Config) validateProducer() error {
	if err := c.validateCommon(); err != nil {
		return err
	}
	if !rocketNamePattern.MatchString(c.ProducerGroup) || len(c.ProducerGroup)+len("-recovery") > 255 || c.SendTimeout <= 0 {
		return errors.New("invalid RocketMQ producer configuration")
	}
	return nil
}

func (c Config) validateConsumer() error {
	if err := c.validateCommon(); err != nil {
		return err
	}
	if !rocketNamePattern.MatchString(c.ConsumerGroup) || c.ConsumeTimeout <= 0 || c.ConsumerConcurrency < 1 || c.ConsumerConcurrency > 128 || c.RetryLimit < 0 {
		return errors.New("invalid RocketMQ consumer configuration")
	}
	return nil
}

func (c Config) validateCommon() error {
	if len(c.NameServers) == 0 || !rocketNamePattern.MatchString(c.Topic) || (c.AccessKey == "") != (c.SecretKey == "") {
		return errors.New("invalid RocketMQ configuration")
	}
	if c.AccessKey != "" && (!rocketNamePattern.MatchString(c.AccessKey) || len(c.SecretKey) > 512 || strings.ContainsAny(c.SecretKey, "\r\n")) {
		return errors.New("invalid RocketMQ credentials")
	}
	for _, address := range c.NameServers {
		host, port, err := net.SplitHostPort(address)
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n") || portErr != nil || portNumber < 1 || portNumber > 65535 {
			return errors.New("invalid RocketMQ name server")
		}
	}
	return nil
}

var _ flashsale.Admission = (*Producer)(nil)
var _ flashsale.EventPublisher = (*Producer)(nil)
