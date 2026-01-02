package cloudnode

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/types"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultHeartbeatFlushInterval = 1000 * time.Millisecond
	defaultHeartbeatBatchSize     = 200
)

type heartbeatUpdateQueue struct {
	dao dao.HeartbeatDAO

	flushInterval time.Duration
	maxBatch      int

	newRecordFn func(req *types.ReportHeartbeatRequest) *types.HeartbeatNode

	mu      sync.Mutex
	pending map[string]*heartbeatUpdate
	flushCh chan struct{}
}

type heartbeatUpdate struct {
	record *types.HeartbeatNode
	isNew  bool
}

func newHeartbeatUpdateQueue(dao dao.HeartbeatDAO, flushInterval time.Duration, maxBatch int, newRecordFn func(*types.ReportHeartbeatRequest) *types.HeartbeatNode) *heartbeatUpdateQueue {
	if flushInterval <= 0 {
		flushInterval = defaultHeartbeatFlushInterval
	}
	if maxBatch <= 0 {
		maxBatch = defaultHeartbeatBatchSize
	}
	return &heartbeatUpdateQueue{
		dao:           dao,
		flushInterval: flushInterval,
		maxBatch:      maxBatch,
		newRecordFn:   newRecordFn,
		pending:       make(map[string]*heartbeatUpdate),
		flushCh:       make(chan struct{}, 1),
	}
}

func (q *heartbeatUpdateQueue) Start() {
	go q.loop()
}

func (q *heartbeatUpdateQueue) Enqueue(ctx context.Context, req *types.ReportHeartbeatRequest) error {
	if req == nil {
		return fmt.Errorf("request is nil")
	}

	key := req.NodeID
	q.mu.Lock()
	if entry, ok := q.pending[key]; ok {
		applyHeartbeatUpdate(entry.record, req)
		q.mu.Unlock()
		return nil
	}
	q.mu.Unlock()

	record, err := q.dao.GetNodeByID(ctx, req.NodeID)
	if err != nil {
		return err
	}

	q.mu.Lock()
	if entry, ok := q.pending[key]; ok {
		applyHeartbeatUpdate(entry.record, req)
		q.mu.Unlock()
		return nil
	}

	if record == nil {
		record = q.newRecordFn(req)
		q.pending[key] = &heartbeatUpdate{record: record, isNew: true}
	} else {
		applyHeartbeatUpdate(record, req)
		q.pending[key] = &heartbeatUpdate{record: record, isNew: false}
	}
	shouldFlush := len(q.pending) >= q.maxBatch
	q.mu.Unlock()

	if shouldFlush {
		q.signalFlush()
	}
	return nil
}

func (q *heartbeatUpdateQueue) loop() {
	ticker := time.NewTicker(q.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			q.flush(trpc.BackgroundContext())
		case <-q.flushCh:
			q.flush(trpc.BackgroundContext())
		}
	}
}

func (q *heartbeatUpdateQueue) flush(ctx context.Context) {
	batch := q.drain()
	if len(batch) == 0 {
		return
	}

	var updates []*types.HeartbeatNode
	var creates []*types.HeartbeatNode

	for _, entry := range batch {
		if entry.isNew {
			creates = append(creates, entry.record)
		} else {
			updates = append(updates, entry.record)
		}
	}

	if len(updates) > 0 {
		if err := q.dao.BatchUpdate(ctx, updates); err != nil {
			log.ErrorContextf(ctx, "[HeartbeatQueue] batch update failed: %v", err)
			return
		}
	}

	for _, record := range creates {
		if err := q.dao.Create(ctx, record); err != nil {
			log.ErrorContextf(ctx, "[HeartbeatQueue] create heartbeat record failed: %v", err)
		}
	}
}

func (q *heartbeatUpdateQueue) drain() []*heartbeatUpdate {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.pending) == 0 {
		return nil
	}

	batch := make([]*heartbeatUpdate, 0, len(q.pending))
	for _, entry := range q.pending {
		batch = append(batch, entry)
	}
	q.pending = make(map[string]*heartbeatUpdate)
	return batch
}

func (q *heartbeatUpdateQueue) signalFlush() {
	select {
	case q.flushCh <- struct{}{}:
	default:
	}
}

func applyHeartbeatUpdate(record *types.HeartbeatNode, req *types.ReportHeartbeatRequest) {
	now := time.Now()
	if req.Timestamp != nil {
		now = *req.Timestamp
	}

	record.LastHeartbeat = &now
	record.TotalHeartbeats++
	record.ConsecutiveTimeouts = 0

	if req.SourceService != "" {
		record.SourceService = req.SourceService
	}

	if req.Metadata != nil {
		if record.Metadata == nil {
			record.Metadata = make(map[string]interface{})
		}
		for key, value := range req.Metadata {
			record.Metadata[key] = value
		}
	}
}
