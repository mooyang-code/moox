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

type dynamicConsumerBoundState struct {
	mu    sync.RWMutex
	bound bool
}

func (s *dynamicConsumerBoundState) set(bound bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.bound = bound
	s.mu.Unlock()
}

func (s *dynamicConsumerBoundState) get() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bound
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

func (s *Service) bindDynamicDatasetConsumer(ctx context.Context, client *jetstream.Client, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
	if s == nil {
		return nil, errors.New("storage view service is nil")
	}
	if client == nil {
		return nil, errors.New("storage view dynamic consumer EventBus client is required")
	}
	if spec.ref.spaceID == "" || spec.ref.datasetID == "" || spec.partitionID == "" || spec.durable == "" {
		return nil, errors.New("storage view dynamic consumer identity is incomplete")
	}
	partitionClient, err := client.Fork(ctx, "storage-view-"+spec.partitionID)
	if err != nil {
		return nil, fmt.Errorf("fork dynamic View consumer %q: %w", spec.partitionID, err)
	}
	state := &dynamicConsumerBoundState{}
	config := spec.config
	config.PartitionConfigs = nil
	config.PartitionID = spec.partitionID
	config.Consumer = spec.durable
	config.FilterSubjects = append([]string(nil), spec.filters...)
	config.DatasetRoutes = []DatasetRoute{{SpaceID: spec.ref.spaceID, DatasetID: spec.ref.datasetID}}
	config.Metrics = s.metrics
	config.Lease = liveDeliveryLease{}
	config.BoundReporter = state.set
	consumer, err := eventconsumer.New(partitionClient, s, config)
	if err != nil {
		_ = partitionClient.Close()
		return nil, err
	}
	stopConsumer, err := consumer.Start(ctx)
	if err != nil {
		_ = partitionClient.Close()
		return nil, fmt.Errorf("start dynamic View consumer %q: %w", spec.partitionID, err)
	}
	binding := &dynamicDatasetConsumerBinding{
		partitionID:     spec.partitionID,
		durable:         spec.durable,
		consumerIsBound: state.get,
		consumerState: func(stateCtx context.Context) (jetstream.ConsumerState, error) {
			return partitionClient.ConsumerState(stateCtx, events.DatasetRowsUpserted.Stream(), spec.durable)
		},
	}
	if err := s.registerDynamicConsumerPartition(spec.ref, binding); err != nil {
		stopConsumer()
		_ = partitionClient.Close()
		return nil, err
	}
	var stopOnce sync.Once
	binding.stop = func() {
		stopOnce.Do(func() {
			s.unregisterDynamicConsumerPartition(spec.ref, spec.partitionID)
			stopConsumer()
			_ = partitionClient.Close()
		})
	}
	return binding, nil
}

func (s *Service) registerDynamicConsumerPartition(ref datasetRef, binding *dynamicDatasetConsumerBinding) error {
	if binding == nil || binding.partitionID == "" || binding.consumerState == nil || binding.consumerIsBound == nil {
		return errors.New("storage view dynamic consumer binding is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.consumerPartitionByDataset[ref]; current != "" && current != binding.partitionID {
		return fmt.Errorf("Dataset %s/%s is already assigned to consumer partition %q", ref.spaceID, ref.datasetID, current)
	}
	if _, exists := s.consumerStates[binding.partitionID]; exists {
		return fmt.Errorf("storage view consumer partition %q is already registered", binding.partitionID)
	}
	if s.consumerStates == nil {
		s.consumerStates = make(map[string]func(context.Context) (jetstream.ConsumerState, error))
	}
	if s.consumerBounds == nil {
		s.consumerBounds = make(map[string]func() bool)
	}
	if s.consumerPartitionByDataset == nil {
		s.consumerPartitionByDataset = make(map[datasetRef]string)
	}
	s.consumerStates[binding.partitionID] = binding.consumerState
	s.consumerBounds[binding.partitionID] = binding.consumerIsBound
	s.consumerPartitionByDataset[ref] = binding.partitionID
	return nil
}

func (s *Service) unregisterDynamicConsumerPartition(ref datasetRef, partitionID string) {
	if s == nil || partitionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumerPartitionByDataset[ref] != partitionID {
		return
	}
	delete(s.consumerPartitionByDataset, ref)
	delete(s.consumerStates, partitionID)
	delete(s.consumerBounds, partitionID)
}
