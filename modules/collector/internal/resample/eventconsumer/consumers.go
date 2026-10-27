// Package eventconsumer 是合成的 JetStream 传输适配层：订阅源周期完成与补采完成事件，解码、确认、重试，
// 领域判断交给 resample 包的触发器。
package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	nats "github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

const defaultNakDelay = 10 * time.Second

// PeriodTrigger 是源周期完成事件的领域处理（resample.PeriodTrigger）；返回错误表示需要重试。
type PeriodTrigger interface {
	OnPeriod(ctx context.Context, spaceID string, payload *storagepb.DatasetPeriodCompleted) error
}

// BackfillTrigger 是补采完成事件的领域处理（resample.BackfillTrigger）；返回错误表示需要重试。
type BackfillTrigger interface {
	OnBackfilled(ctx context.Context, spaceID, eventID string, payload *storagepb.CollectorKlineBackfilled) error
}

// PeriodHandler 把源周期完成事件交给触发器；解码失败直接终止，处理失败延迟重试。
type PeriodHandler struct {
	Registry *events.Registry
	Trigger  PeriodTrigger
	NakDelay time.Duration
}

// Handle 实现 jetstream.DeliveryHandler。
func (h *PeriodHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil || delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("resample period event is undecodable")}
	}
	message, payload, err := events.DecodeDatasetPeriodCompletedWithContentType(h.Registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil || message == nil || payload == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("reject collector period event: %v", err)}
	}
	if err := h.Trigger.OnPeriod(ctx, message.GetSpaceId(), payload); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: nakDelay(h.NakDelay), Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

// BackfilledHandler 把补采完成事件交给触发器。
type BackfilledHandler struct {
	Registry *events.Registry
	Trigger  BackfillTrigger
	NakDelay time.Duration
}

// Handle 实现 jetstream.DeliveryHandler。
func (h *BackfilledHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil || delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("backfilled event is undecodable")}
	}
	message, value, err := events.DecodeRaw(h.Registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil || message == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("reject backfilled event: %v", err)}
	}
	payload, ok := value.(*storagepb.CollectorKlineBackfilled)
	if !ok {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("unexpected backfilled event payload %T", value)}
	}
	if err := h.Trigger.OnBackfilled(ctx, message.GetSpaceId(), message.GetEventId(), payload); err != nil {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: nakDelay(h.NakDelay), Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func nakDelay(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	return defaultNakDelay
}

// ConsumersConfig 是事件消费者的连接参数。
type ConsumersConfig struct {
	URLs           []string
	CredentialFile string
	SpaceIDs       []string
	FetchMaxWait   time.Duration
	AckWait        time.Duration
	MaxAckPending  int
}

// Consumers 持有 JetStream 连接与每个空间的两个持久消费者。
type Consumers struct {
	client    *jetstream.Client
	consumers []*events.Consumer
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// Start 连接事件总线并启动源周期完成与补采完成两个消费者（每个空间各一个持久消费者）。
func Start(ctx context.Context, cfg ConsumersConfig, registry *events.Registry, period *PeriodHandler, backfilled *BackfilledHandler) (*Consumers, error) {
	if len(cfg.SpaceIDs) == 0 {
		return nil, errors.New("resample consumers need at least one space")
	}
	clientCfg := jetstream.ConfigFromEnv(append([]string(nil), cfg.URLs...), "moox-collector")
	if cfg.CredentialFile != "" {
		if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, err
		}
	}
	client, err := jetstream.Connect(ctx, clientCfg)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	c := &Consumers{client: client, cancel: cancel}
	ackWait, fetchWait, pending := cfg.AckWait, cfg.FetchMaxWait, cfg.MaxAckPending
	if ackWait <= 0 {
		ackWait = 2 * time.Minute
	}
	if fetchWait <= 0 {
		fetchWait = time.Second
	}
	if pending <= 0 {
		pending = 16
	}
	for _, spaceID := range cfg.SpaceIDs {
		for _, spec := range []struct {
			name    string
			event   events.Event
			handler jetstream.DeliveryHandler
		}{
			{"collector_resample_period_" + spaceID + "_v1", events.DatasetPeriodCompleted, period},
			{"collector_resample_backfilled_" + spaceID + "_v1", events.CollectorKlineBackfilled, backfilled},
		} {
			consumer, err := events.NewSpaceConsumer(ctx, client, registry, events.SpaceConsumerConfig{
				ConsumerConfig: events.ConsumerConfig{
					Name: spec.name, Event: spec.event, AckWait: ackWait, MaxDeliver: 10, MaxAckPending: pending,
					FetchMaxWait: fetchWait, DeliverPolicy: nats.DeliverNewPolicy, DeliverDecodeErrors: true,
				},
				SpaceID: spaceID,
			})
			if err != nil {
				_ = c.Close()
				return nil, fmt.Errorf("start consumer %s: %w", spec.name, err)
			}
			c.consumers = append(c.consumers, consumer)
			runner := jetstream.NewRunner(consumer, spec.handler, jetstream.RunnerConfig{
				BatchSize: pending, InProgressInterval: ackWait / 4,
				ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
					log.ErrorContextf(runCtx, "[Collector] resample consumer %s error: %v", spec.name, err)
				}),
			})
			c.wg.Add(1)
			go func(name string) {
				defer c.wg.Done()
				for runCtx.Err() == nil {
					if err := runner.Run(runCtx); err != nil && runCtx.Err() == nil {
						log.ErrorContextf(runCtx, "[Collector] resample consumer %s stopped: %v", name, err)
					}
					select {
					case <-runCtx.Done():
					case <-time.After(time.Second):
					}
				}
			}(spec.name)
		}
	}
	return c, nil
}

// Publisher 返回共用这条连接的事件发布者。
func (c *Consumers) Publisher(registry *events.Registry) (*events.Publisher, error) {
	return events.NewPublisher(c.client, registry)
}

// Close 停止消费并关闭连接。
func (c *Consumers) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		c.cancel()
		for _, consumer := range c.consumers {
			err = errors.Join(err, consumer.Close())
		}
		c.wg.Wait()
		err = errors.Join(err, c.client.Close())
	})
	return err
}
