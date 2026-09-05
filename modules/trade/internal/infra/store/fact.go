package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"gorm.io/gorm"
)

type InstrumentRecord struct {
	Exchange             string
	Environment          string
	MarketType           string
	ExchangeSymbol       string
	InstrumentID         string
	BaseAsset            string
	QuoteAsset           string
	SettlementAsset      string
	Linear               bool
	ContractValue        string
	ContractValueAsset   string
	ExchangeQuantityStep string
	MinExchangeQuantity  string
	PriceTick            string
	MinNotional          string
	Status               string
	ExchangeUpdatedAt    int64
}

func (tx *Tx) UpsertInstrument(record InstrumentRecord) error {
	if record.InstrumentID == "" {
		record.InstrumentID = record.ExchangeSymbol
	}
	if record.Environment == "" {
		record.Environment = "PRODUCTION"
	}
	if record.Exchange == "" || record.Environment == "" || record.MarketType == "" ||
		record.ExchangeSymbol == "" || record.InstrumentID == "" ||
		record.BaseAsset == "" || record.QuoteAsset == "" || record.PriceTick == "" ||
		record.ExchangeQuantityStep == "" || record.Status == "" {
		return fmt.Errorf("%w: incomplete instrument", ErrInvalidRecord)
	}
	var err error
	record.PriceTick, err = canonicalDecimal(record.PriceTick, "price tick", decimalPositive)
	if err != nil {
		return err
	}
	record.ExchangeQuantityStep, err = canonicalDecimal(
		record.ExchangeQuantityStep,
		"Exchange quantity step",
		decimalPositive,
	)
	if err != nil {
		return err
	}
	if record.MarketType == "SWAP" &&
		(!record.Linear || record.ContractValueAsset != record.BaseAsset) {
		return fmt.Errorf("%w: incomplete SWAP instrument", ErrInvalidRecord)
	}
	if record.MarketType == "SWAP" {
		record.ContractValue, err = canonicalDecimal(
			record.ContractValue,
			"contract value",
			decimalPositive,
		)
		if err != nil {
			return err
		}
		record.MinExchangeQuantity, err = canonicalDecimal(
			record.MinExchangeQuantity,
			"minimum Exchange quantity",
			decimalPositive,
		)
		if err != nil {
			return err
		}
	}
	record.ContractValue, err = canonicalDefaultZero(
		record.ContractValue,
		"contract value",
		decimalNonNegative,
	)
	if err != nil {
		return err
	}
	record.MinExchangeQuantity, err = canonicalDefaultZero(
		record.MinExchangeQuantity,
		"minimum Exchange quantity",
		decimalNonNegative,
	)
	if err != nil {
		return err
	}
	record.MinNotional, err = canonicalDefaultZero(
		record.MinNotional,
		"minimum notional",
		decimalNonNegative,
	)
	if err != nil {
		return err
	}
	return writeError(tx.db.Exec(`
		INSERT INTO t_trade_instruments (
			c_exchange, c_environment, c_market_type, c_exchange_symbol, c_instrument_id, c_base_asset,
			c_quote_asset, c_settlement_asset, c_linear, c_contract_value,
			c_contract_value_asset, c_exchange_quantity_step,
			c_min_exchange_quantity, c_price_tick, c_min_notional, c_status,
			c_exchange_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_exchange, c_environment, c_market_type, c_exchange_symbol) DO UPDATE SET
			c_instrument_id = excluded.c_instrument_id,
			c_base_asset = excluded.c_base_asset,
			c_quote_asset = excluded.c_quote_asset,
			c_settlement_asset = excluded.c_settlement_asset,
			c_linear = excluded.c_linear,
			c_contract_value = excluded.c_contract_value,
			c_contract_value_asset = excluded.c_contract_value_asset,
			c_exchange_quantity_step = excluded.c_exchange_quantity_step,
			c_min_exchange_quantity = excluded.c_min_exchange_quantity,
			c_price_tick = excluded.c_price_tick,
			c_min_notional = excluded.c_min_notional,
			c_status = excluded.c_status,
			c_exchange_updated_at = excluded.c_exchange_updated_at,
			c_mtime = CURRENT_TIMESTAMP
	`,
		record.Exchange, record.Environment, record.MarketType, record.ExchangeSymbol, record.InstrumentID,
		record.BaseAsset, record.QuoteAsset, record.SettlementAsset, record.Linear,
		record.ContractValue, record.ContractValueAsset,
		record.ExchangeQuantityStep, record.MinExchangeQuantity,
		record.PriceTick, record.MinNotional, record.Status,
		record.ExchangeUpdatedAt,
	).Error)
}

func (s *Store) GetInstrument(
	ctx context.Context,
	exchange string,
	marketType string,
	symbol string,
) (InstrumentRecord, error) {
	return getInstrument(s.db.WithContext(ctx), exchange, "PRODUCTION", marketType, symbol)
}

func (s *Store) GetInstrumentInEnvironment(ctx context.Context, exchange, environment, marketType, exchangeSymbol string) (InstrumentRecord, error) {
	return getInstrument(s.db.WithContext(ctx), exchange, environment, marketType, exchangeSymbol)
}

func (s *Store) GetInstrumentByID(ctx context.Context, instrumentID string) (InstrumentRecord, error) {
	return getInstrumentByID(s.db.WithContext(ctx), instrumentID)
}

func (s *Store) GetInstrumentByIDForAccount(ctx context.Context, spaceID, tradingAccountID, instrumentID string) (InstrumentRecord, error) {
	var record InstrumentRecord
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		record, err = tx.GetInstrumentByIDForAccount(spaceID, tradingAccountID, instrumentID)
		return err
	})
	return record, err
}

func (s *Store) GetInstrumentByIDScoped(ctx context.Context, instrumentID, exchange, marketType string) (InstrumentRecord, error) {
	return getInstrumentByID(s.db.WithContext(ctx), instrumentID, exchange, "", marketType)
}

func (s *Store) ListInstruments(
	ctx context.Context,
	exchange string,
	marketType string,
) ([]InstrumentRecord, error) {
	return s.ListInstrumentsInEnvironment(ctx, exchange, "PRODUCTION", marketType)
}

func (s *Store) ListInstrumentsInEnvironment(
	ctx context.Context,
	exchange string,
	environment string,
	marketType string,
) ([]InstrumentRecord, error) {
	var rows []struct {
		Exchange             string `gorm:"column:c_exchange"`
		Environment          string `gorm:"column:c_environment"`
		MarketType           string `gorm:"column:c_market_type"`
		ExchangeSymbol       string `gorm:"column:c_exchange_symbol"`
		InstrumentID         string `gorm:"column:c_instrument_id"`
		BaseAsset            string `gorm:"column:c_base_asset"`
		QuoteAsset           string `gorm:"column:c_quote_asset"`
		SettlementAsset      string `gorm:"column:c_settlement_asset"`
		Linear               bool   `gorm:"column:c_linear"`
		ContractValue        string `gorm:"column:c_contract_value"`
		ContractValueAsset   string `gorm:"column:c_contract_value_asset"`
		ExchangeQuantityStep string `gorm:"column:c_exchange_quantity_step"`
		MinExchangeQuantity  string `gorm:"column:c_min_exchange_quantity"`
		PriceTick            string `gorm:"column:c_price_tick"`
		MinNotional          string `gorm:"column:c_min_notional"`
		Status               string `gorm:"column:c_status"`
		ExchangeUpdatedAt    int64  `gorm:"column:c_exchange_updated_at"`
	}
	if err := s.db.WithContext(ctx).Table("t_trade_instruments").
		Where("c_exchange = ? AND c_environment = ? AND c_market_type = ?", exchange, environment, marketType).
		Order("c_exchange_symbol").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]InstrumentRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, InstrumentRecord{
			Exchange: row.Exchange, Environment: row.Environment, MarketType: row.MarketType, ExchangeSymbol: row.ExchangeSymbol,
			InstrumentID: row.InstrumentID, BaseAsset: row.BaseAsset,
			QuoteAsset: row.QuoteAsset, SettlementAsset: row.SettlementAsset,
			Linear: row.Linear, ContractValue: row.ContractValue,
			ContractValueAsset:   row.ContractValueAsset,
			ExchangeQuantityStep: row.ExchangeQuantityStep,
			MinExchangeQuantity:  row.MinExchangeQuantity,
			PriceTick:            row.PriceTick, MinNotional: row.MinNotional,
			Status: row.Status, ExchangeUpdatedAt: row.ExchangeUpdatedAt,
		})
	}
	return records, nil
}

