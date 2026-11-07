package store

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
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
// 有成交的策略或人工订单涉及的标的，再减去组合资金设置里的排除资产
// （BNB 保留、稳定币保证金、保证金币、底仓）。其他资产不估值，也不卖出。
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
	var settings fundsettings.Settings
	if err == nil {
		// 资金设置坏了不能挡住清仓、收敛：告警后按默认设置继续。
		var settingsErr error
		if settings, settingsErr = fundsettings.ParseOrDefault(logical.FundSettingsJSON); settingsErr != nil {
			log.Warnf("交易关注项：组合 %s 的资金设置无法解析，按默认设置处理：%v", logical.LogicalAccountID, settingsErr)
		}
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
		// 只有持仓来自余额的市场（现货）的标的会让基础资产变成受管资产：
		// 合约标的的持仓在持仓表里，不能让 BTC 合约变成“受管的 BTC 现货”。
		if !exchange.RuleFor(instrument.MarketType).PositionFromBalance {
			continue
		}
		if _, ok := managed[instrument.InstrumentID]; ok && !settings.ExcludedFromManaged(instrument.BaseAsset) {
			assets[instrument.BaseAsset] = struct{}{}
		}
	}
	return assets, nil
}
