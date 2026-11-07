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
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
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
	// degradedWarned 记录受管资产估值降级告警的时间（执行账户 + 资产 -> 毫秒），同样一小时一次。
	// lastPrices 是受管现货最近一次取到的新鲜报价和取价时间（执行账户 + 资产 -> 价格），取不到价时在保留时间内用它估值。
	warnMu                  sync.Mutex
	missingSettlementWarned map[string]int64
	degradedWarned          map[string]int64
	lastPrices              map[string]execution.LastPrice
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
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if s.missingSettlementWarned == nil {
		s.missingSettlementWarned = make(map[string]int64)
	}
	return s.hourlyLocked(s.missingSettlementWarned, accountID)
}

// hourlyLocked 在 warnMu 内判断某个键这一小时是否已经告警过，没有就记下现在。
func (s *Service) hourlyLocked(seen map[string]int64, key string) bool {
	now := s.now().UnixMilli()
	if last, ok := seen[key]; ok && now-last < int64(time.Hour/time.Millisecond) {
		return false
	}
	seen[key] = now
	return true
}

func priceKey(accountID, asset string) string { return accountID + "\x00" + strings.ToUpper(asset) }

func (s *Service) rememberPrice(accountID, asset string, price shared.Decimal, at time.Time) {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if s.lastPrices == nil {
		s.lastPrices = make(map[string]execution.LastPrice)
	}
	s.lastPrices[priceKey(accountID, asset)] = execution.LastPrice{Price: price, At: at}
}

func (s *Service) lastPrice(accountID, asset string) execution.LastPrice {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	return s.lastPrices[priceKey(accountID, asset)]
}

// noteDegraded 记一条估值降级的关注项并限频写日志。关注项标签只带账户和资产，不带数量和价格（带变化的数字会让指标序列不断增长）。
func (s *Service) noteDegraded(ctx context.Context, account store.TradingAccountRecord, asset, reason, how string) {
	telemetry.Note(telemetry.AttentionValuationDegraded, account.Name+"："+strings.ToUpper(asset)+" 取不到价，权益"+how, s.now())
	s.warnMu.Lock()
	if s.degradedWarned == nil {
		s.degradedWarned = make(map[string]int64)
	}
	warn := s.hourlyLocked(s.degradedWarned, priceKey(account.TradingAccountID, asset))
	s.warnMu.Unlock()
	if warn {
		log.WarnContextf(ctx, "交易关注项：执行账户 %s 的受管资产 %s %s，权益里这部分%s，不阻断采样", account.TradingAccountID, asset, reason, how)
	}
}

// fundSettings 读取执行账户所属组合的资金设置；不在任何组合里时取默认值。
func (s *Service) fundSettings(ctx context.Context, account store.TradingAccountRecord) (fundsettings.Settings, error) {
	logical, _, err := s.Store.FindLogicalAccountByTradingAccount(ctx, account.SpaceID, account.TradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings, _, _ := fundsettings.ParseLenient("")
		return settings, nil
	}
	if err != nil {
		return fundsettings.Settings{}, err
	}
	// 与执行器同一个读取入口（宽松解析）：设置里个别数值坏了不让权益采样中断。
	return s.Store.ResolveFundSettings(logical)
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
	return s.unifiedSnapshot(ctx, account, snapshot, nil)
}

// ValueEquity 按统一公式算执行账户快照的权益，给账户同步写库用：不做新鲜度检查（币安现货余额的时间是最后变更时间，
// 可能很旧），只返回权益，快照的时间和可用资金由同步自己决定。prices 为空时按执行账户取适配器。
func (s *Service) ValueEquity(ctx context.Context, account store.TradingAccountRecord, snapshot store.TradingAccountSnapshot, prices execution.ReferencePriceSource) (string, error) {
	if s == nil || s.Store == nil {
		return "", fmt.Errorf("equity: store is not configured")
	}
	valued, err := s.unifiedSnapshot(ctx, account, snapshot, prices)
	if err != nil {
		return "", err
	}
	return valued.Equity, nil
}

