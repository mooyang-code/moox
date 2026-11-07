// Package wallet 实现币安普通账户的钱包划转与归集（执行计划 4.5、T9）：
// 现货买入前（第 4 阶段）把资金从合约钱包划进现货钱包，目标执行完后（第 6 阶段）把现货钱包多出来的资金归集回合约钱包。
// 统一账户、OKX 和模拟盘不划转。
//
// 币安的万向划转接口没有客户端流水号：先写一条 PENDING 记录再请求；请求超时、进程崩溃时，
// 靠记录的请求时间、资产和金额去对账交易所的划转记录，确认没到账才重试，不会重复划转。
// 阶段不单独存储，每一轮都从划转表和当前余额推出该做什么，重启后接着走。
package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// buyMarginRatio 是现货买单名义总额的放大系数：留出手续费和报价波动的余量。
	buyMarginRatio = "1.02"
	// transferCapRatio 是合约钱包可划额度里实际划出的比例，留一点余地给同时发生的保证金变化。
	transferCapRatio = "0.99"
	// minTransferAmount 是低于它就不划转的金额（USDT）：避免为几分钱来回划转。
	minTransferAmount = "1"
	// collectKeepAmount 是归集后现货钱包里至少留下的 USDT。
	collectKeepAmount = "1"
	// collectMinThreshold、collectEquityRatio：多出来的资金低于 max(100U, 权益 × 1%) 不归集，避免 1 分钟级策略每期来回划转。
	collectMinThreshold = "100"
	collectEquityRatio  = "0.01"
	// confirmGrace 是请求交易所后还没有拿到流水号时，至少等多久才能断定“没有到账”：交易所的划转记录可能有延迟。
	confirmGrace = time.Minute
	// historyLookback 是对账时回看交易所划转记录的提前量（本地与交易所时钟有偏差）。
	historyLookback = 2 * time.Minute
	// maxTransferFailures 是同一个目标、同一方向连续失败多少次后不再重试，交给人处理。
	maxTransferFailures = 3
)

// AdapterSource 按执行账户取适配器。
type AdapterSource interface {
	Adapter(accountID string) (execution.ExecutionAdapter, error)
}

// Service 实现 target.WalletTransfer。
type Service struct {
	Store    *store.Store
	Adapters AdapterSource
	// RequestSync 请求会话尽快做一次完整同步（划转完成后等现货钱包的快照追上）；为空时只等周期同步。
	RequestSync func(accountID string)
	Now         func() time.Time
	NewID       func() string
}

var _ target.WalletTransfer = (*Service)(nil)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) newID() string {
	if s.NewID != nil {
		return s.NewID()
	}
	id, err := gonanoid.New()
	if err != nil {
		panic(fmt.Sprintf("generate transfer ID: %v", err))
	}
	return id
}

func dec(value string) shared.Decimal { return shared.MustDecimal(value) }

// transferer 取账户适配器的划转能力。
func (s *Service) transferer(accountID string) (execution.ExecutionAdapter, execution.WalletTransferer, error) {
	adapter, err := s.Adapters.Adapter(accountID)
	if err != nil {
		return nil, nil, err
	}
	transferer, ok := adapter.(execution.WalletTransferer)
	if !ok || !execution.Supports(adapter, execution.CapabilityTransfer) {
		return nil, nil, fmt.Errorf("执行账户 %s 的适配器不支持钱包划转", accountID)
	}
	return adapter, transferer, nil
}

// BeforeSpotBuy 实现第 4 阶段。对每个账户：
//  1. 先处理这个目标已有的划转记录：PENDING 的对账（拿流水号、确认到账或失败），COMPLETED 的等现货钱包的快照追上；
//  2. 没有未结的划转时，现查现货、合约两个钱包的余额（不用缓存），划转金额 = max(0, 买单名义总额 × 1.02 − 现货钱包可用 USDT)，
//     不超过合约钱包可划额度 × 0.99；
//  3. 额度不够时告警，现货买单按“本轮能用的资金 ÷ 本轮需要的资金”等比缩小（比例按本轮算，不累计）。
func (s *Service) BeforeSpotBuy(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, buys []target.TransferBuy) (target.TransferOutcome, error) {
	outcome := target.TransferOutcome{BuyScale: map[string]shared.Decimal{}}
	byAccount := map[string][]target.TransferBuy{}
	var order []string
	for _, buy := range buys {
		id := buy.Account.TradingAccountID
		if _, seen := byAccount[id]; !seen {
			order = append(order, id)
		}
		byAccount[id] = append(byAccount[id], buy)
	}
	var errs []error
	for _, accountID := range order {
		pending, scale, err := s.beforeSpotBuyForAccount(ctx, spaceID, targetRecord, byAccount[accountID])
		if err != nil {
			errs = append(errs, fmt.Errorf("账户 %s：%w", accountID, err))
			continue
		}
		outcome.Pending = outcome.Pending || pending
		if scale != nil {
			outcome.BuyScale[accountID] = *scale
		}
	}
	return outcome, errors.Join(errs...)
}

