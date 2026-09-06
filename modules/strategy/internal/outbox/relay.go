package outbox

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

type Publisher interface {
	Publish(context.Context, domain.OutboxMessage) error
}

type OutboxStore interface {
	ListPendingOutbox(context.Context, int) ([]domain.OutboxMessage, error)
	DeleteOutbox(context.Context, string) error
}

type ResultStore interface {
	ListPendingResults(context.Context) ([]store.StrategyResult, error)
	PreparePendingResult(context.Context, string, time.Time) (store.StrategyResult, bool, error)
	TransitionPublishStatus(context.Context, string, store.PublishStatus, store.PublishStatus) error
}

type ResultPublisher interface {
	PublishResult(context.Context, store.StrategyResult) error
}

type Relay struct {
	Store     any
	Publisher any
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
	if resultStore, ok := r.Store.(ResultStore); ok {
		publisher, pubOK := r.Publisher.(ResultPublisher)
		if !pubOK {
			ok = false
		}
		if ok {
			rows, err := resultStore.ListPendingResults(ctx)
			if err != nil {
				return err
			}
			// Scan the complete pending set. A fixed prefix cap lets a run of
			// transiently failing rows starve every later result indefinitely.
			var firstErr error
			for _, row := range rows {
				prepared, valid, prepErr := resultStore.PreparePendingResult(ctx, row.ResultID, time.Now().UTC())
				if prepErr != nil {
					if firstErr == nil {
						firstErr = prepErr
					}
					continue
				}
				if !valid {
					continue
				}
				if pubErr := publisher.PublishResult(ctx, prepared); pubErr != nil {
					if firstErr == nil {
						firstErr = pubErr
					}
					continue
				}
				if statusErr := resultStore.TransitionPublishStatus(ctx, prepared.ResultID, store.PublishPending, store.PublishSent); statusErr != nil && firstErr == nil {
					firstErr = statusErr
				}
			}
			return firstErr
		}
	}
	legacyStore, ok := r.Store.(OutboxStore)
	if !ok {
		return errors.New("strategy outbox store is unavailable")
	}
	legacyPublisher, ok := r.Publisher.(Publisher)
	if !ok {
		return errors.New("strategy outbox publisher is unavailable")
	}
	rows, err := legacyStore.ListPendingOutbox(ctx, limit)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if publishErr := legacyPublisher.Publish(ctx, row); publishErr != nil {
			return publishErr
		}
		if deleteErr := legacyStore.DeleteOutbox(ctx, row.MessageID); deleteErr != nil {
			return deleteErr
		}
	}
	return nil
}
