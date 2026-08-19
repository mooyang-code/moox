package view

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type EventConsumerOptions = eventconsumer.Config
type DatasetRoute = eventconsumer.DatasetRoute

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
	partitionConfigs := opts.PartitionConfigs
	if len(partitionConfigs) == 0 {
		legacy := opts
		legacy.PartitionConfigs = nil
		partitionConfigs = []EventConsumerOptions{legacy}
	}
	for i := range partitionConfigs {
		partitionConfigs[i].PartitionConfigs = nil
		partitionConfigs[i].Metrics = opts.Metrics
		partitionConfigs[i].Lease = liveDeliveryLease{}
		if strings.TrimSpace(partitionConfigs[i].PartitionID) == "" {
			partitionConfigs[i].PartitionID = strings.TrimSpace(partitionConfigs[i].Consumer)
		}
		if partitionConfigs[i].PartitionID == "" {
			partitionConfigs[i].PartitionID = "default"
		}
	}

	type boundState struct {
		mu    sync.RWMutex
		bound bool
	}
	states := make(map[string]func(context.Context) (jetstream.ConsumerState, error), len(partitionConfigs))
	bounds := make(map[string]*boundState, len(partitionConfigs))
	var boundReader func() bool
	var boundReaderMu sync.RWMutex
	stops := make([]func(), 0, len(partitionConfigs))
	partitionClients := make([]*jetstream.Client, 0, len(partitionConfigs))
	stopAll := func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
		for i := len(partitionClients) - 1; i >= 0; i-- {
			_ = partitionClients[i].Close()
		}
	}
	for _, partition := range partitionConfigs {
		partition := partition
		partitionID := partition.PartitionID
		state := &boundState{}
		bounds[partitionID] = state
		partition.BoundReporter = func(bound bool) {
			state.mu.Lock()
			state.bound = bound
			state.mu.Unlock()
			boundReaderMu.RLock()
			reader := boundReader
			boundReaderMu.RUnlock()
			if reader != nil && opts.Metrics != nil {
				opts.Metrics.SetConsumerBound(reader())
			}
		}
		partitionClient, err := client.Fork(ctx, "storage-view-"+partitionID)
		if err != nil {
			stopAll()
			return nil, fmt.Errorf("fork eventbus client for storage view partition %q: %w", partitionID, err)
		}
		partitionClients = append(partitionClients, partitionClient)
		consumer, err := eventconsumer.New(partitionClient, s, partition)
		if err != nil {
			stopAll()
			return nil, err
		}
		stop, err := consumer.Start(ctx)
		if err != nil {
			stopAll()
			return nil, fmt.Errorf("start storage view consumer partition %q: %w", partitionID, err)
		}
		stops = append(stops, stop)
		durable := strings.TrimSpace(partition.Consumer)
		if durable == "" {
			durable = events.StorageViewKlineConsumer
		}
		states[partitionID] = func(stateCtx context.Context) (jetstream.ConsumerState, error) {
			return partitionClient.ConsumerState(stateCtx, events.DatasetRowsUpserted.Stream(), durable)
		}
	}

	stateReader := func(stateCtx context.Context) (jetstream.ConsumerState, error) {
		var total jetstream.ConsumerState
		for partitionID, reader := range states {
			state, err := reader(stateCtx)
			if err != nil {
				return jetstream.ConsumerState{}, fmt.Errorf("consumer partition %q: %w", partitionID, err)
			}
			if opts.Metrics != nil {
				opts.Metrics.ObserveConsumerPartitionBacklog(partitionID, state.NumPending, uint64(state.NumAckPending))
			}
			total.NumPending += state.NumPending
			total.NumAckPending += state.NumAckPending
		}
		return total, nil
	}
	computedBoundReader := func() bool {
		for _, state := range bounds {
			state.mu.RLock()
			bound := state.bound
			state.mu.RUnlock()
			if !bound {
				return false
			}
		}
		return true
	}
	boundReaderMu.Lock()
	boundReader = computedBoundReader
	boundReaderMu.Unlock()
	s.mu.Lock()
	s.readyPublisher = publisher
	s.consumerStates = states
	s.consumerBounds = make(map[string]func() bool, len(bounds))
	s.consumerPartitionByDataset = make(map[datasetRef]string)
	for _, partition := range partitionConfigs {
		for _, route := range partition.DatasetRoutes {
			key := datasetRef{spaceID: strings.TrimSpace(route.SpaceID), datasetID: strings.TrimSpace(route.DatasetID)}
			if key.spaceID != "" && key.datasetID != "" {
				s.consumerPartitionByDataset[key] = partition.PartitionID
			}
		}
	}
	for partitionID, state := range bounds {
		state := state
		s.consumerBounds[partitionID] = func() bool {
			state.mu.RLock()
			bound := state.bound
			state.mu.RUnlock()
			return bound
		}
	}
	s.consumerState = stateReader
	s.consumerBound = boundReader
	s.mu.Unlock()
	boundReaderMu.RLock()
	reader := boundReader
	boundReaderMu.RUnlock()
	if opts.Metrics != nil && reader != nil {
		opts.Metrics.SetConsumerBound(reader())
	}
	return func() {
		stopAll()
		if opts.Metrics != nil {
			opts.Metrics.SetConsumerBound(false)
		}
		s.mu.Lock()
		s.consumerState = nil
		s.consumerBound = nil
		s.consumerStates = make(map[string]func(context.Context) (jetstream.ConsumerState, error))
		s.consumerBounds = make(map[string]func() bool)
		s.consumerPartitionByDataset = make(map[datasetRef]string)
		s.mu.Unlock()
	}, nil
}
