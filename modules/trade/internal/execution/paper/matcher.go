package paper

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"sync/atomic"
	"time"
)

type Decision struct {
	Rest   bool
	Cancel bool
	Reason string
	Fill   exchange.Fill
}
type MatchReducer interface {
	ApplyFillToOrderTx(context.Context, *store.Tx, store.OrderRecord, exchange.Fill, consumer.Source) error
}
type Matcher struct {
	Store         *store.Store
	Reducer       MatchReducer
	Decide        func(store.OrderRecord) (Decision, error)
	DecideContext func(context.Context, store.OrderRecord) (Decision, error)
	Enqueue       func(string)
	Refresh       func(context.Context, string) error
	Now           func() time.Time
	ready         atomic.Bool
	lastError     atomic.Value
}
type MatcherState struct {
	Ready     bool
	LastError string
}

func (m *Matcher) SetReady(v bool) {
	if m != nil {
		m.ready.Store(v)
	}
}
func (m *Matcher) Ready() bool { return m != nil && m.ready.Load() }
func (m *Matcher) Scan(ctx context.Context) error {
	if m == nil || m.Store == nil {
		return fmt.Errorf("paper matcher: store is not configured")
	}
	orders, err := m.Store.ListPaperMatchCandidates(ctx, 0)
	if err != nil {
		m.lastError.Store(err.Error())
		return err
	}
	for _, candidate := range orders {
		// A resting GTC order needs a fresh quote decision. Do not invent a
		// price when the market-data decision function is not wired yet.
		if m.Decide == nil && m.DecideContext == nil && !candidate.FirstMatchPending {
			continue
		}
		decision := Decision{Rest: candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.TimeInForce == string(exchange.FillPolicyGTC)}
		if m.DecideContext != nil {
			decision, err = m.DecideContext(ctx, candidate)
			if err != nil {
				return err
			}
		} else if m.Decide != nil {
			decision, err = m.Decide(candidate)
			if err != nil {
				return err
			}
		}
		if err := m.MatchOrder(ctx, candidate, decision); err != nil {
			return err
		}
	}
	return nil
}
func (m *Matcher) MatchOrder(ctx context.Context, candidate store.OrderRecord, decision Decision) error {
	if m == nil || m.Store == nil {
		return fmt.Errorf("paper matcher: store is not configured")
	}
	unlock := m.Store.LockTradingAccount(candidate.TradingAccountID)
	defer unlock()
	err := m.Store.Transaction(ctx, func(tx *store.Tx) error {
		current, err := tx.GetOpenOrderForMatch(candidate.SpaceID, candidate.OrderID, candidate.Version)
		if err != nil {
			return err
		}
		if decision.Rest {
			if !current.FirstMatchPending {
				return nil
			}
			return tx.ClearFirstMatchPending(current, candidate.Version)
		}
		if decision.Cancel {
			return tx.CancelPaperOrder(current, candidate.Version, decision.Reason)
		}
		if current.ReduceOnly {
			ok, err := tx.CanFillReduceOnly(current)
			if err != nil {
				return err
			}
			if !ok {
				return tx.CancelPaperOrder(current, candidate.Version, "paper reduce-only capacity changed")
			}
		}
		if m.Reducer == nil {
			return tx.CancelPaperOrder(current, candidate.Version, "paper matcher reducer is not configured")
		}
		err = m.Reducer.ApplyFillToOrderTx(ctx, tx, current, decision.Fill, consumer.Source{
			SpaceID: current.SpaceID, TradingAccountID: current.TradingAccountID,
			Kind: consumer.OriginPaperMatcher,
		})
		if errors.Is(err, consumer.ErrDuplicateFill) {
			return nil
		}
		return err
	})
	if err == nil && m.Enqueue != nil && !decision.Fill.Quantity.IsZero() {
		m.Enqueue(candidate.TradingAccountID)
	}
	if err == nil && m.Refresh != nil && !decision.Fill.Quantity.IsZero() {
		if refreshErr := m.Refresh(ctx, candidate.TradingAccountID); refreshErr != nil {
			return refreshErr
		}
	}
	return err
}
