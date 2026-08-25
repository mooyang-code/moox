package equity

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"time"
)

type Service struct {
	Store *store.Store
	Now   func() time.Time
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
	pnl := snapshot.UnrealizedPnL
	sourceTime := snapshot.ExchangeUpdatedAt
	if sourceTime <= 0 {
		sourceTime = s.now().UnixMilli()
	}
	point := store.EquityPointRecord{SpaceID: account.SpaceID, TradingAccountID: accountID, BucketTime: MinuteBucket(s.now()), Equity: snapshot.Equity, AvailableFunds: snapshot.AvailableFunds, UsedMargin: snapshot.UsedMargin, UnrealizedPnL: &pnl, SourceTime: sourceTime}
	if err := s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpsertAccountEquityPoint(point) }); err != nil {
		return err
	}
	return s.sampleLogicalAccounts(ctx, account.SpaceID, accountID)
}

func (s *Service) sampleLogicalAccounts(ctx context.Context, spaceID, accountID string) error {
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
		equity, available, used, unrealized := shared.Zero(), shared.Zero(), shared.Zero(), shared.Zero()
		var sourceTime int64
		for _, member := range members {
			if member.TradingAccountID == accountID {
				contains = true
			}
			memberAccount, accountErr := s.Store.GetTradingAccountByID(ctx, member.TradingAccountID)
			if accountErr != nil {
				return accountErr
			}
			equity = equity.Add(decimal(memberAccount.Snapshot.Equity))
			available = available.Add(decimal(memberAccount.Snapshot.AvailableFunds))
			used = used.Add(decimal(memberAccount.Snapshot.UsedMargin))
			unrealized = unrealized.Add(decimal(memberAccount.Snapshot.UnrealizedPnL))
			if memberAccount.Snapshot.ExchangeUpdatedAt > sourceTime {
				sourceTime = memberAccount.Snapshot.ExchangeUpdatedAt
			}
		}
		if !contains || sourceTime <= 0 {
			continue
		}
		pnl := unrealized.String()
		point := store.EquityPointRecord{SpaceID: spaceID, LogicalAccountID: logical.LogicalAccountID, BucketTime: MinuteBucket(s.now()), Equity: equity.String(), AvailableFunds: available.String(), UsedMargin: used.String(), UnrealizedPnL: &pnl, SourceTime: sourceTime}
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
