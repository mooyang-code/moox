package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gorm.io/gorm"
)

type PaperBalanceSnapshot struct {
	Totals   map[string]shared.Decimal
	Reserved map[string]shared.Decimal
}

func (tx *Tx) initializePaperBalance(spaceID, accountID, initial string) error {
	account, err := tx.GetTradingAccount(spaceID, accountID)
	if err != nil {
		return err
	}
	if account.ExecutionMode != "PAPER" || account.SettlementAsset == "" {
		return fmt.Errorf("%w: paper balance account", ErrInvalidRecord)
	}
	total, err := canonicalDecimal(initial, "paper initial balance", decimalPositive)
	if err != nil {
		return err
	}
	if err := tx.db.Exec(execInsertPaperBalanceProjection, spaceID, accountID, time.Now().UTC().UnixMilli()).Error; err != nil {
		return writeError(err)
	}
	return tx.db.Exec(execInsertPaperAssetBalance, spaceID, accountID, account.SettlementAsset, total).Error
}

func (s *Store) GetPaperBalanceSnapshot(ctx context.Context, spaceID, accountID string) (PaperBalanceSnapshot, error) {
	var result PaperBalanceSnapshot
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		result, err = tx.GetPaperBalanceSnapshot(spaceID, accountID)
		return err
	})
	return result, err
}

func (tx *Tx) GetPaperBalanceSnapshot(spaceID, accountID string) (PaperBalanceSnapshot, error) {
	result := PaperBalanceSnapshot{Totals: map[string]shared.Decimal{}, Reserved: map[string]shared.Decimal{}}
	var marker struct {
		InitializedAt int64 `gorm:"column:c_initialized_at"`
	}
	err := tx.db.Table("t_paper_balance_projections").Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, accountID).Take(&marker).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// reset data 清掉了余额但保留账户，按配置的初始资金重新开始。
		var initial string
		if rawErr := tx.db.Raw(queryPaperInitialBalance, spaceID, accountID).Scan(&initial).Error; rawErr != nil {
			return result, rawErr
		}
		if initial == "" {
			return result, err
		}
		if err = tx.initializePaperBalance(spaceID, accountID, initial); err != nil {
			return result, err
		}
		return tx.GetPaperBalanceSnapshot(spaceID, accountID)
	}
	if err != nil {
		return result, err
	}
	var balances []struct {
		Asset string `gorm:"column:c_asset"`
		Total string `gorm:"column:c_total"`
	}
	if err := tx.db.Table("t_paper_asset_balances").Where("c_space_id = ? AND c_trading_account_id = ?", spaceID, accountID).Find(&balances).Error; err != nil {
		return result, err
	}
	for _, balance := range balances {
		value, err := shared.ParseDecimal(balance.Total)
		if err != nil || balance.Asset == "" {
			return result, fmt.Errorf("%w: paper asset balance", ErrInvalidRecord)
		}
		result.Totals[balance.Asset] = value
	}
	var reservations []struct {
		Asset    string `gorm:"column:c_reserved_asset"`
		Quantity string `gorm:"column:c_remaining_reserved_quantity"`
	}
	if err := tx.db.Raw(queryPaperActiveReservations, spaceID, accountID, activeOrderStates).Scan(&reservations).Error; err != nil {
		return result, err
	}
	for _, reservation := range reservations {
		quantity, err := shared.ParseDecimal(reservation.Quantity)
		if err != nil || quantity.IsNegative() || (!quantity.IsZero() && reservation.Asset == "") {
			return result, fmt.Errorf("%w: paper reservation", ErrInvalidRecord)
		}
		if !quantity.IsZero() {
			result.Reserved[reservation.Asset] = result.Reserved[reservation.Asset].Add(quantity)
		}
	}
	return result, nil
}

func (tx *Tx) applyPaperFill(record FillRecord) error {
	identity, err := tx.accountIdentity(record.SpaceID, record.TradingAccountID)
	if err != nil {
		return err
	}
	if identity.ExecutionMode != "PAPER" {
		return nil
	}
	if err := tx.normalizePaperFillSettlement(&record); err != nil {
		return err
	}
	if err := canonicalizeFill(&record); err != nil {
		return err
	}
	price, err := shared.ParseDecimal(record.Price)
	if err != nil || price.IsNegative() || price.IsZero() {
		return fmt.Errorf("%w: paper fill price", ErrInvalidRecord)
	}
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil || quantity.IsNegative() || quantity.IsZero() {
		return fmt.Errorf("%w: paper fill quantity", ErrInvalidRecord)
	}
	fee, err := shared.ParseDecimal(record.Fee)
	if err != nil {
		return fmt.Errorf("%w: paper fill fee", ErrInvalidRecord)
	}
	pnl, err := shared.ParseDecimal(record.RealizedPnL)
	if err != nil {
		return fmt.Errorf("%w: paper fill realized PnL", ErrInvalidRecord)
	}
	deltas := map[string]shared.Decimal{}
	switch rule := exchange.RuleFor(record.MarketType); {
	case !exchange.MarketType(record.MarketType).Valid():
		return fmt.Errorf("%w: paper fill market", ErrInvalidRecord)
	case rule.PositionFromBalance:
		instrument, err := tx.GetInstrumentInEnvironment(identity.Exchange, identity.Environment, record.MarketType, record.ExchangeSymbol)
		if err != nil {
			return err
		}
		if instrument.BaseAsset == "" || instrument.QuoteAsset == "" || instrument.BaseAsset == instrument.QuoteAsset {
			return fmt.Errorf("%w: paper spot asset identity", ErrInvalidRecord)
		}
		switch record.Side {
		case "BUY":
			deltas[instrument.BaseAsset] = quantity
			deltas[instrument.QuoteAsset] = quantity.Mul(price).Neg()
		case "SELL":
			deltas[instrument.BaseAsset] = quantity.Neg()
			deltas[instrument.QuoteAsset] = quantity.Mul(price)
		default:
			return fmt.Errorf("%w: paper fill side", ErrInvalidRecord)
		}
	default:
		if record.SettlementAsset == "" {
			return fmt.Errorf("%w: paper settlement asset", ErrInvalidRecord)
		}
		deltas[record.SettlementAsset] = pnl
	}
	if !fee.IsZero() {
		asset := record.FeeAsset
		if asset == "" {
			return fmt.Errorf("%w: paper fee asset", ErrInvalidRecord)
		}
		deltas[asset] = deltas[asset].Sub(fee)
	}
	for asset, delta := range deltas {
		if err := tx.addPaperBalance(record.SpaceID, record.TradingAccountID, asset, delta); err != nil {
			return err
		}
	}
	return nil
}

