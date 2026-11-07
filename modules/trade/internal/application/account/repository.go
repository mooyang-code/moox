package account

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
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

func (r Repository) LogicalAccountName(ctx context.Context, spaceID, tradingAccountID string) (string, error) {
	logical, _, err := r.Store.FindLogicalAccountMembership(ctx, spaceID, tradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return logical.Name, nil
}

func (r Repository) ListEnabled(ctx context.Context) ([]tradingaccount.Account, error) {
	records, err := r.Store.ListEnabledTradingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	accounts := make([]tradingaccount.Account, 0, len(records))
	for _, record := range records {
		value, err := accountDomain(record)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, value)
	}
	return accounts, nil
}

func (r Repository) Update(ctx context.Context, command UpdateCommand) error {
	unlock, lockErr := r.Store.LockTradingAccountContext(ctx, command.TradingAccountID)
	if lockErr != nil {
		return lockErr
	}
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
	unlock, lockErr := r.Store.LockTradingAccountContext(ctx, id)
	if lockErr != nil {
		return lockErr
	}
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
		Exchange: string(value.Exchange), Markets: marketStrings(value.Markets),
		ExecutionMode:      string(value.ExecutionMode),
		AccountMode:        string(value.AccountMode),
		InspectionJSON:     value.InspectionJSON,
		Environment:        string(value.MarketDataEnvironment()),
		CredentialSecretID: credentialSecret(value),
		SettlementAsset:    value.SettlementAsset, MarginMode: string(value.MarginMode),
		Status: string(value.Status), Ready: value.Ready,
		LeverageSettings: leverage, FillCursors: store.FillCursors{},
		Snapshot:           store.SnapshotFromExchange(value.Snapshot),
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
		Markets:            marketTypes(record.Markets),
		ExecutionMode:      exchange.ExecutionMode(record.ExecutionMode),
		AccountMode:        exchange.AccountMode(record.AccountMode),
		InspectionJSON:     record.InspectionJSON,
		Environment:        exchange.AccountEnvironment(record.Environment),
		CredentialSecretID: record.CredentialSecretID,
		SettlementAsset:    record.SettlementAsset,
		MarginMode:         exchange.MarginMode(record.MarginMode),
		Status:             exchange.AccountStatus(record.Status), Ready: record.Ready,
		LeverageSettings: leverage, Snapshot: record.Snapshot.ToExchange(),
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

func marketStrings(markets []exchange.MarketType) []string {
	result := make([]string, len(markets))
	for i, market := range markets {
		result[i] = string(market)
	}
	return result
}

func marketTypes(markets []string) []exchange.MarketType {
	result := make([]exchange.MarketType, len(markets))
	for i, market := range markets {
		result[i] = exchange.MarketType(market)
	}
	return result
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
