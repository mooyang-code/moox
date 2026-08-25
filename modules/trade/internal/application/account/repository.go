package account

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type Repository struct {
	Store *store.Store
}

func (r Repository) Create(ctx context.Context, value tradingaccount.Account) error {
	return r.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateTradingAccount(accountRecord(value))
	})
}

func (r Repository) Get(ctx context.Context, id string) (tradingaccount.Account, error) {
	record, err := r.Store.GetTradingAccountByID(ctx, id)
	if err != nil {
		return tradingaccount.Account{}, err
	}
	return accountDomain(record)
}

func (r Repository) Update(ctx context.Context, command UpdateCommand) error {
	unlock := r.Store.LockTradingAccount(command.TradingAccountID)
	defer unlock()
	current, err := r.Store.GetTradingAccountByID(ctx, command.TradingAccountID)
	if err != nil {
		return err
	}
	value, err := accountDomain(current)
	if err != nil {
		return err
	}
	applyUpdate(&value, command)
	return r.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountConfiguration(
			value.SpaceID,
			value.ID,
			store.TradingAccountConfiguration{
				Name: value.Name, CredentialSecretID: value.CredentialSecretID,
				SettlementAsset: value.SettlementAsset,
				MarginMode:      string(value.MarginMode), Status: string(value.Status),
				SyncSymbols: append([]string(nil), value.SyncSymbols...),
			},
		)
	})
}

func (r Repository) SetLeverage(
	ctx context.Context,
	id string,
	symbol string,
	leverage shared.Decimal,
) error {
	unlock := r.Store.LockTradingAccount(id)
	defer unlock()
	current, err := r.Store.GetTradingAccountByID(ctx, id)
	if err != nil {
		return err
	}
	settings := cloneLeverage(current.LeverageSettings)
	settings[symbol] = leverage.String()
	return r.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetTradingAccountLeverage(
			current.SpaceID,
			current.TradingAccountID,
			settings,
		)
	})
}

func accountRecord(value tradingaccount.Account) store.TradingAccountRecord {
	leverage := make(store.LeverageSettings, len(value.LeverageSettings))
	for symbol, amount := range value.LeverageSettings {
		leverage[symbol] = amount.String()
	}
	return store.TradingAccountRecord{
		SpaceID: value.SpaceID, TradingAccountID: value.ID, Name: value.Name,
		Exchange: string(value.Exchange), MarketType: string(value.MarketType),
		ExecutionMode:      string(value.ExecutionMode),
		Environment:        string(value.Environment),
		CredentialSecretID: value.CredentialSecretID,
		SettlementAsset:    value.SettlementAsset, MarginMode: string(value.MarginMode),
		Status: string(value.Status), Ready: value.Ready,
		SyncSymbols:      append([]string(nil), value.SyncSymbols...),
		LeverageSettings: leverage, FillCursors: store.FillCursors{},
		Snapshot:           snapshotRecord(value.Snapshot),
		SnapshotSourceTime: timestampMillis(value.SnapshotSourceTime),
		LastSyncAt:         timestampMillis(value.LastSyncAt),
		LastReadyAt:        timestampMillis(value.LastReadyAt), LastError: value.LastError,
	}
}

func accountDomain(record store.TradingAccountRecord) (tradingaccount.Account, error) {
	leverage := make(map[string]shared.Decimal, len(record.LeverageSettings))
	for symbol, raw := range record.LeverageSettings {
		value, err := shared.ParseDecimal(raw)
		if err != nil {
			return tradingaccount.Account{}, err
		}
		leverage[symbol] = value
	}
	return tradingaccount.Account{
		ID: record.TradingAccountID, SpaceID: record.SpaceID, Name: record.Name,
		Exchange:           exchange.Exchange(record.Exchange),
		MarketType:         exchange.MarketType(record.MarketType),
		ExecutionMode:      exchange.ExecutionMode(record.ExecutionMode),
		Environment:        exchange.AccountEnvironment(record.Environment),
		CredentialSecretID: record.CredentialSecretID,
		SettlementAsset:    record.SettlementAsset,
		MarginMode:         exchange.MarginMode(record.MarginMode),
		Status:             exchange.AccountStatus(record.Status), Ready: record.Ready,
		SyncSymbols:      append([]string(nil), record.SyncSymbols...),
		LeverageSettings: leverage, Snapshot: snapshotDomain(record.Snapshot),
		SnapshotSourceTime: millisTime(record.SnapshotSourceTime),
		LastSyncAt:         millisTime(record.LastSyncAt),
		LastReadyAt:        millisTime(record.LastReadyAt), LastError: record.LastError,
	}, nil
}

func cloneLeverage(values store.LeverageSettings) store.LeverageSettings {
	cloned := make(store.LeverageSettings, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func snapshotRecord(value exchange.AccountSnapshot) store.TradingAccountSnapshot {
	balances := make([]store.AssetBalance, 0, len(value.Balances))
	for _, balance := range value.Balances {
		balances = append(balances, store.AssetBalance{
			Asset: balance.Asset, Available: balance.Available.String(),
			Locked: balance.Locked.String(), Total: balance.Total.String(),
		})
	}
	return store.TradingAccountSnapshot{
		Balances: balances, Equity: value.Equity.String(),
		AvailableFunds:    value.AvailableFunds.String(),
		UsedMargin:        value.UsedMargin.String(),
		MaintenanceMargin: value.MaintenanceMargin.String(),
		UnrealizedPnL:     value.UnrealizedPnL.String(),
		ExchangeUpdatedAt: timestampMillis(value.ExchangeUpdatedAt),
	}
}

func snapshotDomain(value store.TradingAccountSnapshot) exchange.AccountSnapshot {
	balances := make([]exchange.AssetBalance, 0, len(value.Balances))
	for _, balance := range value.Balances {
		balances = append(balances, exchange.AssetBalance{
			Asset:     balance.Asset,
			Available: decimalOrZero(balance.Available),
			Locked:    decimalOrZero(balance.Locked), Total: decimalOrZero(balance.Total),
		})
	}
	return exchange.AccountSnapshot{
		Balances: balances, Equity: decimalOrZero(value.Equity),
		AvailableFunds:    decimalOrZero(value.AvailableFunds),
		UsedMargin:        decimalOrZero(value.UsedMargin),
		MaintenanceMargin: decimalOrZero(value.MaintenanceMargin),
		UnrealizedPnL:     decimalOrZero(value.UnrealizedPnL),
		ExchangeUpdatedAt: millisTime(value.ExchangeUpdatedAt),
	}
}

func decimalOrZero(raw string) shared.Decimal {
	if raw == "" {
		return shared.Zero()
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}

func millisTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func timestampMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}
