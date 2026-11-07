package equity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

type Service struct {
	Store    *store.Store
	Adapters interface {
		Adapter(string) (execution.ExecutionAdapter, error)
	}
	Now          func() time.Time
	SourceMaxAge time.Duration

	// missingSettlementWarned 记录缺结算币明细告警的时间（执行账户 -> 毫秒），同一个账户一小时只告警一次。
	warnMu                  sync.Mutex
	missingSettlementWarned map[string]int64
}

func (s *Service) ListSampleAccounts(ctx context.Context) ([]string, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("equity: store is not configured")
	}
	accounts, err := s.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if account.Status == "ENABLED" && account.Ready {
			ids = append(ids, account.TradingAccountID)
		}
	}
	return ids, nil
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) sourceMaxAge() time.Duration {
	if s != nil && s.SourceMaxAge > 0 {
		return s.SourceMaxAge
	}
	// 完整同步间隔默认 30s、权益采样每 20s：有效期必须覆盖“同步间隔 + 采样间隔”并留出执行余量，
	// 否则健康账户会在两次采样之间出现策略目标因权益过期而无法转换的固定空档。
	return 90 * time.Second
}

// confirmedSnapshot 把余额快照的新鲜度来源换成“最近一次被确认的完整余额同步”的时间：
// 交易所给的余额时间（如 Binance 的资产 updateTime）是最后变更时间，余额长期不变时会很旧，
// 但完整同步持续成功就说明余额仍然准确。LastSyncAt 只在被采用且覆盖待确认水位的快照上推进。
func confirmedSnapshot(account store.TradingAccountRecord) store.TradingAccountSnapshot {
	snapshot := account.Snapshot
	if account.LastSyncAt > snapshot.ExchangeUpdatedAt {
		snapshot.ExchangeUpdatedAt = account.LastSyncAt
	}
	return snapshot
}

func (s *Service) sourceFresh(sourceMillis int64) bool {
	if sourceMillis <= 0 {
		return false
	}
	age := s.now().Sub(time.UnixMilli(sourceMillis).UTC())
	return age <= s.sourceMaxAge() && age >= -s.sourceMaxAge()
}

func MinuteBucket(at time.Time) int64 { return at.UTC().Truncate(time.Minute).UnixMilli() }

// ResolveLogicalAccountEquity returns the most recent persisted logical
// equity snapshot and its source timestamp. Target conversion uses this
// method rather than live account balances so retries are auditable.
func (s *Service) ResolveLogicalAccountEquity(ctx context.Context, spaceID, logicalAccountID string) (shared.Decimal, int64, error) {
	if s == nil || s.Store == nil {
		return shared.Zero(), 0, fmt.Errorf("equity: store is not configured")
	}
	point, err := s.Store.LatestLogicalAccountEquityPoint(ctx, spaceID, logicalAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return shared.Zero(), 0, fmt.Errorf("equity: no logical account snapshot")
	}
	if err != nil {
		return shared.Zero(), 0, err
	}
	equity, err := shared.ParseDecimal(point.Equity)
	if err != nil || equity.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), point.SourceTime, fmt.Errorf("equity: 组合账户权益为 0，请先准备结算资产")
	}
	if !s.sourceFresh(point.SourceTime) {
		return shared.Zero(), point.SourceTime, fmt.Errorf("equity: latest logical snapshot is stale")
	}
	return equity, point.SourceTime, nil
}

func (s *Service) SampleAccount(ctx context.Context, accountID string) error {
	if s == nil || s.Store == nil {
		return fmt.Errorf("equity: store is not configured")
	}
	account, err := s.Store.GetTradingAccountByID(ctx, accountID)
	if err != nil {
		return err
	}
	valued, valueErr := s.valueSnapshot(ctx, account, confirmedSnapshot(account))
	if valueErr != nil {
		return valueErr
	}
	snapshot := valued
	var pnl *string
	if strings.TrimSpace(snapshot.UnrealizedPnL) != "" {
		value := snapshot.UnrealizedPnL
		pnl = &value
	}
	sourceTime := snapshot.ExchangeUpdatedAt
	if sourceTime <= 0 {
		sourceTime = s.now().UnixMilli()
	}
	point := store.EquityPointRecord{SpaceID: account.SpaceID, TradingAccountID: accountID, BucketTime: MinuteBucket(s.now()), Equity: snapshot.Equity, AvailableFunds: snapshot.AvailableFunds, UsedMargin: snapshot.UsedMargin, UnrealizedPnL: pnl, SourceTime: sourceTime}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpsertAccountEquityPoint(point) }); err != nil {
		return err
	}
	return s.sampleLogicalAccounts(ctx, account.SpaceID, accountID, &snapshot)
}

