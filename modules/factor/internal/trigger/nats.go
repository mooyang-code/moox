package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	eventstoragepb "github.com/mooyang-code/moox/packages/events/storagepb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

func isStorageRowsUpsertedEnvelope(message *messagepb.MooxMessage) bool {
	_, _, err := validateStorageRowsUpsertedEnvelope(message)
	return err == nil
}

func validateStorageRowsUpsertedEnvelope(message *messagepb.MooxMessage) (string, string, error) {
	if message == nil {
		return "", "", fmt.Errorf("storage envelope is nil")
	}
	if message.GetProtocolVersion() != jetstream.ProtocolVersion || message.GetKind() != messagepb.MessageKind_MESSAGE_KIND_EVENT {
		return "", "", fmt.Errorf("unsupported storage message protocol or kind")
	}
	return jetstream.ValidateStorageRowsUpsertedEnvelope(message)
}

func storageFieldsChangedPayloadMatches(message *messagepb.MooxMessage, event *storagepb.RowsUpserted) bool {
	spaceID, datasetID, err := validateStorageRowsUpsertedEnvelope(message)
	return err == nil && event != nil && event.GetSpaceId() == spaceID && event.GetDatasetId() == datasetID
}

type NATSConfig struct {
	URLs           []string
	URL            string
	Stream         string
	Consumer       string
	FetchMaxWait   time.Duration
	CredentialFile string
}

const (
	// LiveStream and LiveDurable are owned by the EventBus topology registry.
	// Factor realtime must not turn these names into a configurable replay path.
	LiveStream  = "MOOX_STORAGE"
	LiveDurable = "factor_calc"
)

var ErrInvalidLiveConsumer = errors.New("factor: invalid live consumer contract")

// ValidateLiveConsumerConfig keeps the realtime trigger on the EventBus-owned
// live durable. Replay uses a separate durable or an offline entry point.
func ValidateLiveConsumerConfig(cfg NATSConfig) error {
	if strings.TrimSpace(cfg.Stream) != LiveStream || strings.TrimSpace(cfg.Consumer) != LiveDurable {
		return fmt.Errorf("%w: realtime must bind %s/%s, got %q/%q", ErrInvalidLiveConsumer, LiveStream, LiveDurable, cfg.Stream, cfg.Consumer)
	}
	return nil
}

func liveConsumerBindRef(cfg NATSConfig) (jetstream.ConsumerBindRef, error) {
	if err := ValidateLiveConsumerConfig(cfg); err != nil {
		return jetstream.ConsumerBindRef{}, err
	}
	return jetstream.ConsumerBindRef{
		Stream:              LiveStream,
		Durable:             LiveDurable,
		FetchMaxWait:        cfg.FetchMaxWait,
		DeliverDecodeErrors: true,
	}, nil
}

type NATSConsumer struct {
	cfg          NATSConfig
	eventBatcher *EventBatcher
	client       *jetstream.Client
	consumer     *jetstream.PullConsumer
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
	consumerRef, err := liveConsumerBindRef(c.cfg)
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
	consumer, err := client.BindManagedPullConsumer(ctx, consumerRef)
	if err != nil {
		_ = client.Close()
		return err
	}
	c.client, c.consumer = client, consumer
	c.runner = jetstream.NewRunner(rawPullAdapter{consumer}, storageEventHandler{eventBatcher: c.eventBatcher}, jetstream.RunnerConfig{BatchSize: 16})
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

type storageEventHandler struct{ eventBatcher *EventBatcher }

type rawPullAdapter struct{ *jetstream.PullConsumer }

func (a rawPullAdapter) Fetch(ctx context.Context, batch int) ([]*jetstream.Delivery, error) {
	return a.FetchRaw(ctx, batch)
}

func (h storageEventHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM}
	}
	event := &storagepb.RowsUpserted{}
	if delivery.ContentType == events.ContentType {
		outer := &eventpb.EventMessage{}
		if err := proto.Unmarshal(delivery.RawData, outer); err != nil || outer.GetEventName() != events.StorageRowsUpserted.Name || outer.GetEventVersion() != events.StorageRowsUpserted.Version {
			return jetstream.HandlerResult{Decision: jetstream.TERM}
		}
		if delivery.RawMessageID == "" || outer.GetEventId() != delivery.RawMessageID {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("event_id %q does not match NATS message id %q", outer.GetEventId(), delivery.RawMessageID)}
		}
		registry, err := events.DefaultRegistry()
		if err != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		spec, ok := registry.Spec(events.StorageRowsUpserted)
		if !ok {
			return jetstream.HandlerResult{Decision: jetstream.TERM}
		}
		template, err := events.NewSubjectTemplate(spec.Subject)
		if err != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
		}
		expected, err := template.Render(outer.GetSpaceId(), outer.GetSubjectId())
		if err != nil || expected != delivery.Subject {
			return jetstream.HandlerResult{Decision: jetstream.TERM}
		}
		payload := &eventstoragepb.RowsUpserted{}
		if err := proto.Unmarshal(outer.GetPayload(), payload); err != nil || proto.Unmarshal(payload.GetRows(), event) != nil || payload.GetDatasetId() != outer.GetSubjectId() || event.GetSpaceId() != outer.GetSpaceId() || event.GetDatasetId() != outer.GetSubjectId() {
			return jetstream.HandlerResult{Decision: jetstream.TERM}
		}
	} else {
		if delivery.Message == nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM}
		}
		spaceID, datasetID, err := validateStorageRowsUpsertedEnvelope(delivery.Message)
		if err != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM}
		}
		if err := proto.Unmarshal(delivery.Message.GetPayload(), event); err != nil || event.GetSpaceId() != spaceID || event.GetDatasetId() != datasetID {
			return jetstream.HandlerResult{Decision: jetstream.TERM}
		}
	}
	if h.eventBatcher != nil {
		messageID := delivery.RawMessageID
		if messageID == "" {
			messageID = delivery.Message.GetMessageId()
		}
		if err := h.eventBatcher.IngestMessage(ctx, messageID, event, time.Now().UTC()); err != nil {
			return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: err}
		}
	} else {
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: time.Second, Err: errors.New("factor event batcher is unavailable")}
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}
