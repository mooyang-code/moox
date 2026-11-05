package accountsync

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

var ErrServiceConfig = errors.New("trade account sync: service is not configured")

const missingOrderGrace = 5 * time.Minute

type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

type SessionState interface {
	Ready(tradingAccountID string) bool
}

type Metrics interface {
	Observe(tradingAccountID string, now time.Time, maxDifference float64, err error)
}

type Result struct {
	FillsIngested          int
	OrdersUpdated          int
	PositionsUpdated       int
	AccountSnapshotUpdated bool
	UnknownOrdersResolved  int
	ExternalFactsImported  bool
	Ready                  bool
	// BalancePending 表示本轮完整同步没有确认待确认水位（成交或私有余额更新之后的余额）：同步请求本身成功，但余额尚未确认。
	BalancePending bool
	Warnings       []string
}

type Snapshot struct {
	Fills     []exchange.Fill
	Orders    []exchange.Order
	Positions []exchange.Position
	Account   exchange.AccountSnapshot
	// AccountReadAt 是读取账户快照前的本地时间。适配器的快照时间可能是最后变更时间，
	// 余额没变的成交之后追不上成交时间，判断新旧和清除水位都用这个时间。
	AccountReadAt time.Time
	// PositionsReadAt 是读取持仓快照前的本地时间，持仓滞后保护期从它往前算。
	PositionsReadAt time.Time
	FillCursors     store.FillCursors
	Ready           bool
}

// positionLagGrace 是持仓滞后保护期：持仓接口可能比成交落后数秒，这段时间内的本地持仓不删、不被更旧的 REST 覆盖，
// 也不算不一致。测试网的滞后不能代表生产，不按测试网的数字收紧。
const positionLagGrace = 10 * time.Second

// syncMode 是同步范围。
type syncMode int

const (
	// syncFull 拉有上限的全集成交，用于手动同步。
	syncFull syncMode = iota
	// syncIncremental 读挂单、持仓、账户，只拉需要的标的，不推进轮转；用于撤单确认和未知订单收尾。
	syncIncremental
	// syncReconcile 是 30 秒对账：在增量基础上推进轮转，发现没有其他信号的外部成交。
	syncReconcile
	// syncLight 只读持仓和账户，补拉不一致标的的成交，用于成交事件和下单之后恢复准入。
	syncLight
)

type Service struct {
	rotationMu sync.Mutex
	rotation   map[string]int

	Store          *store.Store
	Adapters       AdapterSource
	SessionState   SessionState
	Fills          *consumer.Reducer
	Orders         *orderapp.Service
	Metrics        Metrics
	Wake           func()
	Now            func() time.Time
	LateFillWindow time.Duration
}

// ConfirmCancel exposes the shared reducer's terminal cancellation transition
// to deterministic local adapters. Paper cancellation is authoritative once
// the local adapter accepts it; the following sync only refreshes the account
// snapshot after the reservation has been released.
func (s *Service) ConfirmCancel(ctx context.Context, spaceID, orderID string) error {
	if s == nil || s.Fills == nil {
		return errors.New("trade account sync: fill reducer is not configured")
	}
	return s.Fills.ConfirmCancel(ctx, spaceID, orderID)
}

// SyncAccount 是增量同步：账户、持仓、挂单都读，成交只拉需要的标的，不推进轮转。
func (s *Service) SyncAccount(ctx context.Context, tradingAccountID string) (Result, error) {
	return s.syncAccount(ctx, tradingAccountID, syncIncremental)
}

// SyncAccountReconcile 是 30 秒对账，在增量同步基础上推进轮转。
func (s *Service) SyncAccountReconcile(ctx context.Context, tradingAccountID string) (Result, error) {
	return s.syncAccount(ctx, tradingAccountID, syncReconcile)
}

// SyncAccountLight 只读持仓和账户，补拉不一致标的的成交后恢复准入。永续一致时只要 10 权重。
func (s *Service) SyncAccountLight(ctx context.Context, tradingAccountID string) (Result, error) {
	return s.syncAccount(ctx, tradingAccountID, syncLight)
}

// SyncAccountFull 拉有上限的全集成交，用于控制台手动同步。
func (s *Service) SyncAccountFull(ctx context.Context, tradingAccountID string) (Result, error) {
	return s.syncAccount(ctx, tradingAccountID, syncFull)
}