// shouldWarnMissingSettlement 限频：同一个账户一小时只告警一次（权益采样每 20 秒一次，不能刷屏）。
func (s *Service) shouldWarnMissingSettlement(accountID string) bool {
	now := s.now().UnixMilli()
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if s.missingSettlementWarned == nil {
		s.missingSettlementWarned = make(map[string]int64)
	}
	if last, ok := s.missingSettlementWarned[accountID]; ok && now-last < int64(time.Hour/time.Millisecond) {
		return false
	}
	s.missingSettlementWarned[accountID] = now
	return true
}

// fundSettings 读取执行账户所属组合的资金设置；不在任何组合里时取默认值。
func (s *Service) fundSettings(ctx context.Context, account store.TradingAccountRecord) (fundsettings.Settings, error) {
	logical, _, err := s.Store.FindLogicalAccountByTradingAccount(ctx, account.SpaceID, account.TradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fundsettings.Parse("")
	}
	if err != nil {
		return fundsettings.Settings{}, err
	}
	return fundsettings.Parse(logical.FundSettingsJSON)
}

// valueSnapshot 按统一公式计算执行账户的权益（plan 4.6）：
//
//	权益 = 结算资产在各钱包的净额（含负债为负）+ 永续未实现盈亏 + 受管现货市值 + 套利底仓现货市值
//
// 保证金币的配置值是组合层的数，由组合权益加一次，不在这里加。底仓对冲腿的未实现盈亏已包含在永续未实现盈亏里，底仓净值 = 底仓现货市值 + 对冲腿盈亏，所以公式只多加底仓现货市值。
// 交易所给的总权益不参与计算，只在 ExchangeEquity 里留作展示核对。没有报价时不写入，避免留下误导的零值曲线。
func (s *Service) valueSnapshot(ctx context.Context, account store.TradingAccountRecord, snapshot store.TradingAccountSnapshot) (store.TradingAccountSnapshot, error) {
	if account.HasBalanceMarket() && !s.sourceFresh(snapshot.ExchangeUpdatedAt) {
		return store.TradingAccountSnapshot{}, fmt.Errorf("equity: stale Spot balance snapshot")
	}
	settlement := account.SettlementAsset
	settings, err := s.fundSettings(ctx, account)
	if err != nil {
		return store.TradingAccountSnapshot{}, err
	}
	managed, err := s.Store.ManagedAssets(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return store.TradingAccountSnapshot{}, err
	}
	unrealized := decimal(snapshot.UnrealizedPnL)
	settlementNet := shared.Zero()
	settlementSeen := false
	settlementAvailable := shared.Zero()
	var held []store.AssetBalance
	stableValue := shared.Zero()
	for _, balance := range snapshot.Balances {
		quantity := decimal(balance.Total).Sub(decimal(balance.Borrowed))
		if strings.EqualFold(balance.Asset, settlement) {
			settlementNet = settlementNet.Add(quantity)
			settlementAvailable = settlementAvailable.Add(decimal(balance.Available))
			settlementSeen = true
			continue
		}
		// 多稳定币保证金：除结算币外的稳定币按市价折成结算币计入权益，不然调平时权益会凭空少一块。
		if _, isStable := settings.StablecoinTargets[strings.ToUpper(balance.Asset)]; isStable {
			if !quantity.IsZero() {
				stableValue = stableValue.Add(quantity.Mul(s.stablecoinPrice(ctx, account, balance.Asset)))
			}
			continue
		}
		_, isManaged := managed[balance.Asset]
		if (isManaged || settings.IsArbitrageBase(balance.Asset)) && !quantity.IsZero() && account.IsSpotWallet(balance.Wallet) {
			balance.Total = quantity.String()
			held = append(held, balance)
		}
	}
	if !settlementSeen {
		// 快照里没有结算币明细时，不能退回交易所给的总权益（它含未实现盈亏和其他币，会重复计入）：结算资产净额按 0 处理并告警。
		if s.shouldWarnMissingSettlement(account.TradingAccountID) {
			log.WarnContextf(ctx, "交易关注项：执行账户 %s 的余额快照里没有结算币 %s 的明细，权益里的结算资产净额按 0 计", account.TradingAccountID, settlement)
		}
	}
	// 保证金币的配置值是组合层的数，在组合层只加一次，不在每个成员里各加一遍。
	equity := settlementNet.Add(unrealized).Add(stableValue)
	if len(held) > 0 {
		if s.Adapters == nil {
			return store.TradingAccountSnapshot{}, fmt.Errorf("equity: spot quote adapter is not configured")
		}
		adapter, err := s.Adapters.Adapter(account.TradingAccountID)
		if err != nil {
			return store.TradingAccountSnapshot{}, err
		}
		instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
		if err != nil {
			return store.TradingAccountSnapshot{}, err
		}
		// 只有现货标的给持有的币估值：同名的合约标的有自己的报价，不能顶替现货价。
		byAsset := make(map[string]store.InstrumentRecord)
		for _, instrument := range instruments {
			if exchange.RuleFor(instrument.MarketType).PositionFromBalance && strings.EqualFold(instrument.QuoteAsset, settlement) {
				byAsset[strings.ToUpper(instrument.BaseAsset)] = instrument
			}
		}
		for _, balance := range held {
			instrument, found := byAsset[strings.ToUpper(balance.Asset)]
			if !found {
				return store.TradingAccountSnapshot{}, fmt.Errorf("equity: no %s/%s Spot instrument", balance.Asset, settlement)
			}
			symbol := instrument.ExchangeSymbol
			quote, quoteErr := adapter.GetReferencePrice(ctx, exchange.NewSymbolKey(instrument.MarketType, symbol))
			if quoteErr != nil {
				return store.TradingAccountSnapshot{}, quoteErr
			}
			if !s.sourceFresh(quote.UpdatedAt.UnixMilli()) {
				return store.TradingAccountSnapshot{}, fmt.Errorf("equity: stale Spot quote for %s", symbol)
			}
			equity = equity.Add(decimal(balance.Total).Mul(quote.Price))
			// 新鲜度取决于最旧的输入：新报价不能让旧余额看起来是最新的。
			quoteTime := quote.UpdatedAt.UnixMilli()
			if snapshot.ExchangeUpdatedAt <= 0 || quoteTime < snapshot.ExchangeUpdatedAt {
				snapshot.ExchangeUpdatedAt = quoteTime
			}
		}
	}
	snapshot.Equity = equity.String()
	if !account.HasPositionMarket() {
		snapshot.AvailableFunds = settlementAvailable.String()
	}
	return snapshot, nil
}

