package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gorm.io/gorm"
)

type LeverageSettings map[string]string
type FillCursors map[string]string

// AssetBalance 是一个钱包里一种资产的余额。内存里所有钱包的余额平铺在一起，每项带钱包；
// 落库时按钱包分组，见 TradingAccountSnapshot 的 MarshalJSON。
type AssetBalance struct {
	Wallet    string `json:"-"`
	Asset     string `json:"asset"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
	Total     string `json:"total"`
	Borrowed  string `json:"borrowed"`
}

type TradingAccountSnapshot struct {
	Balances          []AssetBalance
	Equity            string
	AvailableFunds    string
	UsedMargin        string
	MaintenanceMargin string
	UnrealizedPnL     string
	// ExchangeEquity 是交易所给的总权益，只用于展示核对。
	ExchangeEquity    string
	ExchangeUpdatedAt int64
	// WalletUpdatedAt 记录各钱包余额的更新时间（交易所毫秒）；没有记录的钱包按 ExchangeUpdatedAt 算。
	WalletUpdatedAt map[string]int64
}

// walletSnapshot 是快照落库时一个钱包的写法。
type walletSnapshot struct {
	Wallet    string         `json:"wallet"`
	UpdatedAt int64          `json:"updated_at"`
	Balances  []AssetBalance `json:"balances"`
}

// snapshotDocument 是快照落库的 JSON 结构：权益字段 + 各钱包余额。
type snapshotDocument struct {
	Equity            string           `json:"equity"`
	AvailableFunds    string           `json:"available_funds"`
	UsedMargin        string           `json:"used_margin"`
	MaintenanceMargin string           `json:"maintenance_margin"`
	UnrealizedPnL     string           `json:"unrealized_pnl"`
	ExchangeEquity    string           `json:"exchange_equity"`
	ExchangeUpdatedAt int64            `json:"exchange_updated_at"`
	Wallets           []walletSnapshot `json:"wallets"`
}

// MarshalJSON 把平铺的余额按钱包分组，钱包按名称排序，同一个钱包内保持原有顺序。
func (s TradingAccountSnapshot) MarshalJSON() ([]byte, error) {
	document := snapshotDocument{
		Equity: s.Equity, AvailableFunds: s.AvailableFunds, UsedMargin: s.UsedMargin,
		MaintenanceMargin: s.MaintenanceMargin, UnrealizedPnL: s.UnrealizedPnL,
		ExchangeEquity: s.ExchangeEquity, ExchangeUpdatedAt: s.ExchangeUpdatedAt,
		Wallets: []walletSnapshot{},
	}
	index := make(map[string]int)
	for _, balance := range s.Balances {
		at, ok := index[balance.Wallet]
		if !ok {
			updatedAt, known := s.WalletUpdatedAt[balance.Wallet]
			if !known {
				updatedAt = s.ExchangeUpdatedAt
			}
			at = len(document.Wallets)
			index[balance.Wallet] = at
			document.Wallets = append(document.Wallets, walletSnapshot{Wallet: balance.Wallet, UpdatedAt: updatedAt, Balances: []AssetBalance{}})
		}
		document.Wallets[at].Balances = append(document.Wallets[at].Balances, balance)
	}
	sort.SliceStable(document.Wallets, func(i, j int) bool { return document.Wallets[i].Wallet < document.Wallets[j].Wallet })
	return json.Marshal(document)
}

// UnmarshalJSON 读取落库的 JSON，把各钱包的余额还原成带钱包的平铺列表。
func (s *TradingAccountSnapshot) UnmarshalJSON(data []byte) error {
	var document snapshotDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	*s = TradingAccountSnapshot{
		Equity: document.Equity, AvailableFunds: document.AvailableFunds, UsedMargin: document.UsedMargin,
		MaintenanceMargin: document.MaintenanceMargin, UnrealizedPnL: document.UnrealizedPnL,
		ExchangeEquity: document.ExchangeEquity, ExchangeUpdatedAt: document.ExchangeUpdatedAt,
	}
	for _, wallet := range document.Wallets {
		if s.WalletUpdatedAt == nil {
			s.WalletUpdatedAt = make(map[string]int64, len(document.Wallets))
		}
		s.WalletUpdatedAt[wallet.Wallet] = wallet.UpdatedAt
		for _, balance := range wallet.Balances {
			balance.Wallet = wallet.Wallet
			s.Balances = append(s.Balances, balance)
		}
	}
	return nil
}

// SnapshotFromExchange 把适配器返回的快照转成落库的快照。
func SnapshotFromExchange(snapshot exchange.AccountSnapshot) TradingAccountSnapshot {
	balances := make([]AssetBalance, 0, len(snapshot.Balances))
	for _, balance := range snapshot.Balances {
		balances = append(balances, AssetBalance{
			Wallet: string(balance.Wallet), Asset: balance.Asset,
			Available: balance.Available.String(), Locked: balance.Locked.String(),
			Total: balance.Total.String(), Borrowed: balance.Borrowed.String(),
		})
	}
	record := TradingAccountSnapshot{
		Balances: balances, Equity: snapshot.Equity.String(),
		AvailableFunds:    snapshot.AvailableFunds.String(),
		UsedMargin:        snapshot.UsedMargin.String(),
		MaintenanceMargin: snapshot.MaintenanceMargin.String(),
		UnrealizedPnL:     snapshot.UnrealizedPnL.String(),
		ExchangeEquity:    snapshot.ExchangeEquity.String(),
		ExchangeUpdatedAt: snapshot.ExchangeUpdatedAt.UnixMilli(),
	}
	if len(snapshot.WalletUpdatedAt) > 0 {
		record.WalletUpdatedAt = make(map[string]int64, len(snapshot.WalletUpdatedAt))
		for wallet, at := range snapshot.WalletUpdatedAt {
			record.WalletUpdatedAt[string(wallet)] = at.UnixMilli()
		}
	}
	return record
}

// ToExchange 把落库的快照转回交易所快照；空的或无法解析的数值按零处理。
func (s TradingAccountSnapshot) ToExchange() exchange.AccountSnapshot {
	balances := make([]exchange.AssetBalance, 0, len(s.Balances))
	for _, balance := range s.Balances {
		balances = append(balances, exchange.AssetBalance{
			Wallet: exchange.Wallet(balance.Wallet), Asset: balance.Asset,
			Available: decimalOrZero(balance.Available), Locked: decimalOrZero(balance.Locked),
			Total: decimalOrZero(balance.Total), Borrowed: decimalOrZero(balance.Borrowed),
		})
	}
	var updatedAt time.Time
	if s.ExchangeUpdatedAt > 0 {
		updatedAt = time.UnixMilli(s.ExchangeUpdatedAt).UTC()
	}
	var walletUpdatedAt map[exchange.Wallet]time.Time
	if len(s.WalletUpdatedAt) > 0 {
		walletUpdatedAt = make(map[exchange.Wallet]time.Time, len(s.WalletUpdatedAt))
		for wallet, at := range s.WalletUpdatedAt {
			walletUpdatedAt[exchange.Wallet(wallet)] = time.UnixMilli(at).UTC()
		}
	}
	return exchange.AccountSnapshot{
		WalletUpdatedAt: walletUpdatedAt,
		Balances:        balances, Equity: decimalOrZero(s.Equity),
		AvailableFunds:    decimalOrZero(s.AvailableFunds),
		UsedMargin:        decimalOrZero(s.UsedMargin),
		MaintenanceMargin: decimalOrZero(s.MaintenanceMargin),
		UnrealizedPnL:     decimalOrZero(s.UnrealizedPnL),
		ExchangeEquity:    decimalOrZero(s.ExchangeEquity),
		ExchangeUpdatedAt: updatedAt,
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

// WalletBalances 返回某个钱包里的余额。
func (s TradingAccountSnapshot) WalletBalances(wallet string) []AssetBalance {
	var result []AssetBalance
	for _, balance := range s.Balances {
		if balance.Wallet == wallet {
			result = append(result, balance)
		}
	}
	return result
}

// DefaultLeverageKey 是 LeverageSettings 里账户默认杠杆的键，不是交易标的。
const DefaultLeverageKey = "*"

type TradingAccountRecord struct {
	SpaceID          string
	TradingAccountID string
	Name             string
	Exchange         string
	// Markets 是已开通的市场，按 SPOT、SWAP、STOCK 的固定顺序排列。
	Markets       []string
	ExecutionMode string
	// AccountMode 只由交易所检测写入；InspectionJSON 是最近一次检测的结果。
	AccountMode        string
	InspectionJSON     string
	Environment        string
	PaperConfig        *PaperAccountConfigRecord
	CredentialSecretID string
	SettlementAsset    string
	MarginMode         string
	Status             string
	Ready              bool
	LeverageSettings   LeverageSettings
	FillCursors        FillCursors
	Snapshot           TradingAccountSnapshot
	SnapshotSourceTime int64
	// BalancePending 是各钱包待余额确认的水位（钱包 -> 毫秒）；没有条目表示该钱包没有待确认的成交或私有余额更新。
	BalancePending map[string]int64
	// LastDustConvertAt 是上次小额资产兑换 BNB 的时间（毫秒），0 表示从没兑换过。
	LastDustConvertAt int64
	LastSyncAt        int64
	LastReadyAt       int64
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// HasBalanceMarket 报告账户是否开通了持仓来自余额的市场（现货、股票）。
func (r TradingAccountRecord) HasBalanceMarket() bool {
	return slices.ContainsFunc(r.Markets, func(market string) bool {
		return exchange.RuleFor(market).PositionFromBalance
	})
}

// HasPositionMarket 报告账户是否开通了持仓来自持仓表（而不是余额）的市场，例如 SWAP。
func (r TradingAccountRecord) HasPositionMarket() bool {
	return slices.ContainsFunc(r.Markets, func(market string) bool {
		return !exchange.RuleFor(market).PositionFromBalance
	})
}

// WalletFor 返回某个市场的资金在这个账户里所在的钱包；模拟盘只有一个统一钱包。
func (r TradingAccountRecord) WalletFor(market string) exchange.Wallet {
	if r.ExecutionMode == string(exchange.ExecutionModePaper) {
		return exchange.WalletUnified
	}
	return exchange.WalletFor(exchange.Exchange(r.Exchange), exchange.AccountMode(r.AccountMode), exchange.MarketType(market))
}

// IsSpotWallet 报告钱包里是否放着现货（持仓来自余额的市场）；空钱包（快照没有钱包信息）当作现货。
func (r TradingAccountRecord) IsSpotWallet(wallet string) bool {
	if wallet == "" {
		return true
	}
	for _, market := range r.Markets {
		if exchange.RuleFor(market).PositionFromBalance && string(r.WalletFor(market)) == wallet {
			return true
		}
	}
	return false
}

// SpotBalances 返回放着现货的钱包里的余额。普通账户的合约钱包里的非结算资产不是现货持仓，不在其中。
func (r TradingAccountRecord) SpotBalances() []AssetBalance {
	var result []AssetBalance
	for _, balance := range r.Snapshot.Balances {
		if r.IsSpotWallet(balance.Wallet) {
			result = append(result, balance)
		}
	}
	return result
}

// HasBalancePending 报告是否有任何钱包还在等余额确认。
func (r TradingAccountRecord) HasBalancePending() bool {
	return len(r.BalancePending) > 0
}

// SupportsMarket 报告账户是否已开通某个市场。
func (r TradingAccountRecord) SupportsMarket(market string) bool {
	return slices.Contains(r.Markets, market)
}

type tradingAccountRow struct {
	SpaceID              string    `gorm:"column:c_space_id"`
	TradingAccountID     string    `gorm:"column:c_trading_account_id"`
	Name                 string    `gorm:"column:c_name"`
	Exchange             string    `gorm:"column:c_exchange"`
	Markets              string    `gorm:"column:c_markets"`
	ExecutionMode        string    `gorm:"column:c_execution_mode"`
	AccountMode          string    `gorm:"column:c_account_mode"`
	InspectionJSON       string    `gorm:"column:c_inspection_json"`
	Environment          string    `gorm:"column:c_live_environment"`
	CredentialSecretID   string    `gorm:"column:c_credential_secret_id"`
	SettlementAsset      string    `gorm:"column:c_settlement_asset"`
	MarginMode           string    `gorm:"column:c_margin_mode"`
	Status               string    `gorm:"column:c_status"`
	Ready                bool      `gorm:"column:c_ready"`
	LeverageSettingsJSON string    `gorm:"column:c_leverage_settings_json"`
	FillCursorsJSON      string    `gorm:"column:c_fill_cursors_json"`
	SnapshotJSON         string    `gorm:"column:c_snapshot_json"`
	SnapshotSourceTime   int64     `gorm:"column:c_snapshot_source_time"`
	BalancePendingJSON   string    `gorm:"column:c_balance_pending_json"`
	LastDustConvertAt    int64     `gorm:"column:c_last_dust_convert_at"`
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
	tx.accountsWritten = true
	if tx.store != nil {
		tx.store.identities.Invalidate()
	}
	if record.SpaceID == "" || record.TradingAccountID == "" || record.Name == "" ||
		record.Exchange == "" || len(record.Markets) == 0 || record.ExecutionMode == "" ||
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
	markets, err := encodeMarkets(record.Markets)
	if err != nil {
		return err
	}
	if record.InspectionJSON == "" {
		record.InspectionJSON = "{}"
	}
	row := tradingAccountRow{
		SpaceID: record.SpaceID, TradingAccountID: record.TradingAccountID,
		Name: record.Name, Exchange: record.Exchange, Markets: markets,
		ExecutionMode: record.ExecutionMode, AccountMode: record.AccountMode,
		InspectionJSON: record.InspectionJSON, Environment: record.Environment,
		CredentialSecretID: record.CredentialSecretID,
		SettlementAsset:    record.SettlementAsset, MarginMode: record.MarginMode,
		Status:               record.Status,
		Ready:                record.Ready,
		LeverageSettingsJSON: leverageJSON,
		FillCursorsJSON:      fillCursorsJSON, SnapshotJSON: snapshotJSON,
		SnapshotSourceTime: record.SnapshotSourceTime, LastSyncAt: record.LastSyncAt,
		LastReadyAt: record.LastReadyAt, LastError: record.LastError,
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	err = tx.db.Exec(`
		INSERT INTO t_trading_accounts (
			c_space_id, c_trading_account_id, c_name, c_exchange, c_markets,
			c_execution_mode, c_account_mode, c_inspection_json,
			c_live_environment, c_credential_secret_id,
			c_settlement_asset, c_margin_mode,
			c_status, c_ready,
			c_leverage_settings_json,
			c_fill_cursors_json, c_snapshot_json, c_snapshot_source_time,
			c_last_sync_at, c_last_ready_at,
			c_last_error, c_ctime
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		row.SpaceID, row.TradingAccountID, row.Name, row.Exchange, row.Markets,
		row.ExecutionMode, row.AccountMode, row.InspectionJSON,
		row.Environment, row.CredentialSecretID,
		row.SettlementAsset, row.MarginMode,
		row.Status, row.Ready,
		row.LeverageSettingsJSON,
		row.FillCursorsJSON, row.SnapshotJSON,
		row.SnapshotSourceTime, row.LastSyncAt, row.LastReadyAt, row.LastError, createdAt,
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
	result = tx.db.Exec(`
		UPDATE t_trading_accounts
		SET c_name = ?, c_credential_secret_id = ?, c_settlement_asset = ?,
			c_margin_mode = ?, c_status = ?,
			c_ready = CASE
				WHEN c_credential_secret_id <> ?
					OR c_settlement_asset <> ?
					OR c_margin_mode <> ?
					OR c_status <> ?
				THEN 0
				ELSE c_ready
			END,
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, config.Name, config.CredentialSecretID, config.SettlementAsset,
		config.MarginMode, config.Status,
		config.CredentialSecretID, config.SettlementAsset,
		config.MarginMode, config.Status,
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
		SET c_leverage_settings_json = ?,
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

// RaiseBalancePending 把某个钱包待余额确认的水位抬到至少 until（毫秒，只升不降）。
func (tx *Tx) RaiseBalancePending(spaceID, tradingAccountID string, wallet exchange.Wallet, until int64) error {
	if blank(spaceID) || blank(tradingAccountID) || !wallet.Valid() || until <= 0 {
		return fmt.Errorf("%w: incomplete balance pending watermark", ErrInvalidRecord)
	}
	path := "$." + string(wallet)
	result := tx.db.Exec(`
		UPDATE t_trading_accounts
		SET c_balance_pending_json = json_set(
				c_balance_pending_json, ?,
				MAX(COALESCE(json_extract(c_balance_pending_json, ?), 0), ?)
			),
			c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, path, path, until, spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "balance pending watermark")
}

// ClearBalancePendingThrough 在完整余额快照覆盖某个钱包的水位（覆盖时间 >= 水位）后清掉该钱包的水位；水位更高时保持不变。
func (tx *Tx) ClearBalancePendingThrough(spaceID, tradingAccountID string, wallet exchange.Wallet, coveredUntil int64) error {
	if blank(spaceID) || blank(tradingAccountID) || !wallet.Valid() {
		return fmt.Errorf("%w: incomplete balance pending watermark", ErrInvalidRecord)
	}
	path := "$." + string(wallet)
	result := tx.db.Exec(`
		UPDATE t_trading_accounts
		SET c_balance_pending_json = json_remove(c_balance_pending_json, ?), c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
			AND COALESCE(json_extract(c_balance_pending_json, ?), 0) <= ?
	`, path, spaceID, tradingAccountID, path, coveredUntil)
	if result.Error != nil {
		return writeError(result.Error)
	}
	return nil
}

// SetLastDustConvertAt 记录上次小额资产兑换 BNB 的时间（毫秒）。
func (tx *Tx) SetLastDustConvertAt(spaceID, tradingAccountID string, at int64) error {
	if blank(spaceID) || blank(tradingAccountID) || at <= 0 {
		return fmt.Errorf("%w: incomplete dust convert time", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_trading_accounts
		SET c_last_dust_convert_at = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, at, spaceID, tradingAccountID)
	return requireUpdated(result.Error, result.RowsAffected, "dust convert time")
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
	markets, err := decodeMarkets(row.Markets)
	if err != nil {
		return TradingAccountRecord{}, err
	}
	var balancePending map[string]int64
	if err := json.Unmarshal([]byte(row.BalancePendingJSON), &balancePending); err != nil {
		return TradingAccountRecord{}, fmt.Errorf("%w: balance pending JSON: %v", ErrInvalidRecord, err)
	}
	return TradingAccountRecord{
		SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID,
		Name: row.Name, Exchange: row.Exchange, Markets: markets,
		ExecutionMode: row.ExecutionMode, AccountMode: row.AccountMode,
		InspectionJSON: row.InspectionJSON, Environment: row.Environment,
		CredentialSecretID: row.CredentialSecretID,
		SettlementAsset:    row.SettlementAsset, MarginMode: row.MarginMode,
		Status:           row.Status,
		Ready:            row.Ready,
		LeverageSettings: leverage, FillCursors: fillCursors,
		Snapshot:           snapshot,
		SnapshotSourceTime: row.SnapshotSourceTime, BalancePending: balancePending, LastDustConvertAt: row.LastDustConvertAt, LastSyncAt: row.LastSyncAt,
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
	// 先复制余额再规范化，不改动调用方持有的快照。
	snapshot.Balances = append([]AssetBalance(nil), snapshot.Balances...)
	seen := make(map[string]struct{}, len(snapshot.Balances))
	for i := range snapshot.Balances {
		balance := &snapshot.Balances[i]
		if balance.Asset == "" {
			return "", fmt.Errorf("%w: empty balance asset", ErrInvalidRecord)
		}
		if !exchange.Wallet(balance.Wallet).Valid() {
			return "", fmt.Errorf("%w: balance %s has unsupported wallet %q", ErrInvalidRecord, balance.Asset, balance.Wallet)
		}
		key := balance.Wallet + "/" + balance.Asset
		if _, duplicate := seen[key]; duplicate {
			return "", fmt.Errorf("%w: duplicate balance %s in wallet %s", ErrInvalidRecord, balance.Asset, balance.Wallet)
		}
		seen[key] = struct{}{}
		for _, value := range []*string{&balance.Available, &balance.Locked, &balance.Total, &balance.Borrowed} {
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
		&snapshot.ExchangeEquity,
	} {
		canonical, err := canonicalOptionalDecimal(*value)
		if err != nil {
			return "", err
		}
		*value = canonical
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("%w: encode snapshot: %v", ErrInvalidRecord, err)
	}
	return string(data), nil
}

// encodeMarkets 把已开通的市场写成库里的逗号分隔文本；输入必须已经是规范写法。
func encodeMarkets(markets []string) (string, error) {
	typed := make([]exchange.MarketType, len(markets))
	for i, market := range markets {
		typed[i] = exchange.MarketType(market)
	}
	normalized, err := exchange.NormalizeMarkets(typed)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	encoded := exchange.JoinMarkets(normalized)
	if encoded != strings.Join(markets, ",") {
		return "", fmt.Errorf("%w: markets %v are not canonical", ErrInvalidRecord, markets)
	}
	return encoded, nil
}

func decodeMarkets(raw string) ([]string, error) {
	typed, err := exchange.ParseMarkets(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	markets := make([]string, len(typed))
	for i, market := range typed {
		markets[i] = string(market)
	}
	return markets, nil
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
