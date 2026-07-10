package builder

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

const defaultMaxWorkers = 1

// Service consumes storage row-change events and updates derived view stores.
type Service struct {
	events     eventbus.Subscriber
	reader     FactReader
	metadata   MetadataReader
	engines    map[string]viewindex.ViewIndexEngine
	batchOpts  BatchOptions
	maxWorkers int

	mu                 sync.Mutex
	runCtx             context.Context
	cancel             context.CancelFunc
	timeSeriesSub      eventbus.Subscription
	recordCommittedSub eventbus.Subscription
	timeSeriesBatcher  *batcher[timeSeriesDeriveItem]
	recordBatcher      *batcher[recordDeriveItem]
	wg                 sync.WaitGroup
}

type timeSeriesDeriveItem struct {
	key  *pb.TimeSeriesKey
	done chan error
}

type recordDeriveItem struct {
	key  *pb.RecordKey
	done chan error
}

// NewService creates a standalone view builder service.
func NewService(opts Options) *Service {
	batchOpts := normalizeBatchOptions(BatchOptions{
		BatchSize: opts.BatchSize,
		BatchWait: opts.BatchWait,
	})
	maxWorkers := opts.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	engines := make(map[string]viewindex.ViewIndexEngine, len(opts.Engines))
	for name, engine := range opts.Engines {
		if engine != nil {
			engines[strings.ToLower(strings.TrimSpace(name))] = engine
		}
	}
	return &Service{
		events:     opts.Events,
		reader:     opts.Reader,
		metadata:   opts.Metadata,
		engines:    engines,
		batchOpts:  batchOpts,
		maxWorkers: maxWorkers,
	}
}

// Start subscribes the view builder service to row-change events.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("view builder service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.events == nil {
		return errors.New("view builder service requires subscribable event bus")
	}
	if s.reader == nil {
		return errors.New("view builder service requires fact reader")
	}
	if s.metadata == nil {
		return errors.New("view builder service requires metadata client")
	}
	if len(s.engines) == 0 {
		return errors.New("view builder service requires view index engines")
	}

	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("view builder service is already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	timeSeriesBatcher := newBatcher[timeSeriesDeriveItem](s.batchOpts)
	recordBatcher := newBatcher[recordDeriveItem](s.batchOpts)
	s.cancel = cancel
	s.runCtx = runCtx
	s.timeSeriesBatcher = timeSeriesBatcher
	s.recordBatcher = recordBatcher
	timeSeriesOut := make(chan []timeSeriesDeriveItem, s.maxWorkers)
	recordOut := make(chan []recordDeriveItem, s.maxWorkers)
	processCtx := runCtx
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
				s.processTimeSeriesItemBatch(processCtx, batch)
			}
		}()
		go func() {
			defer s.wg.Done()
			for batch := range recordOut {
				s.processRecordItemBatch(processCtx, batch)
			}
		}()
	}

	timeSeriesSub, err := s.events.SubscribeTimeSeriesRowsChanged(ctx, s.enqueueTimeSeries)
	if err != nil {
		s.clearStartedState(cancel)
		return err
	}
	recordCommittedSub, err := s.events.SubscribeRecordRowsCommitted(ctx, s.enqueueRecordCommitted)
	if err != nil {
		_ = timeSeriesSub.Close()
		s.clearStartedState(cancel)
		return err
	}

	s.mu.Lock()
	s.timeSeriesSub = timeSeriesSub
	s.recordCommittedSub = recordCommittedSub
	s.mu.Unlock()
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
	recordCommittedSub := s.recordCommittedSub
	if cancel == nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	var err error
	if timeSeriesSub != nil {
		err = errors.Join(err, timeSeriesSub.Close())
	}
	if recordCommittedSub != nil {
		err = errors.Join(err, recordCommittedSub.Close())
	}
	cancel()
	s.wg.Wait()

	s.mu.Lock()
	s.cancel = nil
	s.runCtx = nil
	s.timeSeriesSub = nil
	s.recordCommittedSub = nil
	s.timeSeriesBatcher = nil
	s.recordBatcher = nil
	s.mu.Unlock()
	return err
}

func (s *Service) enqueueRecordCommitted(ctx context.Context, event *pb.RecordRowsCommittedEvent) error {
	if event == nil {
		return nil
	}
	return s.enqueueRecordKeys(ctx, event.GetRows())
}

func (s *Service) enqueueTimeSeries(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if event == nil {
		return nil
	}
	s.mu.Lock()
	batcher := s.timeSeriesBatcher
	addCtx := s.runCtx
	s.mu.Unlock()
	if batcher == nil {
		return errors.New("view builder time-series batcher is not started")
	}
	if addCtx == nil {
		addCtx = ctx
	}
	var done []chan error
	for _, key := range event.GetKeys() {
		if key == nil {
			continue
		}
		item := timeSeriesDeriveItem{
			key:  proto.Clone(key).(*pb.TimeSeriesKey),
			done: make(chan error, 1),
		}
		if err := batcher.add(addCtx, item); err != nil {
			return err
		}
		done = append(done, item.done)
	}
	return waitDeriveResults(ctx, done)
}

func (s *Service) enqueueRecordKeys(ctx context.Context, rows []*pb.RecordRow) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(rows) == 0 {
		return nil
	}
	s.mu.Lock()
	batcher := s.recordBatcher
	addCtx := s.runCtx
	s.mu.Unlock()
	if batcher == nil {
		return errors.New("view builder record batcher is not started")
	}
	if addCtx == nil {
		addCtx = ctx
	}
	var done []chan error
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		item := recordDeriveItem{
			key:  proto.Clone(row.GetKey()).(*pb.RecordKey),
			done: make(chan error, 1),
		}
		if err := batcher.add(addCtx, item); err != nil {
			return err
		}
		done = append(done, item.done)
	}
	return waitDeriveResults(ctx, done)
}

func (s *Service) clearStartedState(cancel context.CancelFunc) {
	cancel()
	s.wg.Wait()
	s.mu.Lock()
	s.cancel = nil
	s.runCtx = nil
	s.timeSeriesBatcher = nil
	s.recordBatcher = nil
	s.mu.Unlock()
}

func (s *Service) processTimeSeriesItemBatch(ctx context.Context, items []timeSeriesDeriveItem) {
	keys := make([]*pb.TimeSeriesKey, 0, len(items))
	for _, item := range items {
		if item.key != nil {
			keys = append(keys, item.key)
		}
	}
	err := s.processTimeSeriesBatch(ctx, keys)
	for _, item := range items {
		completeDeriveItem(item.done, err)
	}
}

func (s *Service) processRecordItemBatch(ctx context.Context, items []recordDeriveItem) {
	keys := make([]*pb.RecordKey, 0, len(items))
	for _, item := range items {
		if item.key != nil {
			keys = append(keys, item.key)
		}
	}
	err := s.processRecordBatch(ctx, keys)
	for _, item := range items {
		completeDeriveItem(item.done, err)
	}
}

func completeDeriveItem(done chan error, err error) {
	if done == nil {
		return
	}
	done <- err
}

func waitDeriveResults(ctx context.Context, results []chan error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for _, result := range results {
		select {
		case err := <-result:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
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

func (s *Service) engine(name string) (viewindex.ViewIndexEngine, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	engine := s.engines[name]
	if engine == nil {
		return nil, errors.New("view builder has no index engine for " + name)
	}
	return engine, nil
}

func writableIndexSet(item *pb.View) map[string]bool {
	out := make(map[string]bool, 2)
	for _, indexID := range viewindex.WritableIndexIDs(item) {
		out[indexID] = true
	}
	return out
}
