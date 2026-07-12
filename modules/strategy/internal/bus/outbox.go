package bus

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"sync"
	"time"
)

type Publisher interface {
	Publish(context.Context, string, []byte) error
}
type IdempotentPublisher interface {
	PublishWithID(context.Context, string, string, []byte) error
}
type OutboxStore interface {
	ListPendingOutbox(context.Context, int, time.Time) ([]domain.OutboxMessage, error)
	ClaimOutbox(context.Context, string, string, time.Time, time.Duration) (bool, error)
	ReleaseOutbox(context.Context, string, string) error
	MarkOutboxPublished(context.Context, string, string) error
}
type Relay struct {
	Store     OutboxStore
	Publisher Publisher
	mu        sync.Mutex
}

func (r *Relay) PublishPending(ctx context.Context, limit int) error {
	if r == nil || r.Store == nil || r.Publisher == nil {
		return fmt.Errorf("strategy outbox relay dependencies are required")
	}
	if limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	rows, err := r.Store.ListPendingOutbox(ctx, limit, now)
	if err != nil {
		return err
	}
	for _, row := range rows {
		token := fmt.Sprintf("relay-%d", time.Now().UnixNano())
		claimed, claimErr := r.Store.ClaimOutbox(ctx, row.MessageID, token, now, 30*time.Second)
		if claimErr != nil {
			return claimErr
		}
		if !claimed {
			continue
		}
		var publishErr error
		if p, ok := r.Publisher.(IdempotentPublisher); ok {
			publishErr = p.PublishWithID(ctx, row.MessageID, row.Topic, row.Payload)
		} else {
			publishErr = r.Publisher.Publish(ctx, row.Topic, row.Payload)
		}
		if publishErr != nil {
			_ = r.Store.ReleaseOutbox(ctx, row.MessageID, token)
			return publishErr
		}
		if err := r.Store.MarkOutboxPublished(ctx, row.MessageID, token); err != nil {
			return err
		}
	}
	return nil
}
