package builder

import (
	"context"
	"errors"
	"fmt"
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
	lane              chan laneRequest
	sequenceByShard   map[string]uint64
	wg                sync.WaitGroup
	startOnce         sync.Once
	startDone         chan struct{}
	stopOnce          sync.Once
	stopDone          chan struct{}
	stopErr           error
}

type laneRequest struct {
	ctx      context.Context
	shardID  string
	sequence uint64
	fn       func(context.Context) error
	result   chan error
}

func newServiceRun(parent context.Context, opts BatchOptions) *serviceRun {
	ctx, cancel := context.WithCancel(parent)
	return &serviceRun{
		ctx: ctx, cancel: cancel,
		timeSeriesBatcher: newBatcher[timeSeriesDeriveItem](opts),
		recordBatcher:     newBatcher[recordDeriveItem](opts),
		lane:              make(chan laneRequest),
		sequenceByShard:   make(map[string]uint64),
		startDone:         make(chan struct{}),
		stopDone:          make(chan struct{}),
	}
}

func (r *serviceRun) dispatch(ctx context.Context, shardID string, sequence uint64, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	request := laneRequest{ctx: ctx, shardID: shardID, sequence: sequence, fn: fn, result: make(chan error, 1)}
	select {
	case r.lane <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *serviceRun) runLane() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case request := <-r.lane:
			if request.sequence != 0 && request.shardID != "" {
				last := r.sequenceByShard[request.shardID]
				if last != 0 && request.sequence != last+1 {
					request.result <- fmt.Errorf("view builder shard %q sequence gap or reorder: got %d after %d", request.shardID, request.sequence, last)
					continue
				}
			}
			err := request.fn(request.ctx)
			if err == nil && request.sequence != 0 && request.shardID != "" {
				r.sequenceByShard[request.shardID] = request.sequence
			}
			request.result <- err
		}
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
	delete     bool
	completion *deriveCompletion
}

type recordDeriveItem struct {
	row        *pb.RecordRow
	delete     bool
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
	run.wg.Add(3 + 2*s.maxWorkers)
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
	go run.runLane()
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

	timeSeriesSub, err := s.events.SubscribeTimeSeriesRowsCommitted(run.ctx, func(handlerCtx context.Context, event *pb.TimeSeriesRowsCommitted) error {
		return run.dispatch(handlerCtx, event.GetShardId(), event.GetSequence(), func(ctx context.Context) error {
			return s.enqueueTimeSeriesRun(ctx, run, event)
		})
	})
	if err != nil {
		run.finishStart()
		_ = run.stop()
		s.clearRun(run)
		return err
	}
	run.timeSeriesSub = timeSeriesSub
	recordSub, err := s.events.SubscribeRecordRowsCommitted(run.ctx, func(handlerCtx context.Context, event *pb.RecordRowsCommitted) error {
		return run.dispatch(handlerCtx, event.GetShardId(), event.GetSequence(), func(ctx context.Context) error {
			return s.enqueueRecordRun(ctx, run, event)
		})
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

func (s *Service) Ready() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run != nil
}

func (s *Service) clearRun(run *serviceRun) {
	s.mu.Lock()
	if s.run == run {
		s.run = nil
	}
	s.mu.Unlock()
}

func (s *Service) enqueueTimeSeries(ctx context.Context, event *pb.TimeSeriesRowsCommitted) (retErr error) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	return s.enqueueTimeSeriesRun(ctx, run, event)
}

func (s *Service) enqueueTimeSeriesRun(ctx context.Context, run *serviceRun, event *pb.TimeSeriesRowsCommitted) (retErr error) {
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
	rows := make([]*pb.TimeSeriesRow, 0, len(event.GetWrites()))
	deletes := make([]*pb.TimeSeriesRow, 0, len(event.GetWrites()))
	for _, write := range event.GetWrites() {
		row := write.GetRow()
		if row == nil {
			continue
		}
		if write.GetOperation() == pb.RowWriteOperation_ROW_WRITE_OPERATION_DELETE {
			deletes = append(deletes, proto.Clone(row).(*pb.TimeSeriesRow))
		} else {
			rows = append(rows, proto.Clone(row).(*pb.TimeSeriesRow))
		}
	}
	completion := newDeriveCompletion(len(rows) + len(deletes))
	s.viewMetrics().IncDeriveInFlight()
	defer func() {
		s.viewMetrics().DecDeriveInFlight()
		s.viewMetrics().ObserveDerive("time_series", deriveResult(retErr))
	}()
	for i, row := range append(deletes, rows...) {
		item := timeSeriesDeriveItem{
			row:        row,
			delete:     i < len(deletes),
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

func (s *Service) enqueueRecord(ctx context.Context, event *pb.RecordRowsCommitted) (retErr error) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	return s.enqueueRecordRun(ctx, run, event)
}

func (s *Service) enqueueRecordRun(ctx context.Context, run *serviceRun, event *pb.RecordRowsCommitted) (retErr error) {
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
	rows := make([]*pb.RecordRow, 0, len(event.GetWrites()))
	deletes := make([]*pb.RecordRow, 0, len(event.GetWrites()))
	for _, write := range event.GetWrites() {
		row := write.GetRow()
		if row == nil {
			continue
		}
		if write.GetOperation() == pb.RowWriteOperation_ROW_WRITE_OPERATION_DELETE {
			deletes = append(deletes, proto.Clone(row).(*pb.RecordRow))
		} else {
			rows = append(rows, proto.Clone(row).(*pb.RecordRow))
		}
	}
	completion := newDeriveCompletion(len(rows) + len(deletes))
	s.viewMetrics().IncDeriveInFlight()
	defer func() {
		s.viewMetrics().DecDeriveInFlight()
		s.viewMetrics().ObserveDerive("record", deriveResult(retErr))
	}()
	for i, row := range append(deletes, rows...) {
		item := recordDeriveItem{
			row:        row,
			delete:     i < len(deletes),
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
	deletes := make([]*pb.TimeSeriesRow, 0, len(items))
	for _, item := range items {
		if item.row != nil {
			if item.delete {
				deletes = append(deletes, item.row)
			} else {
				rows = append(rows, item.row)
			}
		}
	}
	err := s.processTimeSeriesRowsBatch(ctx, rows, deletes)
	s.viewMetrics().ObserveBatch("duckdb", deriveResult(err), time.Since(started))
	for _, item := range items {
		item.completion.complete(err)
	}
}

func (s *Service) processRecordItemBatch(ctx context.Context, items []recordDeriveItem) {
	started := time.Now()
	rows := make([]*pb.RecordRow, 0, len(items))
	deletes := make([]*pb.RecordRow, 0, len(items))
	for _, item := range items {
		if item.row != nil {
			if item.delete {
				deletes = append(deletes, item.row)
			} else {
				rows = append(rows, item.row)
			}
		}
	}
	err := s.processRecordRowsBatch(ctx, rows, deletes)
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
