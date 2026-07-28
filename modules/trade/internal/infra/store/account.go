package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type LeverageSettings map[string]string
type FillCursors map[string]string

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
	SyncSymbols        []string
	LeverageSettings   LeverageSettings
	FillCursors        FillCursors
	Snapshot           ExchangeAccountSnapshot
	SnapshotSourceTime int64
	LastSyncAt         int64
	LastReadyAt        int64
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type exchangeAccountRow struct {
	SpaceID              string    `gorm:"column:c_space_id"`
	ExchangeAccountID    string    `gorm:"column:c_exchange_account_id"`
	Name                 string    `gorm:"column:c_name"`
	Exchange             string    `gorm:"column:c_exchange"`
	MarketType           string    `gorm:"column:c_market_type"`
	ExecutionMode        string    `gorm:"column:c_execution_mode"`
	CredentialSecretID   string    `gorm:"column:c_credential_secret_id"`
	SettlementAsset      string    `gorm:"column:c_settlement_asset"`
	MarginMode           string    `gorm:"column:c_margin_mode"`
	Status               string    `gorm:"column:c_status"`
	Paused               bool      `gorm:"column:c_paused"`
	PauseReason          string    `gorm:"column:c_pause_reason"`
	Ready                bool      `gorm:"column:c_ready"`
	SyncSymbolsJSON      string    `gorm:"column:c_sync_symbols_json"`
	LeverageSettingsJSON string    `gorm:"column:c_leverage_settings_json"`
	FillCursorsJSON      string    `gorm:"column:c_fill_cursors_json"`
	SnapshotJSON         string    `gorm:"column:c_snapshot_json"`
	SnapshotSourceTime   int64     `gorm:"column:c_snapshot_source_time"`
	LastSyncAt           int64     `gorm:"column:c_last_sync_at"`
	LastReadyAt          int64     `gorm:"column:c_last_ready_at"`
	LastError            string    `gorm:"column:c_last_error"`
	CreatedAt            time.Time `gorm:"column:c_ctime"`
	UpdatedAt            time.Time `gorm:"column:c_mtime"`
}

func (exchangeAccountRow) TableName() string {
	return "t_exchange_accounts"
}

