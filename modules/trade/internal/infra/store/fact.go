package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type InstrumentRecord struct {
	Exchange             string
	MarketType           string
	Symbol               string
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
	if record.Exchange == "" || record.MarketType == "" || record.Symbol == "" ||
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
		INSERT INTO t_exchange_instruments (
			c_exchange, c_market_type, c_symbol, c_instrument_id, c_base_asset,
			c_quote_asset, c_settlement_asset, c_linear, c_contract_value,
			c_contract_value_asset, c_exchange_quantity_step,
			c_min_exchange_quantity, c_price_tick, c_min_notional, c_status,
			c_exchange_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_exchange, c_market_type, c_symbol) DO UPDATE SET
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
		record.Exchange, record.MarketType, record.Symbol, record.InstrumentID,
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
	var row struct {
		Exchange             string `gorm:"column:c_exchange"`
		MarketType           string `gorm:"column:c_market_type"`
		Symbol               string `gorm:"column:c_symbol"`
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
	err := s.db.WithContext(ctx).Table("t_exchange_instruments").
		Where("c_exchange = ? AND c_market_type = ? AND c_symbol = ?", exchange, marketType, symbol).
		Take(&row).Error
	return InstrumentRecord{
		Exchange: row.Exchange, MarketType: row.MarketType, Symbol: row.Symbol,
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

type OrderRecord struct {
	SpaceID                   string
	OrderID                   string
	ExchangeAccountID         string
	ClientOrderID             string
	ExchangeOrderID           string
	Exchange                  string
	MarketType                string
	Symbol                    string
	OrderType                 string
	TimeInForce               string
	Side                      string
	PositionSide              string
	Quantity                  string
	LimitPrice                *string
	ReferencePrice            string
	ReferencePriceAt          int64
	ReduceOnly                bool
	Source                    string
	StrategyExecutionID       string
	State                     string
	FilledQuantity            string
	AveragePrice              string
	ReservedAsset             string
	ReservedQuantity          string
	RemainingReservedQuantity string
	RejectReason              string
	Version                   uint64
	SubmittedAt               int64
	FinishedAt                int64
}

func (tx *Tx) CreateOrder(record OrderRecord) error {
	if record.SpaceID == "" || record.OrderID == "" || record.ExchangeAccountID == "" ||
		record.ClientOrderID == "" || record.Symbol == "" ||
		record.OrderType == "" || record.Side == "" ||
		record.Quantity == "" || record.ReferencePrice == "" || record.Source == "" ||
		record.State == "" {
		return fmt.Errorf("%w: incomplete order", ErrInvalidRecord)
	}
	identity, err := tx.accountIdentity(record.SpaceID, record.ExchangeAccountID)
	if err != nil {
		return err
	}
	if err := validateOrDeriveIdentity(&record.Exchange, identity.Exchange, "order Exchange"); err != nil {
		return err
	}
	if err := validateOrDeriveIdentity(&record.MarketType, identity.MarketType, "order market type"); err != nil {
		return err
	}
	var instrumentCount int64
	if err := tx.db.Raw(`
		SELECT COUNT(*) FROM t_exchange_instruments
		WHERE c_exchange = ? AND c_market_type = ? AND c_symbol = ?
	`, record.Exchange, record.MarketType, record.Symbol).Scan(&instrumentCount).Error; err != nil {
		return err
	}
	if instrumentCount != 1 {
		return fmt.Errorf("%w: order instrument identity", ErrInvalidRecord)
	}
	if err := canonicalizeOrder(&record); err != nil {
		return err
	}
	if record.Version == 0 {
		record.Version = 1
	}
	return writeError(tx.db.Exec(`
		INSERT INTO t_trade_orders (
			c_space_id, c_order_id, c_exchange_account_id, c_client_order_id,
			c_exchange_order_id, c_exchange, c_market_type, c_symbol, c_order_type,
			c_time_in_force, c_side, c_position_side, c_quantity, c_limit_price,
			c_reference_price, c_reference_price_at, c_reduce_only, c_source,
			c_strategy_execution_id, c_state, c_filled_quantity, c_average_price,
			c_reserved_asset, c_reserved_quantity, c_remaining_reserved_quantity,
			c_reject_reason, c_version, c_submitted_at, c_finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.OrderID, record.ExchangeAccountID, record.ClientOrderID,
		record.ExchangeOrderID, record.Exchange, record.MarketType, record.Symbol,
		record.OrderType, record.TimeInForce, record.Side, record.PositionSide,
		record.Quantity, record.LimitPrice, record.ReferencePrice, record.ReferencePriceAt,
		record.ReduceOnly, record.Source, record.StrategyExecutionID, record.State,
		record.FilledQuantity, record.AveragePrice,
		record.ReservedAsset, record.ReservedQuantity,
		record.RemainingReservedQuantity, record.RejectReason,
		record.Version, record.SubmittedAt, record.FinishedAt,
	).Error)
}

type FillRecord struct {
	SpaceID           string
	FillID            string
	ExchangeTradeID   string
	OrderID           string
	ExchangeOrderID   string
	ExchangeAccountID string
	Exchange          string
	MarketType        string
	Symbol            string
	Side              string
	PositionSide      string
	Price             string
	Quantity          string
	Fee               string
	FeeAsset          string
	SettlementAsset   string
	RealizedPnL       string
	Role              string
	TradedAt          int64
}

func (tx *Tx) InsertFill(record FillRecord) (bool, error) {
	if record.SpaceID == "" || record.FillID == "" || record.ExchangeTradeID == "" ||
		record.OrderID == "" || record.Price == "" || record.Quantity == "" {
		return false, fmt.Errorf("%w: incomplete Fill", ErrInvalidRecord)
	}
	var orderIdentity struct {
		ExchangeAccountID string `gorm:"column:c_exchange_account_id"`
		ExchangeOrderID   string `gorm:"column:c_exchange_order_id"`
		Exchange          string `gorm:"column:c_exchange"`
		MarketType        string `gorm:"column:c_market_type"`
		Symbol            string `gorm:"column:c_symbol"`
		Side              string `gorm:"column:c_side"`
		PositionSide      string `gorm:"column:c_position_side"`
	}
	result := tx.db.Raw(`
		SELECT c_exchange_account_id, c_exchange_order_id, c_exchange, c_market_type,
			c_symbol, c_side, c_position_side
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
		{&record.ExchangeAccountID, orderIdentity.ExchangeAccountID, "Fill Exchange account"},
		{&record.Exchange, orderIdentity.Exchange, "Fill Exchange"},
		{&record.MarketType, orderIdentity.MarketType, "Fill market type"},
		{&record.Symbol, orderIdentity.Symbol, "Fill symbol"},
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
	result = tx.db.Exec(`
		INSERT INTO t_order_fills (
			c_space_id, c_fill_id, c_exchange_trade_id, c_order_id,
			c_exchange_order_id, c_exchange_account_id, c_exchange, c_market_type,
			c_symbol, c_side, c_position_side, c_price, c_quantity, c_fee,
			c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.SpaceID, record.FillID, record.ExchangeTradeID, record.OrderID,
		record.ExchangeOrderID, record.ExchangeAccountID, record.Exchange,
		record.MarketType, record.Symbol, record.Side, record.PositionSide,
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
		WHERE c_space_id = ? AND c_exchange_account_id = ? AND c_symbol = ?
			AND c_exchange_trade_id = ?
	`, record.SpaceID, record.ExchangeAccountID, record.Symbol, record.ExchangeTradeID).
		Scan(&count).Error
	return count != 0, err
}

type fillRow struct {
	SpaceID           string `gorm:"column:c_space_id"`
	FillID            string `gorm:"column:c_fill_id"`
	ExchangeTradeID   string `gorm:"column:c_exchange_trade_id"`
	OrderID           string `gorm:"column:c_order_id"`
	ExchangeOrderID   string `gorm:"column:c_exchange_order_id"`
	ExchangeAccountID string `gorm:"column:c_exchange_account_id"`
	Exchange          string `gorm:"column:c_exchange"`
	MarketType        string `gorm:"column:c_market_type"`
	Symbol            string `gorm:"column:c_symbol"`
	Side              string `gorm:"column:c_side"`
	PositionSide      string `gorm:"column:c_position_side"`
	Price             string `gorm:"column:c_price"`
	Quantity          string `gorm:"column:c_quantity"`
	Fee               string `gorm:"column:c_fee"`
	FeeAsset          string `gorm:"column:c_fee_asset"`
	SettlementAsset   string `gorm:"column:c_settlement_asset"`
	RealizedPnL       string `gorm:"column:c_realized_pnl"`
	Role              string `gorm:"column:c_role"`
	TradedAt          int64  `gorm:"column:c_traded_at"`
}

func (tx *Tx) resolveDuplicateFill(
	record FillRecord,
	conflict error,
) (bool, error) {
	var existing fillRow
	result := tx.db.Raw(`
		SELECT c_space_id, c_fill_id, c_exchange_trade_id, c_order_id,
			c_exchange_order_id, c_exchange_account_id, c_exchange, c_market_type,
			c_symbol, c_side, c_position_side, c_price, c_quantity, c_fee,
			c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at
		FROM t_order_fills
		WHERE c_space_id = ? AND c_exchange_account_id = ? AND c_symbol = ?
			AND c_exchange_trade_id = ?
	`, record.SpaceID, record.ExchangeAccountID, record.Symbol, record.ExchangeTradeID).
		Scan(&existing)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 || existing != fillRowFromRecord(record) {
		return false, fmt.Errorf("%w: conflicting immutable Fill replay", conflict)
	}
	return false, nil
}

func fillRowFromRecord(record FillRecord) fillRow {
	return fillRow{
		SpaceID: record.SpaceID, FillID: record.FillID,
		ExchangeTradeID: record.ExchangeTradeID, OrderID: record.OrderID,
		ExchangeOrderID:   record.ExchangeOrderID,
		ExchangeAccountID: record.ExchangeAccountID, Exchange: record.Exchange,
		MarketType: record.MarketType, Symbol: record.Symbol, Side: record.Side,
		PositionSide: record.PositionSide, Price: record.Price,
		Quantity: record.Quantity, Fee: record.Fee, FeeAsset: record.FeeAsset,
		SettlementAsset: record.SettlementAsset, RealizedPnL: record.RealizedPnL,
		Role: record.Role, TradedAt: record.TradedAt,
	}
}

type exchangeAccountIdentity struct {
	Exchange   string `gorm:"column:c_exchange"`
	MarketType string `gorm:"column:c_market_type"`
}

func (tx *Tx) accountIdentity(spaceID string, exchangeAccountID string) (exchangeAccountIdentity, error) {
	var identity exchangeAccountIdentity
	result := tx.db.Raw(`
		SELECT c_exchange, c_market_type
		FROM t_exchange_accounts
		WHERE c_space_id = ? AND c_exchange_account_id = ?
	`, spaceID, exchangeAccountID).Scan(&identity)
	if result.Error != nil {
		return exchangeAccountIdentity{}, result.Error
	}
	if result.RowsAffected != 1 {
		return exchangeAccountIdentity{}, fmt.Errorf("%w: Exchange account identity", ErrInvalidRecord)
	}
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
	ExchangeAccountID string
	Symbol            string
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
}

func (tx *Tx) UpsertPosition(record PositionRecord) error {
	if record.SpaceID == "" || record.ExchangeAccountID == "" || record.Symbol == "" ||
		record.PositionSide == "" {
		return fmt.Errorf("%w: incomplete position", ErrInvalidRecord)
	}
	identity, err := tx.accountIdentity(record.SpaceID, record.ExchangeAccountID)
	if err != nil {
		return err
	}
	if err := canonicalizePosition(&record, identity.MarketType); err != nil {
		return err
	}
	return tx.db.Exec(`
		INSERT INTO t_exchange_positions (
			c_space_id, c_exchange_account_id, c_symbol, c_position_side,
			c_signed_quantity, c_entry_price, c_mark_price, c_leverage, c_margin_mode,
			c_used_margin, c_liquidation_price, c_unrealized_pnl, c_realized_pnl,
			c_exchange_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_exchange_account_id, c_symbol, c_position_side)
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
	`,
		record.SpaceID, record.ExchangeAccountID, record.Symbol, record.PositionSide,
		record.SignedQuantity, record.EntryPrice,
		record.MarkPrice, record.Leverage, record.MarginMode,
		record.UsedMargin, record.LiquidationPrice,
		record.UnrealizedPnL, record.RealizedPnL,
		record.ExchangeUpdatedAt,
	).Error
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