func (s *Service) syncAccount(
	ctx context.Context,
	tradingAccountID string,
	mode syncMode,
) (result Result, err error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	maxDifference := 0.0
	defer func() {
		if s.Metrics != nil {
			observed := err
			if observed == nil && result.BalancePending {
				// 同步请求执行成功不等于余额已确认：余额长期未追上待确认水位时，监控必须看到失败而不是“同步新鲜”
				observed = errBalanceNotConfirmed
			}
			s.Metrics.Observe(tradingAccountID, s.now(), maxDifference, observed)
		}
	}()
	unlockAccount, unlockExecution, err := s.lockSyncScope(ctx, tradingAccountID)
	if err != nil {
		var accountErr *orderapp.AccountExecutionError
		if errors.As(err, &accountErr) {
			return s.failLock(ctx, tradingAccountID, err)
		}
		return Result{}, err
	}
	result, maxDifference, err = s.syncAccountLocked(ctx, tradingAccountID, mode)
	unlockAccount()
	if err == nil && result.ExternalFactsImported {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	unlockExecution()
	if err != nil {
		return result, err
	}
	result, err = s.resolveUnknownOrders(ctx, tradingAccountID, result)
	if err != nil {
		var accountErr *orderapp.AccountExecutionError
		if errors.As(err, &accountErr) && accountErr.Operation == "resolve_unknown_lock" {
			_, err = s.failLock(ctx, tradingAccountID, err)
		}
		return result, err
	}
	s.wakeTargets()
	return result, nil
}

func (s *Service) syncAccountLocked(
	ctx context.Context,
	tradingAccountID string,
	mode syncMode,
) (Result, float64, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return Result{}, 0, err
	}
	adapter, err := s.Adapters.Adapter(tradingAccountID)
	if err != nil {
		result, failErr := s.fail(ctx, account, accountDependencyError(tradingAccountID, "adapter", err))
		return result, 0, failErr
	}

	// 轻量同步不读全部挂单（永续 40 权重、现货 80 权重），挂单由私有流和对账维护。
	var openOrders []exchange.Order
	if mode != syncLight {
		openOrders, err = adapter.ListOpenOrders(ctx)
		if err != nil {
			result, failErr := s.fail(ctx, account, accountDependencyError(tradingAccountID, "list_open_orders", err))
			return result, 0, failErr
		}
	}
	localOrders, err := s.Store.ListOrdersForAccount(
		ctx,
		account.SpaceID,
		account.TradingAccountID,
		s.now().Add(-s.lateFillWindow()).UnixMilli(),
	)
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, 0, failErr
	}
	positionsReadAt := s.now()
	protectAfter := positionsReadAt.Add(-positionLagGrace).UnixMilli()
	positions, err := adapter.ListPositionSnapshots(ctx)
	if err != nil {
		result, failErr := s.fail(ctx, account, accountDependencyError(tradingAccountID, "list_positions", err))
		return result, 0, failErr
	}
	accountReadAt := s.now()
	accountSnapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		result, failErr := s.fail(ctx, account, accountDependencyError(tradingAccountID, "snapshot", err))
		return result, 0, failErr
	}
	maxDifference := maxBalanceDifference(
		account.Snapshot.Balances,
		store.SnapshotFromExchange(accountSnapshot).Balances,
	)
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, maxDifference, failErr
	}
	var symbols []exchange.SymbolKey
	switch mode {
	case syncFull:
		symbols, err = s.BoundedFullSymbols(ctx, account, openOrders, localOrders, positions, instruments, accountSnapshot)
	case syncLight:
		symbols, err = s.inconsistentSymbols(ctx, account, positions, instruments, accountSnapshot, protectAfter)
	default:
		symbols, err = s.incrementalSymbols(ctx, account, openOrders, localOrders, positions, instruments, accountSnapshot,
			FullSymbols(account, openOrders, localOrders, positions, instruments, accountSnapshot), mode == syncReconcile, protectAfter)
	}
	if err != nil {
		result, failErr := s.fail(ctx, account, err)
		return result, maxDifference, failErr
	}
	fills := make([]exchange.Fill, 0)
	cursors := cloneCursors(account.FillCursors)
	for _, key := range symbols {
		rows, cursor, listErr := adapter.ListRecentFills(ctx, key, cursors[key.String()])
		if listErr != nil {
			result, failErr := s.fail(ctx, account, accountDependencyError(tradingAccountID, "list_fills", listErr))
			return result, maxDifference, failErr
		}
		fills = append(fills, rows...)
		if strings.TrimSpace(cursor) != "" {
			cursors[key.String()] = cursor
		}
	}

	orders := append([]exchange.Order(nil), openOrders...)
	// 外部订单可能没有客户端订单号，挂单要同时按交易所订单号比对。
	// 订单号是各市场各自的序列，现货和合约可能撞号，所以都带上市场。
	openByClient := make(map[string]struct{}, len(openOrders))
	openByExchange := make(map[string]struct{}, len(openOrders))
	for _, current := range openOrders {
		openByClient[orderIdentityKey(current.Market, current.ClientOrderID)] = struct{}{}
		openByExchange[orderIdentityKey(current.Market, current.ExchangeOrderID)] = struct{}{}
	}
	var missing []store.OrderRecord
	for _, local := range localOrders {
		if mode == syncLight || orderdomain.State(local.State).Terminal() {
			continue
		}
		localMarket := exchange.MarketType(local.MarketType)
		if _, found := openByClient[orderIdentityKey(localMarket, local.ClientOrderID)]; found {
			continue
		}
		if _, found := openByExchange[orderIdentityKey(localMarket, local.ExchangeOrderID)]; found && local.ExchangeOrderID != "" {
			continue
		}
		current, lookupErr := lookupLocalOrder(ctx, adapter, local)
		switch {
		case lookupErr == nil:
			orders = append(orders, current)
		case exchange.IsKind(lookupErr, exchange.ErrorOrderNotFound):
			if s.orderMissingOnExchange(local, lookupErr) {
				missing = append(missing, local)
			}
			continue
		default:
			result, failErr := s.fail(ctx, account, accountDependencyError(tradingAccountID, "get_order", lookupErr))
			return result, maxDifference, failErr
		}
	}
	ready := account.Ready
	if s.SessionState != nil {
		ready = s.SessionState.Ready(account.TradingAccountID)
	}
	result, err := s.applySnapshot(ctx, account.TradingAccountID, Snapshot{
		Fills: fills, Orders: orders, Positions: positions,
		Account: accountSnapshot, AccountReadAt: accountReadAt, PositionsReadAt: positionsReadAt,
		FillCursors: cursors, Ready: ready,
	})
	if err != nil {
		return result, maxDifference, err
	}
	for _, local := range missing {
		// 迟到成交仍可入账，领域模型允许已撤销订单继续应用成交。
		if err := s.Fills.ConfirmCancel(ctx, local.SpaceID, local.OrderID); err != nil {
			return result, maxDifference, err
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("订单 %s 在交易所已不存在，按已撤销处理", local.OrderID))
	}
	return result, maxDifference, nil
}

// orderIdentityKey 是“市场:订单号”，用于在同一批订单里按订单号去重或比对。
func orderIdentityKey(market exchange.MarketType, id string) string {
	return string(market) + ":" + id
}

func lookupLocalOrder(ctx context.Context, adapter execution.ExecutionAdapter, local store.OrderRecord) (exchange.Order, error) {
	if local.ExchangeOrderID != "" {
		return adapter.GetOrderByExchangeID(ctx, exchange.NewSymbolKey(local.MarketType, local.ExchangeSymbol), local.ExchangeOrderID)
	}
	return adapter.GetOrder(ctx, exchange.NewSymbolKey(local.MarketType, local.ExchangeSymbol), local.ClientOrderID)
}

// orderMissingOnExchange 判断本地活动订单是否可以按已撤销处理。只认交易所明确的不存在错误码，
// HTTP 404、响应解析失败或空结果都不算；还要等过了 5 分钟，避免刚提交的订单被误判。
func (s *Service) orderMissingOnExchange(local store.OrderRecord, lookupErr error) bool {
	switch orderdomain.State(local.State) {
	case orderdomain.Open, orderdomain.PartiallyFilled, orderdomain.Canceling, orderdomain.CancelUnknown:
	default:
		return false
	}
	if !exchange.IsConfirmedOrderMissing(lookupErr) {
		return false
	}
	since := local.CreatedAt
	if submitted := time.UnixMilli(local.SubmittedAt); local.SubmittedAt > 0 && submitted.After(since) {
		since = submitted
	}
	return !since.IsZero() && s.now().Sub(since) >= missingOrderGrace
}

