package datashard

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	trpc "trpc.group/trpc-go/trpc-go"
)

type OutboxConfig struct {
	FlushBatchSize int
	FlushMaxBytes  int
	FlushInterval  time.Duration
	MaxRows        int
	MaxBytes       int
	MaxAge         time.Duration
	BackoffBase    time.Duration
	BackoffMax     time.Duration
}

func (c OutboxConfig) normalized() OutboxConfig {
	if c.FlushBatchSize <= 0 {
		c.FlushBatchSize = 100
	}
	if c.FlushMaxBytes <= 0 {
		c.FlushMaxBytes = 1 << 20
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = 200 * time.Millisecond
	}
	if c.MaxRows <= 0 {
		c.MaxRows = 100000
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = 256 << 20
	}
	if c.MaxAge <= 0 {
		c.MaxAge = 24 * time.Hour
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 200 * time.Millisecond
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	return c
}

type OutboxRelay struct {
	store     contracts.FactStore
	publisher MessagePublisher
	cfg       OutboxConfig
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	failures  atomic.Uint64
	lastAck   atomic.Int64
}

func NewOutboxRelay(store contracts.FactStore, publisher MessagePublisher, cfg OutboxConfig) *OutboxRelay {
	return &OutboxRelay{store: store, publisher: publisher, cfg: cfg.normalized()}
}

func (r *OutboxRelay) Start(ctx context.Context) {
	if r == nil || r.store == nil || r.publisher == nil || r.stop != nil {
		return
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	go r.loop(ctx)
}

func (r *OutboxRelay) Close() error {
	if r == nil || r.stop == nil {
		return nil
	}
	r.closeOnce.Do(func() { close(r.stop) })
	<-r.done
	return nil
}

func (r *OutboxRelay) loop(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.cfg.FlushInterval)
	defer ticker.Stop()
	backoff := r.cfg.BackoffBase
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
		}
		count, err := r.flush(ctx)
		if err != nil {
			r.failures.Add(1)
			d := backoff + time.Duration(rand.Int63n(int64(backoff/4+1)))
			if d > r.cfg.BackoffMax {
				d = r.cfg.BackoffMax
			}
			select {
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			case <-time.After(d):
			}
			if backoff < r.cfg.BackoffMax/2 {
				backoff *= 2
			}
			continue
		}
		backoff = r.cfg.BackoffBase
		if count == 0 {
			continue
		}
	}
}

func (r *OutboxRelay) flush(ctx context.Context) (int, error) {
	entries, err := r.store.ListOutbox(ctx, 0, r.cfg.FlushBatchSize, r.cfg.FlushMaxBytes)
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	// A shard's outbox is a single ordered lane. Publish and acknowledge one
	// entry at a time so a later sequence can never overtake a failed entry.
	var confirmed []uint64
	for _, entry := range entries {
		if entry == nil {
			return len(confirmed), errors.New("storage outbox contains nil entry")
		}
		if err := r.publisher.PublishMessage(ctx, entry.Data); err != nil {
			if len(confirmed) > 0 {
				if deleteErr := r.store.DeleteOutbox(ctx, confirmed); deleteErr != nil {
					return len(confirmed), errors.Join(err, deleteErr)
				}
			}
			return len(confirmed), err
		}
		confirmed = append(confirmed, entry.Sequence)
		r.lastAck.Store(time.Now().UnixNano())
	}
	if err := r.store.DeleteOutbox(ctx, confirmed); err != nil {
		return len(confirmed), err
	}
	return len(confirmed), nil
}

func (r *OutboxRelay) FailureCount() uint64 {
	if r == nil {
		return 0
	}
	return r.failures.Load()
}
func (r *OutboxRelay) LastAckTime() time.Time {
	if r == nil || r.lastAck.Load() == 0 {
		return time.Time{}
	}
	return time.Unix(0, r.lastAck.Load())
}