func (s *Service) beforeSpotBuyForAccount(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, buys []target.TransferBuy) (pending bool, scale *shared.Decimal, err error) {
	account := buys[0].Account
	adapter, transferer, err := s.transferer(account.TradingAccountID)
	if err != nil {
		return false, nil, err
	}
	// 1. 这个目标已有的划转：未结的先处理完。
	settled, pending, err := s.settleExisting(ctx, spaceID, targetRecord, account, store.TransferToSpot, transferer)
	if err != nil || pending {
		return pending, nil, err
	}
	if settled.failures >= maxTransferFailures {
		return false, nil, fmt.Errorf("这个目标的划转已连续失败 %d 次，不再重试：%s", settled.failures, settled.lastError)
	}
	// 2. 现查余额和报价，算要划多少。
	requirement, err := s.buyRequirement(ctx, adapter, buys)
	if err != nil {
		return false, nil, err
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return false, nil, err
	}
	asset := account.SettlementAsset
	spotAvailable := walletAvailable(snapshot, exchange.WalletSpot, asset)
	futuresAvailable := walletAvailable(snapshot, exchange.WalletFutures, asset)
	need := requirement.Sub(spotAvailable)
	if need.Cmp(dec(minTransferAmount)) < 0 {
		return false, scaleFor(spotAvailable, requirement), nil
	}
	capacity := futuresAvailable.Mul(dec(transferCapRatio)).RoundDown(8)
	amount := need
	if amount.Cmp(capacity) > 0 {
		amount = capacity
	}
	if amount.Cmp(dec(minTransferAmount)) < 0 {
		// 合约钱包没有可划的资金：不划转，现货买单按现有资金缩小。
		s.alertShortfall(account, requirement, spotAvailable)
		return false, scaleFor(spotAvailable, requirement), nil
	}
	if amount.Cmp(need) < 0 {
		s.alertShortfall(account, requirement, spotAvailable.Add(amount))
	}
	if err := s.startTransfer(ctx, spaceID, targetRecord, account, store.TransferToSpot, asset, amount, len(settled.history), transferer, exchange.WalletFutures, exchange.WalletSpot); err != nil {
		return false, nil, err
	}
	// 划转已发出，这一轮不买：下一轮对账、等快照追上后再按实际余额缩放。
	return true, nil, nil
}

// scaleFor 返回现货买单要缩小到的比例：现货钱包可用资金够用时返回 nil（不缩小），不够按比例缩小，没有资金时为 0。
func scaleFor(available, requirement shared.Decimal) *shared.Decimal {
	if requirement.Cmp(shared.Zero()) <= 0 || available.Cmp(requirement) >= 0 {
		return nil
	}
	ratio := shared.Zero()
	if available.Cmp(shared.Zero()) > 0 {
		ratio = available.Div(requirement)
	}
	return &ratio
}

// buyRequirement 是这些现货买单需要的资金：每个标的 数量 × 卖一价（缺失时用最新价）求和，再乘 1.02。
func (s *Service) buyRequirement(ctx context.Context, adapter execution.ExecutionAdapter, buys []target.TransferBuy) (shared.Decimal, error) {
	total := shared.Zero()
	for _, buy := range buys {
		quote, err := adapter.GetQuote(ctx, exchange.NewSymbolKey(exchange.MarketType(buy.Instrument.MarketType), buy.Instrument.ExchangeSymbol))
		if err != nil {
			return shared.Zero(), fmt.Errorf("取 %s 报价：%w", buy.Instrument.InstrumentID, err)
		}
		price := quote.Ask
		if price.Cmp(shared.Zero()) <= 0 {
			price = quote.Last
		}
		if price.Cmp(shared.Zero()) <= 0 {
			return shared.Zero(), fmt.Errorf("%s 没有可用报价", buy.Instrument.InstrumentID)
		}
		total = total.Add(buy.Quantity.Mul(price))
	}
	return total.Mul(dec(buyMarginRatio)), nil
}

