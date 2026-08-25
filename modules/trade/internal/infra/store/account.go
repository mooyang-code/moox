package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"gorm.io/gorm"
)

type LeverageSettings map[string]string
type FillCursors map[string]string

type AssetBalance struct {
	Asset     string `json:"asset"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
	Total     string `json:"total"`
}

type TradingAccountSnapshot struct {
	Balances          []AssetBalance `json:"balances"`
	Equity            string         `json:"equity"`
	AvailableFunds    string         `json:"available_funds"`
	UsedMargin        string         `json:"used_margin"`
	MaintenanceMargin string         `json:"maintenance_margin"`
	UnrealizedPnL     string         `json:"unrealized_pnl"`
	ExchangeUpdatedAt int64          `json:"exchange_updated_at"`
}

type TradingAccountRecord struct {
	SpaceID            string
	TradingAccountID   string
	Name               string
	Exchange           string
	MarketType         string
	ExecutionMode      string
	Environment        string
	PaperConfig        *PaperAccountConfigRecord
	CredentialSecretID string
	SettlementAsset    string
	MarginMode         string
	Status             string
	Ready              bool
	SyncSymbols        []string
	LeverageSettings   LeverageSettings
	FillCursors        FillCursors
	Snapshot           TradingAccountSnapshot
	SnapshotSourceTime int64
	LastSyncAt         int64
	LastReadyAt        int64
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type tradingAccountRow struct {
	SpaceID              string    `gorm:"column:c_space_id"`
	TradingAccountID     string    `gorm:"column:c_trading_account_id"`
	Name                 string    `gorm:"column:c_name"`
	Exchange             string    `gorm:"column:c_exchange"`
	MarketType           string    `gorm:"column:c_market_type"`
	ExecutionMode        string    `gorm:"column:c_execution_mode"`
	Environment          string    `gorm:"column:c_live_environment"`
	CredentialSecretID   string    `gorm:"column:c_credential_secret_id"`
	SettlementAsset      string    `gorm:"column:c_settlement_asset"`
	MarginMode           string    `gorm:"column:c_margin_mode"`
	Status               string    `gorm:"column:c_status"`
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

func (tradingAccountRow) TableName() string {
	return "t_trading_accounts"
}

func (tx *Tx) CreateTradingAccount(record TradingAccountRecord) error {
	if record.SpaceID == "" || record.TradingAccountID == "" || record.Name == "" ||
		record.Exchange == "" || record.MarketType == "" || record.ExecutionMode == "" ||
		record.SettlementAsset == "" || record.Status == "" ||
		(record.ExecutionMode == "LIVE" && record.CredentialSecretID == "") {
		return fmt.Errorf("%w: incomplete Exchange account", ErrInvalidRecord)
	}
	if record.ExecutionMode == "PAPER" {
		if record.PaperConfig == nil {
			record.PaperConfig = &PaperAccountConfigRecord{SpaceID: record.SpaceID, TradingAccountID: record.TradingAccountID, InitialBalance: "100000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBPS: "0"}
		}
		record.Environment = ""
		record.CredentialSecretID = ""
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
	row := tradingAccountRow{
		SpaceID: record.SpaceID, TradingAccountID: record.TradingAccountID,
		Name: record.Name, Exchange: record.Exchange, MarketType: record.MarketType,
		ExecutionMode: record.ExecutionMode, Environment: record.Environment,
		CredentialSecretID: record.CredentialSecretID,
		SettlementAsset:    record.SettlementAsset, MarginMode: record.MarginMode,
		Status: record.Status,
		Ready:  record.Ready, SyncSymbolsJSON: syncSymbolsJSON,
		LeverageSettingsJSON: leverageJSON,
		FillCursorsJSON:      fillCursorsJSON, SnapshotJSON: snapshotJSON,
		SnapshotSourceTime: record.SnapshotSourceTime, LastSyncAt: record.LastSyncAt,
		LastReadyAt: record.LastReadyAt, LastError: record.LastError,
	}
	err = tx.db.Exec(`
		INSERT INTO t_trading_accounts (
			c_space_id, c_trading_account_id, c_name, c_exchange, c_market_type,
		c_execution_mode, c_live_environment, c_credential_secret_id,
			c_settlement_asset, c_margin_mode,
			c_status, c_ready, c_sync_symbols_json,
			c_leverage_settings_json,
			c_fill_cursors_json, c_snapshot_json, c_snapshot_source_time,
			c_last_sync_at, c_last_ready_at,
			c_last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		row.SpaceID, row.TradingAccountID, row.Name, row.Exchange, row.MarketType,
		row.ExecutionMode, row.Environment, row.CredentialSecretID,
		row.SettlementAsset, row.MarginMode,
		row.Status, row.Ready,
		row.SyncSymbolsJSON, row.LeverageSettingsJSON,
		row.FillCursorsJSON, row.SnapshotJSON,
		row.SnapshotSourceTime, row.LastSyncAt, row.LastReadyAt, row.LastError,
	).Error
	if err != nil {
		return writeError(err)
	}
	if record.ExecutionMode == "PAPER" && record.PaperConfig != nil {
		return tx.CreatePaperAccountConfig(*record.PaperConfig)
	}
	return nil
}

