package outbox

import (
	"context"
	"errors"
	"sync"

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
			if errors.Is(publishErr, ErrExpiredOutboxMessage) {
				if deleteErr := r.Store.DeleteOutbox(ctx, row.MessageID); deleteErr != nil {
					return deleteErr
				}
				continue
			}
			return publishErr
		}
		if deleteErr := r.Store.DeleteOutbox(ctx, row.MessageID); deleteErr != nil {
			return deleteErr
		}
	}
	return nil
}