func maxBalanceDifference(before, after []store.AssetBalance) float64 {
	// 同一资产可能出现在多个钱包里，按（钱包，资产）比较。
	type balanceKey struct{ wallet, asset string }
	previous := make(map[balanceKey]float64, len(before))
	for _, balance := range before {
		previous[balanceKey{balance.Wallet, balance.Asset}] = parseBalance(balance.Total)
	}
	var maximum float64
	for _, balance := range after {
		key := balanceKey{balance.Wallet, balance.Asset}
		current := parseBalance(balance.Total)
		old := previous[key]
		denominator := math.Max(math.Abs(current), 1e-12)
		maximum = max(maximum, math.Abs(current-old)/denominator)
		delete(previous, key)
	}
	for _, old := range previous {
		if old != 0 {
			maximum = max(maximum, 1)
		}
	}
	return maximum
}

func parseBalance(value string) float64 {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0
	}
	return parsed
}

func (s *Service) ApplySnapshot(
	ctx context.Context,
	tradingAccountID string,
	snapshot Snapshot,
) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	unlockAccount, unlockExecution, err := s.lockSyncScope(ctx, tradingAccountID)
	if err != nil {
		return Result{}, err
	}
	result, err := s.applySnapshot(ctx, tradingAccountID, snapshot)
	unlockAccount()
	if err == nil && result.ExternalFactsImported {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	unlockExecution()
	if err != nil {
		return result, err
	}
	result, err = s.resolveUnknownOrders(ctx, tradingAccountID, result)
	if err != nil {
		return result, err
	}
	s.wakeTargets()
	return result, nil
}

func (s *Service) applySnapshot(
	ctx context.Context,
	tradingAccountID string,
	snapshot Snapshot,
) (Result, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return Result{}, err
	}
	if snapshot.Account.ExchangeUpdatedAt.UnixMilli() <= 0 {
		return s.fail(ctx, account, errors.New(
			"trade account sync: account snapshot has no Exchange timestamp",
		))
	}
	result := Result{Ready: snapshot.Ready}

	// 账户创建前的成交结果已经包含在余额和持仓快照里，只推进游标，不入账也不算外部成交。
	// 要排在 batchQuantities 和 positionDeleteBefore 之前，否则持仓删除边界会被拉回最老的成交。
	snapshot.Fills = fillsAfterCreation(snapshot.Fills, account.CreatedAt)

	batchQuantities := make(map[string]shared.Decimal)
	for _, fill := range snapshot.Fills {
		key := fillBatchKey(fill)
		if key == "" {
			continue
		}
		batchQuantities[key] = batchQuantities[key].Add(fill.Quantity)
	}
	for _, fill := range snapshot.Fills {
		applied, external, applyErr := s.applyFill(
			ctx,
			account,
			fill,
			consumer.OriginRESTSnapshot,
			batchQuantities[fillBatchKey(fill)],
		)
		if applyErr != nil {
			return s.fail(ctx, account, applyErr)
		}
		if applied {
			result.FillsIngested++
		}
		result.ExternalFactsImported = result.ExternalFactsImported || external
	}
	for _, current := range snapshot.Orders {
		updated, external, warning, applyErr := s.applyOrder(ctx, account, current)
		if applyErr != nil {
			return s.fail(ctx, account, applyErr)
		}
		if updated {
			result.OrdersUpdated++
		}
		if warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		result.ExternalFactsImported = result.ExternalFactsImported || external
	}

	positionRecords := make([]store.PositionRecord, 0, len(snapshot.Positions))
	for _, position := range snapshot.Positions {
		if position.ExchangeUpdatedAt.UnixMilli() <= 0 {
			return s.fail(ctx, account, errors.New(
				"trade account sync: position snapshot has no Exchange timestamp",
			))
		}
		positionRecords = append(positionRecords, positionRecord(account, position))
	}
	// 空持仓/缺失持仓的删除边界：持仓列表是先于本轮成交读取的，不能用后取的余额时间当边界，
	// 否则刚补录成交投影出的持仓会被当作“交易所没有”而删除。边界不晚于本轮最早成交之前。
	positionDeleteBefore := snapshot.Account.ExchangeUpdatedAt.UnixMilli()
	for _, fill := range snapshot.Fills {
		if earliest := fill.TradedAt.UnixMilli() - 1; earliest < positionDeleteBefore {
			positionDeleteBefore = earliest
		}
	}
	if positionDeleteBefore <= 0 {
		positionDeleteBefore = 1
	}
	// 持仓滞后保护期：只靠本轮成交压低删除边界只保护第一轮（私有流入账不推进成交游标），
	// 保护期内的本地持仓一律不删、不被更旧的 REST 覆盖。
	protectAfter := int64(0)
	if readAt := snapshot.PositionsReadAt; !readAt.IsZero() {
		protectAfter = readAt.Add(-positionLagGrace).UnixMilli()
	} else if !snapshot.AccountReadAt.IsZero() {
		protectAfter = snapshot.AccountReadAt.Add(-positionLagGrace).UnixMilli()
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.ReplacePositionsForAccount(
			account.SpaceID,
			account.TradingAccountID,
			positionRecords,
			positionDeleteBefore,
			protectAfter,
		)
	}); err != nil {
		return s.fail(ctx, account, err)
	}
	result.PositionsUpdated = len(positionRecords)

	// 完整快照既不能早于已采用的完整快照，也不能早于私有余额更新已合并进来的时间：旧 REST 不得覆盖更新的字段。
	snapshotTime := snapshot.Account.ExchangeUpdatedAt.UnixMilli()
	if !snapshot.AccountReadAt.IsZero() {
		snapshotTime = snapshot.AccountReadAt.UnixMilli()
	}
	freshness := account.SnapshotSourceTime
	if account.Snapshot.ExchangeUpdatedAt > freshness {
		freshness = account.Snapshot.ExchangeUpdatedAt
	}
	if snapshotTime >= freshness {
		account.Snapshot = store.SnapshotFromExchange(snapshot.Account)
		account.SnapshotSourceTime = snapshotTime
		result.AccountSnapshotUpdated = true
	} else {
		result.Warnings = append(
			result.Warnings,
			"ignored stale REST account snapshot",
		)
	}

	now := s.now().UnixMilli()
	if snapshot.FillCursors == nil {
		snapshot.FillCursors = account.FillCursors
	}
	// 待确认水位：只有被采用且覆盖水位的完整余额快照才能清零并恢复准入；
	// 否则余额仍是成交/私有更新之前的值，沿用旧的 LastSyncAt 抵扣边界并保持未就绪。
	// 本轮刚补录的成交已在入账事务内抬高了水位（account 是处理成交之前读取的），这里重新读取，
	// 避免成交之前读到的余额快照误清零水位。
	latest, latestErr := s.Store.GetTradingAccountByID(ctx, account.TradingAccountID)
	if latestErr != nil {
		return Result{}, latestErr
	}
	pending := latest.BalancePendingUntil
	// 订单累计成交仍领先本地成交明细时，余额没有被这些成交的明细解释，不能恢复准入（与水位是否为零无关：
	// 首次导入的终态外部订单、由首笔成交触发的导入都可能在水位为零时留下缺口）。
	gaps, gapErr := s.Store.CountOrdersWithFillGap(ctx, account.SpaceID, account.TradingAccountID)
	if gapErr != nil {
		return Result{}, gapErr
	}
	hasGap := gaps > 0
	if hasGap {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d 个订单的累计成交仍领先已入账的成交明细", gaps))
	}
	clearPending := result.AccountSnapshotUpdated && snapshotTime >= pending && !hasGap
	// 现货持仓来自余额：内容比待确认水位旧的 REST 余额（交易所更新时间更早）不能恢复准入，
	// 否则会按成交前的余额重复下单。读取时间更晚不代表内容更新。
	if clearPending && pending > 0 && account.HasBalanceMarket() &&
		snapshot.Account.ExchangeUpdatedAt.UnixMilli() < pending {
		clearPending = false
	}
	readinessCause := ""
	if (pending > 0 || hasGap) && !clearPending {
		snapshot.Ready = false
		result.Ready = false
		result.BalancePending = true
		readinessCause = awaitingBalanceReason
		result.Warnings = append(result.Warnings, "balance snapshot does not cover the pending fill or private update; admission stays closed")
	}
	// LastSyncAt 同时是资金预留的抵扣边界：只有余额快照真正被采用且没有遗留待确认水位时才能推进；
	// 否则已提交订单的预留会被误认为已反映在余额中，导致可用资金被高估。
	syncBoundary := now
	if (!result.AccountSnapshotUpdated || ((pending > 0 || hasGap) && !clearPending)) && account.LastSyncAt > 0 {
		syncBoundary = account.LastSyncAt
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountFacts(
			account.SpaceID,
			account.TradingAccountID,
			snapshot.FillCursors,
			account.Snapshot,
			account.SnapshotSourceTime,
			syncBoundary,
		); err != nil {
			return err
		}
		if clearPending && pending > 0 {
			if err := tx.ClearBalancePendingThrough(account.SpaceID, account.TradingAccountID, snapshotTime); err != nil {
				return err
			}
		}
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			snapshot.Ready,
			now,
			readinessCause,
		)
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func (s *Service) resolveUnknownOrders(
	ctx context.Context,
	tradingAccountID string,
	result Result,
) (Result, error) {
	if s.Orders == nil {
		return result, nil
	}
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return result, err
	}
	unknown, err := s.Store.ListOrdersForAccount(
		ctx,
		account.SpaceID,
		account.TradingAccountID,
		s.now().Add(-s.lateFillWindow()).UnixMilli(),
	)
	if err != nil {
		return result, err
	}
	for _, current := range unknown {
		state := orderdomain.State(current.State)
		if state != orderdomain.Submitting && state != orderdomain.SubmitUnknown {
			continue
		}
		// 同步内部解析不再触发同步，每笔订单在未知窗口内最多查两次，避免整批超时时耗尽权重。
		resolved, resolveErr := s.Orders.ResolveUnknownQuiet(
			ctx,
			account.SpaceID,
			current.OrderID,
		)
		if resolveErr != nil {
			if ctx.Err() != nil {
				return result, resolveErr
			}
			result.Warnings = append(result.Warnings, resolveErr.Error())
			continue
		}
		if resolved.State != state {
			result.UnknownOrdersResolved++
		}
	}
	return result, nil
}

