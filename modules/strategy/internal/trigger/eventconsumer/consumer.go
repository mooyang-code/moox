// Package eventconsumer 是 ViewDataReady 的 JetStream 传输适配：持有 durable 消费者，解码投递，
// 并把领域处理结果映射为 ACK / RETRY / TERM。投递次数不设上限，终态只由处理器落库后决定。
package eventconsumer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

// 重投退避：第 n 次投递失败后等待 baseRetryDelay × 2^(n−1)，最长 maxRetryDelay。
// 默认 5 次尝试预算下，第 5 次读取发生在首次失败约 30 秒之后，足以跨过依赖的短暂重启。
const (
	baseRetryDelay = 2 * time.Second
	maxRetryDelay  = 30 * time.Second
)

// retryDelay 按投递次数计算退避时长。
func retryDelay(deliveryCount uint64) time.Duration {
	delay := baseRetryDelay
	for n := uint64(1); n < deliveryCount && delay < maxRetryDelay; n++ {
		delay *= 2
	}
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

// ConsumerConfig 是消费者配置。
type ConsumerConfig struct {
	Client        *jetstream.Client
	ConsumerName  string
	AckWait       time.Duration
	MaxAckPending int
	FetchMaxWait  time.Duration
	BatchSize     int
}

// Consumer 持有 durable 消费者与运行循环。
type Consumer struct {
	cfg      ConsumerConfig
	handler  *trigger.Handler
	runner   *jetstream.Runner
	consumer *jetstream.Consumer
	cancel   context.CancelFunc
	ready    bool
	mu       sync.Mutex
}

// New 构造消费者。
func New(cfg ConsumerConfig, handler *trigger.Handler) *Consumer {
	if cfg.ConsumerName == "" {
		cfg.ConsumerName = trigger.ViewDataReadyConsumerName
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxAckPending <= 0 {
		cfg.MaxAckPending = 100
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10
	}
	return &Consumer{cfg: cfg, handler: handler}
}

// Start 创建 durable 消费者并启动运行循环。
func (c *Consumer) Start(ctx context.Context) error {
	if c == nil || c.cfg.Client == nil || c.handler == nil {
		return errors.New("策略 ViewDataReady 消费者未配置")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	filter, err := registry.FamilyPattern(events.ViewDataReady)
	if err != nil {
		return err
	}
	consumer, err := c.cfg.Client.NewConsumer(ctx, jetstream.ConsumerConfig{
		Stream: "MOOX_STORAGE", Durable: c.cfg.ConsumerName, FilterSubject: filter, AckWait: c.cfg.AckWait,
		MaxDeliver: -1, MaxAckPending: c.cfg.MaxAckPending, FetchMaxWait: c.cfg.FetchMaxWait,
		DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true,
	})
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	runner := jetstream.NewRunner(consumer, jetstream.DeliveryHandlerFunc(func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
		return HandleDelivery(ctx, delivery, c.handler)
	}), jetstream.RunnerConfig{BatchSize: c.cfg.BatchSize})
	c.mu.Lock()
	c.consumer = consumer
	c.runner = runner
	c.cancel = cancel
	c.ready = true
	c.mu.Unlock()
	go func() {
		_ = runner.Run(runCtx)
		c.mu.Lock()
		c.ready = false
		c.mu.Unlock()
	}()
	return nil
}

// Close 停止运行循环并关闭消费者。
func (c *Consumer) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	c.ready = false
	if c.consumer != nil {
		return c.consumer.Close()
	}
	return nil
}

// Ready 报告消费者是否仍在运行；运行循环退出后不再就绪，便于外部监控重启进程。
func (c *Consumer) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consumer != nil && c.cancel != nil && c.ready
}

// HandleDelivery 解码一条投递并交给处理器：解码失败或事件不可处理是契约错误，TERM；
// 处理器返回错误表示仍有实例的终态没有落库，RETRY；否则 ACK。
func HandleDelivery(ctx context.Context, delivery *jetstream.Delivery, handler *trigger.Handler) jetstream.HandlerResult {
	if delivery == nil || handler == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: retryDelay(delivery.DeliveryCount), Err: err}
	}
	contentType := delivery.ContentType
	if contentType == "" {
		contentType = events.ContentType
	}
	message, payload, err := events.DecodeViewDataReadyWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, contentType)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	}
	return decide(handler.Handle(ctx, message, payload), delivery.DeliveryCount)
}

func decide(err error, deliveryCount uint64) jetstream.HandlerResult {
	switch {
	case err == nil:
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	case errors.Is(err, trigger.ErrInvalidEvent):
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
	default:
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: retryDelay(deliveryCount), Err: err}
	}
}
