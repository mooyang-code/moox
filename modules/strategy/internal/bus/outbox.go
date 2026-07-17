package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type Publisher interface {
	Publish(context.Context, domain.OutboxMessage) error
}

type OutboxStore interface {
	ListPendingOutbox(context.Context, int, time.Time) ([]domain.OutboxMessage, error)
	ClaimOutbox(context.Context, string, string, time.Time, time.Duration) (bool, error)
	ReleaseOutbox(context.Context, string, string) error
	MarkOutboxPublished(context.Context, string, string) error
}

type Relay struct {
	Store      OutboxStore
	Publisher  Publisher
	Lease      time.Duration
	Now        func() time.Time
	lastErrMu  sync.RWMutex
	lastErr    error
	tokenIndex atomic.Uint64
	mu         sync.Mutex
}

func (r *Relay) PublishPending(ctx context.Context, limit int) error {
	if r == nil || r.Store == nil || r.Publisher == nil {
		return errors.New("strategy outbox relay dependencies are required")
	}
	if limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	rows, err := r.Store.ListPendingOutbox(ctx, limit, now)
	if err != nil {
		return r.recordError(err)
	}
	for _, row := range rows {
		token := fmt.Sprintf("strategy-relay-%d-%d", now.UnixNano(), r.tokenIndex.Add(1))
		claimed, claimErr := r.Store.ClaimOutbox(ctx, row.MessageID, token, now, r.lease())
		if claimErr != nil {
			return r.recordError(claimErr)
		}
		if !claimed {
			continue
		}
		if publishErr := r.Publisher.Publish(ctx, row); publishErr != nil {
			releaseErr := r.Store.ReleaseOutbox(ctx, row.MessageID, token)
			return r.recordError(errors.Join(publishErr, releaseErr))
		}
		if markErr := r.Store.MarkOutboxPublished(ctx, row.MessageID, token); markErr != nil {
			releaseErr := r.Store.ReleaseOutbox(ctx, row.MessageID, token)
			return r.recordError(errors.Join(markErr, releaseErr))
		}
	}
	r.recordError(nil)
	return nil
}

func (r *Relay) Run(ctx context.Context, interval time.Duration, batchSize int) error {
	if interval <= 0 {
		return errors.New("strategy outbox relay interval must be positive")
	}
	if batchSize <= 0 {
		return errors.New("strategy outbox relay batch size must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = r.PublishPending(ctx, batchSize)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Relay) LastError() error {
	if r == nil {
		return nil
	}
	r.lastErrMu.RLock()
	defer r.lastErrMu.RUnlock()
	return r.lastErr
}

func (r *Relay) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Relay) lease() time.Duration {
	if r.Lease > 0 {
		return r.Lease
	}
	return 30 * time.Second
}

func (r *Relay) recordError(err error) error {
	r.lastErrMu.Lock()
	r.lastErr = err
	r.lastErrMu.Unlock()
	return err
}