// awaitingBalanceReason 是私有成交释放预留后、余额快照尚未确认期间持久化的未就绪原因。
var errBalanceNotConfirmed = errors.New("trade account sync: balance snapshot does not cover the pending fill or private update")

const awaitingBalanceReason = "成交待余额同步确认"

func (s *Service) ApplyFill(
	ctx context.Context,
	tradingAccountID string,
	fill exchange.Fill,
) (bool, error) {
	if err := s.validate(); err != nil {
		return false, err
	}
	unlock, unlockExecution, err := s.lockSyncScope(ctx, tradingAccountID)
	if err != nil {
		return false, err
	}
	defer unlockExecution()
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return false, err
	}
	applied, external, err := s.applyFill(
		ctx,
		account,
		fill,
		consumer.OriginPrivateSocket,
		shared.Zero(),
	)
	unlock()
	if err == nil && external {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	if err != nil {
		return applied, err
	}
	s.wakeTargets()
	return applied, nil
}

func (s *Service) ApplyOrder(
	ctx context.Context,
	tradingAccountID string,
	current exchange.Order,
) error {
	if err := s.validate(); err != nil {
		return err
	}
	unlock, unlockExecution, err := s.lockSyncScope(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	defer unlockExecution()
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return err
	}
	_, external, _, err := s.applyOrder(ctx, account, current)
	unlock()
	if err == nil && external {
		err = s.pauseForExternalFact(ctx, tradingAccountID)
	}
	if err != nil {
		return err
	}
	s.wakeTargets()
	return nil
}

// ApplyPosition 处理私有流持仓事件：不写持仓，只关闭准入并抬高待确认水位。
// 持仓数量只由成交投影和 REST 决定：事件先于成交到达时写入数量，后到的成交会再叠加一次。
func (s *Service) ApplyPosition(
	ctx context.Context,
	tradingAccountID string,
	position exchange.Position,
) error {
	if err := s.validate(); err != nil {
		return err
	}
	unlock, lockErr := s.Store.LockTradingAccountContext(ctx, tradingAccountID)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if at := position.ExchangeUpdatedAt.UnixMilli(); at > 0 {
			if err := tx.RaiseBalancePending(account.SpaceID, account.TradingAccountID, at); err != nil {
				return err
			}
		}
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			false,
			s.now().UnixMilli(),
			"private position update awaiting sync",
		)
	})
}

