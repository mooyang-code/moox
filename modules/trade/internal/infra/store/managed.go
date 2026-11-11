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
	err := s.db.WithContext(ctx).Raw(queryManagedInstruments, spaceID, tradingAccountID).Scan(&instruments).Error
	return instruments, err
}

// queryManagedInstruments 的条件与部分覆盖索引 idx_trade_orders_managed_instruments 的 WHERE 逐项一致（含 IN 列表的次序），
// 规划器才会用它：只读索引里有成交的策略、人工订单，不扫账户全部订单，也不回表。
const queryManagedInstruments = `SELECT DISTINCT c_instrument_id
	FROM t_trade_orders
	WHERE c_space_id = ? AND c_trading_account_id = ?
		AND c_owner_type IN ('TARGET', 'OPERATOR') AND c_filled_quantity != '0' AND c_instrument_id != ''`

// ManagedAssets 返回持仓来自余额的账户里归 MooX 管理的资产：结算资产、当前目标里的标的、
// 有成交的策略或人工订单涉及的标的，再减去组合资金设置里的排除资产
// （BNB 保留、稳定币保证金、保证金币、底仓）。其他资产不估值，也不卖出。
func (s *Store) ManagedAssets(ctx context.Context, spaceID, tradingAccountID string) (map[string]struct{}, error) {
	account, err := s.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return nil, err
	}
	// 没开通现货类市场的账户（纯永续）只有结算资产受管：合约标的不会让基础资产受管，不必扫订单历史。
	if !account.HasBalanceMarket() {
		return map[string]struct{}{account.SettlementAsset: {}}, nil
	}
	instrumentIDs, err := s.ListManagedInstruments(ctx, spaceID, tradingAccountID)
	if err != nil {
		return nil, err
	}
	managed := make(map[string]struct{}, len(instrumentIDs))
	for _, instrumentID := range instrumentIDs {
		managed[instrumentID] = struct{}{}
	}
	// 设置按账户所属组合读（含停用成员，和清仓同一个入口）：数值字段坏了按默认处理，排除字段坏了报错、不下单。
	settings, settingsErr := s.FundSettingsForAccount(ctx, spaceID, tradingAccountID)
	if settingsErr != nil {
		return nil, settingsErr
	}
	// 目标里的标的只在成员启用时并入受管资产。
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

// ErrFundSettingsUnreadable 表示资金设置里排除字段读不出来，无法确定排除哪些资产：这个组合不能下单。
var ErrFundSettingsUnreadable = errors.New("资金设置的排除字段无法解析")

// ResolveFundSettings 是执行器、受管资产、清仓共用的资金设置读取入口：逐字段宽松解析，
// 数值字段坏了取默认值并按组合限频告警（一小时一次）；排除字段坏了返回 ErrFundSettingsUnreadable，
// 调用方不能下单，同样按组合限频打交易关注项。
func (s *Store) ResolveFundSettings(logical LogicalAccountRecord) (fundsettings.Settings, error) {
	settings, warning, err := fundsettings.ParseLenient(logical.FundSettingsJSON)
	if err != nil {
		if s.shouldWarnFundSettings(logical.LogicalAccountID) {
			log.Warnf("交易关注项：组合 %s 的资金设置排除字段无法解析，该组合暂停下单：%v", logical.LogicalAccountID, err)
		}
		return fundsettings.Settings{}, fmt.Errorf("%w: 组合 %s：%v", ErrFundSettingsUnreadable, logical.LogicalAccountID, err)
	}
	if warning != nil && s.shouldWarnFundSettings(logical.LogicalAccountID) {
		log.Warnf("交易关注项：组合 %s 的资金设置有字段无法解析，已按默认值处理：%v", logical.LogicalAccountID, warning)
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
