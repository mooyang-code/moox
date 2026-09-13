package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

const ViewSourceSubjectConsumerName = "factor_source_subject"

func subjectConsumerConfig(cfg Config, batch trigger.SubjectBatchConfig) events.ConsumerConfig {
	return events.ConsumerConfig{
		Name: ViewSourceSubjectConsumerName, Event: events.ViewSourceSubjectReady,
		AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: batch.QueueCapacity + batch.MaxBatch,
		FetchMaxWait: min(cfg.FetchMaxWait, batch.Window), DeliverPolicy: nats.DeliverNewPolicy,
		DeliverDecodeErrors: true,
	}
}

// NewSubject reuses the consumer reconnect lifecycle but owns a separate
// durable and batcher. It never dispatches subject events through period logic.
func NewSubject(cfg Config, batch trigger.SubjectBatchConfig, execute func(context.Context, []trigger.SubjectEvent) error) (*Consumer, error) {
	if err := batch.Validate(); err != nil {
		return nil, err
	}
	if execute == nil {
		return nil, fmt.Errorf("subject batch executor is required")
	}
	if cfg.FetchMaxWait <= 0 {
		cfg.FetchMaxWait = batch.Window
	}
	c := New(cfg, nil)
	c.openSession = func(ctx context.Context) (natsConsumerSession, error) {
		batcher, err := trigger.NewSubjectBatcher(batch, execute)
		if err != nil {
			return nil, err
		}
		handler, err := NewSubjectHandler(batcher)
		if err != nil {
			return nil, err
		}
		clientCfg := jetstream.ConfigFromEnv(append([]string(nil), cfg.URLs...), "moox-factor-engine-subject")
		if cfg.CredentialFile != "" {
			if err := clientCfg.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
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
		consumer, err := events.NewConsumer(ctx, client, registry, subjectConsumerConfig(cfg, batch))
		if err != nil {
			_ = client.Close()
			return nil, err
		}
		runner := jetstream.NewRunner(consumer, handler, jetstream.RunnerConfig{
			BatchSize: 1, InProgressInterval: 20 * time.Second,
		})
		return &subjectConsumerSession{jetStreamConsumerSession: &jetStreamConsumerSession{client: client, consumer: consumer, runner: runner}, batcher: batcher, workers: batch.MaxBatch}, nil
	}
	return c, nil
}

type subjectConsumerSession struct {
	*jetStreamConsumerSession
	batcher *trigger.SubjectBatcher
	workers int
}

func (s *subjectConsumerSession) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, s.workers+1)
	go func() { done <- s.batcher.Run(ctx) }()
	// Independent pull loops keep accepting arrivals while earlier handlers
	// await the microbatch window. A fetch-batch barrier would miss late arrivals.
	for i := 0; i < s.workers; i++ {
		go func() { done <- s.runner.Run(ctx) }()
	}
	err := <-done
	cancel()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	for i := 0; i < s.workers; i++ {
		workerErr := <-done
		if !errors.Is(workerErr, context.Canceled) {
			err = errors.Join(err, workerErr)
		}
	}
	return err
}
