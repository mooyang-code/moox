package store

import (
	"context"
	"errors"
	"fmt"
	"time"

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
		// 数值字段坏了按默认处理（告警限频），排除字段坏了直接报错、不下单。
		if settings, err = s.ResolveFundSettings(logical); err != nil {
			return nil, err
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

// ResolveFundSettings 是执行器、受管资产、清仓共用的资金设置读取入口：逐字段宽松解析，
// 数值字段坏了取默认值并按组合限频告警（一小时一次）；排除字段坏了返回错误，调用方不能下单。
func (s *Store) ResolveFundSettings(logical LogicalAccountRecord) (fundsettings.Settings, error) {
	settings, warning, err := fundsettings.ParseLenient(logical.FundSettingsJSON)
	if err != nil {
		return fundsettings.Settings{}, fmt.Errorf("%w: 组合 %s：%v", ErrInvalidRecord, logical.LogicalAccountID, err)
	}
	if warning != nil && s.shouldWarnFundSettings(logical.LogicalAccountID) {
		log.Warnf("交易关注项：组合 %s 的资金设置有字段无法解析，已按默认值（BNB 保留按开启）处理：%v", logical.LogicalAccountID, warning)
	}
	return settings, nil
}

// FundSettingsForAccount 取执行账户所属组合的资金设置：只有账户不在任何组合里（没有记录）才取默认值，
// 其他读错误（数据库忙、超时）原样返回，不能悄悄按默认设置继续。
func (s *Store) FundSettingsForAccount(ctx context.Context, spaceID, tradingAccountID string) (fundsettings.Settings, error) {
	logical, _, err := s.FindLogicalAccountMembership(ctx, spaceID, tradingAccountID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings, _, _ := fundsettings.ParseLenient("")
		return settings, nil
	}
	if err != nil {
		return fundsettings.Settings{}, err
	}
	return s.ResolveFundSettings(logical)
}

func (s *Store) shouldWarnFundSettings(logicalAccountID string) bool {
	s.fundWarnMu.Lock()
	defer s.fundWarnMu.Unlock()
	if s.fundWarned == nil {
		s.fundWarned = make(map[string]time.Time)
	}
	if last, ok := s.fundWarned[logicalAccountID]; ok && time.Since(last) < time.Hour {
		return false
	}
	s.fundWarned[logicalAccountID] = time.Now()
	return true
}