type TradingAccountConfiguration struct {
	Name               string
	CredentialSecretID string
	SettlementAsset    string
	MarginMode         string
	Status             string
	SyncSymbols        []string
}

func (tx *Tx) UpdateTradingAccountConfiguration(
	spaceID string,
	tradingAccountID string,
	config TradingAccountConfiguration,
) error {
	if blank(spaceID) || blank(tradingAccountID) || blank(config.Name) ||
		blank(config.SettlementAsset) || blank(config.Status) {
		return fmt.Errorf("%w: incomplete Exchange account configuration", ErrInvalidRecord)
	}
	var current struct {
		ExecutionMode   string `gorm:"column:c_execution_mode"`
		SettlementAsset string `gorm:"column:c_settlement_asset"`
		Status          string `gorm:"column:c_status"`
	}
	result := tx.db.Raw(`
		SELECT c_execution_mode, c_settlement_asset, c_status
		FROM t_trading_accounts
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, spaceID, tradingAccountID).Scan(&current)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: missing Exchange account configuration", ErrInvalidRecord)
	}
	if current.ExecutionMode == "LIVE" && blank(config.CredentialSecretID) {
		return fmt.Errorf("%w: LIVE requires an Exchange credential", ErrInvalidRecord)
	}
	if current.ExecutionMode == "PAPER" {
		if current.Status == "DISABLED" && config.Status == "ENABLED" {
			return fmt.Errorf("%w: closed paper account cannot be re-enabled", ErrConflict)
		}
		config.CredentialSecretID = ""
	}
	if current.SettlementAsset != config.SettlementAsset {
		var membershipCount int64
		if err := tx.db.Table("t_logical_account_members").
			Where(
				"c_space_id = ? AND c_trading_account_id = ?",
				spaceID,
				tradingAccountID,
			).
			Count(&membershipCount).Error; err != nil {
			return err
		}
		if membershipCount != 0 {
			return fmt.Errorf(
				"%w: remove Exchange account from its logical account before changing settlement asset",
				ErrConflict,
			)
		}
	}
	syncSymbolsJSON, err := encodeSyncSymbols(config.SyncSymbols)
	if err != nil {
		return err
	}
	result = tx.db.Exec(`
		UPDATE t_trading_accounts
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
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, config.Name, config.CredentialSecretID, config.SettlementAsset,
		config.MarginMode, config.Status, syncSymbolsJSON,
		config.CredentialSecretID, config.SettlementAsset,
		config.MarginMode, config.Status, syncSymbolsJSON,
		spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account configuration")
}

func (tx *Tx) SetTradingAccountLeverage(
	spaceID string,
	tradingAccountID string,
	settings LeverageSettings,
) error {
	if blank(spaceID) || blank(tradingAccountID) {
		return fmt.Errorf("%w: incomplete Exchange account leverage", ErrInvalidRecord)
	}
	encoded, err := encodeLeverageSettings(settings)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`
		UPDATE t_trading_accounts
		SET c_leverage_settings_json = ?, c_ready = 0,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, encoded, spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account leverage")
}

type TradingAccountSyncState struct {
	Ready              bool
	LeverageSettings   LeverageSettings
	FillCursors        FillCursors
	Snapshot           TradingAccountSnapshot
	SnapshotSourceTime int64
	LastSyncAt         int64
	LastReadyAt        int64
	LastError          string
}

func (tx *Tx) UpdateTradingAccountSync(
	spaceID string,
	tradingAccountID string,
	state TradingAccountSyncState,
) error {
	if blank(spaceID) || blank(tradingAccountID) || state.LastSyncAt <= 0 {
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
		UPDATE t_trading_accounts
		SET c_ready = ?, c_leverage_settings_json = ?, c_fill_cursors_json = ?,
			c_snapshot_json = ?,
			c_snapshot_source_time = ?, c_last_sync_at = ?, c_last_ready_at = ?,
			c_last_error = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, state.Ready, leverageJSON, fillCursorsJSON, snapshotJSON, state.SnapshotSourceTime,
		state.LastSyncAt, state.LastReadyAt, state.LastError, spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account sync state")
}

func (tx *Tx) UpdateTradingAccountFacts(
	spaceID string,
	tradingAccountID string,
	fillCursors FillCursors,
	snapshot TradingAccountSnapshot,
	snapshotSourceTime int64,
	lastSyncAt int64,
) error {
	if blank(spaceID) || blank(tradingAccountID) || lastSyncAt <= 0 {
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
		UPDATE t_trading_accounts
		SET c_fill_cursors_json = ?, c_snapshot_json = ?,
			c_snapshot_source_time = ?, c_last_sync_at = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, fillCursorsJSON, snapshotJSON, snapshotSourceTime, lastSyncAt,
		spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account facts")
}

func (tx *Tx) UpdateTradingAccountSnapshot(
	spaceID string,
	tradingAccountID string,
	snapshot TradingAccountSnapshot,
) error {
	if blank(spaceID) || blank(tradingAccountID) || snapshot.ExchangeUpdatedAt <= 0 {
		return fmt.Errorf("%w: incomplete Exchange account snapshot", ErrInvalidRecord)
	}
	snapshotJSON, err := encodeSnapshot(snapshot)
	if err != nil {
		return err
	}
	result := tx.db.Exec(`
		UPDATE t_trading_accounts
		SET c_snapshot_json = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, snapshotJSON, spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "Exchange account snapshot")
}

