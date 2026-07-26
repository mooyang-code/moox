package outbox

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type Publisher interface {
	Publish(context.Context, domain.OutboxMessage) error
}

type OutboxStore interface {
	ListPendingOutbox(context.Context, int) ([]domain.OutboxMessage, error)
	DeleteOutbox(context.Context, string) error
}

type Relay struct {
	Store     OutboxStore
	Publisher Publisher
	Now       func() time.Time
	lastErrMu sync.RWMutex
	lastErr   error
	mu        sync.Mutex
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
	rows, err := r.Store.ListPendingOutbox(ctx, limit)
	if err != nil {
		return r.recordError(err)
	}
	for _, row := range rows {
		if publishErr := r.Publisher.Publish(ctx, row); publishErr != nil {
			return r.recordError(publishErr)
		}
		if deleteErr := r.Store.DeleteOutbox(ctx, row.MessageID); deleteErr != nil {
			return r.recordError(deleteErr)
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

func (r *Relay) recordError(err error) error {
	r.lastErrMu.Lock()
	r.lastErr = err
	r.lastErrMu.Unlock()
	return err
}
