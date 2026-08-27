package eventconsumer

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/log"
)

func viewReadyConsumerConfig(cfg Config) events.ConsumerConfig {
	return events.ConsumerConfig{
		Name:       ViewSourceReadyConsumerName,
		Event:      events.ViewSourcePeriodReady,
		AckWait:    time.Minute,
		MaxDeliver: -1,
		// A delayed retry must not occupy the entire durable lane. The runner
		// still fetches and executes one event at a time, while JetStream may
		// keep a bounded set of quarantined timeouts pending for redelivery.
		MaxAckPending: 16,
		FetchMaxWait:  cfg.FetchMaxWait,
		// Realtime factor results must follow the current source period. A new
		// installation (or an intentional history reset) should not replay an
		// unbounded historical View-ready backlog before it can calculate the
		// latest K-line. Historical ranges are handled by the explicit Recalc
		// path instead.
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
	consumerCfg := viewReadyConsumerConfig(c.cfg)
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
	handler := storageEventHandler{
		executor: c.executor, executionTimeout: c.cfg.ExecutionTimeout,
		stallThreshold: c.cfg.StallThreshold, maxExecutionAttempts: c.cfg.MaxExecutionAttempts, progress: c.progress,
	}
	runnerCfg := jetstream.RunnerConfig{
		BatchSize: 1, InProgressInterval: 30 * time.Second,
		ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
			log.ErrorContextf(ctx, "factor ViewSourcePeriodReady consumer error: %v", err)
		}),
		ActionReporter: c.progress,
	}
	return &jetStreamConsumerSession{
		client:   client,
		consumer: consumer,
		runner:   jetstream.NewRunner(consumer, handler, runnerCfg),
	}, nil
}
