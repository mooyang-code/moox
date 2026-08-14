package view

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type EventConsumerOptions = eventconsumer.Config

type liveDeliveryLease struct {
}

func (l liveDeliveryLease) Acquire(ctx context.Context) error {
	if ctx == nil {
		return errors.New("storage view delivery context is required")
	}
	return ctx.Err()
}

func (l liveDeliveryLease) Release() {
}

func (s *Service) StartEventConsumer(ctx context.Context, client *jetstream.Client, configured ...EventConsumerOptions) (func(), error) {
	if s == nil {
		return nil, errors.New("storage view service is nil")
	}
	opts := EventConsumerOptions{}
	if len(configured) > 0 {
		opts = configured[0]
	}
	if opts.Metrics == nil {
		opts.Metrics = s.metrics
	}
	s.metrics = opts.Metrics
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return nil, err
	}
	consumerName := strings.TrimSpace(opts.Consumer)
	if consumerName == "" {
		consumerName = "storage_view_period_v1"
	}
	stateReader := func(stateCtx context.Context) (jetstream.ConsumerState, error) {
		return client.ConsumerState(stateCtx, events.DatasetRowsUpserted.Stream(), consumerName)
	}
	s.mu.Lock()
	s.readyPublisher = publisher
	s.consumerState = stateReader
	s.consumerBound = func() bool {
		return s.metrics == nil || s.metrics.Snapshot().ConsumerBound
	}
	s.mu.Unlock()
	opts.Lease = liveDeliveryLease{}
	consumer, err := eventconsumer.New(client, s, opts)
	if err != nil {
		s.mu.Lock()
		s.consumerState = nil
		s.consumerBound = nil
		s.mu.Unlock()
		return nil, err
	}
	stop, err := consumer.Start(ctx)
	if err != nil {
		s.mu.Lock()
		s.consumerState = nil
		s.consumerBound = nil
		s.mu.Unlock()
		return nil, err
	}
	return func() {
		stop()
		s.mu.Lock()
		s.consumerState = nil
		s.consumerBound = nil
		s.mu.Unlock()
	}, nil
}
