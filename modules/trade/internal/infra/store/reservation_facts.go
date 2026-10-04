package store

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/reservation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

func (tx *Tx) LoadReservationFacts(account TradingAccountRecord, instrument InstrumentRecord, spec order.OrderSpec) (reservation.Facts, error) {
	facts := reservation.Facts{AvailableByAsset: map[string]shared.Decimal{}, AvailableFunds: shared.Zero(), SignedPositionQuantity: shared.Zero(), AvailableReducibleQuantity: shared.Zero(), Leverage: shared.Zero()}
	if account.ExecutionMode == "PAPER" {
		if account.PaperConfig == nil {
			account.PaperConfig = &PaperAccountConfigRecord{InitialBalance: "100000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBPS: "0"}
		}
		initial, err := shared.ParseDecimal(account.PaperConfig.InitialBalance)
		if err != nil {
			return facts, err
		}
		facts.AvailableByAsset[account.SettlementAsset] = initial
	} else {
		var snapshot TradingAccountSnapshot
		snapshot = account.Snapshot
		for _, b := range snapshot.Balances {
			value, err := shared.ParseDecimal(b.Available)
			if err != nil {
				return facts, fmt.Errorf("%w: invalid available balance", ErrInvalidRecord)
			}
			facts.AvailableByAsset[b.Asset] = value
		}
		if snapshot.AvailableFunds != "" {
			value, err := shared.ParseDecimal(snapshot.AvailableFunds)
			if err != nil {
				return facts, fmt.Errorf("%w: invalid available funds", ErrInvalidRecord)
			}
			facts.AvailableFunds = value
		}
	}
	if account.MarketType == "SWAP" {
		var row struct {
			Quantity string `gorm:"column:c_signed_quantity"`
		}
		if err := tx.db.Raw("SELECT c_signed_quantity FROM t_trading_positions WHERE c_space_id=? AND c_trading_account_id=? AND c_exchange_symbol=? AND c_position_side='NET'", account.SpaceID, account.TradingAccountID, instrument.ExchangeSymbol).Scan(&row).Error; err != nil {
			return facts, err
		}
		if row.Quantity != "" {
			facts.SignedPositionQuantity, _ = shared.ParseDecimal(row.Quantity)
		}
		facts.AvailableReducibleQuantity = facts.SignedPositionQuantity.Abs()
		var rows []struct {
			Quantity   string `gorm:"column:c_quantity"`
			Filled     string `gorm:"column:c_filled_quantity"`
			Side       string `gorm:"column:c_side"`
			ReduceOnly bool   `gorm:"column:c_reduce_only"`
			State      string `gorm:"column:c_state"`
		}
		if err := tx.db.Raw("SELECT c_quantity,c_filled_quantity,c_side,c_reduce_only,c_state FROM t_trade_orders WHERE c_space_id=? AND c_trading_account_id=? AND c_exchange_symbol=? AND c_reduce_only=1 AND c_state NOT IN ('FILLED','CANCELED','PARTIALLY_CANCELED','REJECTED','EXPIRED')", account.SpaceID, account.TradingAccountID, instrument.ExchangeSymbol).Scan(&rows).Error; err != nil {
			return facts, err
		}
		for _, row := range rows {
			// Only reduce-only orders that close the current signed position
			// consume capacity; an opposite-side reduce-only order is not a
			// valid reservation for this position.
			if (facts.SignedPositionQuantity.Cmp(shared.Zero()) > 0 && row.Side != "SELL") ||
				(facts.SignedPositionQuantity.Cmp(shared.Zero()) < 0 && row.Side != "BUY") {
				continue
			}
			q, e1 := shared.ParseDecimal(row.Quantity)
			f, e2 := shared.ParseDecimal(row.Filled)
			if e1 != nil || e2 != nil {
				return facts, fmt.Errorf("%w: reduce-only facts", ErrInvalidRecord)
			}
			facts.AvailableReducibleQuantity = facts.AvailableReducibleQuantity.Sub(q.Sub(f))
		}
		if facts.AvailableReducibleQuantity.IsNegative() {
			facts.AvailableReducibleQuantity = shared.Zero()
		}
	}
	var active []struct {
		Asset    string `gorm:"column:c_reserved_asset"`
		Quantity string `gorm:"column:c_remaining_reserved_quantity"`
	}
	if err := tx.db.Raw("SELECT c_reserved_asset,c_remaining_reserved_quantity FROM t_trade_orders WHERE c_space_id=? AND c_trading_account_id=? AND c_state NOT IN ('FILLED','CANCELED','PARTIALLY_CANCELED','REJECTED','EXPIRED') AND c_remaining_reserved_quantity <> '0'", account.SpaceID, account.TradingAccountID).Scan(&active).Error; err != nil {
		return facts, err
	}
	for _, row := range active {
		q, err := shared.ParseDecimal(row.Quantity)
		if err != nil {
			return facts, err
		}
		facts.AvailableByAsset[row.Asset] = facts.AvailableByAsset[row.Asset].Sub(q)
		if row.Asset == account.SettlementAsset {
			facts.AvailableFunds = facts.AvailableFunds.Sub(q)
		}
	}
	return facts, nil
}
