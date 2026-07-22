package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

func isStorageFieldsChangedEnvelope(message *messagepb.MooxMessage) bool {
	_, _, err := validateStorageFieldsChangedEnvelope(message)
	return err == nil
}

func validateStorageFieldsChangedEnvelope(message *messagepb.MooxMessage) (string, string, error) {
	if message == nil {
		return "", "", fmt.Errorf("storage envelope is nil")
	}
	if message.GetProtocolVersion() != jetstream.ProtocolVersion || message.GetKind() != messagepb.MessageKind_MESSAGE_KIND_EVENT {
		return "", "", fmt.Errorf("unsupported storage message protocol or kind")
	}
	return jetstream.ValidateStorageFieldsChangedEnvelope(message)
}

func storageFieldsChangedPayloadMatches(message *messagepb.MooxMessage, event *storagepb.DatasetFieldsChanged) bool {
	spaceID, datasetID, err := validateStorageFieldsChangedEnvelope(message)
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

type storageEventHandler struct{ eventBatcher *EventBatcher }

func (h storageEventHandler) Handle(_ context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil || delivery.Message == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM}
	}
	event := &storagepb.DatasetFieldsChanged{}
	spaceID, datasetID, err := validateStorageFieldsChangedEnvelope(delivery.Message)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM}
	}
	if err := proto.Unmarshal(delivery.Message.GetPayload(), event); err != nil || event.GetSpaceId() != spaceID || event.GetDatasetId() != datasetID {
		return jetstream.HandlerResult{Decision: jetstream.TERM}
	}
	if h.eventBatcher != nil {
		h.eventBatcher.Ingest(event, time.Now().UTC())
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}
