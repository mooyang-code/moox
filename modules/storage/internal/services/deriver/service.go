package deriver

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

const defaultMaxWorkers = 1

// Service consumes storage row-change events and updates derived view stores.
type Service struct {
	events         eventbus.Bus
	reader         FactReader
	metadata       metadata.Store
	metadataReader metadata.Reader
	views          TimeSeriesViewWriter
	search         RecordViewIndexer
	batchOpts      BatchOptions
	maxWorkers     int

	mu                sync.Mutex
	runCtx            context.Context
	cancel            context.CancelFunc
	timeSeriesSub     eventbus.Subscription
	recordSub         eventbus.Subscription
	timeSeriesBatcher *batcher[*pb.TimeSeriesKey]
	recordBatcher     *batcher[*pb.RecordKey]
	wg                sync.WaitGroup
}

// NewService creates a standalone deriver service.
func NewService(opts Options) *Service {
	batchOpts := normalizeBatchOptions(BatchOptions{
		BatchSize: opts.BatchSize,
		BatchWait: opts.BatchWait,
	})
	maxWorkers := opts.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	reader := opts.MetadataReader
	if reader == nil {
		reader = opts.Metadata
	}
	return &Service{
		events:         opts.Events,
		reader:         opts.Reader,
		metadata:       opts.Metadata,
		metadataReader: reader,
		views:          opts.Views,
		search:         opts.Search,
		batchOpts:      batchOpts,
		maxWorkers:     maxWorkers,
	}
}

// Start subscribes the deriver service to row-change events.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("deriver service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	subscriber, ok := s.events.(eventbus.Subscriber)
	if !ok {
		return errors.New("deriver service requires subscribable event bus")
	}

	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("deriver service is already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	timeSeriesBatcher := newBatcher[*pb.TimeSeriesKey](s.batchOpts)
	recordBatcher := newBatcher[*pb.RecordKey](s.batchOpts)
	s.cancel = cancel
	s.runCtx = runCtx
	s.timeSeriesBatcher = timeSeriesBatcher
	s.recordBatcher = recordBatcher
	s.mu.Unlock()

	timeSeriesSub, err := subscriber.SubscribeTimeSeriesRowsChanged(ctx, s.enqueueTimeSeries)
	if err != nil {
		s.clearStartedState(cancel)
		return err
	}
	recordSub, err := subscriber.SubscribeRecordRowsChanged(ctx, s.enqueueRecord)
	if err != nil {
		_ = timeSeriesSub.Close()
		s.clearStartedState(cancel)
		return err
	}

	timeSeriesOut := make(chan []*pb.TimeSeriesKey, s.maxWorkers)
	recordOut := make(chan []*pb.RecordKey, s.maxWorkers)

	s.mu.Lock()
	s.timeSeriesSub = timeSeriesSub
	s.recordSub = recordSub
	s.wg.Add(2 + 2*s.maxWorkers)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer close(timeSeriesOut)
		timeSeriesBatcher.run(runCtx, timeSeriesOut)
	}()
	go func() {
		defer s.wg.Done()
		defer close(recordOut)
		recordBatcher.run(runCtx, recordOut)
	}()
	for i := 0; i < s.maxWorkers; i++ {
		go func() {
			defer s.wg.Done()
			for batch := range timeSeriesOut {
				_ = s.processTimeSeriesBatch(runCtx, batch)
			}
		}()
		go func() {
			defer s.wg.Done()
			for batch := range recordOut {
				_ = s.processRecordBatch(runCtx, batch)
			}
		}()
	}
	return nil
}

// Close stops subscriptions and waits for worker goroutines to exit.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel := s.cancel
	timeSeriesSub := s.timeSeriesSub
	recordSub := s.recordSub
	if cancel == nil {
		s.mu.Unlock()
		return nil
	}
	s.cancel = nil
	s.runCtx = nil
	s.timeSeriesSub = nil
	s.recordSub = nil
	s.timeSeriesBatcher = nil
	s.recordBatcher = nil
	s.mu.Unlock()

	var err error
	if timeSeriesSub != nil {
		err = errors.Join(err, timeSeriesSub.Close())
	}
	if recordSub != nil {
		err = errors.Join(err, recordSub.Close())
	}
	cancel()
	s.wg.Wait()
	return err
}

func (s *Service) enqueueTimeSeries(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
	if event == nil {
		return nil
	}
	s.mu.Lock()
	batcher := s.timeSeriesBatcher
	addCtx := s.runCtx
	s.mu.Unlock()
	if batcher == nil {
		return errors.New("deriver time-series batcher is not started")
	}
	if addCtx == nil {
		addCtx = ctx
	}
	for _, key := range event.GetKeys() {
		if key == nil {
			continue
		}
		if err := batcher.add(addCtx, proto.Clone(key).(*pb.TimeSeriesKey)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enqueueRecord(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
	if event == nil {
		return nil
	}
	s.mu.Lock()
	batcher := s.recordBatcher
	addCtx := s.runCtx
	s.mu.Unlock()
	if batcher == nil {
		return errors.New("deriver record batcher is not started")
	}
	if addCtx == nil {
		addCtx = ctx
	}
	for _, key := range event.GetKeys() {
		if key == nil {
			continue
		}
		if err := batcher.add(addCtx, proto.Clone(key).(*pb.RecordKey)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) clearStartedState(cancel context.CancelFunc) {
	cancel()
	s.mu.Lock()
	s.cancel = nil
	s.runCtx = nil
	s.timeSeriesBatcher = nil
	s.recordBatcher = nil
	s.mu.Unlock()
}

type projectionDatasetKey struct {
	spaceID   string
	datasetID string
}

func retInfoError(ret *pb.RetInfo) error {
	if ret == nil || ret.GetCode() == pb.ErrorCode_SUCCESS {
		return nil
	}
	return errors.New(ret.GetMsg())
}

func markPending(ctx context.Context, store metadata.Store, item *pb.View) error {
	if store == nil || item == nil {
		return nil
	}
	copied := proto.Clone(item).(*pb.View)
	copied.BuildStatus = "pending"
	if copied.Status == "" {
		copied.Status = "active"
	}
	_, err := store.UpsertView(ctx, copied)
	return err
}
