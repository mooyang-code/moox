package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	URL            string
	Stream         string
	Consumer       string
	FetchMaxWait   time.Duration
	CredentialFile string
}

const (
	// LiveStream 和 LiveConsumer 定义 Factor 实时消费契约。
	// 实时消费不能把这些名称变成可配置的重放入口。
	LiveStream   = "MOOX_STORAGE"
	LiveConsumer = "factor_calc"
)

var ErrInvalidLiveConsumer = errors.New("factor: invalid live consumer contract")

// ValidateLiveConsumerConfig 保证实时触发器只使用固定 Consumer。
// 重放应使用独立 Consumer 或离线入口。
func ValidateLiveConsumerConfig(cfg NATSConfig) error {
	if strings.TrimSpace(cfg.Stream) != LiveStream || strings.TrimSpace(cfg.Consumer) != LiveConsumer {
		return fmt.Errorf("%w: realtime must bind %s/%s, got %q/%q", ErrInvalidLiveConsumer, LiveStream, LiveConsumer, cfg.Stream, cfg.Consumer)
	}
	return nil
}

func liveConsumerConfig(cfg NATSConfig) (events.ConsumerConfig, error) {
	if err := ValidateLiveConsumerConfig(cfg); err != nil {
		return events.ConsumerConfig{}, err
	}
	return events.ConsumerConfig{
		Name:                LiveConsumer,
		Event:               events.DatasetRowsUpserted,
		AckWait:             time.Minute,
		MaxDeliver:          5,
		MaxAckPending:       1000,
		FetchMaxWait:        cfg.FetchMaxWait,
		DeliverPolicy:       nats.DeliverNewPolicy,
		DeliverDecodeErrors: true,
	}, nil
}

type NATSConsumer struct {
	cfg          NATSConfig
	eventBatcher *EventBatcher
	client       *jetstream.Client
	consumer     *events.Consumer
	runner       *jetstream.Runner
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	runErr       error
}

func NewNATSConsumer(cfg NATSConfig, eventBatcher *EventBatcher) *NATSConsumer {
	return &NATSConsumer{cfg: cfg, eventBatcher: eventBatcher}
}

func (c *NATSConsumer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	consumerCfg, err := liveConsumerConfig(c.cfg)
	if err != nil {
		return err
	}
	urls := append([]string(nil), c.cfg.URLs...)
	if len(urls) == 0 && strings.TrimSpace(c.cfg.URL) != "" {
		urls = []string{c.cfg.URL}
	}
	clientCfg := jetstream.ConfigFromEnv(urls, "moox-factor")
	if c.cfg.CredentialFile != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(c.cfg.CredentialFile)); err != nil {
			return err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return err
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		_ = client.Close()
		return err
	}
	consumer, err := events.NewConsumer(ctx, client, registry, consumerCfg)
	if err != nil {
		_ = client.Close()
		return err
	}
	c.client, c.consumer = client, consumer
	c.runner = jetstream.NewRunner(consumer, storageEventHandler{eventBatcher: c.eventBatcher}, jetstream.RunnerConfig{BatchSize: 16})
	loopCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.wg.Add(1)
	go c.loop(loopCtx)
	return nil
}

func (c *NATSConsumer) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.consumer != nil {
		_ = c.consumer.Close()
	}
	c.wg.Wait()
	c.mu.Lock()
	runErr := c.runErr
	c.mu.Unlock()
	if c.client != nil {
		return errors.Join(runErr, c.client.Close())
	}
	return runErr
}

func (c *NATSConsumer) loop(ctx context.Context) {
	defer c.wg.Done()
	if err := c.runner.Run(ctx); err != nil && ctx.Err() == nil {
		c.recordError(err)
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
