package outbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type Publisher interface {
	PublishMessage(context.Context, []byte) error
}

// PublishAckPublisher is optional so existing publishers and tests can keep
// the small Publisher contract. JetStream-backed publishers can expose its
// duplicate acknowledgement without making duplicate state part of storage's
// required publisher interface.
type PublishAckPublisher interface {
	PublishMessageWithAck(context.Context, []byte) (*jetstream.PublishAck, error)
}

type RelayOptions struct {
	PollInterval                  time.Duration
	BatchSize                     int
	Metrics                       *observability.ViewMetrics
	ProcessedEventCleanupInterval time.Duration
	ProcessedEventRetention       time.Duration
	// DeleteOutbox is injectable for recovery tests. Production uses Pebble's
	// delete operation, while tests can force a publish/delete split.
	DeleteOutbox  func(context.Context, []uint64) error
	ErrorReporter func(error)
}

// Relay is intentionally single-threaded. A publish failure stops the
// loop before later IDs are attempted; only the contiguous successful prefix
// is removed from Pebble.
type Relay struct {
	store     *pebble.Store
	publisher Publisher
	options   RelayOptions
	metrics   *observability.ViewMetrics
	stop      chan struct{}
	done      chan struct{}
	started   chan struct{}
	once      sync.Once
	startOnce sync.Once
}

func NewRelay(store *pebble.Store, publisher Publisher, opts RelayOptions) (*Relay, error) {
	if store == nil || publisher == nil {
		return nil, errors.New("store and publisher are required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 100 * time.Millisecond
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 100
	}
	if opts.ProcessedEventCleanupInterval <= 0 {
		opts.ProcessedEventCleanupInterval = time.Hour
	}
	if opts.ProcessedEventRetention <= 0 {
		opts.ProcessedEventRetention = store.ProcessedEventRetention()
	}
	if opts.Metrics == nil {
		opts.Metrics = observability.DefaultViewMetrics
	}
	if opts.DeleteOutbox == nil {
		opts.DeleteOutbox = store.DeleteOutbox
	}
	return &Relay{store: store, publisher: publisher, options: opts, metrics: opts.Metrics, stop: make(chan struct{}), done: make(chan struct{}), started: make(chan struct{})}, nil
}

func (r *Relay) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		close(r.started)
		go func() {
			defer close(r.done)
			ticker := time.NewTicker(r.options.PollInterval)
			defer ticker.Stop()
			cleanupTicker := time.NewTicker(r.options.ProcessedEventCleanupInterval)
			defer cleanupTicker.Stop()
			if err := r.cleanupProcessedSourceEvents(ctx); err != nil {
				r.report(fmt.Errorf("cleanup processed source events: %w", err))
			}
			for {
				if err := r.flush(ctx); err != nil {
					r.report(err)
					// Keep the failed entry for the next poll. There is no skip path.
					select {
					case <-time.After(r.options.PollInterval):
					case <-r.stop:
						return
					case <-ctx.Done():
						return
					}
				}
				select {
				case <-ticker.C:
				case <-r.stop:
					return
				case <-ctx.Done():
					return
				case <-cleanupTicker.C:
					if err := r.cleanupProcessedSourceEvents(ctx); err != nil {
						r.report(fmt.Errorf("cleanup processed source events: %w", err))
					}
				}
			}
		}()
	})
}

func (r *Relay) cleanupProcessedSourceEvents(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-r.options.ProcessedEventRetention)
	_, err := r.store.CleanupProcessedSourceEventsBefore(ctx, cutoff)
	return err
}

func (r *Relay) report(err error) {
	if err == nil {
		return
	}
	if r.options.ErrorReporter != nil {
		r.options.ErrorReporter(err)
		return
	}
	log.Printf("storage outbox relay flush failed: %v", err)
}

func (r *Relay) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stop) })
	select {
	case <-r.started:
		<-r.done
	default:
	}
}

func (r *Relay) Flush(ctx context.Context) error { return r.flush(ctx) }

func (r *Relay) flush(ctx context.Context) error {
	if err := r.observeOutboxStats(ctx); err != nil {
		return err
	}
	var err error
	entries, err := r.store.ListOutbox(ctx, 0, r.options.BatchSize)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	confirmed := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		data, err := r.store.PrepareOutboxPublication(ctx, entry.ID, time.Now().UTC())
		if err != nil {
			if len(confirmed) > 0 {
				err = errors.Join(err, r.cleanupConfirmed(ctx, confirmed))
			}
			return err
		}
		ack, err := r.publish(ctx, data)
		if err != nil {
			r.metrics.IncOutboxPublishError()
			if len(confirmed) > 0 {
				err = errors.Join(err, r.cleanupConfirmed(ctx, confirmed))
			}
			return err
		}
		if ack != nil && ack.Duplicate {
			r.metrics.IncOutboxDuplicatePublish()
		}
		confirmed = append(confirmed, entry.ID)
	}
	err = r.options.DeleteOutbox(ctx, confirmed)
	if err == nil {
		err = r.observeOutboxStats(ctx)
	}
	return err
}

func (r *Relay) cleanupConfirmed(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	var result error
	if err := r.options.DeleteOutbox(ctx, ids); err != nil {
		result = errors.Join(result, fmt.Errorf("delete confirmed outbox entries: %w", err))
	}
	if err := r.observeOutboxStats(ctx); err != nil {
		result = errors.Join(result, fmt.Errorf("observe outbox after partial cleanup: %w", err))
	}
	return result
}

func (r *Relay) observeOutboxStats(ctx context.Context) error {
	stats, err := r.store.OutboxStats(ctx)
	if err != nil {
		return err
	}
	r.metrics.SetOutboxSnapshotAt(stats.Pending, stats.OldestEventAt)
	return nil
}

func (r *Relay) publish(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if publisher, ok := r.publisher.(PublishAckPublisher); ok {
		return publisher.PublishMessageWithAck(ctx, data)
	}
	return nil, r.publisher.PublishMessage(ctx, data)
}
