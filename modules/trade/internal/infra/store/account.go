package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type LeverageSettings map[string]string

type AssetBalance struct {
	Asset     string `json:"asset"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
	Total     string `json:"total"`
}

type ExchangeAccountSnapshot struct {
	Balances          []AssetBalance `json:"balances"`
	Equity            string         `json:"equity"`
	AvailableFunds    string         `json:"available_funds"`
	UsedMargin        string         `json:"used_margin"`
	MaintenanceMargin string         `json:"maintenance_margin"`
	UnrealizedPnL     string         `json:"unrealized_pnl"`
	ExchangeUpdatedAt int64          `json:"exchange_updated_at"`
}

type ExchangeAccountRecord struct {
	SpaceID            string
	ExchangeAccountID  string
	Name               string
	Exchange           string
	MarketType         string
	ExecutionMode      string
	CredentialSecretID string
	SettlementAsset    string
	MarginMode         string
	Status             string
	Paused             bool
	PauseReason        string
	Ready              bool
	LeverageSettings   LeverageSettings
	Snapshot           ExchangeAccountSnapshot
	SnapshotSourceTime int64
	LastSyncAt         int64
	LastReadyAt        int64
	LastError          string
}

type exchangeAccountRow struct {
	SpaceID              string `gorm:"column:c_space_id"`
	ExchangeAccountID    string `gorm:"column:c_exchange_account_id"`
	Name                 string `gorm:"column:c_name"`
	Exchange             string `gorm:"column:c_exchange"`
	MarketType           string `gorm:"column:c_market_type"`
	ExecutionMode        string `gorm:"column:c_execution_mode"`
	CredentialSecretID   string `gorm:"column:c_credential_secret_id"`
	SettlementAsset      string `gorm:"column:c_settlement_asset"`
	MarginMode           string `gorm:"column:c_margin_mode"`
	Status               string `gorm:"column:c_status"`
	Paused               bool   `gorm:"column:c_paused"`
	PauseReason          string `gorm:"column:c_pause_reason"`
	Ready                bool   `gorm:"column:c_ready"`
	LeverageSettingsJSON string `gorm:"column:c_leverage_settings_json"`
	SnapshotJSON         string `gorm:"column:c_snapshot_json"`
	SnapshotSourceTime   int64  `gorm:"column:c_snapshot_source_time"`
	LastSyncAt           int64  `gorm:"column:c_last_sync_at"`
	LastReadyAt          int64  `gorm:"column:c_last_ready_at"`
	LastError            string `gorm:"column:c_last_error"`
}

func (exchangeAccountRow) TableName() string {
	return "t_exchange_accounts"
}

func (tx *Tx) UpsertExchangeAccount(record ExchangeAccountRecord) error {
	if record.SpaceID == "" || record.ExchangeAccountID == "" || record.Name == "" ||
		record.Exchange == "" || record.MarketType == "" || record.ExecutionMode == "" ||
		record.CredentialSecretID == "" || record.SettlementAsset == "" || record.Status == "" {
		return fmt.Errorf("%w: incomplete Exchange account", ErrInvalidRecord)
	}
	leverageJSON, err := encodeLeverageSettings(record.LeverageSettings)
	if err != nil {
		return err
	}
	snapshotJSON, err := encodeSnapshot(record.Snapshot)
	if err != nil {
		return err
	}
	row := exchangeAccountRow{
		SpaceID: record.SpaceID, ExchangeAccountID: record.ExchangeAccountID,
		Name: record.Name, Exchange: record.Exchange, MarketType: record.MarketType,
		ExecutionMode: record.ExecutionMode, CredentialSecretID: record.CredentialSecretID,
		SettlementAsset: record.SettlementAsset, MarginMode: record.MarginMode,
		Status: record.Status, Paused: record.Paused, PauseReason: record.PauseReason,
		Ready: record.Ready, LeverageSettingsJSON: leverageJSON, SnapshotJSON: snapshotJSON,
		SnapshotSourceTime: record.SnapshotSourceTime, LastSyncAt: record.LastSyncAt,
		LastReadyAt: record.LastReadyAt, LastError: record.LastError,
	}
	err = tx.db.Exec(`
		INSERT INTO t_exchange_accounts (
			c_space_id, c_exchange_account_id, c_name, c_exchange, c_market_type,
			c_execution_mode, c_credential_secret_id, c_settlement_asset, c_margin_mode,
			c_status, c_paused, c_pause_reason, c_ready, c_leverage_settings_json,
			c_snapshot_json, c_snapshot_source_time, c_last_sync_at, c_last_ready_at,
			c_last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_exchange_account_id) DO UPDATE SET
			c_name = excluded.c_name,
			c_exchange = excluded.c_exchange,
			c_market_type = excluded.c_market_type,
			c_execution_mode = excluded.c_execution_mode,
			c_credential_secret_id = excluded.c_credential_secret_id,
			c_settlement_asset = excluded.c_settlement_asset,
			c_margin_mode = excluded.c_margin_mode,
			c_status = excluded.c_status,
			c_paused = excluded.c_paused,
			c_pause_reason = excluded.c_pause_reason,
			c_ready = excluded.c_ready,
			c_leverage_settings_json = excluded.c_leverage_settings_json,
			c_snapshot_json = excluded.c_snapshot_json,
			c_snapshot_source_time = excluded.c_snapshot_source_time,
			c_last_sync_at = excluded.c_last_sync_at,
			c_last_ready_at = excluded.c_last_ready_at,
			c_last_error = excluded.c_last_error,
			c_mtime = CURRENT_TIMESTAMP
	`,
		row.SpaceID, row.ExchangeAccountID, row.Name, row.Exchange, row.MarketType,
		row.ExecutionMode, row.CredentialSecretID, row.SettlementAsset, row.MarginMode,
		row.Status, row.Paused, row.PauseReason, row.Ready, row.LeverageSettingsJSON,
		row.SnapshotJSON, row.SnapshotSourceTime, row.LastSyncAt, row.LastReadyAt,
		row.LastError,
	).Error
	return writeError(err)
}

func (s *Store) GetExchangeAccount(
	ctx context.Context,
	spaceID string,
	exchangeAccountID string,
) (ExchangeAccountRecord, error) {
	var row exchangeAccountRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_exchange_account_id = ?", spaceID, exchangeAccountID).
		Take(&row).Error
	if err != nil {
		return ExchangeAccountRecord{}, err
	}
	return decodeAccountRow(row)
}

func (s *Store) ListExchangeAccounts(
	ctx context.Context,
	spaceID string,
) ([]ExchangeAccountRecord, error) {
	var rows []exchangeAccountRow
	if err := s.db.WithContext(ctx).
		Where("c_space_id = ?", spaceID).
		Order("c_name, c_exchange_account_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]ExchangeAccountRecord, 0, len(rows))
	for _, row := range rows {
		record, err := decodeAccountRow(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func decodeAccountRow(row exchangeAccountRow) (ExchangeAccountRecord, error) {
	var leverage LeverageSettings
	if err := json.Unmarshal([]byte(row.LeverageSettingsJSON), &leverage); err != nil {
		return ExchangeAccountRecord{}, fmt.Errorf("%w: leverage JSON: %v", ErrInvalidRecord, err)
	}
	var snapshot ExchangeAccountSnapshot
	if err := json.Unmarshal([]byte(row.SnapshotJSON), &snapshot); err != nil {
		return ExchangeAccountRecord{}, fmt.Errorf("%w: snapshot JSON: %v", ErrInvalidRecord, err)
	}
	if _, err := encodeLeverageSettings(leverage); err != nil {
		return ExchangeAccountRecord{}, err
	}
	if _, err := encodeSnapshot(snapshot); err != nil {
		return ExchangeAccountRecord{}, err
	}
	return ExchangeAccountRecord{
		SpaceID: row.SpaceID, ExchangeAccountID: row.ExchangeAccountID,
		Name: row.Name, Exchange: row.Exchange, MarketType: row.MarketType,
		ExecutionMode: row.ExecutionMode, CredentialSecretID: row.CredentialSecretID,
		SettlementAsset: row.SettlementAsset, MarginMode: row.MarginMode,
		Status: row.Status, Paused: row.Paused, PauseReason: row.PauseReason,
		Ready: row.Ready, LeverageSettings: leverage, Snapshot: snapshot,
		SnapshotSourceTime: row.SnapshotSourceTime, LastSyncAt: row.LastSyncAt,
		LastReadyAt: row.LastReadyAt, LastError: row.LastError,
	}, nil
}

func encodeLeverageSettings(settings LeverageSettings) (string, error) {
	if settings == nil {
		return "{}", nil
	}
	canonical := make(LeverageSettings, len(settings))
	for symbol, raw := range settings {
		if symbol == "" {
			return "", fmt.Errorf("%w: empty leverage symbol", ErrInvalidRecord)
		}
		value, err := shared.ParseDecimal(raw)
		if err != nil || value.Cmp(shared.Zero()) <= 0 {
			return "", fmt.Errorf("%w: leverage for %s", ErrInvalidRecord, symbol)
		}
		canonical[symbol] = value.String()
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: encode leverage: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
}

func encodeSnapshot(snapshot ExchangeAccountSnapshot) (string, error) {
	for i := range snapshot.Balances {
		balance := &snapshot.Balances[i]
		if balance.Asset == "" {
			return "", fmt.Errorf("%w: empty balance asset", ErrInvalidRecord)
		}
		for _, value := range []*string{&balance.Available, &balance.Locked, &balance.Total} {
			canonical, err := canonicalOptionalDecimal(*value)
			if err != nil {
				return "", err
			}
			*value = canonical
		}
	}
	for _, value := range []*string{
		&snapshot.Equity,
		&snapshot.AvailableFunds,
		&snapshot.UsedMargin,
		&snapshot.MaintenanceMargin,
		&snapshot.UnrealizedPnL,
	} {
		canonical, err := canonicalOptionalDecimal(*value)
		if err != nil {
			return "", err
		}
		*value = canonical
	}
	if snapshot.Balances == nil {
		snapshot.Balances = []AssetBalance{}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("%w: encode snapshot: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
}

func canonicalOptionalDecimal(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return "", fmt.Errorf("%w: decimal %q", ErrInvalidRecord, raw)
	}
	return value.String(), nil
}