func (tx *Tx) UpdateTradingAccountReadiness(
	spaceID string,
	tradingAccountID string,
	ready bool,
	now int64,
	lastError string,
) error {
	if blank(spaceID) || blank(tradingAccountID) || now <= 0 {
		return fmt.Errorf("%w: incomplete Exchange account readiness", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_trading_accounts
		SET c_ready = ?,
			c_last_ready_at = CASE WHEN ? THEN ? ELSE c_last_ready_at END,
			c_last_error = ?,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, ready, ready, now, lastError, spaceID, tradingAccountID)
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

func (s *Store) GetTradingAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
) (TradingAccountRecord, error) {
	var row tradingAccountRow
	err := s.db.WithContext(ctx).
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID).
		Take(&row).Error
	if err != nil {
		return TradingAccountRecord{}, err
	}
	record, err := decodeAccountRow(row)
	if err != nil {
		return TradingAccountRecord{}, err
	}
	if err := loadPaperConfig(s.db, ctx, &record); err != nil {
		return TradingAccountRecord{}, err
	}
	return record, nil
}

func (tx *Tx) GetTradingAccount(
	spaceID string,
	tradingAccountID string,
) (TradingAccountRecord, error) {
	var row tradingAccountRow
	err := tx.db.
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID).
		Take(&row).Error
	if err != nil {
		return TradingAccountRecord{}, err
	}
	record, err := decodeAccountRow(row)
	if err != nil {
		return TradingAccountRecord{}, err
	}
	if err := loadPaperConfig(tx.db, context.Background(), &record); err != nil {
		return TradingAccountRecord{}, err
	}
	return record, nil
}