func (tx *Tx) CreateExchangeAccount(record ExchangeAccountRecord) error {
	if record.SpaceID == "" || record.ExchangeAccountID == "" || record.Name == "" ||
		record.Exchange == "" || record.MarketType == "" || record.ExecutionMode == "" ||
		record.CredentialSecretID == "" || record.SettlementAsset == "" || record.Status == "" {
		return fmt.Errorf("%w: incomplete Exchange account", ErrInvalidRecord)
	}
	leverageJSON, err := encodeLeverageSettings(record.LeverageSettings)
	if err != nil {
		return err
	}
	fillCursorsJSON, err := encodeFillCursors(record.FillCursors)
	if err != nil {
		return err
	}
	snapshotJSON, err := encodeSnapshot(record.Snapshot)
	if err != nil {
		return err
	}
	syncSymbolsJSON, err := encodeSyncSymbols(record.SyncSymbols)
	if err != nil {
		return err
	}
	row := exchangeAccountRow{
		SpaceID: record.SpaceID, ExchangeAccountID: record.ExchangeAccountID,
		Name: record.Name, Exchange: record.Exchange, MarketType: record.MarketType,
		ExecutionMode: record.ExecutionMode, CredentialSecretID: record.CredentialSecretID,
		SettlementAsset: record.SettlementAsset, MarginMode: record.MarginMode,
		Status: record.Status, Paused: record.Paused, PauseReason: record.PauseReason,
		Ready: record.Ready, SyncSymbolsJSON: syncSymbolsJSON,
		LeverageSettingsJSON: leverageJSON,
		FillCursorsJSON:      fillCursorsJSON, SnapshotJSON: snapshotJSON,
		SnapshotSourceTime: record.SnapshotSourceTime, LastSyncAt: record.LastSyncAt,
		LastReadyAt: record.LastReadyAt, LastError: record.LastError,
	}
	err = tx.db.Exec(`
		INSERT INTO t_exchange_accounts (
			c_space_id, c_exchange_account_id, c_name, c_exchange, c_market_type,
			c_execution_mode, c_credential_secret_id, c_settlement_asset, c_margin_mode,
			c_status, c_paused, c_pause_reason, c_ready, c_sync_symbols_json,
			c_leverage_settings_json,
			c_fill_cursors_json, c_snapshot_json, c_snapshot_source_time,
			c_last_sync_at, c_last_ready_at,
			c_last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		row.SpaceID, row.ExchangeAccountID, row.Name, row.Exchange, row.MarketType,
		row.ExecutionMode, row.CredentialSecretID, row.SettlementAsset, row.MarginMode,
		row.Status, row.Paused, row.PauseReason, row.Ready,
		row.SyncSymbolsJSON, row.LeverageSettingsJSON,
		row.FillCursorsJSON, row.SnapshotJSON,
		row.SnapshotSourceTime, row.LastSyncAt, row.LastReadyAt, row.LastError,
	).Error
	return writeError(err)
}

type ExchangeAccountConfiguration struct {
	Name               string
	CredentialSecretID string
	SettlementAsset    string
	MarginMode         string
	Status             string
	SyncSymbols        []string
}

func (tx *Tx) UpdateExchangeAccountConfiguration(
	spaceID string,
	exchangeAccountID string,
	config ExchangeAccountConfiguration,
) error {
	if blank(spaceID) || blank(exchangeAccountID) || blank(config.Name) ||
		blank(config.CredentialSecretID) || blank(config.SettlementAsset) ||
		blank(config.Status) {
		return fmt.Errorf("%w: incomplete Exchange account configuration", ErrInvalidRecord)
	}
	syncSymbolsJSON, err := encodeSyncSymbols(config.SyncSymbols)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`
		UPDATE t_exchange_accounts
		SET c_name = ?, c_credential_secret_id = ?, c_settlement_asset = ?,
			c_margin_mode = ?, c_status = ?, c_sync_symbols_json = ?,
			c_ready = CASE
				WHEN c_credential_secret_id <> ?
					OR c_settlement_asset <> ?
					OR c_margin_mode <> ?
					OR c_status <> ?
					OR c_sync_symbols_json <> ?
				THEN 0
				ELSE c_ready
			END,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, config.Name, config.CredentialSecretID, config.SettlementAsset,
		config.MarginMode, config.Status, syncSymbolsJSON,
		config.CredentialSecretID, config.SettlementAsset,
		config.MarginMode, config.Status, syncSymbolsJSON,
		spaceID, exchangeAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account configuration")
}

func (tx *Tx) SetExchangeAccountPause(
	spaceID string,
	exchangeAccountID string,
	paused bool,
	reason string,
) error {
	if blank(spaceID) || blank(exchangeAccountID) || (paused && blank(reason)) {
		return fmt.Errorf("%w: incomplete Exchange account pause", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_exchange_accounts
		SET c_paused = ?, c_pause_reason = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, paused, reason, spaceID, exchangeAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account pause")
}

func (tx *Tx) SetExchangeAccountLeverage(
	spaceID string,
	exchangeAccountID string,
	settings LeverageSettings,
) error {
	if blank(spaceID) || blank(exchangeAccountID) {
		return fmt.Errorf("%w: incomplete Exchange account leverage", ErrInvalidRecord)
	}
	encoded, err := encodeLeverageSettings(settings)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`
		UPDATE t_exchange_accounts
		SET c_leverage_settings_json = ?, c_ready = 0,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, encoded, spaceID, exchangeAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account leverage")
}

type ExchangeAccountSyncState struct {
	Ready              bool
	LeverageSettings   LeverageSettings
	FillCursors        FillCursors
	Snapshot           ExchangeAccountSnapshot
	SnapshotSourceTime int64
	LastSyncAt         int64
	LastReadyAt        int64
	LastError          string
}

func (tx *Tx) UpdateExchangeAccountSync(
	spaceID string,
	exchangeAccountID string,
	state ExchangeAccountSyncState,
) error {
	if blank(spaceID) || blank(exchangeAccountID) || state.LastSyncAt <= 0 {
		return fmt.Errorf("%w: incomplete Exchange account sync state", ErrInvalidRecord)
	}
	leverageJSON, err := encodeLeverageSettings(state.LeverageSettings)
	if err != nil {
		return err
	}
	fillCursorsJSON, err := encodeFillCursors(state.FillCursors)
	if err != nil {
		return err
	}
	snapshotJSON, err := encodeSnapshot(state.Snapshot)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`
		UPDATE t_exchange_accounts
		SET c_ready = ?, c_leverage_settings_json = ?, c_fill_cursors_json = ?,
			c_snapshot_json = ?,
			c_snapshot_source_time = ?, c_last_sync_at = ?, c_last_ready_at = ?,
			c_last_error = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, state.Ready, leverageJSON, fillCursorsJSON, snapshotJSON, state.SnapshotSourceTime,
		state.LastSyncAt, state.LastReadyAt, state.LastError, spaceID, exchangeAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account sync state")
}

func (tx *Tx) UpdateExchangeAccountFacts(
	spaceID string,
	exchangeAccountID string,
	fillCursors FillCursors,
	snapshot ExchangeAccountSnapshot,
	snapshotSourceTime int64,
	lastSyncAt int64,
) error {
	if blank(spaceID) || blank(exchangeAccountID) || lastSyncAt <= 0 {
		return fmt.Errorf("%w: incomplete Exchange account facts", ErrInvalidRecord)
	}
	fillCursorsJSON, err := encodeFillCursors(fillCursors)
	if err != nil {
		return err
	}
	snapshotJSON, err := encodeSnapshot(snapshot)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`
		UPDATE t_exchange_accounts
		SET c_fill_cursors_json = ?, c_snapshot_json = ?,
			c_snapshot_source_time = ?, c_last_sync_at = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, fillCursorsJSON, snapshotJSON, snapshotSourceTime, lastSyncAt,
		spaceID, exchangeAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account facts")
}

func (tx *Tx) UpdateExchangeAccountSnapshot(
	spaceID string,
	exchangeAccountID string,
	snapshot ExchangeAccountSnapshot,
) error {
	if blank(spaceID) || blank(exchangeAccountID) || snapshot.ExchangeUpdatedAt <= 0 {
		return fmt.Errorf("%w: incomplete Exchange account snapshot", ErrInvalidRecord)
	}
	snapshotJSON, err := encodeSnapshot(snapshot)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`
		UPDATE t_exchange_accounts
		SET c_snapshot_json = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, snapshotJSON, spaceID, exchangeAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account snapshot")
}

func (tx *Tx) UpdateExchangeAccountReadiness(
	spaceID string,
	exchangeAccountID string,
	ready bool,
	now int64,
	lastError string,
) error {
	if blank(spaceID) || blank(exchangeAccountID) || now <= 0 {
		return fmt.Errorf("%w: incomplete Exchange account readiness", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_exchange_accounts
		SET c_ready = ?,
			c_last_ready_at = CASE WHEN ? THEN ? ELSE c_last_ready_at END,
			c_last_error = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, ready, ready, now, lastError, spaceID, exchangeAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account readiness")
}

func requireUpdated(err error, rowsAffected int64, label string) error {
	if err != nil {
		return writeError(err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("%w: missing %s", ErrInvalidRecord, label)
	}
	return nil
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

func (tx *Tx) GetExchangeAccount(
	spaceID string,
	exchangeAccountID string,
) (ExchangeAccountRecord, error) {
	var row exchangeAccountRow
	err := tx.db.
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

func (s *Store) GetExchangeAccountByID(
	ctx context.Context,
	exchangeAccountID string,
) (ExchangeAccountRecord, error) {
	if blank(exchangeAccountID) {
		return ExchangeAccountRecord{}, fmt.Errorf("%w: empty Exchange account ID", ErrInvalidRecord)
	}
	var rows []exchangeAccountRow
	if err := s.db.WithContext(ctx).
		Where("c_exchange_account_id = ?", exchangeAccountID).
		Limit(2).
		Find(&rows).Error; err != nil {
		return ExchangeAccountRecord{}, err
	}
	if len(rows) != 1 {
		return ExchangeAccountRecord{}, fmt.Errorf(
			"%w: Exchange account ID must identify exactly one account",
			ErrInvalidRecord,
		)
	}
	return decodeAccountRow(rows[0])
}

func (s *Store) ListEnabledLiveExchangeAccounts(
	ctx context.Context,
) ([]ExchangeAccountRecord, error) {
	var rows []exchangeAccountRow
	if err := s.db.WithContext(ctx).
		Where("c_status = ? AND c_execution_mode = ? AND c_paused = 0", "ENABLED", "LIVE").
		Order("c_space_id, c_exchange_account_id").
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
	var syncSymbols []string
	if err := json.Unmarshal([]byte(row.SyncSymbolsJSON), &syncSymbols); err != nil {
		return ExchangeAccountRecord{}, fmt.Errorf("%w: sync symbols JSON: %v", ErrInvalidRecord, err)
	}
	if _, err := encodeSyncSymbols(syncSymbols); err != nil {
		return ExchangeAccountRecord{}, err
	}
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
	var fillCursors FillCursors
	if err := json.Unmarshal([]byte(row.FillCursorsJSON), &fillCursors); err != nil {
		return ExchangeAccountRecord{}, fmt.Errorf("%w: Fill cursors JSON: %v", ErrInvalidRecord, err)
	}
	if _, err := encodeFillCursors(fillCursors); err != nil {
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
		Ready: row.Ready, SyncSymbols: syncSymbols,
		LeverageSettings: leverage, FillCursors: fillCursors,
		Snapshot:           snapshot,
		SnapshotSourceTime: row.SnapshotSourceTime, LastSyncAt: row.LastSyncAt,
		LastReadyAt: row.LastReadyAt, LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func encodeSyncSymbols(symbols []string) (string, error) {
	canonical := make([]string, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		symbol = strings.TrimSpace(symbol)
		if symbol == "" {
			return "", fmt.Errorf("%w: empty sync symbol", ErrInvalidRecord)
		}
		if _, found := seen[symbol]; found {
			continue
		}
		seen[symbol] = struct{}{}
		canonical = append(canonical, symbol)
	}
	sort.Strings(canonical)
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: encode sync symbols: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
}

func encodeFillCursors(cursors FillCursors) (string, error) {
	if cursors == nil {
		return "{}", nil
	}
	canonical := make(FillCursors, len(cursors))
	for symbol, cursor := range cursors {
		if blank(symbol) || blank(cursor) {
			return "", fmt.Errorf("%w: incomplete Fill cursor", ErrInvalidRecord)
		}
		canonical[symbol] = cursor
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: encode Fill cursors: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
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