func walletAvailable(snapshot exchange.AccountSnapshot, wallet exchange.Wallet, asset string) shared.Decimal {
	total := shared.Zero()
	for _, balance := range snapshot.Balances {
		if balance.Wallet == wallet && strings.EqualFold(balance.Asset, asset) {
			total = total.Add(balance.Available)
		}
	}
	return total
}

func (s *Service) alertShortfall(account store.TradingAccountRecord, requirement, usable shared.Decimal) {
	detail := fmt.Sprintf("%s：现货买入需要 %s，合约钱包可划转的资金不足，只能用 %s，买单按比例缩小", account.Name, requirement.String(), usable.String())
	log.Warnf("交易划转：%s", detail)
	telemetry.Note(telemetry.AttentionTransferShortfall, detail, s.now())
}

// settleState 是对一个目标、一个方向的划转记录处理后的结果。
type settleState struct {
	history   []store.WalletTransferRecord
	failures  int
	lastError string
	// completed 是最近一条完成的记录（没有为零值）。
	completed store.WalletTransferRecord
}

// settleExisting 处理这个目标在这个账户、这个方向上已有的划转记录：
//   - PENDING：对账交易所的划转记录，拿到流水号、确认到账（置 COMPLETED）或失败（置 FAILED）；还没结论返回 pending；
//   - 最近一条 COMPLETED：等现货钱包的快照晚于完成时间（用最近一次被采用的完整同步时间判断），没追上返回 pending 并请求同步。
func (s *Service) settleExisting(
	ctx context.Context,
	spaceID string,
	targetRecord store.LogicalAccountTargetRecord,
	account store.TradingAccountRecord,
	direction string,
	transferer execution.WalletTransferer,
) (settleState, bool, error) {
	history, err := s.Store.ListWalletTransfersForTarget(ctx, spaceID, targetRecord.LogicalAccountID, targetRecord.TargetID, account.TradingAccountID, direction)
	if err != nil {
		return settleState{}, false, err
	}
	state := settleState{history: history}
	for index, row := range history {
		if row.State == store.TransferStatePending {
			resolved, err := s.reconcile(ctx, spaceID, row, transferer)
			if err != nil {
				return state, false, err
			}
			history[index] = resolved
			row = resolved
		}
		switch row.State {
		case store.TransferStatePending:
			return state, true, nil
		case store.TransferStateFailed:
			state.failures++
			state.lastError = row.LastError
		case store.TransferStateCompleted:
			state.completed = row
		}
	}
	// 最近一条记录是完成的：等快照追上。失败之后又有新记录的不受影响。
	if len(history) > 0 && history[len(history)-1].State == store.TransferStateCompleted && direction == store.TransferToSpot {
		latest, err := s.Store.GetTradingAccount(ctx, spaceID, account.TradingAccountID)
		if err != nil {
			return state, false, err
		}
		if latest.LastSyncAt <= state.completed.CompletedAt {
			if s.RequestSync != nil {
				s.RequestSync(account.TradingAccountID)
			}
			return state, true, nil
		}
	}
	return state, false, nil
}

// reconcile 对账一条 PENDING 的划转记录。
func (s *Service) reconcile(ctx context.Context, spaceID string, row store.WalletTransferRecord, transferer execution.WalletTransferer) (store.WalletTransferRecord, error) {
	since := time.UnixMilli(row.RequestedAt).Add(-historyLookback)
	records, err := transferer.ListTransfers(ctx, since)
	if err != nil {
		// 查不了记录（限频、网络）：保持 PENDING，下一轮再查，不重试划转。
		return row, nil
	}
	claimed, err := s.claimedTransferIDs(ctx, spaceID, row)
	if err != nil {
		return row, err
	}
	var match *execution.TransferRecord
	for index := range records {
		record := records[index]
		if row.ExchangeTransferID != "" {
			if record.ExchangeTransferID == row.ExchangeTransferID {
				match = &record
				break
			}
			continue
		}
		if matchesRow(record, row) && !claimed[record.ExchangeTransferID] {
			match = &record
			break
		}
	}
	if match == nil {
		if row.ExchangeTransferID == "" && s.now().Sub(time.UnixMilli(row.RequestedAt)) >= confirmGrace {
			// 过了对账宽限期，交易所的划转记录里仍然没有：确认没到账，这条记录作废，允许重试。
			return s.finish(ctx, spaceID, row, "", store.TransferStateFailed, "交易所划转记录里没有找到这笔划转，确认没到账", 0)
		}
		return row, nil
	}
	switch match.Status {
	case execution.TransferConfirmed:
		return s.finish(ctx, spaceID, row, match.ExchangeTransferID, store.TransferStateCompleted, "", s.now().UnixMilli())
	case execution.TransferFailed:
		return s.finish(ctx, spaceID, row, match.ExchangeTransferID, store.TransferStateFailed, "交易所划转记录显示失败", 0)
	}
	// 交易所记录还是 PENDING：记下流水号，继续等。PENDING 不等于没到账，不能重试。
	if row.ExchangeTransferID == "" {
		return s.finish(ctx, spaceID, row, match.ExchangeTransferID, store.TransferStatePending, "", 0)
	}
	return row, nil
}

