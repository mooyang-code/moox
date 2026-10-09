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

// ConsumerConfig 是消费者配置。BatchSize 默认 1：顺序批次里一条消息 RETRY 会让同批后面的消息一起退避，
// 事件量很小，逐条拉取既保持同一 View 的周期顺序，又不会让一个 View 的故障拖住其他 View。
//
// Connect 建立 EventBus 连接；EventBus 暂不可用时监督循环按退避重试，进程照常启动，消费者在连上之前不就绪。
type ConsumerConfig struct {
	Connect       func(context.Context) (*jetstream.Client, error)
	ConsumerName  string
	AckWait       time.Duration
	MaxAckPending int
	FetchMaxWait  time.Duration
	BatchSize     int
	Logf          func(format string, args ...any)
}

// Consumer 持有 durable 消费者与监督循环：运行循环因 EventBus 重启等原因退出后，退避重建消费者继续消费。
type Consumer struct {
	cfg     ConsumerConfig
	handler *trigger.Handler

	mu       sync.Mutex
	client   *jetstream.Client
	consumer *jetstream.Consumer
	cancel   context.CancelFunc
	done     chan struct{}
	ready    bool
}

// 重建消费者的退避区间。
const (
	minRestartDelay = time.Second
	maxRestartDelay = 30 * time.Second
)

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
		cfg.BatchSize = 1
	}
	return &Consumer{cfg: cfg, handler: handler}
}

func (c *Consumer) consumerConfig() (jetstream.ConsumerConfig, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return jetstream.ConsumerConfig{}, err
	}
	filter, err := registry.FamilyPattern(events.ViewDataReady)
	if err != nil {
		return jetstream.ConsumerConfig{}, err
	}
	return jetstream.ConsumerConfig{
		Stream: "MOOX_STORAGE", Durable: c.cfg.ConsumerName, FilterSubject: filter, AckWait: c.cfg.AckWait,
		MaxDeliver: -1, MaxAckPending: c.cfg.MaxAckPending, FetchMaxWait: c.cfg.FetchMaxWait,
		DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true,
	}, nil
}

// Start 启动监督循环：连接 EventBus、创建 durable 消费者并消费；连接或创建失败都按退避重试，不阻塞进程启动。
func (c *Consumer) Start(ctx context.Context) error {
	if c == nil || c.cfg.Connect == nil || c.handler == nil {
		return errors.New("策略 ViewDataReady 消费者未配置")
	}
	config, err := c.consumerConfig()
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.mu.Lock()
	c.cancel, c.done = cancel, done
	c.mu.Unlock()
	go func() {
		defer close(done)
		c.supervise(runCtx, config)
	}()
	return nil
}

// supervise 运行消费循环；循环退出（例如 EventBus 重启时拉取请求收到 Server Shutdown）后记录原因，
// 按退避重新创建消费者，直到 ctx 结束。
func (c *Consumer) supervise(ctx context.Context, config jetstream.ConsumerConfig) {
	delay := minRestartDelay
	for {
		consumer, err := c.open(ctx, config)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logf("创建 ViewDataReady 消费者失败，%s 后重试：%v", delay, err)
			if !sleepContext(ctx, delay) {
				return
			}
			delay = min(delay*2, maxRestartDelay)
			continue
		}
		c.setCurrent(consumer, true)
		runner := jetstream.NewRunner(consumer, jetstream.DeliveryHandlerFunc(func(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
			return HandleDelivery(ctx, delivery, c.handler)
		}), jetstream.RunnerConfig{BatchSize: c.cfg.BatchSize, ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			c.logf("ViewDataReady 投递处理失败：%v", err)
		})})
		started := time.Now()
		err = runner.Run(ctx)
		c.setCurrent(nil, false)
		_ = consumer.Close()
		if ctx.Err() != nil {
			return
		}
		c.logf("ViewDataReady 消费循环退出，稍后重建：%v", err)
		if time.Since(started) > maxRestartDelay {
			delay = minRestartDelay
		}
		if !sleepContext(ctx, delay) {
			return
		}
		delay = min(delay*2, maxRestartDelay)
	}
}

// open 在需要时建立连接（只建立一次，之后由 nats.go 自动重连），再创建 durable 消费者。
func (c *Consumer) open(ctx context.Context, config jetstream.ConsumerConfig) (*jetstream.Consumer, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		connected, err := c.cfg.Connect(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.client = connected
		c.mu.Unlock()
		client = connected
	}
	return client.NewConsumer(ctx, config)
}

func (c *Consumer) setCurrent(consumer *jetstream.Consumer, ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consumer = consumer
	c.ready = ready
}

func (c *Consumer) logf(format string, args ...any) {
	if c.cfg.Logf != nil {
		c.cfg.Logf(format, args...)
	}
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Close 停止监督循环，等它退出（在途投递处理完毕、消费者已关闭）后关闭连接。
func (c *Consumer) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	cancel, done, consumer := c.cancel, c.done, c.consumer
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if consumer != nil {
		// 关闭当前消费者以打断正在等待的拉取，让循环尽快退出。
		_ = consumer.Close()
	}
	if done != nil {
		<-done
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = false
	if c.client != nil {
		err := c.client.Close()
		c.client = nil
		return err
	}
	return nil
}

// Ready 报告当前是否有运行中的消费循环；重建期间不就绪。
func (c *Consumer) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consumer != nil && c.ready
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
