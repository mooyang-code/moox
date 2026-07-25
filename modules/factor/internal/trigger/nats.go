package trigger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

type NATSConfig struct {
	URLs           []string
	FetchMaxWait   time.Duration
	CredentialFile string
}

const (
	// LiveStream 和 LiveConsumer 定义 Factor 实时消费契约。
	// 实时消费不能把这些名称变成可配置的重放入口。
	LiveStream   = "MOOX_STORAGE"
	LiveConsumer = "factor_calc"
)

func liveConsumerConfig(cfg NATSConfig) events.ConsumerConfig {
	return events.ConsumerConfig{
		Name:                LiveConsumer,
		Event:               events.DatasetRowsUpserted,
		AckWait:             time.Minute,
		MaxDeliver:          5,
		MaxAckPending:       1000,
		FetchMaxWait:        cfg.FetchMaxWait,
		DeliverPolicy:       nats.DeliverNewPolicy,
		DeliverDecodeErrors: true,
	}
}

type NATSConsumer struct {
	cfg          NATSConfig
	eventBatcher *EventBatcher
	openSession  func(context.Context) (natsConsumerSession, error)
	retryDelay   time.Duration
	session      natsConsumerSession
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	runErr       error
	ready        bool
}

func NewNATSConsumer(cfg NATSConfig, eventBatcher *EventBatcher) *NATSConsumer {
	consumer := &NATSConsumer{cfg: cfg, eventBatcher: eventBatcher, retryDelay: time.Second}
	consumer.openSession = consumer.open
	return consumer
}

func (c *NATSConsumer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	session, err := c.openSession(ctx)
	if err != nil {
		return err
	}
	c.startSessionLoop(ctx, session)
	return nil
}

type natsConsumerSession interface {
	Run(context.Context) error
	Close() error
}

type jetStreamConsumerSession struct {
	client   *jetstream.Client
	consumer *events.Consumer
	runner   *jetstream.Runner
}

func (s *jetStreamConsumerSession) Run(ctx context.Context) error {
	return s.runner.Run(ctx)
}

func (s *jetStreamConsumerSession) Close() error {
	if s == nil {
		return nil
	}
	var consumerErr error
	if s.consumer != nil {
		consumerErr = s.consumer.Close()
	}
	if s.client != nil {
		return errors.Join(consumerErr, s.client.Close())
	}
	return consumerErr
}

func (c *NATSConsumer) open(ctx context.Context) (natsConsumerSession, error) {
	consumerCfg := liveConsumerConfig(c.cfg)
	urls := append([]string(nil), c.cfg.URLs...)
	clientCfg := jetstream.ConfigFromEnv(urls, "moox-factor")
	if c.cfg.CredentialFile != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(c.cfg.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	consumer, err := events.NewConsumer(ctx, client, registry, consumerCfg)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &jetStreamConsumerSession{
		client:   client,
		consumer: consumer,
		runner:   jetstream.NewRunner(consumer, storageEventHandler{eventBatcher: c.eventBatcher}, jetstream.RunnerConfig{BatchSize: 16}),
	}, nil
}

func (c *NATSConsumer) startSessionLoop(ctx context.Context, session natsConsumerSession) {
	loopCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.session = session
	c.ready = true
	c.mu.Unlock()
	c.wg.Add(1)
	go c.loop(loopCtx, session)
}

func (c *NATSConsumer) Close() error {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.ready = false
	c.mu.Unlock()
	c.wg.Wait()
	c.mu.Lock()
	runErr := c.runErr
	c.mu.Unlock()
	return runErr
}

func (c *NATSConsumer) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *NATSConsumer) loop(ctx context.Context, session natsConsumerSession) {
	defer c.wg.Done()
	for {
		runErr := session.Run(ctx)
		c.detachSession(session)
		closeErr := session.Close()
		if ctx.Err() != nil {
			return
		}
		c.recordError(errors.Join(runErr, closeErr))

		for {
			if !sleepNATSConsumer(ctx, c.retryDelay) {
				return
			}
			next, err := c.openSession(ctx)
			if err != nil {
				c.recordError(err)
				continue
			}
			c.attachSession(next)
			session = next
			break
		}
	}
}

func (c *NATSConsumer) detachSession(session natsConsumerSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == session {
		c.session = nil
		c.ready = false
	}
}

func (c *NATSConsumer) attachSession(session natsConsumerSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
	c.ready = true
	c.runErr = nil
}

func sleepNATSConsumer(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *NATSConsumer) recordError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.runErr = errors.Join(c.runErr, err)
	c.mu.Unlock()
}

type storageEventHandler struct {
	eventBatcher *EventBatcher
}

func (h storageEventHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	if delivery.ContentType == events.ContentType {
		registry, err := events.DefaultRegistry()
		if err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
		_, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
		if err != nil {
			return h.reject(ctx, delivery, err)
		}
		event := payload
		if event.GetSpaceId() == "" || event.GetDatasetId() == "" {
			return h.reject(ctx, delivery, fmt.Errorf("storage event payload identity is incomplete"))
		}
		return h.ingest(ctx, delivery, event)
	} else {
		return h.reject(ctx, delivery, fmt.Errorf("unexpected storage event content type %q", delivery.ContentType))
	}
}

func (h storageEventHandler) reject(_ context.Context, _ *jetstream.Delivery, reason error) jetstream.HandlerResult {
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor event rejected: %w", reason)}
}

func (h storageEventHandler) ingest(ctx context.Context, delivery *jetstream.Delivery, event *storagepb.DatasetRowsUpserted) jetstream.HandlerResult {
	if h.eventBatcher != nil {
		messageID := delivery.RawMessageID
		if messageID == "" {
			messageID = delivery.RawMessageID
		}
		if err := h.eventBatcher.IngestMessage(ctx, messageID, event, time.Now().UTC()); err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
	} else {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: errors.New("factor event batcher is unavailable")}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}
