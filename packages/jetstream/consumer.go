package jetstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

type ConsumerConfig struct {
	Stream        string
	Durable       string
	FilterSubject string
	AckWait       time.Duration
	MaxDeliver    int
	MaxAckPending int
	FetchMaxWait  time.Duration
	// DeliverDecodeErrors returns poison deliveries to the caller so it can
	// publish a domain-specific DLQ record. The default remains false for
	// callers that only need the transport to terminate malformed messages.
	DeliverDecodeErrors bool
}

type PullConsumer struct {
	client *Client
	sub    *nats.Subscription
	cfg    ConsumerConfig

	mu     sync.RWMutex
	closed bool
}

func (c *Client) NewPullConsumer(ctx context.Context, cfg ConsumerConfig) (*PullConsumer, error) {
	if ctx == nil {
		ctx = context.Background()
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
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.MaxDeliver == 0 {
		cfg.MaxDeliver = -1
	}
	if cfg.MaxAckPending == 0 {
		cfg.MaxAckPending = 1000
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = time.Second
	}

	consumerCfg := &nats.ConsumerConfig{
		Name:          cfg.Durable,
		Durable:       cfg.Durable,
		FilterSubject: cfg.FilterSubject,
		AckPolicy:     nats.AckExplicitPolicy,
		AckWait:       cfg.AckWait,
		MaxDeliver:    cfg.MaxDeliver,
		MaxAckPending: cfg.MaxAckPending,
		DeliverPolicy: nats.DeliverAllPolicy,
	}
	info, err := c.js.ConsumerInfo(cfg.Stream, cfg.Durable, nats.Context(ctx))
	if err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
		return nil, classifyConsumerError("inspect consumer", err)
	}
	if err := contextErr(ctx, "after consumer inspection"); err != nil {
		return nil, err
	}
	if errors.Is(err, nats.ErrConsumerNotFound) {
		if _, addErr := c.js.AddConsumer(cfg.Stream, consumerCfg, nats.Context(ctx)); addErr != nil && !errors.Is(addErr, nats.ErrConsumerNameAlreadyInUse) {
			return nil, classifyConsumerError("create consumer", addErr)
		}
		if err := contextErr(ctx, "after consumer creation"); err != nil {
			return nil, err
		}
		// Re-fetch after creation. This closes the race where another process creates
		// the same durable between the initial lookup and AddConsumer.
		info, err = c.js.ConsumerInfo(cfg.Stream, cfg.Durable, nats.Context(ctx))
		if err != nil {
			return nil, classifyConsumerError("inspect created consumer", err)
		}
	}
	if err := contextErr(ctx, "before consumer validation"); err != nil {
		return nil, err
	}
	if err := validateConsumerConfig(info, cfg); err != nil {
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
	return &PullConsumer{client: c, sub: sub, cfg: cfg}, nil
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

func validateConsumerConfig(info *nats.ConsumerInfo, cfg ConsumerConfig) error {
	if info == nil {
		return fmt.Errorf("%w: consumer info is empty", ErrInvalidConsumer)
	}
	actual := info.Config
	switch {
	case actual.FilterSubject != cfg.FilterSubject:
		return fmt.Errorf("%w: filter subject mismatch: existing %q, requested %q", ErrInvalidConsumer, actual.FilterSubject, cfg.FilterSubject)
	case actual.AckPolicy != nats.AckExplicitPolicy:
		return fmt.Errorf("%w: ack policy mismatch: existing %s", ErrInvalidConsumer, actual.AckPolicy)
	case actual.AckWait != cfg.AckWait:
		return fmt.Errorf("%w: ack wait mismatch: existing %s, requested %s", ErrInvalidConsumer, actual.AckWait, cfg.AckWait)
	case actual.MaxDeliver != cfg.MaxDeliver:
		return fmt.Errorf("%w: max deliver mismatch: existing %d, requested %d", ErrInvalidConsumer, actual.MaxDeliver, cfg.MaxDeliver)
	case actual.MaxAckPending != cfg.MaxAckPending:
		return fmt.Errorf("%w: max ack pending mismatch: existing %d, requested %d", ErrInvalidConsumer, actual.MaxAckPending, cfg.MaxAckPending)
	case actual.DeliverPolicy != nats.DeliverAllPolicy:
		return fmt.Errorf("%w: deliver policy mismatch: existing %v", ErrInvalidConsumer, actual.DeliverPolicy)
	}
	return nil
}

func (p *PullConsumer) Fetch(ctx context.Context, batch int) ([]*Delivery, error) {
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
		ctx = context.Background()
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
	for _, msg := range msgs {
		delivery, decodeErr := deliveryFromMessage(msg, p.cfg.Stream, p.cfg.Durable, p.client.cfg.MaxPayload)
		if decodeErr != nil {
			if !p.cfg.DeliverDecodeErrors {
				// Poison messages must be terminated even when the caller's fetch context
				// has expired; otherwise they immediately redeliver forever.
				termCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = msg.Term(nats.Context(termCtx))
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
	return deliveries, firstDecodeErr
}

func (p *PullConsumer) Close() error {
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
