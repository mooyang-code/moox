package builder

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const defaultMaxWorkers = 1
const journalProcessorWorkers = 1

// Service consumes storage row-update events and updates derived view stores.
type Service struct {
	events        eventbus.Bus
	reader        FactReader
	metadata      viewsvc.Metadata
	views         TimeSeriesViewWriter
	search        RecordViewIndexer
	batchOpts     BatchOptions
	maxWorkers    int
	viewWriters   *viewWriterPool
	recordWriters *recordWriterPool

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
	journal *pb.TimeSeriesRowsUpdated
}

type recordDeriveItem struct {
	journal *pb.RecordRowsUpdated
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
	return &Service{
		events:        opts.Events,
		reader:        opts.Reader,
		metadata:      opts.Metadata,
		views:         opts.Views,
		search:        opts.Search,
		batchOpts:     batchOpts,
		maxWorkers:    maxWorkers,
		viewWriters:   newViewWriterPool(opts.Views),
		recordWriters: newRecordWriterPool(opts.Search),
	}
}

// Start subscribes the view builder service to row-update events.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("view builder service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	subscriber, ok := s.events.(eventbus.Subscriber)
	if !ok {
		return errors.New("view builder service requires subscribable event bus")
	}
	if s.reader == nil {
		return errors.New("view builder service requires fact reader")
	}
	if s.metadata == nil {
		return errors.New("view builder service requires metadata client")
	}
	if s.views == nil {
		return errors.New("view builder service requires time-series view writer")
	}
	if s.search == nil {
		return errors.New("view builder service requires record view indexer")
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
	s.viewWriters = newViewWriterPool(s.views)
	s.recordWriters = newRecordWriterPool(s.search)
	timeSeriesOut := make(chan []timeSeriesDeriveItem, s.maxWorkers)
	recordOut := make(chan []recordDeriveItem, s.maxWorkers)
	processCtx := trpc.CloneContext(runCtx)
	s.wg.Add(2 + 2*journalProcessorWorkers)
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
	for i := 0; i < journalProcessorWorkers; i++ {
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

	timeSeriesSub, err := subscriber.SubscribeTimeSeriesRowsUpdated(ctx, s.enqueueTimeSeries)
	if err != nil {
		s.clearStartedState(cancel)
		return err
	}
	recordSub, err := subscriber.SubscribeRecordRowsUpdated(ctx, s.enqueueRecord)
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
	if s.viewWriters != nil {
		s.viewWriters.close()
	}
	if s.recordWriters != nil {
		s.recordWriters.close()
	}

	s.mu.Lock()
	s.cancel = nil
	s.runCtx = nil
	s.timeSeriesSub = nil
	s.recordSub = nil
	s.timeSeriesBatcher = nil
	s.recordBatcher = nil
	s.viewWriters = nil
	s.recordWriters = nil
	s.mu.Unlock()
	return err
}

func (s *Service) insertTimeSeriesRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	if s.viewWriters == nil {
		s.viewWriters = newViewWriterPool(s.views)
	}
	return s.viewWriters.insert(ctx, tableName, rows)
}

func (s *Service) indexRecordRows(ctx context.Context, resultName string, columns []*pb.ViewColumn, rows []*pb.RecordRow) error {
	if s.recordWriters == nil {
		s.recordWriters = newRecordWriterPool(s.search)
	}
	return s.recordWriters.index(ctx, resultName, columns, rows)
}

func (s *Service) enqueueTimeSeries(ctx context.Context, event *pb.TimeSeriesRowsUpdated) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if event == nil || len(event.GetRows()) == 0 {
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
	return batcher.add(addCtx, timeSeriesDeriveItem{
		journal: proto.Clone(event).(*pb.TimeSeriesRowsUpdated),
	})
}

func (s *Service) enqueueRecord(ctx context.Context, event *pb.RecordRowsUpdated) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if event == nil || len(event.GetRows()) == 0 {
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
	return batcher.add(addCtx, recordDeriveItem{
		journal: proto.Clone(event).(*pb.RecordRowsUpdated),
	})
}

func (s *Service) clearStartedState(cancel context.CancelFunc) {
	cancel()
	s.wg.Wait()
	s.mu.Lock()
	s.cancel = nil
	s.runCtx = nil
	s.timeSeriesBatcher = nil
	s.recordBatcher = nil
	if s.viewWriters != nil {
		s.viewWriters.close()
	}
	if s.recordWriters != nil {
		s.recordWriters.close()
	}
	s.viewWriters = nil
	s.recordWriters = nil
	s.mu.Unlock()
}

func (s *Service) processTimeSeriesItemBatch(ctx context.Context, items []timeSeriesDeriveItem) {
	rows := make([]*pb.TimeSeriesRow, 0, len(items))
	for _, item := range items {
		if item.journal != nil {
			rows = append(rows, item.journal.GetRows()...)
		}
	}
	if err := s.processTimeSeriesBatch(ctx, rows); err != nil {
		log.ErrorContextf(ctx, "[ViewBuilder] apply time-series write journal failed: %v", err)
	}
}

func (s *Service) processRecordItemBatch(ctx context.Context, items []recordDeriveItem) {
	rows := make([]*pb.RecordRow, 0, len(items))
	for _, item := range items {
		if item.journal != nil {
			rows = append(rows, item.journal.GetRows()...)
		}
	}
	if err := s.processRecordBatch(ctx, rows); err != nil {
		log.ErrorContextf(ctx, "[ViewBuilder] apply record write journal failed: %v", err)
	}
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

func markPending(ctx context.Context, store viewsvc.Metadata, item *pb.View) error {
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
