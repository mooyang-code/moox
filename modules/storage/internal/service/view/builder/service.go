package builder

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	metrics    *observability.ViewMetrics

	mu                sync.Mutex
	runCtx            context.Context
	cancel            context.CancelFunc
	timeSeriesSub     eventbus.Subscription
	recordSub         eventbus.Subscription
	timeSeriesBatcher *batcher[timeSeriesDeriveItem]
	recordBatcher     *batcher[recordDeriveItem]
	wg                sync.WaitGroup
}

type timeSeriesDeriveItem struct {
	row        *pb.TimeSeriesRow
	completion *deriveCompletion
}

type recordDeriveItem struct {
	row        *pb.RecordRow
	completion *deriveCompletion
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
		metrics:    defaultViewMetrics(opts.Metrics),
	}
}

func defaultViewMetrics(metrics *observability.ViewMetrics) *observability.ViewMetrics {
	if metrics != nil {
		return metrics
	}
	return observability.DefaultViewMetrics
}

func (s *Service) viewMetrics() *observability.ViewMetrics {
	if s != nil && s.metrics != nil {
		return s.metrics
	}
	return observability.DefaultViewMetrics
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

	timeSeriesSub, err := s.events.SubscribeTimeSeriesRowsUpdated(ctx, s.enqueueTimeSeries)
	if err != nil {
		s.clearStartedState(cancel)
		return err
	}
	recordSub, err := s.events.SubscribeRecordRowsUpdated(ctx, s.enqueueRecord)
	if err != nil {
		_ = timeSeriesSub.Close()
		s.clearStartedState(cancel)
		return err
	}

	s.mu.Lock()
	s.timeSeriesSub = timeSeriesSub
	s.recordSub = recordSub
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
	recordSub := s.recordSub
	if cancel == nil {
		s.mu.Unlock()
		return nil
	}
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

	s.mu.Lock()
	s.cancel = nil
	s.runCtx = nil
	s.timeSeriesSub = nil
	s.recordSub = nil
	s.timeSeriesBatcher = nil
	s.recordBatcher = nil
	s.mu.Unlock()
	return err
}

func (s *Service) enqueueTimeSeries(ctx context.Context, event *pb.TimeSeriesRowsUpdated) (retErr error) {
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
	rows := make([]*pb.TimeSeriesRow, 0, len(event.GetRows()))
	for _, row := range event.GetRows() {
		if row == nil {
			continue
		}
		rows = append(rows, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	completion := newDeriveCompletion(len(rows))
	s.viewMetrics().IncDeriveInFlight()
	defer func() {
		s.viewMetrics().DecDeriveInFlight()
		s.viewMetrics().ObserveDerive("time_series", deriveResult(retErr))
	}()
	for i, row := range rows {
		item := timeSeriesDeriveItem{
			row:        row,
			completion: completion,
		}
		if err := batcher.add(addCtx, item); err != nil {
			for range rows[i:] {
				completion.complete(err)
			}
			return completion.wait(ctx)
		}
	}
	return completion.wait(addCtx)
}

func (s *Service) enqueueRecord(ctx context.Context, event *pb.RecordRowsUpdated) (retErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if event == nil {
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
	rows := make([]*pb.RecordRow, 0, len(event.GetRows()))
	for _, row := range event.GetRows() {
		if row == nil {
			continue
		}
		rows = append(rows, proto.Clone(row).(*pb.RecordRow))
	}
	completion := newDeriveCompletion(len(rows))
	s.viewMetrics().IncDeriveInFlight()
	defer func() {
		s.viewMetrics().DecDeriveInFlight()
		s.viewMetrics().ObserveDerive("record", deriveResult(retErr))
	}()
	for i, row := range rows {
		item := recordDeriveItem{
			row:        row,
			completion: completion,
		}
		if err := batcher.add(addCtx, item); err != nil {
			for range rows[i:] {
				completion.complete(err)
			}
			return completion.wait(ctx)
		}
	}
	return completion.wait(addCtx)
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
	started := time.Now()
	rows := make([]*pb.TimeSeriesRow, 0, len(items))
	for _, item := range items {
		if item.row != nil {
			rows = append(rows, item.row)
		}
	}
	err := s.processTimeSeriesRowsBatch(ctx, rows)
	s.viewMetrics().ObserveBatch("duckdb", deriveResult(err), time.Since(started))
	for _, item := range items {
		item.completion.complete(err)
	}
}

func (s *Service) processRecordItemBatch(ctx context.Context, items []recordDeriveItem) {
	started := time.Now()
	rows := make([]*pb.RecordRow, 0, len(items))
	for _, item := range items {
		if item.row != nil {
			rows = append(rows, item.row)
		}
	}
	err := s.processRecordRowsBatch(ctx, rows)
	s.viewMetrics().ObserveBatch("bleve", deriveResult(err), time.Since(started))
	for _, item := range items {
		item.completion.complete(err)
	}
}

func deriveResult(err error) string {
	if err == nil {
		return "success"
	}
	return "error"
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