// unifiedSnapshot 是统一权益公式本身（见 valueSnapshot），不检查余额新鲜度。
func (s *Service) unifiedSnapshot(ctx context.Context, account store.TradingAccountRecord, snapshot store.TradingAccountSnapshot, prices execution.ReferencePriceSource) (store.TradingAccountSnapshot, error) {
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
				stableValue = stableValue.Add(quantity.Mul(s.stablecoinPrice(ctx, account, balance.Asset, prices)))
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
		if prices == nil {
			if s.Adapters == nil {
				return store.TradingAccountSnapshot{}, fmt.Errorf("equity: spot quote adapter is not configured")
			}
			adapter, err := s.Adapters.Adapter(account.TradingAccountID)
			if err != nil {
				return store.TradingAccountSnapshot{}, err
			}
			prices = adapter
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
			price, quoteTime, err := s.heldPrice(ctx, prices, account, byAsset, balance.Asset)
			if err != nil {
				return store.TradingAccountSnapshot{}, err
			}
			equity = equity.Add(decimal(balance.Total).Mul(price))
			// 新鲜度取决于最旧的输入：新报价不能让旧余额看起来是最新的。降级估值（quoteTime 为 0）不参与，
			// 否则一个下架零头会让整个组合的权益一直判为过期。
			if quoteTime > 0 && (snapshot.ExchangeUpdatedAt <= 0 || quoteTime < snapshot.ExchangeUpdatedAt) {
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

// heldPrice 给一笔受管现货余额取估值价格，quoteTime 是报价时间（降级估值时为 0）。
//
// 取不到新鲜报价时按 execution.DegradedPrice 的统一规则降级：不超龄的最后已知价；标的表里没有、在下架名单里、
// 交易所确认不存在的按 0 计；其余不在交易状态的退回最近一笔成交价；标的仍在交易只是暂时取价失败的照旧报错。
// 降级估值不拉低权益的来源时间：一个下架零头不能让整个组合的权益一直判为过期；最后已知价超过保留时间（SourceMaxAge）就不再使用。
func (s *Service) heldPrice(
	ctx context.Context,
	prices execution.ReferencePriceSource,
	account store.TradingAccountRecord,
	byAsset map[string]store.InstrumentRecord,
	asset string,
) (shared.Decimal, int64, error) {
	instrument, found := byAsset[strings.ToUpper(asset)]
	if !found {
		return s.degradedPrice(ctx, account, asset, nil, execution.ValuationMissingInstrument,
			fmt.Errorf("没有 %s/%s 现货标的", asset, account.SettlementAsset))
	}
	if instrument.Status != "" && !exchange.StatusTradable(instrument.Status) {
		return s.degradedPrice(ctx, account, asset, &instrument, execution.ValuationNotTrading,
			fmt.Errorf("所在标的 %s 状态为 %s", instrument.ExchangeSymbol, instrument.Status))
	}
	symbol := instrument.ExchangeSymbol
	quote, quoteErr := prices.GetReferencePrice(ctx, exchange.NewSymbolKey(instrument.MarketType, symbol))
	if quoteErr == nil && !s.sourceFresh(quote.UpdatedAt.UnixMilli()) {
		quoteErr = fmt.Errorf("equity: stale Spot quote for %s", symbol)
	}
	if quoteErr == nil && quote.Price.Cmp(shared.Zero()) <= 0 {
		quoteErr = fmt.Errorf("equity: empty Spot quote for %s", symbol)
	}
	if quoteErr != nil {
		failure := execution.ValuationQuoteFailed
		if exchange.IsInstrumentNotFound(quoteErr) {
			failure = execution.ValuationInstrumentGone
		}
		return s.degradedPrice(ctx, account, asset, &instrument, failure, quoteErr)
	}
	s.rememberPrice(account.TradingAccountID, asset, quote.Price, quote.UpdatedAt)
	return quote.Price, quote.UpdatedAt.UnixMilli(), nil
}

// degradedPrice 按统一规则给取不到新鲜报价的受管现货定价，用上降级估值时记关注项。标的在下架名单里时按“下架”处理（可以按 0 计）。
func (s *Service) degradedPrice(
	ctx context.Context,
	account store.TradingAccountRecord,
	asset string,
	instrument *store.InstrumentRecord,
	failure execution.ValuationFailure,
	cause error,
) (shared.Decimal, int64, error) {
	reason := degradeReason(cause)
	if instrument != nil && failure != execution.ValuationInstrumentGone {
		blocked, err := s.Store.LoadBlockedSet(ctx)
		if err != nil {
			return shared.Zero(), 0, err
		}
		if blocked.ContainsInstrument(account.Exchange, *instrument) {
			failure, reason = execution.ValuationDelisted, "所在标的 "+instrument.ExchangeSymbol+" 在下架名单里"
		}
	}
	var lastFill execution.LastFillPrice
	if instrument != nil {
		lastFill = func(ctx context.Context) (shared.Decimal, bool, error) {
			return latestFillPrice(ctx, s.Store, account, *instrument)
		}
	}
	value, err := execution.DegradedPrice(ctx, failure, s.lastPrice(account.TradingAccountID, asset), s.now(), s.sourceMaxAge(), lastFill, cause)
	if err != nil {
		return shared.Zero(), 0, err
	}
	s.noteDegraded(ctx, account, asset, reason, value.How)
	return value.Price, 0, nil
}

// degradeReason 是写进日志的取不到价的原因：交易所错误只给类别和错误码（原文可能带请求串），本地的原因原样给出。
func degradeReason(err error) string {
	var typed *exchange.Error
	if errors.As(err, &typed) {
		if exchange.IsInstrumentNotFound(err) {
			return "交易所确认标的不存在"
		}
		return "取价失败：" + exchange.FailureSummary(err)
	}
	return err.Error()
}

// latestFillPrice 取这个标的在本账户最近一笔成交的价格。
func latestFillPrice(ctx context.Context, fills interface {
	ListFills(context.Context, string, store.FillQuery) ([]store.FillRecord, int64, error)
}, account store.TradingAccountRecord, instrument store.InstrumentRecord) (shared.Decimal, bool, error) {
	records, _, err := fills.ListFills(ctx, account.SpaceID, store.FillQuery{
		TradingAccountID: account.TradingAccountID, Market: instrument.MarketType, ExchangeSymbol: instrument.ExchangeSymbol, Limit: 1,
	})
	if err != nil || len(records) == 0 {
		return shared.Zero(), false, err
	}
	price, err := shared.ParseDecimal(records[0].Price)
	if err != nil {
		return shared.Zero(), false, nil
	}
	return price, true, nil
}

// stablecoinPrice 返回稳定币折成结算币的价格：取它对结算币的现货标的参考价；
// 没有标的、取不到报价时按 1 计（稳定币锚定结算币，估值不能因为一时没有报价就中断权益采样）。
// prices 非空时直接用它取价（同步路径传入读快照的适配器，不再经 Manager 取适配器）。
func (s *Service) stablecoinPrice(ctx context.Context, account store.TradingAccountRecord, asset string, prices execution.ReferencePriceSource) shared.Decimal {
	one := shared.MustDecimal("1")
	if prices == nil && s.Adapters == nil {
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
		if prices == nil {
			adapter, adapterErr := s.Adapters.Adapter(account.TradingAccountID)
			if adapterErr != nil {
				return one
			}
			prices = adapter
		}
		quote, quoteErr := prices.GetReferencePrice(ctx, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
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
	settings, settingsErr := s.Store.ResolveFundSettings(logical)
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
