package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// ListManagedInstruments 返回账户上有成交的策略或人工订单涉及的标的。
// 这些是 MooX 买过的标的，目标不再包含时要能卖掉。
func (s *Store) ListManagedInstruments(ctx context.Context, spaceID, tradingAccountID string) ([]string, error) {
	var instruments []string
	err := s.db.WithContext(ctx).Table("t_trade_orders").
		Distinct("c_instrument_id").
		Where(`c_space_id = ? AND c_trading_account_id = ? AND c_owner_type IN ('TARGET', 'OPERATOR')
			AND c_filled_quantity != '0' AND c_instrument_id != ''`, spaceID, tradingAccountID).
		Pluck("c_instrument_id", &instruments).Error
	return instruments, err
}

// ManagedAssets 返回持仓来自余额的账户里归 MooX 管理的资产：结算资产、当前目标里的标的、
// 有成交的策略或人工订单涉及的标的。其他资产不估值，也不卖出。
func (s *Store) ManagedAssets(ctx context.Context, spaceID, tradingAccountID string) (map[string]struct{}, error) {
	account, err := s.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return nil, err
	}
	instrumentIDs, err := s.ListManagedInstruments(ctx, spaceID, tradingAccountID)
	if err != nil {
		return nil, err
	}
	managed := make(map[string]struct{}, len(instrumentIDs))
	for _, instrumentID := range instrumentIDs {
		managed[instrumentID] = struct{}{}
	}
	logical, _, err := s.FindLogicalAccountByTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil {
		target, targetErr := s.GetLogicalAccountTarget(ctx, spaceID, logical.LogicalAccountID)
		if targetErr != nil && !errors.Is(targetErr, gorm.ErrRecordNotFound) {
			return nil, targetErr
		}
		for _, current := range target.Targets {
			managed[current.InstrumentID] = struct{}{}
		}
	}
	instruments, err := s.ListInstrumentsForAccount(ctx, tradingAccountID)
	if err != nil {
		return nil, err
	}
	assets := map[string]struct{}{account.SettlementAsset: {}}
	for _, instrument := range instruments {
		if _, ok := managed[instrument.InstrumentID]; ok {
			assets[instrument.BaseAsset] = struct{}{}
		}
	}
	return assets, nil
}
