package trigger

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// NATSConfig describes a durable Storage event consumer.
type NATSConfig struct {
	URL      string
	Stream   string
	Consumer string
	Subject  string
}

// NATSConsumer consumes Storage rows_changed events and feeds a Debouncer.
type NATSConsumer struct {
	cfg       NATSConfig
	debounce  *Debouncer
	conn      *nats.Conn
	sub       *nats.Subscription
	closeChan chan struct{}
}

// NewNATSConsumer creates a consumer.
func NewNATSConsumer(cfg NATSConfig, debounce *Debouncer) *NATSConsumer {
	return &NATSConsumer{cfg: cfg, debounce: debounce, closeChan: make(chan struct{})}
}

// Start connects to NATS JetStream and begins pull-consuming events.
func (c *NATSConsumer) Start(ctx context.Context) error {
	if c.cfg.URL == "" {
		return nil
	}
	nc, err := nats.Connect(c.cfg.URL)
	if err != nil {
		return err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return err
	}
	consumer := &nats.ConsumerConfig{
		Durable:       c.cfg.Consumer,
		FilterSubject: c.cfg.Subject,
		AckWait:       60 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: nats.DeliverNewPolicy,
		AckPolicy:     nats.AckExplicitPolicy,
	}
	if _, err := js.ConsumerInfo(c.cfg.Stream, c.cfg.Consumer); err != nil {
		if _, addErr := js.AddConsumer(c.cfg.Stream, consumer); addErr != nil {
			nc.Close()
			return fmt.Errorf("add NATS consumer: %w", addErr)
		}
	} else if _, err := js.UpdateConsumer(c.cfg.Stream, consumer); err != nil {
		nc.Close()
		return fmt.Errorf("update NATS consumer: %w", err)
	}
	sub, err := js.PullSubscribe(c.cfg.Subject, c.cfg.Consumer, nats.Bind(c.cfg.Stream, c.cfg.Consumer))
	if err != nil {
		nc.Close()
		return err
	}
	c.conn = nc
	c.sub = sub
	go c.loop(ctx)
	return nil
}

// Close stops the consumer.
func (c *NATSConsumer) Close() error {
	if c.sub != nil {
		_ = c.sub.Drain()
	}
	if c.conn != nil {
		c.conn.Close()
	}
	return nil
}

func (c *NATSConsumer) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closeChan:
			return
		default:
		}
		msgs, err := c.sub.Fetch(16, nats.MaxWait(time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			continue
		}
		for _, msg := range msgs {
			event := &storagepb.TimeSeriesRowsChangedEvent{}
			if err := protojson.Unmarshal(msg.Data, event); err == nil && c.debounce != nil {
				c.debounce.Ingest(event, time.Now())
			}
			_ = msg.Ack()
		}
	}
}