func (s *Service) ApplyAccountSnapshot(
	ctx context.Context,
	tradingAccountID string,
	snapshot exchange.AccountSnapshot,
) error {
	if err := s.validate(); err != nil {
		return err
	}
	unlock, lockErr := s.Store.LockTradingAccountContext(ctx, tradingAccountID)
	if lockErr != nil {
		return lockErr
	}
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return err
	}
	if snapshot.ExchangeUpdatedAt.UnixMilli() <= 0 {
		unlock()
		return errors.New("trade account sync: account snapshot has no Exchange timestamp")
	}
	// 旧于已存时间的私有更新不合并余额，但账户确实变过，仍要等完整同步确认。
	stale := snapshot.ExchangeUpdatedAt.UnixMilli() < account.Snapshot.ExchangeUpdatedAt
	merged := mergePrivateSnapshot(account.Snapshot, snapshot)
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if !stale {
			if err := tx.UpdateTradingAccountSnapshot(
				account.SpaceID,
				account.TradingAccountID,
				merged,
			); err != nil {
				return err
			}
		}
		// 私有更新只合并了部分字段：完整余额快照覆盖这个时间之前，不能恢复准入
		if err := tx.RaiseBalancePending(account.SpaceID, account.TradingAccountID, snapshot.ExchangeUpdatedAt.UnixMilli()); err != nil {
			return err
		}
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			false,
			s.now().UnixMilli(),
			"private account update awaiting full sync",
		)
	})
	unlock()
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) SetReady(
	ctx context.Context,
	tradingAccountID string,
	ready bool,
	cause error,
) error {
	if s == nil || s.Store == nil {
		return ErrServiceConfig
	}
	unlock, lockErr := s.Store.LockTradingAccountContext(ctx, tradingAccountID)
	if lockErr != nil {
		return lockErr
	}
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		unlock()
		return err
	}
	err = s.setReady(ctx, account, ready, cause)
	unlock()
	if err != nil {
		return err
	}
	s.wakeTargets()
	return nil
}

func (s *Service) setReady(
	ctx context.Context,
	account store.TradingAccountRecord,
	ready bool,
	cause error,
) error {
	now := s.now().UnixMilli()
	lastError := ""
	if cause != nil && !errors.Is(cause, context.Canceled) {
		lastError = cause.Error()
	}
	if ready && account.BalancePendingUntil > 0 {
		// 还有未被完整余额快照覆盖的成交或私有更新：不能仅凭会话就绪恢复准入
		ready, lastError = false, awaitingBalanceReason
	}
	if ready {
		// 订单累计成交仍领先已入账成交时也不能恢复：回执没有交易所时间时只记了缺口、没抬水位。
		gaps, err := s.Store.CountOrdersWithFillGap(ctx, account.SpaceID, account.TradingAccountID)
		if err != nil {
			return err
		}
		if gaps > 0 {
			ready, lastError = false, awaitingBalanceReason
		}
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID,
			account.TradingAccountID,
			ready,
			now,
			lastError,
		)
	})
}

func (s *Service) applyFill(
	ctx context.Context,
	account store.TradingAccountRecord,
	fill exchange.Fill,
	kind consumer.FillOrigin,
	syntheticQuantity shared.Decimal,
) (bool, bool, error) {
	external, err := s.ensureOrderForFill(
		ctx, account, fill, syntheticQuantity,
	)
	if err != nil {
		return false, false, err
	}
	applied, err := s.Fills.ApplyFill(ctx, fill, consumer.Source{
		SpaceID: account.SpaceID, TradingAccountID: account.TradingAccountID,
		Kind: kind,
	})
	return applied, external && applied, err
}

func (s *Service) ensureOrderForFill(
	ctx context.Context,
	account store.TradingAccountRecord,
	fill exchange.Fill,
	syntheticQuantity shared.Decimal,
) (bool, error) {
	if fill.ExchangeOrderID != "" {
		if record, err := s.Store.GetOrderByExchangeID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			exchange.SymbolKey{Market: fill.Market, Symbol: fill.ExchangeSymbol},
			fill.ExchangeOrderID,
		); err == nil {
			return record.OwnerType == string(orderdomain.OwnerExternal), nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
	}
	if fill.ClientOrderID != "" {
		if record, err := s.Store.GetOrderByClientID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			fill.ClientOrderID,
		); err == nil {
			return record.OwnerType == string(orderdomain.OwnerExternal), nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
	}
	adapter, err := s.Adapters.Adapter(account.TradingAccountID)
	if err != nil {
		return false, accountDependencyError(account.TradingAccountID, "adapter", err)
	}
	var current exchange.Order
	var lookupErr error
	if fill.ClientOrderID != "" {
		current, lookupErr = adapter.GetOrder(ctx, exchange.NewSymbolKey(fill.Market, fill.ExchangeSymbol), fill.ClientOrderID)
	} else {
		current, lookupErr = adapter.GetOrderByExchangeID(ctx, exchange.NewSymbolKey(fill.Market, fill.ExchangeSymbol), fill.ExchangeOrderID)
	}
	if lookupErr != nil {
		if !exchange.IsKind(lookupErr, exchange.ErrorOrderNotFound) {
			return false, accountDependencyError(account.TradingAccountID, "fill_order_lookup", lookupErr)
		}
		if syntheticQuantity.Cmp(shared.Zero()) <= 0 {
			syntheticQuantity = fill.Quantity
		}
		current = exchange.Order{
			ExchangeOrderID: fill.ExchangeOrderID, ClientOrderID: fill.ClientOrderID,
			Market: fill.Market, ExchangeSymbol: fill.ExchangeSymbol, OrderType: exchange.OrderTypeMarket,
			Side: fill.Side, PositionSide: fill.PositionSide,
			Quantity: syntheticQuantity, Status: exchange.OrderStatusOpen,
			CreatedAt: fill.TradedAt, UpdatedAt: fill.TradedAt,
		}
	}
	// The fill stream is authoritative for symbol identity. Some exchange
	// lookup adapters return a cached order without the requested symbol (or
	// with a stale one); never let that collapse fills from two instruments.
	current.ExchangeSymbol = fill.ExchangeSymbol
	current.Market = fill.Market
	batchFilled := syntheticQuantity
	if batchFilled.Cmp(shared.Zero()) <= 0 {
		batchFilled = fill.Quantity
	}
	imported, err := s.importExternalOrder(ctx, account, current, batchFilled)
	if err != nil {
		return false, err
	}
	if err := s.observeOrderFills(ctx, account, imported, current); err != nil {
		return false, err
	}
	return true, nil
}

