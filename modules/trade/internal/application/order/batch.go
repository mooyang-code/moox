package order

import (
	"context"
	"errors"
	"sync"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// BatchResult 是批量提交里一笔订单的结果。
type BatchResult struct {
	OrderID string
	Order   orderdomain.Order
	Err     error
}

// SubmitBatch 并发提交同一组合账户的一批待提交子单：
//
//	批前（不持锁）：并发设置杠杆，同一标的按标的加锁
//	持执行锁覆盖整批：
//	  逐笔持账户锁：外部冲突检查、全部提交前校验、落 SUBMITTING，释放账户锁
//	  有界并发发往交易所，发送前再确认一次目标授权
//	  逐笔持账户锁记录结果
//	批后：每个账户一次轻量同步
//
// 私有流的订单和成交事件要拿执行锁，会在整批结束后才入账，这是预期行为。
func (s *Service) SubmitBatch(ctx context.Context, spaceID string, orderIDs []string) ([]BatchResult, error) {
	if s == nil || s.Store == nil || s.Adapters == nil {
		return nil, ErrServiceConfig
	}
	if len(orderIDs) == 0 {
		return nil, nil
	}
	records := make([]store.OrderRecord, 0, len(orderIDs))
	for _, orderID := range orderIDs {
		record, err := s.Store.GetOrder(ctx, spaceID, orderID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	logicalAccountID := records[0].LogicalAccountID
	for _, record := range records {
		if record.LogicalAccountID != logicalAccountID {
			return nil, errors.New("trade order: batch spans logical accounts")
		}
	}
	s.prepareSymbolConfigs(ctx, records)

	results := make([]BatchResult, len(records))
	syncAccounts := make(map[string]struct{})
	err := func() error {
		if logicalAccountID != "" {
			unlock, lockErr := s.Store.LockLogicalAccountExecutionContext(ctx, spaceID, logicalAccountID)
			if lockErr != nil {
				return &AccountExecutionError{TradingAccountID: records[0].TradingAccountID, Operation: "submit_execution_lock", Err: lockErr}
			}
			defer unlock()
		}
		sends := make([]*submitSend, len(records))
		for index, record := range records {
			results[index].OrderID = record.OrderID
			send, result, prepareErr := s.prepareBatchOrder(ctx, record)
			sends[index] = send
			if send == nil {
				results[index].Order, results[index].Err = result, prepareErr
			}
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		for index, send := range sends {
			if send == nil {
				continue
			}
			wg.Add(1)
			go func(index int, send *submitSend) {
				defer wg.Done()
				accountID := send.record.TradingAccountID
				result, synchronize, deliverErr := s.deliverSubmit(ctx, send, func(lockCtx context.Context) (func(), error) {
					return s.Store.LockTradingAccountContext(lockCtx, accountID)
				})
				mu.Lock()
				results[index].Order, results[index].Err = result, deliverErr
				if synchronize || (result.State == orderdomain.Rejected && exchange.IsKind(deliverErr, exchange.ErrorInsufficientBalance)) {
					syncAccounts[accountID] = struct{}{}
				}
				mu.Unlock()
			}(index, send)
		}
		wg.Wait()
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if s.Syncer != nil {
		syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		var syncErrors []error
		for accountID := range syncAccounts {
			if syncErr := s.syncLight(syncCtx, accountID); syncErr != nil {
				syncErrors = append(syncErrors, syncErr)
			}
		}
		return results, errors.Join(syncErrors...)
	}
	return results, nil
}

// prepareBatchOrder 持账户锁做一笔订单的提交前检查并落 SUBMITTING。
func (s *Service) prepareBatchOrder(ctx context.Context, record store.OrderRecord) (*submitSend, orderdomain.Order, error) {
	unlock, err := s.Store.LockTradingAccountContext(ctx, record.TradingAccountID)
	if err != nil {
		return nil, orderdomain.Order{}, &AccountExecutionError{TradingAccountID: record.TradingAccountID, Operation: "submit_lock", Err: err}
	}
	defer unlock()
	current, err := s.Store.GetOrder(ctx, record.SpaceID, record.OrderID)
	if err != nil {
		return nil, orderdomain.Order{}, err
	}
	aggregate, err := domainOrder(current)
	if err != nil {
		return nil, orderdomain.Order{}, err
	}
	if aggregate.State != orderdomain.Pending {
		return nil, aggregate, nil
	}
	if conflictErr := s.rejectTargetExternalConflict(ctx, current); conflictErr != nil {
		return nil, aggregate, conflictErr
	}
	return s.prepareSubmit(ctx, current)
}

// prepareSymbolConfigs 在加锁之前并发设置这批新开仓标的的杠杆，提交前检查里再调用时直接命中缓存。
// 失败不影响这里，提交前检查会再试一次并按原规则处理拒绝。
func (s *Service) prepareSymbolConfigs(ctx context.Context, records []store.OrderRecord) {
	if s.SymbolConfig == nil {
		return
	}
	var wg sync.WaitGroup
	for _, record := range records {
		if record.ReduceOnly || record.State != string(orderdomain.Pending) {
			continue
		}
		account, err := s.Store.GetTradingAccountByID(ctx, record.TradingAccountID)
		if err != nil || exchange.ExecutionMode(account.ExecutionMode) != exchange.ExecutionModeLive ||
			!exchange.RuleFor(account.MarketType).Leverage {
			continue
		}
		leverage, ok := configuredLeverage(account, record.InstrumentID, record.ExchangeSymbol)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(accountID, symbol string, leverage shared.Decimal) {
			defer wg.Done()
			_ = s.SymbolConfig.EnsureSymbolConfig(ctx, accountID, symbol, leverage)
		}(record.TradingAccountID, record.ExchangeSymbol, leverage)
	}
	wg.Wait()
}

// configuredLeverage 按标的 ID、交易所代码、默认值的顺序取账户配置的杠杆，和下单校验一致。
func configuredLeverage(account store.TradingAccountRecord, instrumentID, symbol string) (shared.Decimal, bool) {
	for _, key := range []string{instrumentID, symbol, store.DefaultLeverageKey} {
		if raw, ok := account.LeverageSettings[key]; ok && raw != "" {
			leverage, err := shared.ParseDecimal(raw)
			if err == nil && leverage.Cmp(shared.Zero()) > 0 {
				return leverage, true
			}
		}
	}
	return shared.Decimal{}, false
}

// ResolveUnknownBatch 批量解析结果未知的子单，不在每笔之后同步：全部解析完后每个账户只做一次轻量同步。
// 同一订单在未知窗口内只查两次：发送后第一次机会和窗口到期时，避免整批超时后每秒全量查询耗尽权重。
func (s *Service) ResolveUnknownBatch(ctx context.Context, spaceID string, orderIDs []string) ([]BatchResult, error) {
	if s == nil || s.Store == nil || s.Adapters == nil {
		return nil, ErrServiceConfig
	}
	results := make([]BatchResult, 0, len(orderIDs))
	changed := make(map[string]struct{})
	for _, orderID := range orderIDs {
		record, err := s.Store.GetOrder(ctx, spaceID, orderID)
		if err != nil {
			return results, err
		}
		if !s.lookupDue(record) {
			results = append(results, BatchResult{OrderID: orderID})
			continue
		}
		resolved, err := s.resolveUnknownLocked(ctx, record)
		results = append(results, BatchResult{OrderID: orderID, Order: resolved, Err: err})
		if err == nil && resolved.State != orderdomain.Submitting && resolved.State != orderdomain.SubmitUnknown {
			changed[record.TradingAccountID] = struct{}{}
		}
	}
	if s.Syncer != nil {
		var syncErrors []error
		for accountID := range changed {
			if err := s.syncLight(ctx, accountID); err != nil {
				syncErrors = append(syncErrors, err)
			}
		}
		return results, errors.Join(syncErrors...)
	}
	return results, nil
}

// ResolveUnknownQuiet 解析一笔未知订单，不做尾部同步，受同样的查询次数限制。用于同步流程内部：
// 同步本身会更新事实，再触发同步只会递归。没到查询时机时返回订单当前状态。
func (s *Service) ResolveUnknownQuiet(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	if s == nil || s.Store == nil || s.Adapters == nil {
		return orderdomain.Order{}, ErrServiceConfig
	}
	record, err := s.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	if !s.lookupDue(record) {
		return domainOrder(record)
	}
	return s.resolveUnknownLocked(ctx, record)
}

// lookupDue 判断这笔未知订单现在是否该查：没查过就查；查过的要等未知窗口到期再查最后一次。
func (s *Service) lookupDue(record store.OrderRecord) bool {
	window := s.UnknownLookupWindow
	if window <= 0 {
		window = 30 * time.Second
	}
	now := s.now()
	s.lookupMu.Lock()
	defer s.lookupMu.Unlock()
	if s.lookups == nil {
		s.lookups = make(map[string]time.Time)
	}
	last, seen := s.lookups[record.OrderID]
	submitted := time.UnixMilli(record.SubmittedAt)
	due := !seen || (record.SubmittedAt > 0 && !now.Before(submitted.Add(window)) && last.Before(submitted.Add(window)))
	if record.SubmittedAt <= 0 || now.Sub(last) >= window {
		due = true
	}
	if due {
		s.lookups[record.OrderID] = now
	}
	// 清掉早已过窗的记录，避免长期运行时变大。
	for orderID, at := range s.lookups {
		if now.Sub(at) > 10*window {
			delete(s.lookups, orderID)
		}
	}
	return due
}
