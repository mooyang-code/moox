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
	PublishTimeout                time.Duration
	Metrics                       *observability.ViewMetrics
	ProcessedEventCleanupInterval time.Duration
	ProcessedEventRetention       time.Duration
	// ReconnectAfterFailures lets a long-lived relay replace a stale NATS
	// connection after EventBus restarts instead of waiting for the process to
	// be restarted manually. It is ignored when the publisher does not expose
	// a Reconnect method.
	ReconnectAfterFailures int
	ReconnectCooldown      time.Duration
	// DeleteOutbox is injectable for recovery tests. Production uses Pebble's
	// delete operation, while tests can force a publish/delete split.
	DeleteOutbox  func(context.Context, []uint64) error
	ErrorReporter func(error)
}

// Relay is intentionally single-threaded. A publish failure stops the
// loop before later IDs are attempted; only the contiguous successful prefix
// is removed from Pebble.
type Relay struct {
	store           *pebble.Store
	publisher       Publisher
	options         RelayOptions
	metrics         *observability.ViewMetrics
	stop            chan struct{}
	done            chan struct{}
	started         chan struct{}
	once            sync.Once
	startOnce       sync.Once
	publishFailures int
	lastReconnect   time.Time
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
	if opts.PublishTimeout <= 0 {
		opts.PublishTimeout = 5 * time.Second
	}
	if opts.ProcessedEventCleanupInterval <= 0 {
		opts.ProcessedEventCleanupInterval = time.Hour
	}
	if opts.ProcessedEventRetention <= 0 {
		opts.ProcessedEventRetention = store.ProcessedEventRetention()
	}
	if opts.ReconnectAfterFailures <= 0 {
		opts.ReconnectAfterFailures = 3
	}
	if opts.ReconnectCooldown <= 0 {
		opts.ReconnectCooldown = 5 * time.Second
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

func (r *Relay) flush(ctx context.Context) error {
	// OutboxStats builds a Pebble iterator across every SSTable. On a large
	// DataNode this is an expensive operation, so the relay relies on the
	// atomic write/delete hint and only scans when it is unknown or non-empty.
	// This keeps an idle relay from monopolising a CPU while retaining a single
	// restart-time scan to discover legacy entries.
	if pending, known, mayHaveMore, oldest := r.store.OutboxPendingHint(); known && pending == 0 && !mayHaveMore {
		r.metrics.SetOutboxSnapshotAt(0, oldest)
		return nil
	}
	var err error
	entries, err := r.store.ListOutbox(ctx, 0, r.options.BatchSize)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		pending, _, _, oldest := r.store.OutboxPendingHint()
		r.metrics.SetOutboxSnapshotAt(pending, oldest)
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
		// A disconnected EventBus client must not wedge the single relay
		// goroutine forever. Keep the outbox entry for the next poll when the
		// bounded publish attempt expires; the EventID makes the retry idempotent.
		publishCtx, cancel := context.WithTimeout(ctx, r.options.PublishTimeout)
		ack, err := r.publish(publishCtx, data)
		cancel()
		if err != nil {
			r.metrics.IncOutboxPublishError()
			r.publishFailures++
			r.reconnectPublisher(ctx)
			if len(confirmed) > 0 {
				err = errors.Join(err, r.cleanupConfirmed(ctx, confirmed))
			}
			return err
		}
		if ack != nil && ack.Duplicate {
			r.metrics.IncOutboxDuplicatePublish()
		}
		confirmed = append(confirmed, entry.ID)
		r.publishFailures = 0
	}
	err = r.options.DeleteOutbox(ctx, confirmed)
	if err == nil {
		pending, _, _, oldest := r.store.OutboxPendingHint()
		r.metrics.SetOutboxSnapshotAt(pending, oldest)
	}
	return err
}

// reconnectPublisher is deliberately best-effort. The outbox entry remains
// pending when the replacement connection cannot be established, so the next
// poll retries both the reconnect and the publish without losing data.
func (r *Relay) reconnectPublisher(ctx context.Context) {
	if r.publishFailures < r.options.ReconnectAfterFailures {
		return
	}
	reconnecter, ok := r.publisher.(interface{ Reconnect(context.Context) error })
	if !ok {
		return
	}
	if !r.lastReconnect.IsZero() && time.Since(r.lastReconnect) < r.options.ReconnectCooldown {
		return
	}
	r.lastReconnect = time.Now()
	reconnectCtx, cancel := context.WithTimeout(ctx, r.options.PublishTimeout)
	defer cancel()
	if err := reconnecter.Reconnect(reconnectCtx); err != nil {
		r.report(fmt.Errorf("reconnect storage eventbus publisher: %w", err))
		return
	}
	r.publishFailures = 0
}

func (r *Relay) cleanupConfirmed(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	var result error
	if err := r.options.DeleteOutbox(ctx, ids); err != nil {
		result = errors.Join(result, fmt.Errorf("delete confirmed outbox entries: %w", err))
	}
	pending, _, _, oldest := r.store.OutboxPendingHint()
	r.metrics.SetOutboxSnapshotAt(pending, oldest)
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