func fillBatchKey(fill exchange.Fill) string {
	if fill.ExchangeOrderID != "" {
		return string(fill.Market) + "\x00" + fill.ExchangeSymbol + "\x00exchange\x00" + fill.ExchangeOrderID
	}
	if fill.ClientOrderID != "" {
		return string(fill.Market) + "\x00" + fill.ExchangeSymbol + "\x00client\x00" + fill.ClientOrderID
	}
	return ""
}

func (s *Service) applyOrder(
	ctx context.Context,
	account store.TradingAccountRecord,
	current exchange.Order,
) (bool, bool, string, error) {
	var record store.OrderRecord
	var err error
	switch {
	case current.ClientOrderID != "":
		record, err = s.Store.GetOrderByClientID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			current.ClientOrderID,
		)
	case current.ExchangeOrderID != "":
		record, err = s.Store.GetOrderByExchangeID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			exchange.SymbolKey{Market: current.Market, Symbol: current.ExchangeSymbol},
			current.ExchangeOrderID,
		)
	default:
		return false, false, "", errors.New(
			"trade account sync: Exchange order has no stable identifier",
		)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		record, err = s.importExternalOrder(ctx, account, current, shared.Zero())
		if err != nil {
			return false, false, "", err
		}
		if observeErr := s.observeOrderFills(ctx, account, record, current); observeErr != nil {
			return false, false, "", observeErr
		}
		return true, true, "", s.applyOrderState(ctx, record, current)
	}
	if err != nil {
		return false, false, "", err
	}
	if observeErr := s.observeOrderFills(ctx, account, record, current); observeErr != nil {
		return false, false, "", observeErr
	}
	storedFilled := shared.MustDecimal(record.FilledQuantity)
	externalChanged := record.OwnerType == string(orderdomain.OwnerExternal) &&
		(current.UpdatedAt.UnixMilli() > record.ExchangeUpdatedAt ||
			current.FilledQuantity.Cmp(storedFilled) > 0)
	if current.FilledQuantity.Cmp(storedFilled) < 0 {
		return false, false, fmt.Sprintf(
			"order %s ignored regressing cumulative filled quantity",
			record.OrderID,
		), nil
	}
	if current.FilledQuantity.Cmp(storedFilled) > 0 {
		return false, externalChanged, fmt.Sprintf(
			"order %s snapshot is ahead of ingested Fills",
			record.OrderID,
		), s.applyOrderState(ctx, record, current)
	}
	return true, externalChanged, "", s.applyOrderState(ctx, record, current)
}

// observeOrderFills 在所有订单入口（已有订单更新、首次导入、由成交触发的导入）统一记录交易所观察到的累计成交量；
// 累计成交领先本地成交明细时，终态转换会释放预留，而余额还没有反映这些成交，所以先抬高待确认水位并关闭准入，
// 明细补齐且余额覆盖该时间后才由完整同步恢复。
func (s *Service) observeOrderFills(
	ctx context.Context,
	account store.TradingAccountRecord,
	record store.OrderRecord,
	current exchange.Order,
) error {
	stored, parseErr := shared.ParseDecimal(record.FilledQuantity)
	if parseErr != nil {
		return parseErr
	}
	ahead := current.FilledQuantity.Cmp(stored) > 0 && account.ExecutionMode != string(exchange.ExecutionModePaper)
	until := current.UpdatedAt.UnixMilli()
	if until <= 0 {
		until = s.now().UnixMilli()
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.RaiseObservedFilledQuantity(record.SpaceID, record.OrderID, current.FilledQuantity); err != nil {
			return err
		}
		if !ahead {
			return nil
		}
		if err := tx.RaiseBalancePending(account.SpaceID, account.TradingAccountID, until); err != nil {
			return err
		}
		return tx.UpdateTradingAccountReadiness(
			account.SpaceID, account.TradingAccountID, false, s.now().UnixMilli(), awaitingBalanceReason,
		)
	})
}