func (s *Store) ListInstrumentsForAccount(ctx context.Context, tradingAccountID string) ([]InstrumentRecord, error) {
	account, err := s.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return nil, err
	}
	environment := account.Environment
	if account.ExecutionMode == "PAPER" || environment == "" {
		environment = "PRODUCTION"
	}
	items, err := s.ListInstrumentsInEnvironment(ctx, account.Exchange, environment, account.MarketType)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 && environment != "PRODUCTION" {
		return s.ListInstrumentsInEnvironment(ctx, account.Exchange, "PRODUCTION", account.MarketType)
	}
	return items, nil
}

func (tx *Tx) GetInstrument(
	exchange string,
	marketType string,
	symbol string,
) (InstrumentRecord, error) {
	return getInstrument(tx.db, exchange, "PRODUCTION", marketType, symbol)
}

func (tx *Tx) GetInstrumentInEnvironment(exchange, environment, marketType, exchangeSymbol string) (InstrumentRecord, error) {
	return getInstrument(tx.db, exchange, environment, marketType, exchangeSymbol)
}

func (tx *Tx) GetInstrumentByID(instrumentID string) (InstrumentRecord, error) {
	return getInstrumentByID(tx.db, instrumentID)
}

func (tx *Tx) GetInstrumentByIDForAccount(spaceID, tradingAccountID, instrumentID string) (InstrumentRecord, error) {
	identity, err := tx.accountIdentity(spaceID, tradingAccountID)
	if err != nil {
		return InstrumentRecord{}, err
	}
	return getInstrumentByID(tx.db, instrumentID, identity.Exchange, identity.Environment, identity.MarketType)
}

func getInstrument(
	db *gorm.DB,
	exchange string,
	environment string,
	marketType string,
	exchangeSymbol string,
) (InstrumentRecord, error) {
	var row struct {
		Exchange             string `gorm:"column:c_exchange"`
		Environment          string `gorm:"column:c_environment"`
		MarketType           string `gorm:"column:c_market_type"`
		ExchangeSymbol       string `gorm:"column:c_exchange_symbol"`
		InstrumentID         string `gorm:"column:c_instrument_id"`
		BaseAsset            string `gorm:"column:c_base_asset"`
		QuoteAsset           string `gorm:"column:c_quote_asset"`
		SettlementAsset      string `gorm:"column:c_settlement_asset"`
		Linear               bool   `gorm:"column:c_linear"`
		ContractValue        string `gorm:"column:c_contract_value"`
		ContractValueAsset   string `gorm:"column:c_contract_value_asset"`
		ExchangeQuantityStep string `gorm:"column:c_exchange_quantity_step"`
		MinExchangeQuantity  string `gorm:"column:c_min_exchange_quantity"`
		PriceTick            string `gorm:"column:c_price_tick"`
		MinNotional          string `gorm:"column:c_min_notional"`
		Status               string `gorm:"column:c_status"`
		ExchangeUpdatedAt    int64  `gorm:"column:c_exchange_updated_at"`
	}
	err := db.Table("t_trade_instruments").
		Where("c_exchange = ? AND c_environment = ? AND c_market_type = ? AND c_exchange_symbol = ?", exchange, environment, marketType, exchangeSymbol).
		Take(&row).Error
	return InstrumentRecord{
		Exchange: row.Exchange, Environment: row.Environment, MarketType: row.MarketType, ExchangeSymbol: row.ExchangeSymbol,
		InstrumentID: row.InstrumentID, BaseAsset: row.BaseAsset,
		QuoteAsset: row.QuoteAsset, SettlementAsset: row.SettlementAsset,
		Linear: row.Linear, ContractValue: row.ContractValue,
		ContractValueAsset:   row.ContractValueAsset,
		ExchangeQuantityStep: row.ExchangeQuantityStep,
		MinExchangeQuantity:  row.MinExchangeQuantity, PriceTick: row.PriceTick,
		MinNotional: row.MinNotional, Status: row.Status,
		ExchangeUpdatedAt: row.ExchangeUpdatedAt,
	}, err
}

func getInstrumentByID(db *gorm.DB, instrumentID string, scope ...string) (InstrumentRecord, error) {
	if strings.TrimSpace(instrumentID) == "" {
		return InstrumentRecord{}, fmt.Errorf("%w: empty instrument ID", ErrInvalidRecord)
	}
	var row struct {
		Exchange             string `gorm:"column:c_exchange"`
		Environment          string `gorm:"column:c_environment"`
		MarketType           string `gorm:"column:c_market_type"`
		ExchangeSymbol       string `gorm:"column:c_exchange_symbol"`
		InstrumentID         string `gorm:"column:c_instrument_id"`
		BaseAsset            string `gorm:"column:c_base_asset"`
		QuoteAsset           string `gorm:"column:c_quote_asset"`
		SettlementAsset      string `gorm:"column:c_settlement_asset"`
		Linear               bool   `gorm:"column:c_linear"`
		ContractValue        string `gorm:"column:c_contract_value"`
		ContractValueAsset   string `gorm:"column:c_contract_value_asset"`
		ExchangeQuantityStep string `gorm:"column:c_exchange_quantity_step"`
		MinExchangeQuantity  string `gorm:"column:c_min_exchange_quantity"`
		PriceTick            string `gorm:"column:c_price_tick"`
		MinNotional          string `gorm:"column:c_min_notional"`
		Status               string `gorm:"column:c_status"`
		ExchangeUpdatedAt    int64  `gorm:"column:c_exchange_updated_at"`
	}
	query := db.Table("t_trade_instruments").Where("c_instrument_id = ?", instrumentID)
	if len(scope) >= 1 && scope[0] != "" {
		query = query.Where("c_exchange = ?", scope[0])
	}
	if len(scope) >= 2 && scope[1] != "" {
		query = query.Where("c_environment = ?", scope[1])
	}
	if len(scope) >= 3 && scope[2] != "" {
		query = query.Where("c_market_type = ?", scope[2])
	}
	err := query.Take(&row).Error
	return InstrumentRecord{
		Exchange: row.Exchange, Environment: row.Environment, MarketType: row.MarketType,
		ExchangeSymbol: row.ExchangeSymbol, InstrumentID: row.InstrumentID,
		BaseAsset: row.BaseAsset, QuoteAsset: row.QuoteAsset, SettlementAsset: row.SettlementAsset,
		Linear: row.Linear, ContractValue: row.ContractValue, ContractValueAsset: row.ContractValueAsset,
		ExchangeQuantityStep: row.ExchangeQuantityStep, MinExchangeQuantity: row.MinExchangeQuantity,
		PriceTick: row.PriceTick, MinNotional: row.MinNotional, Status: row.Status,
		ExchangeUpdatedAt: row.ExchangeUpdatedAt,
	}, err
}

