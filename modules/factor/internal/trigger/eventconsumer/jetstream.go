package eventconsumer

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

func liveConsumerConfig(cfg Config) events.ConsumerConfig {
	return events.ConsumerConfig{
		Name:                DatasetRowsConsumerName,
		Event:               events.DatasetRowsUpserted,
		AckWait:             time.Minute,
		MaxDeliver:          5,
		MaxAckPending:       1000,
		FetchMaxWait:        cfg.FetchMaxWait,
		DeliverPolicy:       nats.DeliverNewPolicy,
		DeliverDecodeErrors: true,
	}
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

func (c *Consumer) open(ctx context.Context) (natsConsumerSession, error) {
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