func (s *Service) applyOrderState(
	ctx context.Context,
	record store.OrderRecord,
	current exchange.Order,
) error {
	exchangeUpdatedAt := current.UpdatedAt.UnixMilli()
	if exchangeUpdatedAt <= 0 {
		exchangeUpdatedAt = current.CreatedAt.UnixMilli()
	}
	if exchangeUpdatedAt > 0 && record.ExchangeUpdatedAt > exchangeUpdatedAt {
		return nil
	}
	if current.ExchangeOrderID != "" && record.ExchangeOrderID != "" &&
		record.ExchangeOrderID != current.ExchangeOrderID {
		return fmt.Errorf("trade account sync: conflicting Exchange order ID")
	}
	state := orderdomain.State(record.State)
	if state.Terminal() {
		return nil
	}
	switch current.Status {
	case exchange.OrderStatusCanceled, exchange.OrderStatusPartiallyCanceled:
		return s.Fills.ConfirmCancel(ctx, record.SpaceID, record.OrderID)
	case exchange.OrderStatusRejected, exchange.OrderStatusExpired:
		record.ExchangeUpdatedAt = exchangeUpdatedAt
		return s.terminalize(ctx, record, current.Status)
	}
	next := state
	switch current.Status {
	case exchange.OrderStatusOpen:
		if state == orderdomain.Pending || state == orderdomain.Submitting ||
			state == orderdomain.SubmitUnknown || state == orderdomain.CancelUnknown {
			next = orderdomain.Open
		}
	case exchange.OrderStatusPartiallyFilled:
		if !shared.MustDecimal(record.FilledQuantity).IsZero() &&
			state != orderdomain.Filled {
			next = orderdomain.PartiallyFilled
		}
	case exchange.OrderStatusFilled:
		if shared.MustDecimal(record.FilledQuantity).Cmp(shared.MustDecimal(record.Quantity)) == 0 {
			next = orderdomain.Filled
		}
	}
	exchangeOrderID := record.ExchangeOrderID
	if exchangeOrderID == "" {
		exchangeOrderID = current.ExchangeOrderID
	}
	if next == state && exchangeOrderID == record.ExchangeOrderID &&
		exchangeUpdatedAt <= record.ExchangeUpdatedAt {
		return nil
	}
	expected := record.Version
	record.State = string(next)
	record.ExchangeOrderID = exchangeOrderID
	record.ExchangeUpdatedAt = max(record.ExchangeUpdatedAt, exchangeUpdatedAt)
	record.Version++
	if next.Terminal() && record.FinishedAt == 0 {
		record.FinishedAt = s.now().UnixMilli()
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

func (s *Service) terminalize(
	ctx context.Context,
	record store.OrderRecord,
	status exchange.OrderStatus,
) error {
	state := orderdomain.State(record.State)
	if state.Terminal() {
		if (status == exchange.OrderStatusRejected && state == orderdomain.Rejected) ||
			(status == exchange.OrderStatusExpired && state == orderdomain.Expired) {
			return nil
		}
		return fmt.Errorf("trade account sync: conflicting terminal order state")
	}
	expected := record.Version
	record.Version++
	record.FinishedAt = s.now().UnixMilli()
	if status == exchange.OrderStatusRejected {
		record.State = string(orderdomain.Rejected)
		if record.RejectReason == "" {
			record.RejectReason = "交易所拒单"
		}
	} else {
		record.State = string(orderdomain.Expired)
	}
	record.RemainingReservedQuantity = "0"
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

func (s *Service) importExternalOrder(
	ctx context.Context,
	account store.TradingAccountRecord,
	current exchange.Order,
	batchFilled shared.Decimal,
) (store.OrderRecord, error) {
	if strings.TrimSpace(current.ExchangeOrderID) == "" ||
		strings.TrimSpace(current.ExchangeSymbol) == "" ||
		!current.Market.Valid() ||
		!account.SupportsMarket(string(current.Market)) ||
		!current.Side.Valid() ||
		current.Quantity.Cmp(shared.Zero()) <= 0 {
		return store.OrderRecord{}, fmt.Errorf("trade account sync: incomplete external order")
	}
	clientOrderID := current.ClientOrderID
	if strings.TrimSpace(clientOrderID) == "" {
		clientOrderID = "external-" + string(current.Market) + "-" + current.ExchangeSymbol + "-" + current.ExchangeOrderID
	}
	environment := account.Environment
	if account.ExecutionMode == "PAPER" || environment == "" {
		environment = "PRODUCTION"
	}
	instrument, err := s.Store.GetInstrumentInEnvironment(
		ctx, account.Exchange, environment, string(current.Market), current.ExchangeSymbol,
	)
	if err != nil {
		return store.OrderRecord{}, err
	}
	reference := current.AveragePrice
	if current.LimitPrice != nil {
		reference = *current.LimitPrice
	}
	if reference.Cmp(shared.Zero()) <= 0 {
		reference = shared.MustDecimal(instrument.PriceTick)
	}
	orderType := current.OrderType
	if orderType != exchange.OrderTypeLimit && orderType != exchange.OrderTypeMarket {
		orderType = exchange.OrderTypeMarket
	}
	tif := current.TimeInForce
	var limitPrice *string
	if orderType == exchange.OrderTypeLimit {
		if current.LimitPrice == nil {
			return store.OrderRecord{}, fmt.Errorf(
				"trade account sync: external LIMIT order has no price",
			)
		}
		value := current.LimitPrice.String()
		limitPrice = &value
		if !tif.ValidForLimit() {
			tif = exchange.TimeInForceGTC
		}
	} else {
		tif = exchange.TimeInForceUnspecified
	}
	positionSide := exchange.RuleFor(current.Market).PositionSide
	referenceAt := current.CreatedAt
	if referenceAt.IsZero() {
		referenceAt = current.UpdatedAt
	}
	if referenceAt.IsZero() {
		referenceAt = s.now()
	}
	ownerID := current.ExchangeOrderID
	if ownerID == "" {
		ownerID = clientOrderID
	}
	logicalAccountID := ""
	if logicalAccount, _, findErr := s.Store.FindLogicalAccountByTradingAccount(
		ctx, account.SpaceID, account.TradingAccountID,
	); findErr == nil {
		logicalAccountID = logicalAccount.LogicalAccountID
	} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
		return store.OrderRecord{}, findErr
	}
	// 账户创建前下的挂单，创建前的成交已被过滤，累计成交量要从这里起步，否则缺口永远补不上。
	// 成交先于订单处理，要减掉本批还会入账的部分。
	filledQuantity, averagePrice := "0", "0"
	if !current.CreatedAt.IsZero() && current.CreatedAt.Before(account.CreatedAt) {
		if seed := current.FilledQuantity.Sub(batchFilled); seed.Cmp(shared.Zero()) > 0 {
			filledQuantity, averagePrice = seed.String(), current.AveragePrice.String()
		}
	}
	record := store.OrderRecord{
		SpaceID: account.SpaceID,
		OrderID: "external:" + account.TradingAccountID + ":" + string(current.Market) + ":" +
			current.ExchangeSymbol + ":" + current.ExchangeOrderID,
		TradingAccountID: account.TradingAccountID,
		ClientOrderID:    clientOrderID, ExchangeOrderID: current.ExchangeOrderID,
		Exchange: account.Exchange, MarketType: string(current.Market),
		ExchangeSymbol: current.ExchangeSymbol, OrderType: string(orderType),
		TimeInForce: string(tif), Side: string(current.Side),
		PositionSide: string(positionSide), Quantity: current.Quantity.String(),
		LimitPrice: limitPrice, ReferencePrice: reference.String(),
		ReferencePriceAt: referenceAt.UnixMilli(), ReduceOnly: current.ReduceOnly,
		OwnerType: "EXTERNAL", OwnerID: ownerID,
		LogicalAccountID: logicalAccountID, State: string(orderdomain.Open),
		FilledQuantity: filledQuantity, AveragePrice: averagePrice, ReservedQuantity: "0",
		RemainingReservedQuantity: "0",
		ExchangeUpdatedAt:         current.UpdatedAt.UnixMilli(),
		Version:                   1,
	}
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	})
	if errors.Is(err, store.ErrConflict) {
		return s.Store.GetOrderByClientID(
			ctx,
			account.SpaceID,
			account.TradingAccountID,
			clientOrderID,
		)
	}
	return record, err
}

func (s *Service) fail(
	ctx context.Context,
	account store.TradingAccountRecord,
	cause error,
) (Result, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if setErr := s.setReady(cleanupCtx, account, false, cause); setErr != nil {
		return Result{}, errors.Join(cause, setErr)
	}
	return Result{}, cause
}

// A failed refresh must disable stale admission even while another operation
// owns the account mutex. Only persist readiness; never retry that mutex here.
func (s *Service) failLock(ctx context.Context, tradingAccountID string, cause error) (Result, error) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	account, err := s.Store.GetTradingAccountByID(cleanupCtx, tradingAccountID)
	if err != nil {
		return Result{}, errors.Join(cause, err)
	}
	if err := s.setReady(cleanupCtx, account, false, cause); err != nil {
		return Result{}, errors.Join(cause, err)
	}
	return Result{}, cause
}