func (tx *Tx) normalizePaperFillSettlement(record *FillRecord) error {
	if exchange.RuleFor(record.MarketType).PositionFromBalance {
		return nil
	}
	var settlement string
	if err := tx.db.Raw(queryAccountSettlementAsset, record.SpaceID, record.TradingAccountID).Scan(&settlement).Error; err != nil {
		return err
	}
	if settlement == "" {
		return fmt.Errorf("%w: paper account settlement asset", ErrInvalidRecord)
	}
	return validateOrDeriveIdentity(&record.SettlementAsset, settlement, "paper fill settlement asset")
}

// PaperT1Bought 返回 since 之后买入、按 T+1 当天还不能卖的数量（资产 -> 数量），只算有 T+1 规则的市场（股票）。
func (s *Store) PaperT1Bought(ctx context.Context, spaceID, accountID string, since int64) (map[string]shared.Decimal, error) {
	var result map[string]shared.Decimal
	err := s.Transaction(ctx, func(tx *Tx) error {
		var err error
		result, err = tx.PaperT1Bought(spaceID, accountID, since)
		return err
	})
	return result, err
}

// PaperT1Bought 是 Store.PaperT1Bought 的事务内版本：下单资金检查在写事务里读余额，T+1 也要在同一个事务里扣。
func (tx *Tx) PaperT1Bought(spaceID, accountID string, since int64) (map[string]shared.Decimal, error) {
	result := make(map[string]shared.Decimal)
	markets := exchange.MarketsWhere(func(rule exchange.MarketRule) bool { return rule.T1 })
	if len(markets) == 0 {
		return result, nil
	}
	identity, err := tx.accountIdentity(spaceID, accountID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(markets))
	for i, market := range markets {
		names[i] = string(market)
	}
	var rows []struct {
		BaseAsset string `gorm:"column:c_base_asset"`
		Quantity  string `gorm:"column:c_quantity"`
	}
	if err := tx.db.Raw(queryPaperT1Buys, identity.Exchange, identity.Environment, spaceID, accountID, since, names).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		quantity, err := shared.ParseDecimal(row.Quantity)
		if err != nil || row.BaseAsset == "" {
			return nil, fmt.Errorf("%w: paper fill quantity", ErrInvalidRecord)
		}
		result[row.BaseAsset] = result[row.BaseAsset].Add(quantity)
	}
	return result, nil
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	execInsertPaperBalanceProjection = `INSERT INTO t_paper_balance_projections (c_space_id, c_trading_account_id, c_initialized_at)
		VALUES (?, ?, ?)`
	execInsertPaperAssetBalance = `INSERT INTO t_paper_asset_balances (c_space_id, c_trading_account_id, c_asset, c_total)
		VALUES (?, ?, ?, ?)`
	queryPaperInitialBalance = `SELECT c_initial_balance FROM t_paper_account_configs
		WHERE c_space_id = ? AND c_trading_account_id = ?`
	// 状态列表由调用方传入 activeOrderStates。
	queryPaperActiveReservations = `SELECT c_reserved_asset, c_remaining_reserved_quantity FROM t_trade_orders
		WHERE c_space_id = ? AND c_trading_account_id = ? AND c_state IN ?`
	queryAccountSettlementAsset = `SELECT c_settlement_asset FROM t_trading_accounts
		WHERE c_space_id = ? AND c_trading_account_id = ?`
	// 一次读出一个模拟账户在某个时刻之后、在给定市场上的买入成交和它们的基础资产（T+1 只看当日买入），
	// 不在写事务里逐笔查标的。
	queryPaperT1Buys = `SELECT i.c_base_asset AS c_base_asset, f.c_quantity AS c_quantity
		FROM t_order_fills f
		JOIN t_trade_instruments i ON i.c_exchange = ? AND i.c_environment = ? AND i.c_market_type = f.c_market_type
			AND i.c_exchange_symbol = f.c_exchange_symbol
		WHERE f.c_space_id = ? AND f.c_trading_account_id = ? AND f.c_side = 'BUY' AND f.c_traded_at >= ? AND f.c_market_type IN ?`
)
