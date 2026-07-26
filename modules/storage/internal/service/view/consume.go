package view

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type EventConsumerOptions = eventconsumer.Config

type liveDeliveryLease struct {
	service *Service
}

func (l liveDeliveryLease) Acquire(ctx context.Context) error {
	return l.service.acquireLiveDelivery(ctx)
}

func (l liveDeliveryLease) Release() {
	l.service.releaseLiveDelivery()
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
	opts.Lease = liveDeliveryLease{service: s}
	consumer, err := eventconsumer.New(client, s, opts)
	if err != nil {
		return nil, err
	}
	return consumer.Start(ctx)
}