func (s *Service) validate() error {
	if s == nil || s.Store == nil || s.Adapters == nil || s.Fills == nil {
		return ErrServiceConfig
	}
	return nil
}

// wakeTargets 在账户事实变化后唤醒目标收敛，调用时不能持有账户锁。
func (s *Service) wakeTargets() {
	if s.Wake != nil {
		s.Wake()
	}
}

// lockSyncScope 拿到同步需要的执行锁和账户锁，不碰全局成员锁：同步期间要调用 REST，
// 持有全局锁会让其他组合账户的同步和成交入账一起排队。成员变更同时持有这两把锁，
// 所以复核通过后归属不会变化；复核不一致说明刚好发生了成员变更，释放后重试。
func (s *Service) lockSyncScope(ctx context.Context, tradingAccountID string) (unlockAccount, unlockExecution func(), err error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return nil, nil, err
	}
	for attempt := 0; attempt < 4; attempt++ {
		logicalAccountID, err := s.logicalAccountOf(ctx, account.SpaceID, tradingAccountID)
		if err != nil {
			return nil, nil, err
		}
		execution := func() {}
		if logicalAccountID != "" {
			execution, err = s.Store.LockLogicalAccountExecutionContext(ctx, account.SpaceID, logicalAccountID)
			if err != nil {
				return nil, nil, &orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "sync_execution_lock", Err: err}
			}
		}
		unlock, err := s.Store.LockTradingAccountContext(ctx, tradingAccountID)
		if err != nil {
			execution()
			return nil, nil, &orderapp.AccountExecutionError{TradingAccountID: tradingAccountID, Operation: "sync_account_lock", Err: err}
		}
		confirmed, err := s.logicalAccountOf(ctx, account.SpaceID, tradingAccountID)
		if err == nil && confirmed == logicalAccountID {
			return unlock, execution, nil
		}
		unlock()
		execution()
		if err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, fmt.Errorf("%w: logical account membership changed during sync", store.ErrConflict)
}

func (s *Service) logicalAccountOf(ctx context.Context, spaceID, tradingAccountID string) (string, error) {
	logicalAccount, _, err := s.Store.FindLogicalAccountByTradingAccount(ctx, spaceID, tradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return logicalAccount.LogicalAccountID, nil
}

func (s *Service) pauseForExternalFact(
	ctx context.Context,
	tradingAccountID string,
) error {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return err
	}
	logicalAccount, _, err := s.Store.FindLogicalAccountByTradingAccount(
		ctx,
		account.SpaceID,
		tradingAccountID,
	)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// 自主下单账户没有自动执行，用户在交易所手工成交是正常操作。
	if logicalAccount.ControlMode == "MANUAL" {
		return nil
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLogicalAccountAutomation(
			logicalAccount.SpaceID,
			logicalAccount.LogicalAccountID,
			"PAUSED",
			store.AutoPauseReason("发现外部订单或成交 EXTERNAL："+tradingAccountID),
		)
	})
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) lateFillWindow() time.Duration {
	if s != nil && s.LateFillWindow > 0 {
		return s.LateFillWindow
	}
	return 5 * time.Minute
}

func cloneCursors(current store.FillCursors) store.FillCursors {
	result := make(store.FillCursors, len(current))
	for symbol, cursor := range current {
		result[symbol] = cursor
	}
	return result
}

func mergePrivateSnapshot(
	current store.TradingAccountSnapshot,
	update exchange.AccountSnapshot,
) store.TradingAccountSnapshot {
	incoming := store.SnapshotFromExchange(update)
	if update.Present.Balances {
		type balanceKey struct{ wallet, asset string }
		balances := make(
			map[balanceKey]store.AssetBalance,
			len(current.Balances)+len(incoming.Balances),
		)
		order := make([]balanceKey, 0, len(current.Balances)+len(incoming.Balances))
		for _, balance := range current.Balances {
			key := balanceKey{balance.Wallet, balance.Asset}
			if _, found := balances[key]; !found {
				order = append(order, key)
			}
			balances[key] = balance
		}
		for _, balance := range incoming.Balances {
			key := balanceKey{balance.Wallet, balance.Asset}
			if _, found := balances[key]; !found {
				order = append(order, key)
			}
			balances[key] = balance
			// 私有流只带来部分钱包的余额：这些钱包的更新时间跟着推进，其他钱包保持原样。
			if current.WalletUpdatedAt == nil {
				current.WalletUpdatedAt = make(map[string]int64)
			}
			current.WalletUpdatedAt[balance.Wallet] = incoming.ExchangeUpdatedAt
		}
		current.Balances = current.Balances[:0]
		for _, key := range order {
			current.Balances = append(current.Balances, balances[key])
		}
	}
	for _, field := range []struct {
		present     bool
		destination *string
		source      string
	}{
		{update.Present.Equity, &current.Equity, incoming.Equity},
		{update.Present.AvailableFunds, &current.AvailableFunds, incoming.AvailableFunds},
		{update.Present.UsedMargin, &current.UsedMargin, incoming.UsedMargin},
		{
			update.Present.MaintenanceMargin,
			&current.MaintenanceMargin,
			incoming.MaintenanceMargin,
		},
		{update.Present.UnrealizedPnL, &current.UnrealizedPnL, incoming.UnrealizedPnL},
	} {
		if field.present {
			*field.destination = field.source
		}
	}
	current.ExchangeUpdatedAt = incoming.ExchangeUpdatedAt
	return current
}

func positionRecord(
	account store.TradingAccountRecord,
	position exchange.Position,
) store.PositionRecord {
	exchangeSymbol := position.ExchangeSymbol
	return store.PositionRecord{
		SpaceID: account.SpaceID, TradingAccountID: account.TradingAccountID,
		MarketType: string(position.Market), InstrumentID: position.InstrumentID, ExchangeSymbol: exchangeSymbol,
		PositionSide:   string(position.PositionSide),
		SignedQuantity: position.SignedQuantity.String(),
		EntryPrice:     position.EntryPrice.String(), MarkPrice: position.MarkPrice.String(),
		Leverage: position.Leverage.String(), MarginMode: string(position.MarginMode),
		UsedMargin:        position.UsedMargin.String(),
		LiquidationPrice:  position.LiquidationPrice.String(),
		UnrealizedPnL:     position.UnrealizedPnL.String(),
		RealizedPnL:       position.RealizedPnL.String(),
		ExchangeUpdatedAt: position.ExchangeUpdatedAt.UnixMilli(),
	}
}
