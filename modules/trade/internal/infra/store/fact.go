package store

import (
	"fmt"
	"strings"
)

type InstrumentRecord struct {
	Exchange          string
	MarketType        string
	Symbol            string
	InstrumentID      string
	BaseAsset         string
	QuoteAsset        string
	SettlementAsset   string
	PriceTick         string
	QuantityStep      string
	MinQuantity       string
	MinNotional       string
	ContractSize      string
	Status            string
	ExchangeUpdatedAt int64
}

func (tx *Tx) UpsertInstrument(record InstrumentRecord) error {
	if record.Exchange == "" || record.MarketType == "" || record.Symbol == "" ||
		record.BaseAsset == "" || record.QuoteAsset == "" || record.PriceTick == "" ||
		record.QuantityStep == "" || record.ContractSize == "" || record.Status == "" {
		return fmt.Errorf("%w: incomplete instrument", ErrInvalidRecord)
	}
	return writeError(tx.db.Exec(`
		INSERT INTO t_exchange_instruments (
			c_exchange, c_market_type, c_symbol, c_instrument_id, c_base_asset,
			c_quote_asset, c_settlement_asset, c_price_tick, c_quantity_step,
			c_min_quantity, c_min_notional, c_contract_size, c_status,
			c_exchange_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_exchange, c_market_type, c_symbol) DO UPDATE SET
			c_instrument_id = excluded.c_instrument_id,
			c_base_asset = excluded.c_base_asset,
			c_quote_asset = excluded.c_quote_asset,
			c_settlement_asset = excluded.c_settlement_asset,
			c_price_tick = excluded.c_price_tick,
			c_quantity_step = excluded.c_quantity_step,
			c_min_quantity = excluded.c_min_quantity,
			c_min_notional = excluded.c_min_notional,
			c_contract_size = excluded.c_contract_size,
			c_status = excluded.c_status,
			c_exchange_updated_at = excluded.c_exchange_updated_at,
			c_mtime = CURRENT_TIMESTAMP
	`,
		record.Exchange, record.MarketType, record.Symbol, record.InstrumentID,
		record.BaseAsset, record.QuoteAsset, record.SettlementAsset, record.PriceTick,
		record.QuantityStep, defaultDecimal(record.MinQuantity),
		defaultDecimal(record.MinNotional), record.ContractSize, record.Status,
		record.ExchangeUpdatedAt,
	).Error)
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
		record.ClientOrderID == "" || record.Exchange == "" || record.MarketType == "" ||
		record.Symbol == "" || record.OrderType == "" || record.Side == "" ||
		record.Quantity == "" || record.ReferencePrice == "" || record.Source == "" ||
		record.State == "" {
		return fmt.Errorf("%w: incomplete order", ErrInvalidRecord)
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
		defaultDecimal(record.FilledQuantity), defaultDecimal(record.AveragePrice),
		record.ReservedAsset, defaultDecimal(record.ReservedQuantity),
		defaultDecimal(record.RemainingReservedQuantity), record.RejectReason,
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
		record.OrderID == "" || record.ExchangeAccountID == "" || record.Exchange == "" ||
		record.MarketType == "" || record.Symbol == "" || record.Side == "" ||
		record.Price == "" || record.Quantity == "" {
		return false, fmt.Errorf("%w: incomplete Fill", ErrInvalidRecord)
	}
	result := tx.db.Exec(`
		INSERT INTO t_order_fills (
			c_space_id, c_fill_id, c_exchange_trade_id, c_order_id,
			c_exchange_order_id, c_exchange_account_id, c_exchange, c_market_type,
			c_symbol, c_side, c_position_side, c_price, c_quantity, c_fee,
			c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_exchange_account_id, c_symbol, c_exchange_trade_id)
		DO NOTHING
	`,
		record.SpaceID, record.FillID, record.ExchangeTradeID, record.OrderID,
		record.ExchangeOrderID, record.ExchangeAccountID, record.Exchange,
		record.MarketType, record.Symbol, record.Side, record.PositionSide,
		record.Price, record.Quantity, defaultDecimal(record.Fee), record.FeeAsset,
		record.SettlementAsset, defaultDecimal(record.RealizedPnL), record.Role,
		record.TradedAt,
	)
	if result.Error != nil {
		return false, writeError(result.Error)
	}
	return result.RowsAffected == 1, nil
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
		defaultDecimal(record.SignedQuantity), defaultDecimal(record.EntryPrice),
		defaultDecimal(record.MarkPrice), defaultDecimal(record.Leverage), record.MarginMode,
		defaultDecimal(record.UsedMargin), defaultDecimal(record.LiquidationPrice),
		defaultDecimal(record.UnrealizedPnL), defaultDecimal(record.RealizedPnL),
		record.ExchangeUpdatedAt,
	).Error
}

func defaultDecimal(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "0"
	}
	return raw
}
