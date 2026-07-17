package primary

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
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
	store     device.FactStore
	publisher EnvelopePublisher
	cfg       OutboxConfig
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	failures  atomic.Uint64
	lastAck   atomic.Int64
}

func NewOutboxRelay(store device.FactStore, publisher EnvelopePublisher, cfg OutboxConfig) *OutboxRelay {
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
	messages := make([][]byte, len(entries))
	for i, entry := range entries {
		messages[i] = entry.Data
	}
	acks := make([]error, len(entries))
	if batch, ok := r.publisher.(interface {
		PublishEnvelopes(context.Context, [][]byte) []error
	}); ok {
		acks = batch.PublishEnvelopes(ctx, messages)
	} else {
		for i, data := range messages {
			acks[i] = r.publisher.PublishEnvelope(ctx, data)
		}
	}
	var success []uint64
	for i, err := range acks {
		if err == nil {
			success = append(success, entries[i].Sequence)
			r.lastAck.Store(time.Now().UnixNano())
		}
	}
	if len(success) > 0 {
		if err := r.store.DeleteOutbox(ctx, success); err != nil {
			return len(success), err
		}
	}
	for _, err := range acks {
		if err != nil {
			return len(success), err
		}
	}
	return len(success), nil
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
