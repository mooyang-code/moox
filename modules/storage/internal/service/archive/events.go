package archive

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

type RowsCommittedHandler func(ctx context.Context, event any) error

type EventConsumerOptions struct {
	Events           eventbus.Subscriber
	HandleTimeSeries eventbus.TimeSeriesRowsCommittedHandler
	HandleRecord     eventbus.RecordRowsCommittedHandler
}

// EventConsumer subscribes the archive runtime to storage row-change events.
type EventConsumer struct {
	events           eventbus.Subscriber
	handleTimeSeries eventbus.TimeSeriesRowsCommittedHandler
	handleRecord     eventbus.RecordRowsCommittedHandler

	mu            sync.Mutex
	timeSeriesSub eventbus.Subscription
	recordSub     eventbus.Subscription
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
	return &EventConsumer{
		events:           opts.Events,
		handleTimeSeries: handleTimeSeries,
		handleRecord:     handleRecord,
	}
}

func (c *EventConsumer) Start(ctx context.Context) error {
	if c == nil {
		return errors.New("archive event consumer is nil")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
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

	timeSeriesSub, err := c.events.SubscribeTimeSeriesRowsCommitted(ctx, c.handleTimeSeries)
	if err != nil {
		c.clearStartedState()
		return err
	}
	recordSub, err := c.events.SubscribeRecordRowsCommitted(ctx, c.handleRecord)
	if err != nil {
		_ = timeSeriesSub.Close()
		c.clearStartedState()
		return err
	}

	c.mu.Lock()
	c.timeSeriesSub = timeSeriesSub
	c.recordSub = recordSub
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

	c.mu.Lock()
	c.timeSeriesSub = nil
	c.recordSub = nil
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

func noopTimeSeriesArchiveEvent(ctx context.Context, event *pb.TimeSeriesRowsCommitted) error {
	log.DebugContextf(ctx, "[Archive] received time-series rows committed writes=%d", len(event.GetWrites()))
	return nil
}

func noopRecordArchiveEvent(ctx context.Context, event *pb.RecordRowsCommitted) error {
	log.DebugContextf(ctx, "[Archive] received record rows committed writes=%d", len(event.GetWrites()))
	return nil
}
