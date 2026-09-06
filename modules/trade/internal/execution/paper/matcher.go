package paper

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

// InfrastructureError marks shared storage failures originating in a callback.
type InfrastructureError struct{ Err error }

func (e InfrastructureError) Error() string        { return e.Err.Error() }
func (e InfrastructureError) Unwrap() error        { return e.Err }
func (e InfrastructureError) infrastructureFault() {}

func IsInfrastructureError(err error) bool {
	var infrastructure interface{ infrastructureFault() }
	return errors.As(err, &infrastructure)
}

func storageError(err error) error {
	if err == nil || errors.Is(err, store.ErrInvalidRecord) || errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return InfrastructureError{Err: err}
}

type CandidateError struct {
	Stage string
	Err   error
}

func (e *CandidateError) Error() string { return "paper " + e.Stage + ": " + e.Err.Error() }
func (e *CandidateError) Unwrap() error { return e.Err }

var errStaleCandidate = errors.New("paper matcher: stale candidate")
var errBusyCandidate = errors.New("paper matcher: account is busy")

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
	Store            *store.Store
	Reducer          MatchReducer
	Decide           func(store.OrderRecord) (Decision, error)
	DecideContext    func(context.Context, store.OrderRecord) (Decision, error)
	Enqueue          func(string)
	Refresh          func(context.Context, string) error
	Now              func() time.Time
	CandidateTimeout time.Duration
	PageSize         int
	accountsMu       sync.Mutex
	accounts         map[string]MatcherState
	ready            atomic.Bool
	lastError        atomic.Value
}
type MatcherState struct {
	Ready      bool
	LastError  string
	Generation uint64
	Stage      string
	OrderID    string
	ErrorCode  string
}

func (m *Matcher) AccountErrors() map[string]MatcherState {
	m.accountsMu.Lock()
	defer m.accountsMu.Unlock()
	result := make(map[string]MatcherState)
	for id, state := range m.accounts {
		if !state.Ready {
			result[id] = state
		}
	}
	return result
}

func (m *Matcher) AccountState(accountID string) MatcherState {
	m.accountsMu.Lock()
	defer m.accountsMu.Unlock()
	state, found := m.accounts[accountID]
	if !found {
		state.Ready = true
	}
	return state
}

func (m *Matcher) RecoverAccount(accountID string, generation uint64) {
	m.accountsMu.Lock()
	defer m.accountsMu.Unlock()
	state := m.accounts[accountID]
	if state.Generation != generation {
		return
	}
	state.Ready, state.LastError, state.Stage, state.OrderID, state.ErrorCode = true, "", "", "", ""
	if m.accounts == nil {
		m.accounts = make(map[string]MatcherState)
	}
	m.accounts[accountID] = state
}

func (m *Matcher) failAccount(accountID string, err error, orderID ...string) {
	m.accountsMu.Lock()
	defer m.accountsMu.Unlock()
	if m.accounts == nil {
		m.accounts = make(map[string]MatcherState)
	}
	state := m.accounts[accountID]
	state.Ready, state.LastError = false, boundedMatcherError(err)
	if len(orderID) != 0 {
		state.OrderID = orderID[0]
	}
	var candidateError *CandidateError
	if errors.As(err, &candidateError) {
		state.Stage = candidateError.Stage
	}
	state.ErrorCode = "PAPER_DECISION_FAILED"
	if state.Stage == "refresh" {
		state.ErrorCode = "PAPER_REFRESH_FAILED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		state.ErrorCode = "PAPER_CANDIDATE_TIMEOUT"
	}
	state.Generation++
	m.accounts[accountID] = state
}

