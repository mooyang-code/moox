package archive

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

type RowsChangedHandler func(ctx context.Context, event any) error

type EventConsumerOptions struct {
	Events                eventbus.Subscriber
	HandleTimeSeries      eventbus.TimeSeriesRowsChangedHandler
	HandleRecord          eventbus.RecordRowsChangedHandler
	HandleRecordCommitted eventbus.RecordRowsCommittedHandler
}

// EventConsumer subscribes the archive runtime to storage row-change events.
type EventConsumer struct {
	events                eventbus.Subscriber
	handleTimeSeries      eventbus.TimeSeriesRowsChangedHandler
	handleRecord          eventbus.RecordRowsChangedHandler
	handleRecordCommitted eventbus.RecordRowsCommittedHandler

	mu            sync.Mutex
	timeSeriesSub eventbus.Subscription
	recordSub     eventbus.Subscription
	committedSub  eventbus.Subscription
	started       bool
}

func NewEventConsumer(opts EventConsumerOptions) *EventConsumer {
	handleTimeSeries := opts.HandleTimeSeries
	if handleTimeSeries == nil {
		handleTimeSeries = noopTimeSeriesArchiveEvent
	}
	handleRecord := opts.HandleRecord
	if handleRecord == nil {
		handleRecord = noopRecordArchiveEvent
	}
	handleRecordCommitted := opts.HandleRecordCommitted
	if handleRecordCommitted == nil {
		handleRecordCommitted = noopRecordCommittedArchiveEvent
	}
	return &EventConsumer{
		events:                opts.Events,
		handleTimeSeries:      handleTimeSeries,
		handleRecord:          handleRecord,
		handleRecordCommitted: handleRecordCommitted,
	}
}

func (c *EventConsumer) Start(ctx context.Context) error {
	if c == nil {
		return errors.New("archive event consumer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.events == nil {
		return errors.New("archive event consumer requires subscribable event bus")
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("archive event consumer is already started")
	}
	c.started = true
	c.mu.Unlock()

	timeSeriesSub, err := c.events.SubscribeTimeSeriesRowsChanged(ctx, c.handleTimeSeries)
	if err != nil {
		c.clearStartedState()
		return err
	}
	recordSub, err := c.events.SubscribeRecordRowsChanged(ctx, c.handleRecord)
	if err != nil {
		_ = timeSeriesSub.Close()
		c.clearStartedState()
		return err
	}
	committedSub, err := c.events.SubscribeRecordRowsCommitted(ctx, c.handleRecordCommitted)
	if err != nil {
		_ = timeSeriesSub.Close()
		_ = recordSub.Close()
		c.clearStartedState()
		return err
	}

	c.mu.Lock()
	c.timeSeriesSub = timeSeriesSub
	c.recordSub = recordSub
	c.committedSub = committedSub
	c.mu.Unlock()
	return nil
}

func (c *EventConsumer) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	timeSeriesSub := c.timeSeriesSub
	recordSub := c.recordSub
	committedSub := c.committedSub
	if !c.started {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	var err error
	if timeSeriesSub != nil {
		err = errors.Join(err, timeSeriesSub.Close())
	}
	if recordSub != nil {
		err = errors.Join(err, recordSub.Close())
	}
	if committedSub != nil {
		err = errors.Join(err, committedSub.Close())
	}

	c.mu.Lock()
	c.timeSeriesSub = nil
	c.recordSub = nil
	c.committedSub = nil
	c.started = false
	c.mu.Unlock()
	return err
}

func (c *EventConsumer) clearStartedState() {
	c.mu.Lock()
	c.started = false
	c.timeSeriesSub = nil
	c.recordSub = nil
	c.mu.Unlock()
}

func noopTimeSeriesArchiveEvent(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
	log.DebugContextf(ctx, "[Archive] received time-series rows changed event keys=%d", len(event.GetKeys()))
	return nil
}

func noopRecordArchiveEvent(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
	log.DebugContextf(ctx, "[Archive] received record rows changed event keys=%d", len(event.GetKeys()))
	return nil
}

func noopRecordCommittedArchiveEvent(ctx context.Context, event *pb.RecordRowsCommittedEvent) error {
	log.DebugContextf(ctx, "[Archive] received committed record event rows=%d", len(event.GetRows()))
	return nil
}
