package trigger

import (
	"context"
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

type NATSConsumer struct {
	cfg          NATSConfig
	eventBatcher *EventBatcher
	client       *jetstream.Client
	consumer     *jetstream.PullConsumer
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

func NewNATSConsumer(cfg NATSConfig, eventBatcher *EventBatcher) *NATSConsumer {
	return &NATSConsumer{cfg: cfg, eventBatcher: eventBatcher}
}

func (c *NATSConsumer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
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
	consumer, err := client.BindManagedPullConsumer(ctx, jetstream.ConsumerBindRef{Stream: c.cfg.Stream, Durable: c.cfg.Consumer, FetchMaxWait: c.cfg.FetchMaxWait, DeliverDecodeErrors: true})
	if err != nil {
		_ = client.Close()
		return err
	}
	c.client, c.consumer = client, consumer
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
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

func (c *NATSConsumer) loop(ctx context.Context) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		deliveries, err := c.consumer.Fetch(ctx, 16)
		if err != nil && len(deliveries) == 0 {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, delivery := range deliveries {
			event := &storagepb.DatasetFieldsChanged{}
			spaceID, datasetID, err := validateStorageFieldsChangedEnvelope(delivery.Message)
			if err != nil {
				_ = delivery.Term(ctx)
				continue
			}
			if err := proto.Unmarshal(delivery.Message.GetPayload(), event); err != nil || event.GetSpaceId() != spaceID || event.GetDatasetId() != datasetID {
				_ = delivery.Term(ctx)
				continue
			}
			if c.eventBatcher != nil {
				c.eventBatcher.Ingest(event, time.Now().UTC())
			}
			_ = delivery.Ack(ctx)
		}
	}
}