// matchesRow 报告交易所划转记录是否就是这条本地记录：方向、资产、金额一致，发生时间不早于请求时间（容许时钟偏差）。
func matchesRow(record execution.TransferRecord, row store.WalletTransferRecord) bool {
	wantFrom, wantTo := exchange.WalletFutures, exchange.WalletSpot
	if row.Direction == store.TransferToFutures {
		wantFrom, wantTo = exchange.WalletSpot, exchange.WalletFutures
	}
	amount, err := shared.ParseDecimal(row.Amount)
	if err != nil {
		return false
	}
	return record.From == wantFrom && record.To == wantTo && strings.EqualFold(record.Asset, row.Asset) &&
		record.Amount.Cmp(amount) == 0 &&
		!record.At.Before(time.UnixMilli(row.RequestedAt).Add(-historyLookback))
}

// claimedTransferIDs 返回这个账户已经被别的记录认领的交易所流水号，防止两条本地记录对应同一笔划转。
func (s *Service) claimedTransferIDs(ctx context.Context, spaceID string, row store.WalletTransferRecord) (map[string]bool, error) {
	records, err := s.Store.ListWalletTransfersByAccount(ctx, spaceID, row.TradingAccountID, 0, s.now().UnixMilli()+int64(time.Hour/time.Millisecond))
	if err != nil {
		return nil, err
	}
	claimed := map[string]bool{}
	for _, record := range records {
		if record.TransferID != row.TransferID && record.ExchangeTransferID != "" {
			claimed[record.ExchangeTransferID] = true
		}
	}
	return claimed, nil
}

func (s *Service) finish(ctx context.Context, spaceID string, row store.WalletTransferRecord, exchangeID, state, lastError string, completedAt int64) (store.WalletTransferRecord, error) {
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateWalletTransfer(spaceID, row.TransferID, exchangeID, state, lastError, completedAt)
	}); err != nil {
		return row, err
	}
	row.ExchangeTransferID, row.State, row.LastError, row.CompletedAt = exchangeID, state, lastError, completedAt
	return row, nil
}

// startTransfer 先写 PENDING 记录，再请求交易所。请求结果分三种：
//   - 成功：记下流水号，状态仍是 PENDING，下一轮对账确认到账；
//   - 确定没有受理（限频、被拒绝、余额不足）：记录置 FAILED，错误返回，由调用方告警；
//   - 结果不明（超时、传输错误）：记录保持 PENDING 且没有流水号，下一轮靠交易所划转记录对账，确认没到账才重试。
func (s *Service) startTransfer(
	ctx context.Context,
	spaceID string,
	targetRecord store.LogicalAccountTargetRecord,
	account store.TradingAccountRecord,
	direction, asset string,
	amount shared.Decimal,
	attempt int,
	transferer execution.WalletTransferer,
	from, to exchange.Wallet,
) error {
	now := s.now()
	row := store.WalletTransferRecord{
		SpaceID: spaceID, TransferID: s.newID(), TradingAccountID: account.TradingAccountID,
		LogicalAccountID: targetRecord.LogicalAccountID, TargetID: targetRecord.TargetID, Direction: direction,
		Asset: asset, Amount: amount.RoundDown(8).String(),
		ClientRef: fmt.Sprintf("%s:%s:%d", targetRecord.TargetID, direction, attempt+1), State: store.TransferStatePending,
		RequestedAt: now.UnixMilli(),
	}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.InsertWalletTransfer(row) }); err != nil {
		return err
	}
	receipt, err := transferer.Transfer(ctx, from, to, asset, amount.RoundDown(8), row.ClientRef)
	if err != nil {
		if definitelyNotSent(err) {
			if _, finishErr := s.finish(ctx, spaceID, row, "", store.TransferStateFailed, truncate(err.Error(), 200), 0); finishErr != nil {
				return errors.Join(err, finishErr)
			}
		}
		return fmt.Errorf("划转 %s %s（%s）：%w", amount.String(), asset, direction, err)
	}
	if _, err := s.finish(ctx, spaceID, row, receipt.ExchangeTransferID, store.TransferStatePending, "", 0); err != nil {
		// 已经划出去但没记上流水号：下一轮靠金额和时间窗口对账认领，不会重复划。
		return err
	}
	return nil
}