func (s *Store) ListTradingAccounts(
	ctx context.Context,
	spaceID string,
) ([]TradingAccountRecord, error) {
	var rows []tradingAccountRow
	if err := s.db.WithContext(ctx).
		Where("c_space_id = ?", spaceID).
		Order("c_name, c_trading_account_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]TradingAccountRecord, 0, len(rows))
	for _, row := range rows {
		record, err := decodeAccountRow(row)
		if err != nil {
			return nil, err
		}
		if err := loadPaperConfig(s.db, ctx, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) ListAllTradingAccounts(ctx context.Context) ([]TradingAccountRecord, error) {
	var rows []tradingAccountRow
	if err := s.db.WithContext(ctx).Order("c_space_id, c_name, c_trading_account_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]TradingAccountRecord, 0, len(rows))
	for _, row := range rows {
		record, err := decodeAccountRow(row)
		if err != nil {
			return nil, err
		}
		if err := loadPaperConfig(s.db, ctx, &record); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, nil
}

func (s *Store) GetTradingAccountByID(
	ctx context.Context,
	tradingAccountID string,
) (TradingAccountRecord, error) {
	if blank(tradingAccountID) {
		return TradingAccountRecord{}, fmt.Errorf("%w: empty Exchange account ID", ErrInvalidRecord)
	}
	var rows []tradingAccountRow
	if err := s.db.WithContext(ctx).
		Where("c_trading_account_id = ?", tradingAccountID).
		Limit(2).
		Find(&rows).Error; err != nil {
		return TradingAccountRecord{}, err
	}
	if len(rows) != 1 {
		return TradingAccountRecord{}, fmt.Errorf(
			"%w: Exchange account ID must identify exactly one account",
			ErrInvalidRecord,
		)
	}
	record, err := decodeAccountRow(rows[0])
	if err != nil {
		return TradingAccountRecord{}, err
	}
	if err := loadPaperConfig(s.db, ctx, &record); err != nil {
		return TradingAccountRecord{}, err
	}
	return record, nil
}

func (s *Store) ListEnabledTradingAccounts(
	ctx context.Context,
) ([]TradingAccountRecord, error) {
	var rows []tradingAccountRow
	if err := s.db.WithContext(ctx).
		Where("c_status = ?", "ENABLED").
		Order("c_space_id, c_trading_account_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]TradingAccountRecord, 0, len(rows))
	for _, row := range rows {
		record, err := decodeAccountRow(row)
		if err != nil {
			return nil, err
		}
		if err := loadPaperConfig(s.db, ctx, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func decodeAccountRow(row tradingAccountRow) (TradingAccountRecord, error) {
	var syncSymbols []string
	if err := json.Unmarshal([]byte(row.SyncSymbolsJSON), &syncSymbols); err != nil {
		return TradingAccountRecord{}, fmt.Errorf("%w: sync symbols JSON: %v", ErrInvalidRecord, err)
	}
	if _, err := encodeSyncSymbols(syncSymbols); err != nil {
		return TradingAccountRecord{}, err
	}
	var leverage LeverageSettings
	if err := json.Unmarshal([]byte(row.LeverageSettingsJSON), &leverage); err != nil {
		return TradingAccountRecord{}, fmt.Errorf("%w: leverage JSON: %v", ErrInvalidRecord, err)
	}
	var snapshot TradingAccountSnapshot
	if err := json.Unmarshal([]byte(row.SnapshotJSON), &snapshot); err != nil {
		return TradingAccountRecord{}, fmt.Errorf("%w: snapshot JSON: %v", ErrInvalidRecord, err)
	}
	if _, err := encodeLeverageSettings(leverage); err != nil {
		return TradingAccountRecord{}, err
	}
	var fillCursors FillCursors
	if err := json.Unmarshal([]byte(row.FillCursorsJSON), &fillCursors); err != nil {
		return TradingAccountRecord{}, fmt.Errorf("%w: Fill cursors JSON: %v", ErrInvalidRecord, err)
	}
	if _, err := encodeFillCursors(fillCursors); err != nil {
		return TradingAccountRecord{}, err
	}
	if _, err := encodeSnapshot(snapshot); err != nil {
		return TradingAccountRecord{}, err
	}
	return TradingAccountRecord{
		SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID,
		Name: row.Name, Exchange: row.Exchange, MarketType: row.MarketType,
		ExecutionMode: row.ExecutionMode, Environment: row.Environment,
		CredentialSecretID: row.CredentialSecretID,
		SettlementAsset:    row.SettlementAsset, MarginMode: row.MarginMode,
		Status: row.Status,
		Ready:  row.Ready, SyncSymbols: syncSymbols,
		LeverageSettings: leverage, FillCursors: fillCursors,
		Snapshot:           snapshot,
		SnapshotSourceTime: row.SnapshotSourceTime, LastSyncAt: row.LastSyncAt,
		LastReadyAt: row.LastReadyAt, LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func loadPaperConfig(db *gorm.DB, ctx context.Context, record *TradingAccountRecord) error {
	if record == nil || record.ExecutionMode != "PAPER" {
		return nil
	}
	var row struct {
		SpaceID          string `gorm:"column:c_space_id"`
		TradingAccountID string `gorm:"column:c_trading_account_id"`
		InitialBalance   string `gorm:"column:c_initial_balance"`
		MakerFeeRate     string `gorm:"column:c_maker_fee_rate"`
		TakerFeeRate     string `gorm:"column:c_taker_fee_rate"`
		SlippageBPS      string `gorm:"column:c_slippage_bps"`
	}
	if err := db.WithContext(ctx).Table("t_paper_account_configs").
		Where("c_space_id = ? AND c_trading_account_id = ?", record.SpaceID, record.TradingAccountID).
		Take(&row).Error; err != nil {
		return err
	}
	record.PaperConfig = &PaperAccountConfigRecord{
		SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID,
		InitialBalance: row.InitialBalance, MakerFeeRate: row.MakerFeeRate,
		TakerFeeRate: row.TakerFeeRate, SlippageBPS: row.SlippageBPS,
	}
	return nil
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

func encodeSnapshot(snapshot TradingAccountSnapshot) (string, error) {
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