type OrderRecord struct {
	SpaceID                   string
	OrderID                   string
	TradingAccountID          string
	ClientOrderID             string
	ExchangeOrderID           string
	Exchange                  string
	MarketType                string
	InstrumentID              string
	ExchangeSymbol            string
	OrderType                 string
	TimeInForce               string
	Side                      string
	PositionSide              string
	Quantity                  string
	LimitPrice                *string
	ReferencePrice            string
	ReferencePriceAt          int64
	ReduceOnly                bool
	OwnerType                 string
	OwnerID                   string
	LogicalAccountID          string
	RunnerID                  string
	State                     string
	FilledQuantity            string
	AveragePrice              string
	ReservedAsset             string
	ReservedQuantity          string
	RemainingReservedQuantity string
	PaperExecutionPrice       *string
	FirstMatchPending         bool
	RejectReason              string
	ExchangeUpdatedAt         int64
	Version                   uint64
	SubmittedAt               int64
	FinishedAt                int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type orderRow struct {
	SpaceID                   string    `gorm:"column:c_space_id"`
	OrderID                   string    `gorm:"column:c_order_id"`
	TradingAccountID          string    `gorm:"column:c_trading_account_id"`
	ClientOrderID             string    `gorm:"column:c_client_order_id"`
	ExchangeOrderID           string    `gorm:"column:c_exchange_order_id"`
	Exchange                  string    `gorm:"column:c_exchange"`
	MarketType                string    `gorm:"column:c_market_type"`
	InstrumentID              string    `gorm:"column:c_instrument_id"`
	ExchangeSymbol            string    `gorm:"column:c_exchange_symbol"`
	OrderType                 string    `gorm:"column:c_order_type"`
	TimeInForce               string    `gorm:"column:c_time_in_force"`
	Side                      string    `gorm:"column:c_side"`
	PositionSide              string    `gorm:"column:c_position_side"`
	Quantity                  string    `gorm:"column:c_quantity"`
	LimitPrice                *string   `gorm:"column:c_limit_price"`
	ReferencePrice            string    `gorm:"column:c_reference_price"`
	ReferencePriceAt          int64     `gorm:"column:c_reference_price_at"`
	ReduceOnly                bool      `gorm:"column:c_reduce_only"`
	OwnerType                 string    `gorm:"column:c_owner_type"`
	OwnerID                   string    `gorm:"column:c_owner_id"`
	LogicalAccountID          *string   `gorm:"column:c_logical_account_id"`
	RunnerID                  *string   `gorm:"column:c_runner_id"`
	State                     string    `gorm:"column:c_state"`
	FilledQuantity            string    `gorm:"column:c_filled_quantity"`
	AveragePrice              string    `gorm:"column:c_average_price"`
	ReservedAsset             string    `gorm:"column:c_reserved_asset"`
	ReservedQuantity          string    `gorm:"column:c_reserved_quantity"`
	RemainingReservedQuantity string    `gorm:"column:c_remaining_reserved_quantity"`
	PaperExecutionPrice       *string   `gorm:"column:c_paper_execution_price"`
	FirstMatchPending         bool      `gorm:"column:c_first_match_pending"`
	RejectReason              string    `gorm:"column:c_reject_reason"`
	ExchangeUpdatedAt         int64     `gorm:"column:c_exchange_updated_at"`
	Version                   uint64    `gorm:"column:c_version"`
	SubmittedAt               int64     `gorm:"column:c_submitted_at"`
	FinishedAt                int64     `gorm:"column:c_finished_at"`
	CreatedAt                 time.Time `gorm:"column:c_ctime"`
	UpdatedAt                 time.Time `gorm:"column:c_mtime"`
}

func (tx *Tx) CreateOrder(record OrderRecord) error {
	if record.SpaceID == "" || record.OrderID == "" || record.TradingAccountID == "" ||
		record.ClientOrderID == "" || record.ExchangeSymbol == "" ||
		record.OrderType == "" || record.Side == "" ||
		record.Quantity == "" || record.ReferencePrice == "" || record.OwnerType == "" ||
		record.State == "" {
		return fmt.Errorf("%w: incomplete order", ErrInvalidRecord)
	}
	if err := validateOrderOwnership(tx, record); err != nil {
		return err
	}
	identity, err := tx.accountIdentity(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if err := validateOrDeriveIdentity(&record.Exchange, identity.Exchange, "order Exchange"); err != nil {
		return err
	}
	if err := validateOrDeriveIdentity(&record.MarketType, identity.MarketType, "order market type"); err != nil {
		return err
	}
	var instrument struct {
		InstrumentID string `gorm:"column:c_instrument_id"`
	}
	query := `
		SELECT c_instrument_id FROM t_trade_instruments
		WHERE c_exchange = ? AND c_environment = ? AND c_market_type = ?
			AND c_exchange_symbol = ?`
	args := []any{record.Exchange, identity.Environment, record.MarketType, record.ExchangeSymbol}
	if err := tx.db.Raw(query, args...).Scan(&instrument).Error; err != nil {
		return err
	}
	if instrument.InstrumentID == "" {
		instrument.InstrumentID = record.InstrumentID
		if instrument.InstrumentID == "" {
			instrument.InstrumentID = record.ExchangeSymbol
		}
	}
	if record.InstrumentID != "" && record.InstrumentID != instrument.InstrumentID {
		return fmt.Errorf("%w: order instrument ID does not match exchange identity", ErrConflict)
	}
	record.InstrumentID = instrument.InstrumentID
	if err := canonicalizeOrder(&record); err != nil {
		return err
	}
	if record.Version == 0 {
		record.Version = 1
	}
	return writeError(tx.db.Exec(`
		INSERT INTO t_trade_orders (
			c_space_id, c_order_id, c_trading_account_id, c_client_order_id,
			c_exchange_order_id, c_exchange, c_market_type, c_instrument_id, c_exchange_symbol, c_order_type,
			c_time_in_force, c_side, c_position_side, c_quantity, c_limit_price,
			c_reference_price, c_reference_price_at, c_reduce_only, c_owner_type,
			c_owner_id, c_logical_account_id, c_runner_id,
			c_state, c_filled_quantity, c_average_price,
			c_reserved_asset, c_reserved_quantity, c_remaining_reserved_quantity,
			c_paper_execution_price, c_first_match_pending, c_reject_reason,
			c_exchange_updated_at, c_version, c_submitted_at, c_finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.OrderID, record.TradingAccountID, record.ClientOrderID,
		record.ExchangeOrderID, record.Exchange, record.MarketType, record.InstrumentID, record.ExchangeSymbol,
		record.OrderType, record.TimeInForce, record.Side, record.PositionSide,
		record.Quantity, record.LimitPrice, record.ReferencePrice, record.ReferencePriceAt,
		record.ReduceOnly, record.OwnerType, record.OwnerID,
		nullableString(record.LogicalAccountID), nullableString(record.RunnerID), record.State,
		record.FilledQuantity, record.AveragePrice,
		record.ReservedAsset, record.ReservedQuantity,
		record.RemainingReservedQuantity, record.PaperExecutionPrice, record.FirstMatchPending,
		record.RejectReason, record.ExchangeUpdatedAt, record.Version, record.SubmittedAt, record.FinishedAt,
	).Error)
}

func validateOrderOwnership(tx *Tx, record OrderRecord) error {
	var runnerID *string
	if record.RunnerID != "" {
		value := record.RunnerID
		runnerID = &value
	}
	owner := orderdomain.OrderOwner{
		Type:             orderdomain.OwnerType(record.OwnerType),
		OwnerID:          record.OwnerID,
		LogicalAccountID: record.LogicalAccountID,
		RunnerID:         runnerID,
	}
	if err := owner.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
	}
	if record.LogicalAccountID == "" {
		return nil
	}
	account, err := tx.GetLogicalAccount(record.SpaceID, record.LogicalAccountID)
	if err != nil {
		return err
	}
	if owner.Type == orderdomain.OwnerTarget {
		if account.OwnerInstanceID != "" {
			if account.OwnerInstanceID != record.RunnerID {
				return fmt.Errorf("%w: TARGET instance does not own logical account", ErrConflict)
			}
		} else if account.OwnerRunnerID != record.RunnerID {
			return fmt.Errorf("%w: TARGET runner does not own logical account", ErrConflict)
		}
	}
	var membership int64
	if err := tx.db.Raw(`
		SELECT COUNT(*)
		FROM t_logical_account_members
		WHERE c_space_id = ? AND c_logical_account_id = ?
			AND c_trading_account_id = ?
	`,
		record.SpaceID, record.LogicalAccountID, record.TradingAccountID,
	).Scan(&membership).Error; err != nil {
		return err
	}
	if membership != 1 {
		return fmt.Errorf("%w: order account is not a logical account member", ErrInvalidRecord)
	}
	return nil
}

func (s *Store) GetOrder(
	ctx context.Context,
	spaceID string,
	orderID string,
) (OrderRecord, error) {
	var row orderRow
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where("c_space_id = ? AND c_order_id = ?", spaceID, orderID).
		Take(&row).Error
	return orderRecordFromRow(row), err
}

func (s *Store) GetOrderByClientID(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	clientOrderID string,
) (OrderRecord, error) {
	var row orderRow
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(
			"c_space_id = ? AND c_trading_account_id = ? AND c_client_order_id = ?",
			spaceID,
			tradingAccountID,
			clientOrderID,
		).
		Take(&row).Error
	return orderRecordFromRow(row), err
}

func (s *Store) GetOrderByExchangeID(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	symbol string,
	exchangeOrderID string,
) (OrderRecord, error) {
	var row orderRow
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(
			"c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ? AND c_exchange_order_id = ?",
			spaceID,
			tradingAccountID,
			symbol,
			exchangeOrderID,
		).
		Take(&row).Error
	return orderRecordFromRow(row), err
}

func (s *Store) ListOrdersForAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	terminalSince int64,
) ([]OrderRecord, error) {
	var rows []orderRow
	query := s.db.WithContext(ctx).Table("t_trade_orders").
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID)
	if terminalSince > 0 {
		query = query.Where(
			"c_state NOT IN (?, ?, ?, ?, ?) OR c_finished_at >= ?",
			"FILLED",
			"CANCELED",
			"PARTIALLY_CANCELED",
			"REJECTED",
			"EXPIRED",
			terminalSince,
		)
	}
	if err := query.Order("c_order_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orderRecordFromRow(row))
	}
	return records, nil
}

func (s *Store) ListOrdersForLane(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	symbol string,
) ([]OrderRecord, error) {
	var rows []orderRow
	if err := s.db.WithContext(ctx).Table("t_trade_orders").
		Where(
			"c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ?",
			spaceID,
			tradingAccountID,
			symbol,
		).
		Order("c_order_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orderRecordFromRow(row))
	}
	return records, nil
}

type OrderQuery struct {
	LogicalAccountID string
	TradingAccountID string
	InstrumentID     string
	ExchangeSymbol   string
	State            string
	OnlyOpen         bool
	StartTime        int64
	EndTime          int64
	Offset           int
	Limit            int
}

func (s *Store) ListOrders(
	ctx context.Context,
	spaceID string,
	query OrderQuery,
) ([]OrderRecord, int64, error) {
	db := s.db.WithContext(ctx).Table("t_trade_orders").
		Where("c_space_id = ?", spaceID)
	if query.LogicalAccountID != "" {
		db = db.Where("c_logical_account_id = ?", query.LogicalAccountID)
	}
	if query.TradingAccountID != "" {
		db = db.Where("c_trading_account_id = ?", query.TradingAccountID)
	}
	if query.InstrumentID != "" {
		db = db.Where("c_instrument_id = ?", query.InstrumentID)
	}
	if query.ExchangeSymbol != "" {
		db = db.Where("c_exchange_symbol = ?", query.ExchangeSymbol)
	}
	if query.State != "" {
		db = db.Where("c_state = ?", query.State)
	}
	if query.OnlyOpen {
		db = db.Where("c_state NOT IN ?", []string{
			"FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED",
		})
	}
	if query.StartTime > 0 {
		db = db.Where("c_ctime >= datetime(? / 1000, 'unixepoch')", query.StartTime)
	}
	if query.EndTime > 0 {
		db = db.Where("c_ctime <= datetime(? / 1000, 'unixepoch')", query.EndTime)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []orderRow
	if err := db.Order("c_ctime DESC, c_order_id DESC").
		Offset(query.Offset).Limit(query.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	records := make([]OrderRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, orderRecordFromRow(row))
	}
	return records, total, nil
}

func (tx *Tx) GetOrder(spaceID string, orderID string) (OrderRecord, error) {
	var row orderRow
	result := tx.db.Raw(`
		SELECT * FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id = ?
	`, spaceID, orderID).Scan(&row)
	if result.Error != nil {
		return OrderRecord{}, result.Error
	}
	if result.RowsAffected != 1 {
		return OrderRecord{}, fmt.Errorf("%w: order not found", ErrInvalidRecord)
	}
	return orderRecordFromRow(row), nil
}

func (tx *Tx) FindOrderForFill(
	spaceID string,
	tradingAccountID string,
	clientOrderID string,
	exchangeOrderID string,
	symbol string,
) (OrderRecord, error) {
	if spaceID == "" || tradingAccountID == "" || symbol == "" ||
		(clientOrderID == "" && exchangeOrderID == "") {
		return OrderRecord{}, fmt.Errorf("%w: incomplete Fill order identity", ErrInvalidRecord)
	}
	var row orderRow
	query := `
		SELECT * FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ?
	`
	args := []any{spaceID, tradingAccountID, symbol}
	if clientOrderID != "" && exchangeOrderID != "" {
		query += " AND (c_client_order_id = ? OR c_exchange_order_id = ?)"
		args = append(args, clientOrderID, exchangeOrderID)
	} else if clientOrderID != "" {
		query += " AND c_client_order_id = ?"
		args = append(args, clientOrderID)
	} else {
		query += " AND c_exchange_order_id = ?"
		args = append(args, exchangeOrderID)
	}
	result := tx.db.Raw(query, args...).Scan(&row)
	if result.Error != nil {
		return OrderRecord{}, result.Error
	}
	if result.RowsAffected != 1 {
		return OrderRecord{}, fmt.Errorf("%w: Fill order not found", ErrInvalidRecord)
	}
	if clientOrderID != "" && row.ClientOrderID != clientOrderID {
		return OrderRecord{}, fmt.Errorf("%w: conflicting Fill client order ID", ErrConflict)
	}
	if exchangeOrderID != "" && row.ExchangeOrderID != "" &&
		row.ExchangeOrderID != exchangeOrderID {
		return OrderRecord{}, fmt.Errorf("%w: conflicting Fill Exchange order ID", ErrConflict)
	}
	return orderRecordFromRow(row), nil
}

func (tx *Tx) UpdateOrder(record OrderRecord, expectedVersion uint64) error {
	if record.SpaceID == "" || record.OrderID == "" || record.Version != expectedVersion+1 {
		return fmt.Errorf("%w: invalid order update", ErrInvalidRecord)
	}
	if record.State == "" {
		return fmt.Errorf("%w: empty order state", ErrInvalidRecord)
	}
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil || quantity.Cmp(shared.Zero()) <= 0 {
		return fmt.Errorf("%w: invalid order quantity", ErrInvalidRecord)
	}
	for label, value := range map[string]*string{
		"filled quantity":             &record.FilledQuantity,
		"average price":               &record.AveragePrice,
		"reserved quantity":           &record.ReservedQuantity,
		"remaining reserved quantity": &record.RemainingReservedQuantity,
	} {
		canonical, canonicalErr := canonicalDefaultZero(*value, label, decimalNonNegative)
		if canonicalErr != nil {
			return canonicalErr
		}
		*value = canonical
	}
	filled, _ := shared.ParseDecimal(record.FilledQuantity)
	reserved, _ := shared.ParseDecimal(record.ReservedQuantity)
	remaining, _ := shared.ParseDecimal(record.RemainingReservedQuantity)
	if filled.Cmp(quantity) > 0 || remaining.Cmp(reserved) > 0 {
		return fmt.Errorf("%w: inconsistent order quantities", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		UPDATE t_trade_orders
		SET c_exchange_order_id = ?, c_state = ?, c_filled_quantity = ?,
			c_average_price = ?, c_reduce_only = ?, c_reserved_asset = ?,
			c_reserved_quantity = ?, c_remaining_reserved_quantity = ?,
			c_reject_reason = ?, c_exchange_updated_at = ?, c_version = ?,
			c_submitted_at = ?, c_finished_at = ?, c_mtime = CURRENT_TIMESTAMP
		WHERE c_space_id = ? AND c_order_id = ? AND c_version = ?
		`,
		record.ExchangeOrderID, record.State, record.FilledQuantity,
		record.AveragePrice, record.ReduceOnly, record.ReservedAsset,
		record.ReservedQuantity, record.RemainingReservedQuantity,
		record.RejectReason, record.ExchangeUpdatedAt, record.Version,
		record.SubmittedAt, record.FinishedAt, record.SpaceID, record.OrderID, expectedVersion,
	)
	if result.Error != nil {
		return writeError(result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: stale order version", ErrConflict)
	}
	return nil
}

func orderRecordFromRow(row orderRow) OrderRecord {
	return OrderRecord{
		SpaceID: row.SpaceID, OrderID: row.OrderID,
		TradingAccountID: row.TradingAccountID, ClientOrderID: row.ClientOrderID,
		ExchangeOrderID: row.ExchangeOrderID, Exchange: row.Exchange,
		MarketType: row.MarketType, InstrumentID: row.InstrumentID, ExchangeSymbol: row.ExchangeSymbol, OrderType: row.OrderType,
		TimeInForce: row.TimeInForce, Side: row.Side, PositionSide: row.PositionSide,
		Quantity: row.Quantity, LimitPrice: row.LimitPrice,
		ReferencePrice: row.ReferencePrice, ReferencePriceAt: row.ReferencePriceAt,
		ReduceOnly: row.ReduceOnly, OwnerType: row.OwnerType, OwnerID: row.OwnerID,
		LogicalAccountID: valueOrEmpty(row.LogicalAccountID),
		RunnerID:         valueOrEmpty(row.RunnerID), State: row.State,
		FilledQuantity: row.FilledQuantity, AveragePrice: row.AveragePrice,
		ReservedAsset: row.ReservedAsset, ReservedQuantity: row.ReservedQuantity,
		RemainingReservedQuantity: row.RemainingReservedQuantity,
		PaperExecutionPrice:       row.PaperExecutionPrice, FirstMatchPending: row.FirstMatchPending,
		RejectReason: row.RejectReason, ExchangeUpdatedAt: row.ExchangeUpdatedAt,
		Version:     row.Version,
		SubmittedAt: row.SubmittedAt, FinishedAt: row.FinishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type FillRecord struct {
	SpaceID          string
	FillID           string
	ExchangeTradeID  string
	OrderID          string
	ExchangeOrderID  string
	TradingAccountID string
	Exchange         string
	MarketType       string
	ExchangeSymbol   string
	InstrumentID     string
	Side             string
	PositionSide     string
	Price            string
	Quantity         string
	Fee              string
	FeeAsset         string
	SettlementAsset  string
	RealizedPnL      string
	Role             string
	TradedAt         int64
	CreatedAt        time.Time
}

func (tx *Tx) InsertFill(record FillRecord) (bool, error) {
	if record.SpaceID == "" || record.FillID == "" || record.ExchangeTradeID == "" ||
		record.OrderID == "" || record.Price == "" || record.Quantity == "" {
		return false, fmt.Errorf("%w: incomplete Fill", ErrInvalidRecord)
	}
	var orderIdentity struct {
		TradingAccountID string `gorm:"column:c_trading_account_id"`
		ExchangeOrderID  string `gorm:"column:c_exchange_order_id"`
		Exchange         string `gorm:"column:c_exchange"`
		MarketType       string `gorm:"column:c_market_type"`
		InstrumentID     string `gorm:"column:c_instrument_id"`
		ExchangeSymbol   string `gorm:"column:c_exchange_symbol"`
		Side             string `gorm:"column:c_side"`
		PositionSide     string `gorm:"column:c_position_side"`
	}
	result := tx.db.Raw(`
		SELECT c_trading_account_id, c_exchange_order_id, c_exchange, c_market_type,
			c_instrument_id, c_exchange_symbol, c_side, c_position_side
		FROM t_trade_orders
		WHERE c_space_id = ? AND c_order_id = ?
	`, record.SpaceID, record.OrderID).Scan(&orderIdentity)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, fmt.Errorf("%w: Fill order identity", ErrInvalidRecord)
	}
	for _, values := range []struct {
		actual   *string
		expected string
		label    string
	}{
		{&record.TradingAccountID, orderIdentity.TradingAccountID, "Fill Exchange account"},
		{&record.Exchange, orderIdentity.Exchange, "Fill Exchange"},
		{&record.MarketType, orderIdentity.MarketType, "Fill market type"},
		{&record.InstrumentID, orderIdentity.InstrumentID, "Fill instrument ID"},
		{&record.ExchangeSymbol, orderIdentity.ExchangeSymbol, "Fill symbol"},
	} {
		if err := validateOrDeriveIdentity(values.actual, values.expected, values.label); err != nil {
			return false, err
		}
	}
	duplicate, err := tx.fillIdentityExists(record)
	if err != nil {
		return false, err
	}
	for _, values := range []struct {
		actual   *string
		expected string
		label    string
	}{
		{&record.Side, orderIdentity.Side, "Fill side"},
		{&record.PositionSide, orderIdentity.PositionSide, "Fill position side"},
	} {
		if err := validateOrDeriveIdentity(values.actual, values.expected, values.label); err != nil {
			if duplicate {
				return false, fmt.Errorf("%w: conflicting immutable %s", ErrConflict, values.label)
			}
			return false, err
		}
	}
	if record.ExchangeOrderID == "" {
		record.ExchangeOrderID = orderIdentity.ExchangeOrderID
	} else if orderIdentity.ExchangeOrderID != "" &&
		record.ExchangeOrderID != orderIdentity.ExchangeOrderID {
		return false, fmt.Errorf("%w: Fill Exchange order", ErrInvalidRecord)
	}
	if err := canonicalizeFill(&record); err != nil {
		return false, err
	}
	if duplicate {
		return tx.resolveDuplicateFill(record, ErrConflict)
	}
	result = tx.db.Exec(`
		INSERT INTO t_order_fills (
			c_space_id, c_fill_id, c_exchange_trade_id, c_order_id,
			c_exchange_order_id, c_trading_account_id, c_exchange, c_market_type,
			c_instrument_id, c_exchange_symbol, c_side, c_position_side, c_price, c_quantity, c_fee,
			c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.FillID, record.ExchangeTradeID, record.OrderID,
		record.ExchangeOrderID, record.TradingAccountID, record.Exchange,
		record.MarketType, record.InstrumentID, record.ExchangeSymbol, record.Side, record.PositionSide,
		record.Price, record.Quantity, record.Fee, record.FeeAsset,
		record.SettlementAsset, record.RealizedPnL, record.Role,
		record.TradedAt,
	)
	if result.Error != nil {
		conflict := writeError(result.Error)
		if errors.Is(conflict, ErrConflict) {
			return tx.resolveDuplicateFill(record, conflict)
		}
		return false, conflict
	}
	return true, nil
}

func (tx *Tx) fillIdentityExists(record FillRecord) (bool, error) {
	var count int64
	err := tx.db.Raw(`
		SELECT COUNT(*) FROM t_order_fills
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ?
			AND c_exchange_trade_id = ?
	`, record.SpaceID, record.TradingAccountID, record.ExchangeSymbol, record.ExchangeTradeID).
		Scan(&count).Error
	return count != 0, err
}

type fillRow struct {
	SpaceID          string    `gorm:"column:c_space_id"`
	FillID           string    `gorm:"column:c_fill_id"`
	ExchangeTradeID  string    `gorm:"column:c_exchange_trade_id"`
	OrderID          string    `gorm:"column:c_order_id"`
	ExchangeOrderID  string    `gorm:"column:c_exchange_order_id"`
	TradingAccountID string    `gorm:"column:c_trading_account_id"`
	Exchange         string    `gorm:"column:c_exchange"`
	MarketType       string    `gorm:"column:c_market_type"`
	InstrumentID     string    `gorm:"column:c_instrument_id"`
	ExchangeSymbol   string    `gorm:"column:c_exchange_symbol"`
	Side             string    `gorm:"column:c_side"`
	PositionSide     string    `gorm:"column:c_position_side"`
	Price            string    `gorm:"column:c_price"`
	Quantity         string    `gorm:"column:c_quantity"`
	Fee              string    `gorm:"column:c_fee"`
	FeeAsset         string    `gorm:"column:c_fee_asset"`
	SettlementAsset  string    `gorm:"column:c_settlement_asset"`
	RealizedPnL      string    `gorm:"column:c_realized_pnl"`
	Role             string    `gorm:"column:c_role"`
	TradedAt         int64     `gorm:"column:c_traded_at"`
	CreatedAt        time.Time `gorm:"column:c_ctime"`
}

type FillQuery struct {
	TradingAccountID string
	OrderID          string
	InstrumentID     string
	ExchangeSymbol   string
	StartTime        int64
	EndTime          int64
	Offset           int
	Limit            int
}

func (s *Store) ListFills(
	ctx context.Context,
	spaceID string,
	query FillQuery,
) ([]FillRecord, int64, error) {
	db := s.db.WithContext(ctx).Table("t_order_fills").
		Where("c_space_id = ?", spaceID)
	if query.TradingAccountID != "" {
		db = db.Where("c_trading_account_id = ?", query.TradingAccountID)
	}
	if query.OrderID != "" {
		db = db.Where("c_order_id = ?", query.OrderID)
	}
	if query.InstrumentID != "" {
		db = db.Where("c_instrument_id = ?", query.InstrumentID)
	}
	if query.ExchangeSymbol != "" {
		db = db.Where("c_exchange_symbol = ?", query.ExchangeSymbol)
	}
	if query.StartTime > 0 {
		db = db.Where("c_traded_at >= ?", query.StartTime)
	}
	if query.EndTime > 0 {
		db = db.Where("c_traded_at <= ?", query.EndTime)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []fillRow
	if err := db.Order("c_traded_at DESC, c_fill_id DESC").
		Offset(query.Offset).Limit(query.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	records := make([]FillRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, FillRecord{
			SpaceID: row.SpaceID, FillID: row.FillID,
			ExchangeTradeID: row.ExchangeTradeID, OrderID: row.OrderID,
			ExchangeOrderID:  row.ExchangeOrderID,
			TradingAccountID: row.TradingAccountID, Exchange: row.Exchange,
			MarketType: row.MarketType, InstrumentID: row.InstrumentID, ExchangeSymbol: row.ExchangeSymbol, Side: row.Side,
			PositionSide: row.PositionSide, Price: row.Price, Quantity: row.Quantity,
			Fee: row.Fee, FeeAsset: row.FeeAsset,
			SettlementAsset: row.SettlementAsset, RealizedPnL: row.RealizedPnL,
			Role: row.Role, TradedAt: row.TradedAt, CreatedAt: row.CreatedAt,
		})
	}
	return records, total, nil
}

func (tx *Tx) resolveDuplicateFill(
	record FillRecord,
	conflict error,
) (bool, error) {
	var existing fillRow
	result := tx.db.Raw(`
		SELECT c_space_id, c_fill_id, c_exchange_trade_id, c_order_id,
			c_exchange_order_id, c_trading_account_id, c_exchange, c_market_type,
			c_instrument_id, c_exchange_symbol, c_side, c_position_side, c_price, c_quantity, c_fee,
			c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at
		FROM t_order_fills
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_exchange_symbol = ?
			AND c_exchange_trade_id = ?
	`, record.SpaceID, record.TradingAccountID, record.ExchangeSymbol, record.ExchangeTradeID).
		Scan(&existing)
	if result.Error != nil {
		return false, result.Error
	}
	want := fillRowFromRecord(record)
	canonicalReplay := strings.HasPrefix(record.FillID, record.TradingAccountID+":") &&
		strings.HasPrefix(existing.FillID, existing.TradingAccountID+":")
	if result.RowsAffected != 1 || (!canonicalReplay && existing.FillID != want.FillID) ||
		existing.SpaceID != want.SpaceID || existing.ExchangeTradeID != want.ExchangeTradeID ||
		existing.TradingAccountID != want.TradingAccountID || existing.Exchange != want.Exchange ||
		existing.MarketType != want.MarketType || existing.ExchangeSymbol != want.ExchangeSymbol ||
		existing.Side != want.Side || existing.PositionSide != want.PositionSide ||
		existing.Price != want.Price || existing.Quantity != want.Quantity ||
		existing.Fee != want.Fee || existing.FeeAsset != want.FeeAsset ||
		existing.SettlementAsset != want.SettlementAsset || existing.RealizedPnL != want.RealizedPnL ||
		existing.Role != want.Role || existing.TradedAt != want.TradedAt {
		return false, fmt.Errorf("%w: conflicting immutable Fill replay", conflict)
	}
	if !canonicalReplay && (existing.OrderID != want.OrderID || existing.ExchangeOrderID != want.ExchangeOrderID) {
		return false, fmt.Errorf("%w: conflicting immutable Fill order replay", conflict)
	}
	// The unique Exchange trade identity is authoritative. Older Paper
	// snapshots can replay the same trade with a generated fill/order ID;
	// treating the row as an idempotent replay avoids re-applying the fill.
	return false, nil
}

func fillRowFromRecord(record FillRecord) fillRow {
	return fillRow{
		SpaceID: record.SpaceID, FillID: record.FillID,
		ExchangeTradeID: record.ExchangeTradeID, OrderID: record.OrderID,
		ExchangeOrderID:  record.ExchangeOrderID,
		TradingAccountID: record.TradingAccountID, Exchange: record.Exchange,
		MarketType: record.MarketType, InstrumentID: record.InstrumentID, ExchangeSymbol: record.ExchangeSymbol, Side: record.Side,
		PositionSide: record.PositionSide, Price: record.Price,
		Quantity: record.Quantity, Fee: record.Fee, FeeAsset: record.FeeAsset,
		SettlementAsset: record.SettlementAsset, RealizedPnL: record.RealizedPnL,
		Role: record.Role, TradedAt: record.TradedAt,
	}
}

type tradingAccountIdentity struct {
	Exchange      string `gorm:"column:c_exchange"`
	MarketType    string `gorm:"column:c_market_type"`
	Environment   string `gorm:"column:c_environment"`
	ExecutionMode string `gorm:"column:c_execution_mode"`
}

func (tx *Tx) accountIdentity(spaceID string, tradingAccountID string) (tradingAccountIdentity, error) {
	var identity tradingAccountIdentity
	result := tx.db.Raw(`
		SELECT c_exchange, c_market_type, c_live_environment AS c_environment, c_execution_mode
		FROM t_trading_accounts
		WHERE c_space_id = ? AND c_trading_account_id = ?
	`, spaceID, tradingAccountID).Scan(&identity)
	if result.Error != nil {
		return tradingAccountIdentity{}, result.Error
	}
	if result.RowsAffected != 1 {
		return tradingAccountIdentity{}, fmt.Errorf("%w: Exchange account identity", ErrInvalidRecord)
	}
	if identity.ExecutionMode == "PAPER" {
		identity.Environment = "PRODUCTION"
	}
	if identity.Environment == "" {
		return tradingAccountIdentity{}, fmt.Errorf("%w: missing account environment", ErrInvalidRecord)
	}
	// identity is normalized to the market-data environment for instrument lookup.
	return identity, nil
}

func validateOrDeriveIdentity(actual *string, expected string, label string) error {
	if *actual == "" {
		*actual = expected
		return nil
	}
	if *actual != expected {
		return fmt.Errorf("%w: contradictory %s", ErrInvalidRecord, label)
	}
	return nil
}

type PositionRecord struct {
	SpaceID           string
	TradingAccountID  string
	InstrumentID      string
	ExchangeSymbol    string
	PositionSide      string
	SignedQuantity    string
	EntryPrice        string
	MarkPrice         string
	Leverage          string
	MarginMode        string
	UsedMargin        string
	LiquidationPrice  string
	UnrealizedPnL     string
	RealizedPnL       string
	ExchangeUpdatedAt int64
	UpdatedAt         time.Time
}

func (tx *Tx) UpsertPosition(record PositionRecord) error {
	if record.SpaceID == "" || record.TradingAccountID == "" || record.ExchangeSymbol == "" ||
		record.PositionSide == "" {
		return fmt.Errorf("%w: incomplete position", ErrInvalidRecord)
	}
	identity, err := tx.accountIdentity(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if record.InstrumentID == "" {
		var instrument struct {
			InstrumentID string `gorm:"column:c_instrument_id"`
		}
		result := tx.db.Raw(`
			SELECT c_instrument_id FROM t_trade_instruments
			WHERE c_exchange = ? AND c_environment = ? AND c_market_type = ? AND c_exchange_symbol = ?
		`, identity.Exchange, identity.Environment, identity.MarketType, record.ExchangeSymbol).Scan(&instrument)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || instrument.InstrumentID == "" {
			record.InstrumentID = record.ExchangeSymbol
		} else {
			record.InstrumentID = instrument.InstrumentID
		}
	}
	if err := canonicalizePosition(&record, identity.MarketType); err != nil {
		return err
	}
	return tx.db.Exec(`
		INSERT INTO t_trading_positions (
			c_space_id, c_trading_account_id, c_instrument_id, c_exchange_symbol, c_position_side,
			c_signed_quantity, c_entry_price, c_mark_price, c_leverage, c_margin_mode,
			c_used_margin, c_liquidation_price, c_unrealized_pnl, c_realized_pnl,
			c_exchange_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_trading_account_id, c_exchange_symbol, c_position_side)
		DO UPDATE SET
			c_signed_quantity = excluded.c_signed_quantity,
			c_entry_price = excluded.c_entry_price,
			c_mark_price = excluded.c_mark_price,
			c_leverage = excluded.c_leverage,
			c_margin_mode = excluded.c_margin_mode,
			c_used_margin = excluded.c_used_margin,
			c_liquidation_price = excluded.c_liquidation_price,
			c_unrealized_pnl = excluded.c_unrealized_pnl,
			c_realized_pnl = excluded.c_realized_pnl,
			c_exchange_updated_at = excluded.c_exchange_updated_at,
			c_mtime = CURRENT_TIMESTAMP
		WHERE excluded.c_exchange_updated_at >=
			t_trading_positions.c_exchange_updated_at
	`,
		record.SpaceID, record.TradingAccountID, record.InstrumentID, record.ExchangeSymbol, record.PositionSide,
		record.SignedQuantity, record.EntryPrice,
		record.MarkPrice, record.Leverage, record.MarginMode,
		record.UsedMargin, record.LiquidationPrice,
		record.UnrealizedPnL, record.RealizedPnL,
		record.ExchangeUpdatedAt,
	).Error
}

func (tx *Tx) ReplacePositionsForAccount(
	spaceID string,
	tradingAccountID string,
	records []PositionRecord,
	snapshotAt int64,
) error {
	if blank(spaceID) || blank(tradingAccountID) || snapshotAt <= 0 {
		return fmt.Errorf("%w: incomplete position snapshot identity", ErrInvalidRecord)
	}
	for _, record := range records {
		if record.SpaceID != spaceID || record.TradingAccountID != tradingAccountID {
			return fmt.Errorf("%w: conflicting position snapshot identity", ErrInvalidRecord)
		}
		if err := tx.UpsertPosition(record); err != nil {
			return err
		}
	}
	query := `
		DELETE FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ?
			AND c_exchange_updated_at <= ?
	`
	args := []any{spaceID, tradingAccountID, snapshotAt}
	if len(records) > 0 {
		var retained strings.Builder
		retained.WriteString(" AND NOT (")
		for index, record := range records {
			if index > 0 {
				retained.WriteString(" OR ")
			}
			retained.WriteString("(c_exchange_symbol = ? AND c_position_side = ?)")
			args = append(args, record.ExchangeSymbol, record.PositionSide)
		}
		retained.WriteString(")")
		query += retained.String()
	}
	if err := tx.db.Exec(query, args...).Error; err != nil {
		return writeError(err)
	}
	return nil
}

func (tx *Tx) GetPosition(
	spaceID string,
	tradingAccountID string,
	symbol string,
	positionSide string,
) (PositionRecord, bool, error) {
	var row struct {
		SpaceID           string    `gorm:"column:c_space_id"`
		TradingAccountID  string    `gorm:"column:c_trading_account_id"`
		InstrumentID      string    `gorm:"column:c_instrument_id"`
		ExchangeSymbol    string    `gorm:"column:c_exchange_symbol"`
		PositionSide      string    `gorm:"column:c_position_side"`
		SignedQuantity    string    `gorm:"column:c_signed_quantity"`
		EntryPrice        string    `gorm:"column:c_entry_price"`
		MarkPrice         string    `gorm:"column:c_mark_price"`
		Leverage          string    `gorm:"column:c_leverage"`
		MarginMode        string    `gorm:"column:c_margin_mode"`
		UsedMargin        string    `gorm:"column:c_used_margin"`
		LiquidationPrice  string    `gorm:"column:c_liquidation_price"`
		UnrealizedPnL     string    `gorm:"column:c_unrealized_pnl"`
		RealizedPnL       string    `gorm:"column:c_realized_pnl"`
		ExchangeUpdatedAt int64     `gorm:"column:c_exchange_updated_at"`
		UpdatedAt         time.Time `gorm:"column:c_mtime"`
	}
	result := tx.db.Raw(`
		SELECT * FROM t_trading_positions
		WHERE c_space_id = ? AND c_trading_account_id = ?
			AND c_exchange_symbol = ? AND c_position_side = ?
	`, spaceID, tradingAccountID, symbol, positionSide).Scan(&row)
	if result.Error != nil {
		return PositionRecord{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return PositionRecord{}, false, nil
	}
	return PositionRecord{
		SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID, InstrumentID: row.InstrumentID,
		ExchangeSymbol: row.ExchangeSymbol, PositionSide: row.PositionSide,
		SignedQuantity: row.SignedQuantity, EntryPrice: row.EntryPrice,
		MarkPrice: row.MarkPrice, Leverage: row.Leverage,
		MarginMode: row.MarginMode, UsedMargin: row.UsedMargin,
		LiquidationPrice: row.LiquidationPrice,
		UnrealizedPnL:    row.UnrealizedPnL, RealizedPnL: row.RealizedPnL,
		ExchangeUpdatedAt: row.ExchangeUpdatedAt,
		UpdatedAt:         row.UpdatedAt,
	}, true, nil
}

func (s *Store) GetPosition(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	symbol string,
	positionSide string,
) (PositionRecord, bool, error) {
	var record PositionRecord
	var found bool
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		record, found, err = tx.GetPosition(
			spaceID,
			tradingAccountID,
			symbol,
			positionSide,
		)
		return err
	})
	return record, found, err
}

func (s *Store) ListPositions(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	symbol string,
) ([]PositionRecord, error) {
	return s.listPositions(ctx, spaceID, tradingAccountID, "c_exchange_symbol", symbol)
}

// ListPositionsByInstrument filters positions by the canonical instrument ID.
func (s *Store) ListPositionsByInstrument(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	instrumentID string,
) ([]PositionRecord, error) {
	return s.listPositions(ctx, spaceID, tradingAccountID, "c_instrument_id", instrumentID)
}

func (s *Store) listPositions(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	filterColumn string,
	filterValue string,
) ([]PositionRecord, error) {
	db := s.db.WithContext(ctx).Table("t_trading_positions").
		Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, tradingAccountID)
	if filterValue != "" {
		db = db.Where(filterColumn+" = ?", filterValue)
	}
	var rows []struct {
		SpaceID           string    `gorm:"column:c_space_id"`
		TradingAccountID  string    `gorm:"column:c_trading_account_id"`
		InstrumentID      string    `gorm:"column:c_instrument_id"`
		ExchangeSymbol    string    `gorm:"column:c_exchange_symbol"`
		PositionSide      string    `gorm:"column:c_position_side"`
		SignedQuantity    string    `gorm:"column:c_signed_quantity"`
		EntryPrice        string    `gorm:"column:c_entry_price"`
		MarkPrice         string    `gorm:"column:c_mark_price"`
		Leverage          string    `gorm:"column:c_leverage"`
		MarginMode        string    `gorm:"column:c_margin_mode"`
		UsedMargin        string    `gorm:"column:c_used_margin"`
		LiquidationPrice  string    `gorm:"column:c_liquidation_price"`
		UnrealizedPnL     string    `gorm:"column:c_unrealized_pnl"`
		RealizedPnL       string    `gorm:"column:c_realized_pnl"`
		ExchangeUpdatedAt int64     `gorm:"column:c_exchange_updated_at"`
		UpdatedAt         time.Time `gorm:"column:c_mtime"`
	}
	if err := db.Order("c_exchange_symbol, c_position_side").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]PositionRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, PositionRecord{
			SpaceID: row.SpaceID, TradingAccountID: row.TradingAccountID,
			InstrumentID: row.InstrumentID, ExchangeSymbol: row.ExchangeSymbol, PositionSide: row.PositionSide,
			SignedQuantity: row.SignedQuantity, EntryPrice: row.EntryPrice,
			MarkPrice: row.MarkPrice, Leverage: row.Leverage,
			MarginMode: row.MarginMode, UsedMargin: row.UsedMargin,
			LiquidationPrice: row.LiquidationPrice,
			UnrealizedPnL:    row.UnrealizedPnL, RealizedPnL: row.RealizedPnL,
			ExchangeUpdatedAt: row.ExchangeUpdatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return records, nil
}

func canonicalizeOrder(record *OrderRecord) error {
	var err error
	record.Quantity, err = canonicalDecimal(record.Quantity, "order quantity", decimalPositive)
	if err != nil {
		return err
	}
	record.ReferencePrice, err = canonicalDecimal(
		record.ReferencePrice,
		"order reference price",
		decimalPositive,
	)
	if err != nil {
		return err
	}
	if record.LimitPrice != nil {
		value, err := canonicalDecimal(*record.LimitPrice, "order limit price", decimalPositive)
		if err != nil {
			return err
		}
		record.LimitPrice = &value
	}
	if record.OrderType == "LIMIT" && record.LimitPrice == nil {
		return fmt.Errorf("%w: LIMIT order price", ErrInvalidRecord)
	}
	if record.OrderType == "MARKET" && record.LimitPrice != nil {
		return fmt.Errorf("%w: MARKET order price", ErrInvalidRecord)
	}
	for _, field := range []struct {
		value *string
		label string
	}{
		{&record.FilledQuantity, "filled quantity"},
		{&record.AveragePrice, "average price"},
		{&record.ReservedQuantity, "reserved quantity"},
		{&record.RemainingReservedQuantity, "remaining reserved quantity"},
	} {
		*field.value, err = canonicalDefaultZero(*field.value, field.label, decimalNonNegative)
		if err != nil {
			return err
		}
	}
	if decimalGreater(record.FilledQuantity, record.Quantity) ||
		decimalGreater(record.RemainingReservedQuantity, record.ReservedQuantity) {
		return fmt.Errorf("%w: inconsistent order totals", ErrInvalidRecord)
	}
	return nil
}

func canonicalizeFill(record *FillRecord) error {
	var err error
	record.Price, err = canonicalDecimal(record.Price, "Fill price", decimalPositive)
	if err != nil {
		return err
	}
	record.Quantity, err = canonicalDecimal(record.Quantity, "Fill quantity", decimalPositive)
	if err != nil {
		return err
	}
	record.Fee, err = canonicalDefaultZero(record.Fee, "Fill fee", decimalNonNegative)
	if err != nil {
		return err
	}
	record.RealizedPnL, err = canonicalDefaultZero(
		record.RealizedPnL,
		"Fill realized PnL",
		decimalSigned,
	)
	return err
}

func canonicalizePosition(record *PositionRecord, marketType string) error {
	var err error
	for _, field := range []struct {
		value *string
		label string
		sign  decimalSign
	}{
		{&record.SignedQuantity, "position signed quantity", decimalSigned},
		{&record.EntryPrice, "position entry price", decimalNonNegative},
		{&record.MarkPrice, "position mark price", decimalNonNegative},
		{&record.UsedMargin, "position used margin", decimalNonNegative},
		{&record.LiquidationPrice, "position liquidation price", decimalNonNegative},
		{&record.UnrealizedPnL, "position unrealized PnL", decimalSigned},
		{&record.RealizedPnL, "position realized PnL", decimalSigned},
	} {
		*field.value, err = canonicalDefaultZero(*field.value, field.label, field.sign)
		if err != nil {
			return err
		}
	}
	leverageSign := decimalNonNegative
	if marketType == "SWAP" {
		leverageSign = decimalPositive
	}
	record.Leverage, err = canonicalDefaultZero(
		record.Leverage,
		"position leverage",
		leverageSign,
	)
	return err
}

func decimalGreater(left string, right string) bool {
	leftValue, _ := shared.ParseDecimal(left)
	rightValue, _ := shared.ParseDecimal(right)
	return leftValue.Cmp(rightValue) > 0
}
