package accountsync

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type LogicalAccountFactsObserver struct {
	Store         *store.Store
	Wake          func()
	RetryInterval time.Duration

	initOnce sync.Once
	signal   chan struct{}

	mu        sync.RWMutex
	pending   map[string]struct{}
	ready     bool
	lastError string
}

type FactsObserverSnapshot struct {
	Ready     bool
	LastError string
}

func (o *LogicalAccountFactsObserver) AccountFactsChanged(
	_ context.Context,
	exchangeAccountID string,
	external bool,
) error {
	if o == nil || o.Store == nil {
		return ErrServiceConfig
	}
	if o.Wake != nil {
		o.Wake()
	}
	if !external {
		return nil
	}
	o.init()
	o.mu.Lock()
	o.pending[exchangeAccountID] = struct{}{}
	o.mu.Unlock()
	select {
	case o.signal <- struct{}{}:
	default:
	}
	return nil
}

func (o *LogicalAccountFactsObserver) Run(ctx context.Context) error {
	if o == nil || o.Store == nil {
		return ErrServiceConfig
	}
	o.init()
	interval := o.RetryInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	o.setResult(o.runOnce(ctx))
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-o.signal:
		case <-ticker.C:
		}
		o.setResult(o.runOnce(ctx))
	}
}

func (o *LogicalAccountFactsObserver) Snapshot() FactsObserverSnapshot {
	if o == nil {
		return FactsObserverSnapshot{}
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return FactsObserverSnapshot{
		Ready: o.ready, LastError: o.lastError,
	}
}

func (o *LogicalAccountFactsObserver) runOnce(ctx context.Context) error {
	for {
		exchangeAccountID, found := o.pop()
		if !found {
			return nil
		}
		if err := o.pause(ctx, exchangeAccountID); err != nil {
			o.requeue(exchangeAccountID)
			return err
		}
	}
}

func (o *LogicalAccountFactsObserver) pause(
	ctx context.Context,
	exchangeAccountID string,
) error {
	account, err := o.Store.GetExchangeAccountByID(ctx, exchangeAccountID)
	if err != nil {
		return err
	}
	for {
		logicalAccount, _, findErr := o.Store.FindLogicalAccountByExchangeAccount(
			ctx, account.SpaceID, exchangeAccountID,
		)
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return nil
		}
		if findErr != nil {
			return findErr
		}
		unlock := o.Store.LockLogicalAccount(
			logicalAccount.SpaceID,
			logicalAccount.LogicalAccountID,
		)
		current, _, currentErr := o.Store.FindLogicalAccountByExchangeAccount(
			ctx, account.SpaceID, exchangeAccountID,
		)
		if currentErr == nil &&
			current.LogicalAccountID != logicalAccount.LogicalAccountID {
			unlock()
			continue
		}
		if errors.Is(currentErr, gorm.ErrRecordNotFound) {
			unlock()
			return nil
		}
		if currentErr != nil {
			unlock()
			return currentErr
		}
		err = o.Store.Transaction(ctx, func(tx *store.Tx) error {
			return tx.SetLogicalAccountAutomation(
				current.SpaceID,
				current.LogicalAccountID,
				"PAUSED",
				"EXTERNAL order or fill detected on "+exchangeAccountID,
			)
		})
		unlock()
		return err
	}
}

func (o *LogicalAccountFactsObserver) pop() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for exchangeAccountID := range o.pending {
		delete(o.pending, exchangeAccountID)
		return exchangeAccountID, true
	}
	return "", false
}

func (o *LogicalAccountFactsObserver) requeue(exchangeAccountID string) {
	o.mu.Lock()
	o.pending[exchangeAccountID] = struct{}{}
	o.mu.Unlock()
}

func (o *LogicalAccountFactsObserver) setResult(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ready = err == nil
	o.lastError = ""
	if err != nil && !errors.Is(err, context.Canceled) {
		o.lastError = err.Error()
	}
}

func (o *LogicalAccountFactsObserver) init() {
	o.initOnce.Do(func() {
		o.signal = make(chan struct{}, 1)
		o.pending = make(map[string]struct{})
	})
}
