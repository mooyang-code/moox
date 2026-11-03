package equity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

type Service struct {
	Store    *store.Store
	Adapters interface {
		Adapter(string) (execution.ExecutionAdapter, error)
	}
	Now          func() time.Time
	SourceMaxAge time.Duration
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
	snapshot := confirmedSnapshot(account)
	if exchange.RuleFor(account.MarketType).PositionFromBalance {
		valued, valueErr := s.valueSpotSnapshot(ctx, account, snapshot)
		if valueErr != nil {
			return valueErr
		}
		snapshot = valued
	}
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

// valueSpotSnapshot 计算受管资产的权益：结算资产加受管资产的市值。没有报价时不写入，
// 避免留下误导的零值曲线。未受管的资产不计入，也不影响权益。
func (s *Service) valueSpotSnapshot(ctx context.Context, account store.TradingAccountRecord, snapshot store.TradingAccountSnapshot) (store.TradingAccountSnapshot, error) {
	if !s.sourceFresh(snapshot.ExchangeUpdatedAt) {
		return store.TradingAccountSnapshot{}, fmt.Errorf("equity: stale Spot balance snapshot")
	}
	settlement := account.SettlementAsset
	managed, err := s.Store.ManagedAssets(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return store.TradingAccountSnapshot{}, err
	}
	equity := shared.Zero()
	available := shared.Zero()
	var held []store.AssetBalance
	for _, balance := range snapshot.Balances {
		quantity := decimal(balance.Total)
		if strings.EqualFold(balance.Asset, settlement) {
			equity = equity.Add(quantity)
			available = available.Add(decimal(balance.Available))
			continue
		}
		if _, ok := managed[balance.Asset]; ok && !quantity.IsZero() {
			held = append(held, balance)
		}
	}
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
		byAsset := make(map[string]store.InstrumentRecord)
		for _, instrument := range instruments {
			if strings.EqualFold(instrument.QuoteAsset, settlement) {
				byAsset[strings.ToUpper(instrument.BaseAsset)] = instrument
			}
		}
		for _, balance := range held {
			instrument, found := byAsset[strings.ToUpper(balance.Asset)]
			if !found {
				return store.TradingAccountSnapshot{}, fmt.Errorf("equity: no %s/%s Spot instrument", balance.Asset, settlement)
			}
			symbol := instrument.ExchangeSymbol
			quote, quoteErr := adapter.GetReferencePrice(ctx, symbol)
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
	snapshot.AvailableFunds = available.String()
	return snapshot, nil
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
		} else if exchange.RuleFor(memberAccount.MarketType).PositionFromBalance {
			valued, valueErr := s.valueSpotSnapshot(ctx, memberAccount, memberSnapshot)
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
