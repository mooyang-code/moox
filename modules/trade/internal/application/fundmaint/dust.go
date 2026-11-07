package fundmaint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// convertDust 每隔 D 天把账户里 MooX 交易过留下的小额零头兑成 BNB。只兑同时满足下面几条的资产：
//   - 是受管资产（MooX 的策略或人工单成交过的币）：用户自己放在账户里、MooX 从没碰过的币不动，
//     和“用户自持的币不受管、不会被卖”一致——这是对计划“非目标、非排除”的有意收窄；
//   - 不在当前目标里，也不是排除资产（BNB 保留、稳定币保证金、保证金币、套利底仓）；
//   - 价值低于 5 个结算币。
//
// 上次兑换时间记在执行账户的 c_last_dust_convert_at 列里（不是检测结果）；检查过但没有要兑的零头也算一次。
func (s *Service) convertDust(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
) error {
	converter, ok := adapter.(execution.DustConverter)
	if !ok {
		return nil
	}
	now := s.now()
	if account.LastDustConvertAt > 0 && now.Sub(time.UnixMilli(account.LastDustConvertAt)) < settings.DustConvertInterval {
		return nil
	}
	s.mu.Lock()
	retryAt := s.dustRetryAt[account.TradingAccountID]
	s.mu.Unlock()
	if now.Before(retryAt) {
		return nil
	}
	// 失败了就退避：第一次把错误报出去，之后 6 小时内不再调交易所。
	err := s.convertDustNow(ctx, logical, settings, account, adapter, converter, now)
	if err != nil {
		s.mu.Lock()
		if s.dustRetryAt == nil {
			s.dustRetryAt = make(map[string]time.Time)
		}
		s.dustRetryAt[account.TradingAccountID] = now.Add(dustRetryBackoff)
		s.mu.Unlock()
	}
	return err
}

func (s *Service) convertDustNow(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
	converter execution.DustConverter,
	now time.Time,
) error {
	dust, err := converter.ListDust(ctx)
	if err != nil {
		return err
	}
	eligible, err := s.convertibleAssets(ctx, logical, settings, account)
	if err != nil {
		return err
	}
	var assets []string
	if len(dust) > 0 {
		btc, found, err := s.spotInstrument(ctx, account, "BTC")
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("账户 %s 上没有 BTC/%s 现货标的，无法折算零头价值", account.TradingAccountID, account.SettlementAsset)
		}
		btcPrice, _, err := s.referencePrice(ctx, adapter, btc)
		if err != nil {
			return err
		}
		for _, item := range dust {
			asset := strings.ToUpper(item.Asset)
			if asset == fundsettings.BNBAsset || strings.EqualFold(asset, account.SettlementAsset) {
				continue
			}
			if _, isEligible := eligible[asset]; !isEligible {
				continue
			}
			if item.ValueBTC.Mul(btcPrice).Cmp(dustValueLimit) >= 0 {
				continue
			}
			assets = append(assets, item.Asset)
		}
	}
	if len(assets) > 0 {
		result, err := converter.ConvertDust(ctx, assets)
		if err != nil {
			return err
		}
		log.Infof("执行账户 %s 小额资产兑换 BNB：%d 种资产，换到 %s BNB", account.TradingAccountID, len(result.Converted), result.BNBReceived.String())
	}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.SetLastDustConvertAt(account.SpaceID, account.TradingAccountID, now.UnixMilli())
	})
}

// convertibleAssets 返回可以兑换的资产：受管资产（ManagedAssets 已经减掉了排除资产）去掉当前目标里的基础资产、
// 结算币和 BNB 自己。
func (s *Service) convertibleAssets(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
) (map[string]struct{}, error) {
	managed, err := s.Store.ManagedAssets(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	eligible := make(map[string]struct{}, len(managed))
	for asset := range managed {
		if !settings.ExcludedFromManaged(asset) {
			eligible[strings.ToUpper(asset)] = struct{}{}
		}
	}
	delete(eligible, strings.ToUpper(account.SettlementAsset))
	delete(eligible, fundsettings.BNBAsset)
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	base := make(map[string]string, len(instruments))
	for _, instrument := range instruments {
		base[instrument.InstrumentID] = strings.ToUpper(instrument.BaseAsset)
	}
	target, err := s.Store.GetLogicalAccountTarget(ctx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && !errors.Is(err, store.ErrTargetExpired) {
		return nil, err
	}
	for _, item := range target.Targets {
		if asset, known := base[item.InstrumentID]; known {
			delete(eligible, asset)
		}
	}
	return eligible, nil
}
