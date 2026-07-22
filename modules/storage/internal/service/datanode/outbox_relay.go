package datanode

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
)

type Publisher interface {
	PublishMessage(context.Context, []byte) error
}

type OutboxRelayOptions struct {
	PollInterval time.Duration
	BatchSize    int
}

// OutboxRelay is intentionally single-threaded. A publish failure stops the
// loop before later IDs are attempted; only the contiguous successful prefix
// is removed from Pebble.
type OutboxRelay struct {
	store     *pebble.Store
	publisher Publisher
	options   OutboxRelayOptions
	stop      chan struct{}
	done      chan struct{}
	started   chan struct{}
	once      sync.Once
	startOnce sync.Once
}

func NewOutboxRelay(store *pebble.Store, publisher Publisher, opts OutboxRelayOptions) (*OutboxRelay, error) {
	if store == nil || publisher == nil {
		return nil, errors.New("store and publisher are required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 100 * time.Millisecond
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 100
	}
	return &OutboxRelay{store: store, publisher: publisher, options: opts, stop: make(chan struct{}), done: make(chan struct{}), started: make(chan struct{})}, nil
}

func (r *OutboxRelay) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		close(r.started)
		go func() {
			defer close(r.done)
			ticker := time.NewTicker(r.options.PollInterval)
			defer ticker.Stop()
			for {
				if err := r.flush(ctx); err != nil {
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
				}
			}
		}()
	})
}

func (r *OutboxRelay) Close() {
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

func (r *OutboxRelay) Flush(ctx context.Context) error { return r.flush(ctx) }

func (r *OutboxRelay) flush(ctx context.Context) error {
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
				_ = r.store.DeleteOutbox(ctx, confirmed)
			}
			return err
		}
		if err := r.publisher.PublishMessage(ctx, data); err != nil {
			if len(confirmed) > 0 {
				_ = r.store.DeleteOutbox(ctx, confirmed)
			}
			return err
		}
		confirmed = append(confirmed, entry.ID)
	}
	return r.store.DeleteOutbox(ctx, confirmed)
}