// definitelyNotSent 报告划转请求是否确定没有被交易所执行：限流、被拒绝、余额不足；超时和传输错误结果不明。
func definitelyNotSent(err error) bool {
	if exchange.NotAccepted(err) {
		return true
	}
	return exchange.IsKind(err, exchange.ErrorRejected) || exchange.IsKind(err, exchange.ErrorInsufficientBalance) ||
		exchange.IsKind(err, exchange.ErrorAuthentication)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// Collect 实现第 6 阶段：目标执行完毕后，把现货钱包里多出来的结算资产归集回合约钱包。
// 现货钱包只留 BNB 补买所需的金额（开启 BNB 保留时一半的保留金额）和 1U；多出来的低于 max(100U, 权益 × 1%) 不划，
// 避免 1 分钟级策略每期来回划转。每一轮都会调用：这个目标已经归集过（有记录）就不再划，未结的记录对账到完成为止。
func (s *Service) Collect(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, accounts []store.TradingAccountRecord) (bool, error) {
	pending := false
	var errs []error
	for _, account := range accounts {
		accountPending, err := s.collectForAccount(ctx, spaceID, targetRecord, account)
		if err != nil {
			errs = append(errs, fmt.Errorf("账户 %s：%w", account.TradingAccountID, err))
			continue
		}
		pending = pending || accountPending
	}
	return pending, errors.Join(errs...)
}

func (s *Service) collectForAccount(ctx context.Context, spaceID string, targetRecord store.LogicalAccountTargetRecord, account store.TradingAccountRecord) (bool, error) {
	adapter, transferer, err := s.transferer(account.TradingAccountID)
	if err != nil {
		return false, err
	}
	state, pending, err := s.settleExisting(ctx, spaceID, targetRecord, account, store.TransferToFutures, transferer)
	if err != nil || pending {
		return pending, err
	}
	if len(state.history) > 0 {
		// 这个目标已经归集过（或失败过）：每个目标最多归集一次，失败的留给下一份目标。
		return false, nil
	}
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return false, err
	}
	asset := account.SettlementAsset
	spotAvailable := walletAvailable(snapshot, exchange.WalletSpot, asset)
	keep := dec(collectKeepAmount)
	logical, err := s.Store.GetLogicalAccount(ctx, spaceID, targetRecord.LogicalAccountID)
	if err != nil {
		return false, err
	}
	if settings, _, parseErr := fundsettings.ParseLenient(logical.FundSettingsJSON); parseErr == nil && settings.BNBReserveAmount.Cmp(shared.Zero()) > 0 {
		keep = keep.Add(settings.BNBReserveAmount.Div(dec("2")))
	}
	excess := spotAvailable.Sub(keep).RoundDown(8)
	threshold, err := s.collectThreshold(ctx, spaceID, account, snapshot)
	if err != nil {
		return false, err
	}
	if excess.Cmp(threshold) < 0 {
		return false, nil
	}
	if err := s.startTransfer(ctx, spaceID, targetRecord, account, store.TransferToFutures, asset, excess, 0, transferer, exchange.WalletSpot, exchange.WalletFutures); err != nil {
		return false, err
	}
	return true, nil
}

// collectThreshold 是归集阈值 max(100U, 权益 × 1%)。权益取最近一次权益采样；没有采样时用两个钱包的结算资产合计估算。
func (s *Service) collectThreshold(ctx context.Context, spaceID string, account store.TradingAccountRecord, snapshot exchange.AccountSnapshot) (shared.Decimal, error) {
	threshold := dec(collectMinThreshold)
	equity := shared.Zero()
	now := s.now()
	points, err := s.Store.ListAccountEquityPoints(ctx, spaceID, account.TradingAccountID, now.Add(-time.Hour).Truncate(time.Minute).UnixMilli(), now.UnixMilli())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return shared.Zero(), err
	}
	if len(points) > 0 {
		if parsed, parseErr := shared.ParseDecimal(points[len(points)-1].Equity); parseErr == nil {
			equity = parsed
		}
	}
	if equity.IsZero() {
		for _, balance := range snapshot.Balances {
			if strings.EqualFold(balance.Asset, account.SettlementAsset) {
				equity = equity.Add(balance.Total)
			}
		}
	}
	if scaled := equity.Mul(dec(collectEquityRatio)); scaled.Cmp(threshold) > 0 {
		threshold = scaled
	}
	return threshold, nil
}
