package jetstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

type ConsumerConfig struct {
	Stream        string
	Durable       string
	FilterSubject string
	AckWait       time.Duration
	MaxDeliver    int
	MaxAckPending int
	FetchMaxWait  time.Duration
	DeliverPolicy nats.DeliverPolicy
	// DeliverDecodeErrors 控制是否把解码失败的消息交给业务层分类处理。
	DeliverDecodeErrors bool
}

type Consumer struct {
	client *Client
	sub    *nats.Subscription
	cfg    ConsumerConfig

	mu     sync.RWMutex
	closed bool
}

// NewConsumer 创建缺失的 Consumer、对账已有配置并完成绑定。
func (c *Client) NewConsumer(ctx context.Context, cfg ConsumerConfig) (*Consumer, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	cfg.Stream = strings.TrimSpace(cfg.Stream)
	cfg.Durable = strings.TrimSpace(cfg.Durable)
	cfg.FilterSubject = strings.TrimSpace(cfg.FilterSubject)
	if cfg.Stream == "" || cfg.Durable == "" {
		return nil, fmt.Errorf("%w: stream and durable are required", ErrInvalidConsumer)
	}
	if strings.ContainsAny(cfg.Stream, " \t\r\n") || strings.ContainsAny(cfg.Durable, " \t\r\n") {
		return nil, fmt.Errorf("%w: stream and durable cannot contain whitespace", ErrInvalidConsumer)
	}
	if cfg.FilterSubject == "" {
		return nil, fmt.Errorf("%w: filter_subject is required for durable consumers", ErrInvalidConsumer)
	}
	if err := contextErr(ctx, "before consumer setup"); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("%w: client is nil", ErrConnection)
	}
	if err := c.alive(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	if cfg.AckWait <= 0 || cfg.MaxDeliver == 0 || cfg.MaxAckPending <= 0 || cfg.FetchMaxWait <= 0 {
		return nil, fmt.Errorf("%w: ack_wait, max_deliver, max_ack_pending and fetch_max_wait must be configured", ErrInvalidConsumer)
	}
	if cfg.DeliverPolicy != nats.DeliverAllPolicy && cfg.DeliverPolicy != nats.DeliverNewPolicy {
		return nil, fmt.Errorf("%w: unsupported deliver policy %d", ErrInvalidConsumer, cfg.DeliverPolicy)
	}
	if err := c.rejectConsumerOwnedByAnotherStream(ctx, cfg.Stream, cfg.Durable); err != nil {
		return nil, err
	}

	info, err := c.inspectConsumer(ctx, cfg.Stream, cfg.Durable)
	if err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
		return nil, classifyConsumerError("inspect consumer", err)
	}
	if err := contextErr(ctx, "after consumer inspection"); err != nil {
		return nil, err
	}
	if errors.Is(err, nats.ErrConsumerNotFound) {
		consumerCfg := &nats.ConsumerConfig{
			Name:          cfg.Durable,
			Durable:       cfg.Durable,
			FilterSubject: cfg.FilterSubject,
			AckPolicy:     nats.AckExplicitPolicy,
			AckWait:       cfg.AckWait,
			MaxDeliver:    cfg.MaxDeliver,
			MaxAckPending: cfg.MaxAckPending,
			DeliverPolicy: cfg.DeliverPolicy,
		}
		if _, addErr := c.js.AddConsumer(cfg.Stream, consumerCfg, nats.Context(ctx)); addErr != nil && !errors.Is(addErr, nats.ErrConsumerNameAlreadyInUse) {
			return nil, classifyConsumerError("create consumer", addErr)
		}
		if err := contextErr(ctx, "after consumer creation"); err != nil {
			return nil, err
		}
		// 创建后重新读取，处理同一 Stream 内其他进程抢先创建同名 Consumer 的情况。
		info, err = c.js.ConsumerInfo(cfg.Stream, cfg.Durable, nats.Context(ctx))
		if err != nil {
			return nil, classifyConsumerError("inspect created consumer", err)
		}
	}
	if err := contextErr(ctx, "before consumer validation"); err != nil {
		return nil, err
	}
	if err := reconcileConsumerConfig(ctx, c, info, cfg); err != nil {
		return nil, err
	}
	if err := contextErr(ctx, "before consumer bind"); err != nil {
		return nil, err
	}
	sub, err := c.js.PullSubscribe(cfg.FilterSubject, cfg.Durable, nats.Bind(cfg.Stream, cfg.Durable), nats.ManualAck(), nats.Context(ctx))
	if err != nil {
		return nil, classifyConsumerError("bind consumer", err)
	}
	if err := contextErr(ctx, "after consumer bind"); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return &Consumer{client: c, sub: sub, cfg: cfg}, nil
}

func (c *Client) rejectConsumerOwnedByAnotherStream(ctx context.Context, requestedStream, durable string) error {
	// JetStream 只保证单个 Stream 内名称唯一。这里用于启动时发现静态命名冲突；
	// V1 依靠单实例 Consumer owner 和固定命名，不为跨 Stream 并发创建引入分布式锁。
	names := c.js.StreamNames(nats.Context(ctx))
	for stream := range names {
		if stream == requestedStream {
			continue
		}
		_, err := c.js.ConsumerInfo(stream, durable, nats.Context(ctx))
		switch {
		case err == nil:
			return fmt.Errorf("%w: consumer %s already belongs to stream %s", ErrConsumerConfigConflict, durable, stream)
		case errors.Is(err, nats.ErrConsumerNotFound):
			continue
		default:
			return classifyConsumerError("inspect consumer ownership", err)
		}
	}
	return contextErr(ctx, "after consumer ownership inspection")
}

