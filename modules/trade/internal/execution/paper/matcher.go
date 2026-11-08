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
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
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

// OutOfSessionReason 是休市后撤掉没撮合过的订单时写的原因；目标执行器据此不把它算作一次伪市价尝试。
const OutOfSessionReason = "不在交易时段"

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
	Store         *store.Store
	Reducer       MatchReducer
	Decide        func(store.OrderRecord) (Decision, error)
	DecideContext func(context.Context, store.OrderRecord) (Decision, error)
	Enqueue       func(string)
	// WakeTarget 在成交入账后唤醒目标收敛，下一笔子单不必等定时器。
	WakeTarget func(spaceID, logicalAccountID string)
	Refresh    func(context.Context, string) error
	// Exchanges 提供场所的交易时段：时段外（A 股午休、收盘后）不扫挂单，也不检查盘口新鲜度；为空时全天撮合。
	Exchanges        execution.ExchangeCatalog
	Now              func() time.Time
	CandidateTimeout time.Duration
	PageSize         int
	accountsMu       sync.Mutex
	accounts         map[string]MatcherState
	ready            atomic.Bool
	// pendingRefresh 是有成交但还没刷新快照、唤醒收敛的账户（账户锁忙时留到下一轮）。
	pendingMu      sync.Mutex
	pendingRefresh map[string]map[filledTarget]struct{}
	// failedRefresh 是刷新快照失败的账户涉及的目标，账户恢复（会话核对后已刷新快照）时再唤醒。
	failedRefresh map[string]map[filledTarget]struct{}
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
	state := m.accounts[accountID]
	if state.Generation != generation {
		m.accountsMu.Unlock()
		return
	}
	state.Ready, state.LastError, state.Stage, state.OrderID, state.ErrorCode = true, "", "", "", ""
	if m.accounts == nil {
		m.accounts = make(map[string]MatcherState)
	}
	m.accounts[accountID] = state
	m.accountsMu.Unlock()

	m.pendingMu.Lock()
	targets := m.failedRefresh[accountID]
	delete(m.failedRefresh, accountID)
	m.pendingMu.Unlock()
	if m.WakeTarget != nil {
		for target := range targets {
			m.WakeTarget(target.spaceID, target.logicalAccountID)
		}
	}
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
	// 不论这轮怎么结束（某个账户失败、基础设施错误、取消），已入账成交的账户都要刷新快照并唤醒收敛。
	defer m.flushRefresh(ctx)
	if m == nil || m.Store == nil {
		return fmt.Errorf("paper matcher: store is not configured")
	}
	accounts, err := m.Store.ListEnabledTradingAccounts(ctx)
	if err != nil {
		return err
	}
	enabled := make(map[string]bool, len(accounts))
	inSession := make(map[string]bool, len(accounts))
	now := m.now()
	for _, account := range accounts {
		enabled[account.TradingAccountID] = true
		inSession[account.TradingAccountID] = execution.InSession(m.Exchanges, account.Exchange, now)
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
				if open, known := inSession[candidate.TradingAccountID]; known && !open {
					// 时段外不撮合：挂着的单留到开盘；还没撮合过的单（休市前一刻发出的）按下单时的价格成交会用到旧价，整笔撤掉。
					if !candidate.FirstMatchPending {
						cancel()
						continue
					}
					decision = Decision{Cancel: true, Reason: OutOfSessionReason}
				} else if m.DecideContext != nil {
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
					err = m.matchOrder(candidateCtx, candidate, decision, true)
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
				if !decision.Fill.Quantity.IsZero() {
					if m.Refresh != nil {
						refreshed[candidate.TradingAccountID] = true
					}
					m.noteFilled(candidate.TradingAccountID, filledTarget{candidate.SpaceID, candidate.LogicalAccountID})
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

type filledTarget struct {
	spaceID          string
	logicalAccountID string
}

func (m *Matcher) noteFilled(accountID string, target filledTarget) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	if m.pendingRefresh == nil {
		m.pendingRefresh = make(map[string]map[filledTarget]struct{})
	}
	if m.pendingRefresh[accountID] == nil {
		m.pendingRefresh[accountID] = make(map[filledTarget]struct{})
	}
	m.pendingRefresh[accountID][target] = struct{}{}
}

// flushRefresh 给本轮（及之前因锁忙留下的）有成交的账户各刷新一次快照，再唤醒采样和目标收敛：
// 逐笔刷新一批 16 单要算 16 次快照。同一轮后面的撮合判断按成交投影现算快照，不依赖已落库的快照。
// 账户锁忙时留到下一轮，不判失败；刷新本身失败记账户失败，账户恢复时再唤醒涉及的目标。
func (m *Matcher) flushRefresh(ctx context.Context) {
	if m == nil {
		return
	}
	m.pendingMu.Lock()
	pending := m.pendingRefresh
	m.pendingRefresh = nil
	m.pendingMu.Unlock()
	for accountID, targets := range pending {
		done, err := m.refreshFilled(ctx, accountID)
		if !done && err == nil {
			for target := range targets {
				m.noteFilled(accountID, target)
			}
			continue
		}
		if err != nil {
			// 刷新失败的账户由会话核对恢复，恢复时再唤醒这些目标，避免收敛读到旧快照后停在阻塞。
			m.pendingMu.Lock()
			if m.failedRefresh == nil {
				m.failedRefresh = make(map[string]map[filledTarget]struct{})
			}
			if m.failedRefresh[accountID] == nil {
				m.failedRefresh[accountID] = make(map[filledTarget]struct{})
			}
			for target := range targets {
				m.failedRefresh[accountID][target] = struct{}{}
			}
			m.pendingMu.Unlock()
			m.failAccount(accountID, err)
			continue
		}
		if m.Enqueue != nil {
			m.Enqueue(accountID)
		}
		if m.WakeTarget != nil {
			for target := range targets {
				m.WakeTarget(target.spaceID, target.logicalAccountID)
			}
		}
	}
}

// refreshFilled 拿得到账户锁时刷新一次已落库的账户快照；锁忙返回 done=false。
func (m *Matcher) refreshFilled(ctx context.Context, accountID string) (bool, error) {
	if m.Refresh == nil {
		return true, nil
	}
	unlock, acquired := m.Store.TryLockTradingAccount(accountID)
	if !acquired {
		return false, nil
	}
	defer unlock()
	timeout := m.CandidateTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := m.Refresh(refreshCtx, accountID); err != nil {
		return true, &CandidateError{Stage: "refresh", Err: err}
	}
	return true, nil
}

// MatchOrder 撮合一笔订单，成交后立即刷新快照并唤醒。
func (m *Matcher) MatchOrder(ctx context.Context, candidate store.OrderRecord, decision Decision) error {
	return m.matchOrder(ctx, candidate, decision, false)
}

// matchOrder 撮合一笔订单。deferRefresh 为真时由扫描在本轮结束后按账户统一刷新和唤醒。
func (m *Matcher) matchOrder(ctx context.Context, candidate store.OrderRecord, decision Decision, deferRefresh bool) error {
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
	if deferRefresh {
		return err
	}
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
	if err == nil && m.WakeTarget != nil && !decision.Fill.Quantity.IsZero() {
		m.WakeTarget(candidate.SpaceID, candidate.LogicalAccountID)
	}
	return err
}

func (m *Matcher) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
