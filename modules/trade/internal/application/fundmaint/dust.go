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

// convertDust 每隔 D 天把账户里的小额零头兑成 BNB。只兑同时满足下面三条的资产：
//   - 不在当前目标里（也不是 MooX 交易过的受管资产）；
//   - 不是排除资产（BNB 保留、稳定币保证金、保证金币、套利底仓）；
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
	dust, err := converter.ListDust(ctx)
	if err != nil {
		return err
	}
	protected, err := s.protectedAssets(ctx, logical, settings, account)
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
			if _, isProtected := protected[asset]; isProtected {
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

// protectedAssets 返回不能兑换的资产：当前目标里的基础资产、受管资产和排除资产（排除资产用 settings 判断）。
func (s *Service) protectedAssets(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
) (map[string]struct{}, error) {
	protected := make(map[string]struct{})
	managed, err := s.Store.ManagedAssets(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	for asset := range managed {
		protected[strings.ToUpper(asset)] = struct{}{}
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	base := make(map[string]string, len(instruments))
	for _, instrument := range instruments {
		base[instrument.InstrumentID] = strings.ToUpper(instrument.BaseAsset)
		if settings.ExcludedFromManaged(instrument.BaseAsset) {
			protected[strings.ToUpper(instrument.BaseAsset)] = struct{}{}
		}
	}
	target, err := s.Store.GetLogicalAccountTarget(ctx, logical.SpaceID, logical.LogicalAccountID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) && !errors.Is(err, store.ErrTargetExpired) {
		return nil, err
	}
	for _, item := range target.Targets {
		if asset, known := base[item.InstrumentID]; known {
			protected[asset] = struct{}{}
		}
	}
	for _, asset := range append(append([]string(nil), settings.StablecoinMargin...), settings.ArbitrageBaseAssets...) {
		protected[strings.ToUpper(asset)] = struct{}{}
	}
	for asset := range settings.MarginCoins {
		protected[strings.ToUpper(asset)] = struct{}{}
	}
	return protected, nil
}
