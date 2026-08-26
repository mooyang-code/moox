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
	if record.ExecutionMode == string(exchange.ExecutionModePaper) {
		if cfg, cfgErr := r.Store.GetPaperAccountConfig(ctx, record.SpaceID, record.TradingAccountID); cfgErr == nil {
			record.PaperConfig = &cfg
		}
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
				Name: value.Name, CredentialSecretID: credentialSecret(value),
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
	record := store.TradingAccountRecord{
		SpaceID: value.SpaceID, TradingAccountID: value.ID, Name: value.Name,
		Exchange: string(value.Exchange), MarketType: string(value.MarketType),
		ExecutionMode:      string(value.ExecutionMode),
		Environment:        string(value.MarketDataEnvironment()),
		CredentialSecretID: credentialSecret(value),
		SettlementAsset:    value.SettlementAsset, MarginMode: string(value.MarginMode),
		Status: string(value.Status), Ready: value.Ready,
		SyncSymbols:      append([]string(nil), value.SyncSymbols...),
		LeverageSettings: leverage, FillCursors: store.FillCursors{},
		Snapshot:           snapshotRecord(value.Snapshot),
		SnapshotSourceTime: timestampMillis(value.SnapshotSourceTime),
		LastSyncAt:         timestampMillis(value.LastSyncAt),
		LastReadyAt:        timestampMillis(value.LastReadyAt), LastError: value.LastError,
	}
	if value.ExecutionMode == exchange.ExecutionModePaper {
		paper := value.Paper
		if paper == nil {
			paper = &tradingaccount.PaperConfig{InitialBalance: shared.MustDecimal("100000"), MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero()}
		}
		record.PaperConfig = &store.PaperAccountConfigRecord{SpaceID: value.SpaceID, TradingAccountID: value.ID, InitialBalance: paper.InitialBalance.String(), MakerFeeRate: paper.MakerFeeRate.String(), TakerFeeRate: paper.TakerFeeRate.String(), SlippageBPS: paper.SlippageBPS.String()}
	}
	return record
}

func credentialSecret(value tradingaccount.Account) string {
	if value.Live != nil {
		return value.Live.CredentialSecretID
	}
	return value.CredentialSecretID
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
	value := tradingaccount.Account{
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
	}
	if value.ExecutionMode == exchange.ExecutionModePaper {
		value.Environment = exchange.AccountEnvironmentPaper
		if cfg, err := loadPaperConfig(record); err == nil {
			value.Paper = cfg
		}
	} else {
		value.Live = &tradingaccount.LiveConfig{Environment: exchange.AccountEnvironment(record.Environment), CredentialSecretID: record.CredentialSecretID}
	}
	return value, nil
}

func loadPaperConfig(record store.TradingAccountRecord) (*tradingaccount.PaperConfig, error) {
	if record.PaperConfig == nil {
		return &tradingaccount.PaperConfig{InitialBalance: shared.MustDecimal("100000"), MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero()}, nil
	}
	initial, err := shared.ParseDecimal(record.PaperConfig.InitialBalance)
	if err != nil {
		return nil, err
	}
	maker, err := shared.ParseDecimal(record.PaperConfig.MakerFeeRate)
	if err != nil {
		return nil, err
	}
	taker, err := shared.ParseDecimal(record.PaperConfig.TakerFeeRate)
	if err != nil {
		return nil, err
	}
	slip, err := shared.ParseDecimal(record.PaperConfig.SlippageBPS)
	if err != nil {
		return nil, err
	}
	return &tradingaccount.PaperConfig{InitialBalance: initial, MakerFeeRate: maker, TakerFeeRate: taker, SlippageBPS: slip}, nil
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
