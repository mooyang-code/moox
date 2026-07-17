package trigger

import (
	"context"
	"strings"
	"sync"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	nats "github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

type NATSConfig struct {
	URLs           []string
	URL            string
	Stream         string
	Consumer       string
	Subject        string
	CredentialFile string
}

type NATSConsumer struct {
	cfg      NATSConfig
	debounce *Debouncer
	client   *jetstream.Client
	consumer *jetstream.PullConsumer
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewNATSConsumer(cfg NATSConfig, debounce *Debouncer) *NATSConsumer {
	return &NATSConsumer{cfg: cfg, debounce: debounce}
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
	consumer, err := client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: c.cfg.Stream, Durable: c.cfg.Consumer, FilterSubject: c.cfg.Subject, AckWait: 60 * time.Second, MaxDeliver: 5, MaxAckPending: 1000, FetchMaxWait: time.Second, DeliverPolicy: nats.DeliverNewPolicy})
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
			actionCtx := trpc.CloneContext(ctx)
			event := &storagepb.TimeSeriesRowsUpdated{}
			if delivery.Message.GetContentType() != "application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsUpdated" {
				_ = delivery.Term(actionCtx)
				continue
			}
			if err := proto.Unmarshal(delivery.Message.GetPayload(), event); err != nil {
				_ = delivery.Term(actionCtx)
				continue
			}
			if c.debounce != nil {
				c.debounce.Ingest(event, time.Now().UTC())
			}
			_ = delivery.Ack(actionCtx)
		}
	}
}