func (c *Client) inspectConsumer(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	stream = strings.TrimSpace(stream)
	durable = strings.TrimSpace(durable)
	if stream == "" || durable == "" {
		return nil, fmt.Errorf("%w: stream and durable are required", ErrInvalidConsumer)
	}
	if strings.ContainsAny(stream, " \t\r\n") || strings.ContainsAny(durable, " \t\r\n") {
		return nil, fmt.Errorf("%w: stream and durable cannot contain whitespace", ErrInvalidConsumer)
	}
	if err := contextErr(ctx, "before consumer inspection"); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("%w: client is nil", ErrConnection)
	}
	if err := c.alive(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	info, err := c.js.ConsumerInfo(stream, durable, nats.Context(ctx))
	if err != nil {
		return nil, err
	}
	if err := contextErr(ctx, "after consumer inspection"); err != nil {
		return nil, err
	}
	return info, nil
}

func contextErr(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrConnection, operation, err)
	}
	return nil
}

func classifyConsumerError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, nats.ErrConnectionClosed) || errors.Is(err, nats.ErrDisconnected) ||
		errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, nats.ErrNoStreamResponse) ||
		errors.Is(err, nats.ErrJetStreamNotEnabled) {
		return fmt.Errorf("%w: %s: %w", ErrConnection, operation, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalidConsumer, operation, err)
}

func reconcileConsumerConfig(ctx context.Context, client *Client, info *nats.ConsumerInfo, cfg ConsumerConfig) error {
	if info == nil {
		return fmt.Errorf("%w: consumer info is empty", ErrInvalidConsumer)
	}
	actual := info.Config
	var conflicts []string
	if actual.FilterSubject != cfg.FilterSubject {
		conflicts = append(conflicts, "FilterSubject")
	}
	if actual.DeliverPolicy != cfg.DeliverPolicy {
		conflicts = append(conflicts, "DeliverPolicy")
	}
	switch {
	case actual.AckPolicy != nats.AckExplicitPolicy:
		conflicts = append(conflicts, "AckPolicy")
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("%w: consumer %s/%s conflicts in %s", ErrConsumerConfigConflict, cfg.Stream, cfg.Durable, strings.Join(conflicts, ","))
	}
	if actual.AckWait == cfg.AckWait && actual.MaxDeliver == cfg.MaxDeliver && actual.MaxAckPending == cfg.MaxAckPending {
		return nil
	}
	next := actual
	next.AckWait = cfg.AckWait
	next.MaxDeliver = cfg.MaxDeliver
	next.MaxAckPending = cfg.MaxAckPending
	if _, err := client.js.UpdateConsumer(cfg.Stream, &next, nats.Context(ctx)); err != nil {
		return classifyConsumerError("update consumer", err)
	}
	return nil
}

func (p *Consumer) Fetch(ctx context.Context, batch int) ([]*Delivery, error) {
	if p == nil {
		return nil, ErrInvalidConsumer
	}
	p.mu.RLock()
	closed := p.closed
	sub := p.sub
	p.mu.RUnlock()
	if closed || sub == nil {
		return nil, ErrClosed
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if batch <= 0 {
		batch = 1
	}
	fetchCtx := ctx
	internalTimeout := false
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > p.cfg.FetchMaxWait {
		var cancel context.CancelFunc
		fetchCtx, cancel = context.WithTimeout(ctx, p.cfg.FetchMaxWait)
		defer cancel()
		internalTimeout = true
	}
	msgs, err := sub.Fetch(batch, nats.Context(fetchCtx))
	if err != nil {
		if internalTimeout && errors.Is(err, context.DeadlineExceeded) {
			return nil, nats.ErrTimeout
		}
		return nil, err
	}
	deliveries := make([]*Delivery, 0, len(msgs))
	var firstDecodeErr error
	var transportErr error
	for _, msg := range msgs {
		delivery, decodeErr := deliveryFromMessage(msg, p.cfg.Stream, p.cfg.Durable, p.client.cfg.MaxPayload)
		if decodeErr != nil {
			if !p.cfg.DeliverDecodeErrors {
				// Poison messages must be terminated even when the caller's fetch context
				// has expired; otherwise they immediately redeliver forever.
				termCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), time.Second)
				if termErr := msg.Term(nats.Context(termCtx)); termErr != nil {
					transportErr = errors.Join(transportErr, fmt.Errorf("term poison delivery: %w", termErr))
				}
				cancel()
			} else if delivery != nil {
				deliveries = append(deliveries, delivery)
			}
			if firstDecodeErr == nil {
				firstDecodeErr = decodeErr
			}
			continue
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, errors.Join(firstDecodeErr, transportErr)
}

func (p *Consumer) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	sub := p.sub
	p.mu.Unlock()
	if sub != nil {
		return sub.Unsubscribe()
	}
	return nil
}
