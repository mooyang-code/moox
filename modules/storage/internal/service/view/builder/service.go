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
	"trpc.group/trpc-go/trpc-go"
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

	mu  sync.Mutex
	run *serviceRun
}

type serviceRun struct {
	ctx               context.Context
	cancel            context.CancelFunc
	timeSeriesSub     eventbus.Subscription
	recordSub         eventbus.Subscription
	timeSeriesBatcher *batcher[timeSeriesDeriveItem]
	recordBatcher     *batcher[recordDeriveItem]
	wg                sync.WaitGroup
	startOnce         sync.Once
	startDone         chan struct{}
	stopOnce          sync.Once
	stopDone          chan struct{}
	stopErr           error
}

func newServiceRun(parent context.Context, opts BatchOptions) *serviceRun {
	ctx, cancel := context.WithCancel(parent)
	return &serviceRun{
		ctx: ctx, cancel: cancel,
		timeSeriesBatcher: newBatcher[timeSeriesDeriveItem](opts),
		recordBatcher:     newBatcher[recordDeriveItem](opts),
		startDone:         make(chan struct{}),
		stopDone:          make(chan struct{}),
	}
}

func (r *serviceRun) finishStart() {
	r.startOnce.Do(func() { close(r.startDone) })
}

func (r *serviceRun) stop() error {
	r.stopOnce.Do(func() {
		if r.timeSeriesSub != nil {
			r.stopErr = errors.Join(r.stopErr, r.timeSeriesSub.Close())
		}
		if r.recordSub != nil {
			r.stopErr = errors.Join(r.stopErr, r.recordSub.Close())
		}
		r.cancel()
		r.wg.Wait()
		close(r.stopDone)
	})
	<-r.stopDone
	return r.stopErr
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
		ctx = trpc.BackgroundContext()
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

	run := newServiceRun(ctx, s.batchOpts)
	s.mu.Lock()
	if s.run != nil {
		s.mu.Unlock()
		run.cancel()
		return errors.New("view builder service is already started")
	}
	s.run = run
	timeSeriesOut := make(chan []timeSeriesDeriveItem, s.maxWorkers)
	recordOut := make(chan []recordDeriveItem, s.maxWorkers)
	run.wg.Add(2 + 2*s.maxWorkers)
	s.mu.Unlock()
	defer run.finishStart()

	go func() {
		defer run.wg.Done()
		defer close(timeSeriesOut)
		run.timeSeriesBatcher.run(run.ctx, timeSeriesOut)
	}()
	go func() {
		defer run.wg.Done()
		defer close(recordOut)
		run.recordBatcher.run(run.ctx, recordOut)
	}()
	for i := 0; i < s.maxWorkers; i++ {
		go func() {
			defer run.wg.Done()
			for batch := range timeSeriesOut {
				s.processTimeSeriesItemBatch(run.ctx, batch)
			}
		}()
		go func() {
			defer run.wg.Done()
			for batch := range recordOut {
				s.processRecordItemBatch(run.ctx, batch)
			}
		}()
	}

	timeSeriesSub, err := s.events.SubscribeTimeSeriesRowsUpdated(run.ctx, func(handlerCtx context.Context, event *pb.TimeSeriesRowsUpdated) error {
		return s.enqueueTimeSeriesRun(handlerCtx, run, event)
	})
	if err != nil {
		run.finishStart()
		_ = run.stop()
		s.clearRun(run)
		return err
	}
	run.timeSeriesSub = timeSeriesSub
	recordSub, err := s.events.SubscribeRecordRowsUpdated(run.ctx, func(handlerCtx context.Context, event *pb.RecordRowsUpdated) error {
		return s.enqueueRecordRun(handlerCtx, run, event)
	})
	if err != nil {
		run.finishStart()
		_ = run.stop()
		s.clearRun(run)
		return err
	}
	run.recordSub = recordSub
	return nil
}

// Close stops subscriptions and waits for worker goroutines to exit.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	if run == nil {
		return nil
	}
	select {
	case <-run.startDone:
	default:
		run.cancel()
		<-run.startDone
	}
	err := run.stop()
	s.clearRun(run)
	return err
}

func (s *Service) clearRun(run *serviceRun) {
	s.mu.Lock()
	if s.run == run {
		s.run = nil
	}
	s.mu.Unlock()
}

func (s *Service) enqueueTimeSeries(ctx context.Context, event *pb.TimeSeriesRowsUpdated) (retErr error) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	return s.enqueueTimeSeriesRun(ctx, run, event)
}

func (s *Service) enqueueTimeSeriesRun(ctx context.Context, run *serviceRun, event *pb.TimeSeriesRowsUpdated) (retErr error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if event == nil {
		return nil
	}
	if run == nil || run.timeSeriesBatcher == nil {
		return errors.New("view builder time-series batcher is not started")
	}
	batcher := run.timeSeriesBatcher
	addCtx := run.ctx
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
	return completion.wait(ctx)
}

func (s *Service) enqueueRecord(ctx context.Context, event *pb.RecordRowsUpdated) (retErr error) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	return s.enqueueRecordRun(ctx, run, event)
}

func (s *Service) enqueueRecordRun(ctx context.Context, run *serviceRun, event *pb.RecordRowsUpdated) (retErr error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if event == nil {
		return nil
	}
	if run == nil || run.recordBatcher == nil {
		return errors.New("view builder record batcher is not started")
	}
	batcher := run.recordBatcher
	addCtx := run.ctx
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
	return completion.wait(ctx)
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
