package equity

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
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
	return 30 * time.Second
}

func (s *Service) sourceFresh(sourceMillis int64) bool {
	if sourceMillis <= 0 {
		return false
	}
	age := s.now().Sub(time.UnixMilli(sourceMillis).UTC())
	return age <= s.sourceMaxAge() && age >= -s.sourceMaxAge()
}

func MinuteBucket(at time.Time) int64 { return at.UTC().Truncate(time.Minute).UnixMilli() }
func (s *Service) SampleAccount(ctx context.Context, accountID string) error {
	if s == nil || s.Store == nil {
		return fmt.Errorf("equity: store is not configured")
	}
	account, err := s.Store.GetTradingAccountByID(ctx, accountID)
	if err != nil {
		return err
	}
	snapshot := account.Snapshot
	if account.MarketType == string(exchange.MarketTypeSpot) {
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

// valueSpotSnapshot fills the equity fields that spot account endpoints do
// not provide. It deliberately fails closed when a non-settlement asset has
// no current public quote instead of persisting a misleading zero curve.
func (s *Service) valueSpotSnapshot(ctx context.Context, account store.TradingAccountRecord, snapshot store.TradingAccountSnapshot) (store.TradingAccountSnapshot, error) {
	if !s.sourceFresh(snapshot.ExchangeUpdatedAt) {
		return store.TradingAccountSnapshot{}, fmt.Errorf("equity: stale Spot balance snapshot")
	}
	settlement := account.SettlementAsset
	equity := shared.Zero()
	available := shared.Zero()
	needsQuote := false
	for _, balance := range snapshot.Balances {
		quantity := decimal(balance.Total)
		if strings.EqualFold(balance.Asset, settlement) {
			equity = equity.Add(quantity)
			available = available.Add(decimal(balance.Available))
			continue
		}
		if !quantity.IsZero() {
			needsQuote = true
		}
	}
	if needsQuote {
		if s.Adapters == nil {
			return store.TradingAccountSnapshot{}, fmt.Errorf("equity: spot quote adapter is not configured")
		}
		adapter, err := s.Adapters.Adapter(account.TradingAccountID)
		if err != nil {
			return store.TradingAccountSnapshot{}, err
		}
		quotes, ok := adapter.(execution.ReferencePriceSource)
		if !ok {
			return store.TradingAccountSnapshot{}, fmt.Errorf("equity: spot quote source is unavailable")
		}
		instrumentSource, ok := adapter.(interface {
			LoadInstruments(context.Context) ([]exchange.Instrument, error)
		})
		if !ok {
			return store.TradingAccountSnapshot{}, fmt.Errorf("equity: instrument source is unavailable")
		}
		instruments, err := instrumentSource.LoadInstruments(ctx)
		if err != nil {
			return store.TradingAccountSnapshot{}, err
		}
		byAsset := make(map[string]exchange.Instrument)
		for _, instrument := range instruments {
			if instrument.MarketType == exchange.MarketTypeSpot && strings.EqualFold(instrument.QuoteAsset, settlement) {
				byAsset[strings.ToUpper(instrument.BaseAsset)] = instrument
			}
		}
		for _, balance := range snapshot.Balances {
			quantity := decimal(balance.Total)
			if strings.EqualFold(balance.Asset, settlement) || quantity.IsZero() {
				continue
			}
			instrument, found := byAsset[strings.ToUpper(balance.Asset)]
			if !found {
				return store.TradingAccountSnapshot{}, fmt.Errorf("equity: no %s/%s Spot instrument", balance.Asset, settlement)
			}
			symbol := instrument.ExchangeSymbol
			if symbol == "" {
				symbol = instrument.Symbol
			}
			quote, quoteErr := quotes.GetReferencePrice(ctx, symbol)
			if quoteErr != nil {
				return store.TradingAccountSnapshot{}, quoteErr
			}
			if !s.sourceFresh(quote.UpdatedAt.UnixMilli()) {
				return store.TradingAccountSnapshot{}, fmt.Errorf("equity: stale Spot quote for %s", symbol)
			}
			equity = equity.Add(quantity.Mul(quote.Price))
			if quote.UpdatedAt.UnixMilli() > snapshot.ExchangeUpdatedAt {
				snapshot.ExchangeUpdatedAt = quote.UpdatedAt.UnixMilli()
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
	for _, logical := range logicals {
		members, memberErr := s.Store.ListLogicalAccountMembers(ctx, spaceID, logical.LogicalAccountID, true)
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
			continue
		}
		equity, available, used, unrealized := shared.Zero(), shared.Zero(), shared.Zero(), shared.Zero()
		allPnl := true
		logicalValid := true
		var sourceTime int64
		for _, member := range members {
			memberAccount, accountErr := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
			if accountErr != nil {
				return accountErr
			}
			if memberAccount.Status != string(exchange.AccountStatusEnabled) || !memberAccount.Ready {
				logicalValid = false
				break
			}
			memberSnapshot := memberAccount.Snapshot
			if member.TradingAccountID == accountID && sampled != nil {
				memberSnapshot = *sampled
			} else if memberAccount.MarketType == string(exchange.MarketTypeSpot) {
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
			if memberSnapshot.ExchangeUpdatedAt > sourceTime {
				sourceTime = memberSnapshot.ExchangeUpdatedAt
			}
		}
		if !logicalValid || !contains || sourceTime <= 0 {
			continue
		}
		var pnl *string
		if allPnl {
			value := unrealized.String()
			pnl = &value
		}
		point := store.EquityPointRecord{SpaceID: spaceID, LogicalAccountID: logical.LogicalAccountID, BucketTime: MinuteBucket(s.now()), Equity: equity.String(), AvailableFunds: available.String(), UsedMargin: used.String(), UnrealizedPnL: pnl, SourceTime: sourceTime}
		if err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpsertLogicalAccountEquityPoint(point) }); err != nil {
			return err
		}
	}
	return nil
}

func decimal(raw string) shared.Decimal {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}