// stablecoinPrice 返回稳定币折成结算币的价格：取它对结算币的现货标的参考价；
// 没有标的、取不到报价时按 1 计（稳定币锚定结算币，估值不能因为一时没有报价就中断权益采样）。
func (s *Service) stablecoinPrice(ctx context.Context, account store.TradingAccountRecord, asset string) shared.Decimal {
	one := shared.MustDecimal("1")
	if s.Adapters == nil {
		return one
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		return one
	}
	for _, instrument := range instruments {
		if !exchange.RuleFor(instrument.MarketType).PositionFromBalance ||
			!strings.EqualFold(instrument.BaseAsset, asset) || !strings.EqualFold(instrument.QuoteAsset, account.SettlementAsset) {
			continue
		}
		adapter, adapterErr := s.Adapters.Adapter(account.TradingAccountID)
		if adapterErr != nil {
			return one
		}
		quote, quoteErr := adapter.GetReferencePrice(ctx, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
		if quoteErr != nil || quote.Price.Cmp(shared.Zero()) <= 0 || !s.sourceFresh(quote.UpdatedAt.UnixMilli()) {
			return one
		}
		return quote.Price
	}
	return one
}

func (s *Service) sampleLogicalAccounts(ctx context.Context, spaceID, accountID string, sampled *store.TradingAccountSnapshot) error {
	logicals, err := s.Store.ListLogicalAccounts(ctx, spaceID)
	if err != nil {
		return err
	}
	// 单个组合账户失败不影响其他组合账户。
	var sampleErrors []error
	for _, logical := range logicals {
		if err := s.sampleLogicalAccount(ctx, spaceID, accountID, logical, sampled); err != nil {
			sampleErrors = append(sampleErrors, fmt.Errorf("equity: logical account %s: %w", logical.LogicalAccountID, err))
		}
	}
	return errors.Join(sampleErrors...)
}

func (s *Service) sampleLogicalAccount(
	ctx context.Context,
	spaceID string,
	accountID string,
	logical store.LogicalAccountRecord,
	sampled *store.TradingAccountSnapshot,
) error {
	// Disabled memberships are not executable and must not contribute
	// capital to a logical account target conversion.
	members, memberErr := s.Store.ListLogicalAccountMembers(ctx, spaceID, logical.LogicalAccountID, false)
	if memberErr != nil {
		return memberErr
	}
	contains := false
	for _, member := range members {
		if member.TradingAccountID == accountID {
			contains = true
			break
		}
	}
	if !contains {
		return nil
	}
	equity, available, used, unrealized := shared.Zero(), shared.Zero(), shared.Zero(), shared.Zero()
	allPnl := true
	logicalValid := true
	var sourceTime int64
	// Store ordering is priority/account ID, which is not the same as the
	// account that produced this fresh sample. Prefer that member so its
	// newest snapshot is used.
	orderedMembers := append([]store.LogicalAccountMemberRecord(nil), members...)
	for i := range orderedMembers {
		if orderedMembers[i].TradingAccountID == accountID {
			orderedMembers[0], orderedMembers[i] = orderedMembers[i], orderedMembers[0]
			break
		}
	}
	for _, member := range orderedMembers {
		memberAccount, accountErr := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
		if accountErr != nil {
			return accountErr
		}
		if memberAccount.Status != string(exchange.AccountStatusEnabled) || !memberAccount.Ready {
			logicalValid = false
			break
		}
		memberSnapshot := confirmedSnapshot(memberAccount)
		if member.TradingAccountID == accountID && sampled != nil {
			memberSnapshot = *sampled
		} else {
			valued, valueErr := s.valueSnapshot(ctx, memberAccount, memberSnapshot)
			if valueErr != nil {
				return valueErr
			}
			memberSnapshot = valued
		}
		if !s.sourceFresh(memberSnapshot.ExchangeUpdatedAt) {
			logicalValid = false
			break
		}
		equity = equity.Add(decimal(memberSnapshot.Equity))
		available = available.Add(decimal(memberSnapshot.AvailableFunds))
		used = used.Add(decimal(memberSnapshot.UsedMargin))
		unrealized = unrealized.Add(decimal(memberSnapshot.UnrealizedPnL))
		if strings.TrimSpace(memberSnapshot.UnrealizedPnL) == "" {
			allPnl = false
		}
		memberSourceTime := memberSnapshot.ExchangeUpdatedAt
		if sourceTime == 0 || memberSourceTime < sourceTime {
			sourceTime = memberSourceTime
		}
	}
	if !logicalValid || sourceTime <= 0 {
		return nil
	}
	settings, settingsErr := fundsettings.Parse(logical.FundSettingsJSON)
	if settingsErr != nil {
		return settingsErr
	}
	equity = equity.Add(settings.MarginCoinValue())
	var pnl *string
	if allPnl {
		value := unrealized.String()
		pnl = &value
	}
	point := store.EquityPointRecord{SpaceID: spaceID, LogicalAccountID: logical.LogicalAccountID, BucketTime: MinuteBucket(s.now()), Equity: equity.String(), AvailableFunds: available.String(), UsedMargin: used.String(), UnrealizedPnL: pnl, SourceTime: sourceTime}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpsertLogicalAccountEquityPoint(point) })
}

func decimal(raw string) shared.Decimal {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}