func boundedMatcherError(err error) string {
	value := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(err.Error(), "?"))
	runes := []rune(value)
	if len(runes) > 512 {
		return string(runes[:509]) + "..."
	}
	return value
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
	accounts, err := m.Store.ListEnabledTradingAccounts(ctx)
	if err != nil {
		return err
	}
	enabled := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		enabled[account.TradingAccountID] = true
	}
	m.accountsMu.Lock()
	for accountID := range m.accounts {
		if !enabled[accountID] {
			delete(m.accounts, accountID)
		}
	}
	m.accountsMu.Unlock()
	failed := make(map[string]bool)
	deferred := make(map[string]bool)
	succeeded := make(map[string]uint64)
	refreshed := make(map[string]bool)
	processedFirstMatch := make(map[string]struct{})
	pageSize := m.PageSize
	if pageSize <= 0 {
		pageSize = 256
	}
	for _, firstMatchPending := range []bool{true, false} {
		afterSpaceID, afterOrderID := "", ""
		for {
			orders, err := m.Store.ListPaperMatchCandidatePage(ctx, firstMatchPending, afterSpaceID, afterOrderID, pageSize)
			if err != nil {
				return err
			}
			if len(orders) == 0 {
				break
			}
			for _, candidate := range orders {
				candidateKey := candidate.SpaceID + "\x00" + candidate.OrderID
				if !firstMatchPending {
					if _, alreadyProcessed := processedFirstMatch[candidateKey]; alreadyProcessed {
						continue
					}
				}
				err = nil
				if err := ctx.Err(); err != nil {
					return err
				}
				if failed[candidate.TradingAccountID] || deferred[candidate.TradingAccountID] {
					continue
				}
				timeout := m.CandidateTimeout
				if timeout <= 0 {
					timeout = 5 * time.Second
				}
				candidateCtx, cancel := context.WithTimeout(ctx, timeout)
				// A resting GTC order needs a fresh quote decision. Do not invent a
				// price when the market-data decision function is not wired yet.
				if m.Decide == nil && m.DecideContext == nil && !candidate.FirstMatchPending {
					cancel()
					continue
				}
				generation := m.AccountState(candidate.TradingAccountID).Generation
				decision := Decision{Rest: candidate.OrderType == string(exchange.OrderTypeLimit) && candidate.TimeInForce == string(exchange.FillPolicyGTC)}
				if m.DecideContext != nil {
					decision, err = m.DecideContext(candidateCtx, candidate)
				} else if m.Decide != nil {
					decision, err = m.Decide(candidate)
				}
				if err == nil {
					err = candidateCtx.Err()
				}
				if err != nil {
					err = &CandidateError{Stage: "decision", Err: err}
				} else {
					err = m.MatchOrder(candidateCtx, candidate, decision)
				}
				cancel()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if errors.Is(err, errBusyCandidate) {
					deferred[candidate.TradingAccountID] = true
					continue
				}
				if errors.Is(err, errStaleCandidate) {
					continue
				}
				if err != nil {
					var candidateError *CandidateError
					if IsInfrastructureError(err) || !errors.As(err, &candidateError) {
						return err
					}
					m.failAccount(candidate.TradingAccountID, err, candidate.OrderID)
					failed[candidate.TradingAccountID] = true
					continue
				}
				if _, seen := succeeded[candidate.TradingAccountID]; !seen {
					succeeded[candidate.TradingAccountID] = generation
				}
				if firstMatchPending && decision.Rest {
					processedFirstMatch[candidateKey] = struct{}{}
				}
				if !decision.Fill.Quantity.IsZero() && m.Refresh != nil {
					refreshed[candidate.TradingAccountID] = true
				}
			}
			last := orders[len(orders)-1]
			afterSpaceID, afterOrderID = last.SpaceID, last.OrderID
			if len(orders) < pageSize {
				break
			}
		}
	}
	for accountID, generation := range succeeded {
		if !failed[accountID] && !deferred[accountID] && (m.AccountState(accountID).Stage != "refresh" || refreshed[accountID]) {
			m.RecoverAccount(accountID, generation)
		}
	}
	return nil
}
func (m *Matcher) MatchOrder(ctx context.Context, candidate store.OrderRecord, decision Decision) error {
	if m == nil || m.Store == nil {
		return fmt.Errorf("paper matcher: store is not configured")
	}
	unlock, acquired := m.Store.TryLockTradingAccount(candidate.TradingAccountID)
	if !acquired {
		return errBusyCandidate
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	err := m.Store.Transaction(ctx, func(tx *store.Tx) error {
		current, err := tx.GetOpenOrderForMatch(candidate.SpaceID, candidate.OrderID, candidate.Version)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, store.ErrConflict) {
				return errStaleCandidate
			}
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
	if err == nil && m.Refresh != nil && !decision.Fill.Quantity.IsZero() {
		if refreshErr := m.Refresh(ctx, candidate.TradingAccountID); refreshErr != nil {
			return &CandidateError{Stage: "refresh", Err: refreshErr}
		}
		if err := ctx.Err(); err != nil {
			return &CandidateError{Stage: "refresh", Err: err}
		}
	}
	unlock()
	locked = false
	// Refresh the persisted account snapshot before waking the sampler so a
	// fill cannot enqueue a point that still observes the pre-fill snapshot.
	if err == nil && m.Enqueue != nil && !decision.Fill.Quantity.IsZero() {
		m.Enqueue(candidate.TradingAccountID)
	}
	return err
}
