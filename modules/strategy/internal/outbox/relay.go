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
		return err
	}
	for _, row := range rows {
		if publishErr := r.Publisher.Publish(ctx, row); publishErr != nil {
			return publishErr
		}
		if deleteErr := r.Store.DeleteOutbox(ctx, row.MessageID); deleteErr != nil {
			return deleteErr
		}
	}
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
