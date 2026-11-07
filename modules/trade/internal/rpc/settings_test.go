package rpc

import (
	"context"
	"testing"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func newSettingsHandler(t *testing.T) (*LogicalAccountServer, context.Context) {
	t.Helper()
	return newSettingsHandlerFor(t, tradepb.ExecutionMode_EXECUTION_MODE_PAPER)
}

func newSettingsHandlerFor(t *testing.T, mode tradepb.ExecutionMode) (*LogicalAccountServer, context.Context) {
	t.Helper()
	tradeStore := openRPCStore(t)
	handler := &LogicalAccountServer{
		LogicalAccounts: &logicalapp.Service{Store: tradeStore},
		Store:           tradeStore,
		NewID:           func() string { return "logical-1" },
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	created, err := handler.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name: "main", ExecutionMode: mode, SettlementAsset: "USDT",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode())
	// 新建的组合读到的就是默认设置。
	require.Equal(t, "TARGET_RATIO", created.GetLogicalAccount().GetOrderSettings().GetRebalanceThresholdMode())
	require.Equal(t, "PSEUDO_MARKET", created.GetLogicalAccount().GetOrderSettings().GetOrderMode())
	return handler, ctx
}

func TestUpdateOrderSettingsStoresAndReturnsEffectiveValues(t *testing.T) {
	handler, ctx := newSettingsHandler(t)
	response, err := handler.UpdateLogicalAccountOrderSettings(ctx, &tradepb.UpdateLogicalAccountOrderSettingsReq{
		LogicalAccountId: "logical-1",
		Settings: &tradepb.OrderSettings{
			RebalanceThresholdMode: "EQUITY_RATIO", RebalanceThresholdRatio: "0.02",
			HourlyCapSwap: map[string]string{"btc-usdt-swap": "5000"}, RestingRatio: "0.3", RestingTtlSeconds: 600,
		},
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode(), response.GetRetInfo().GetMsg())
	settings := response.GetLogicalAccount().GetOrderSettings()
	require.Equal(t, "EQUITY_RATIO", settings.GetRebalanceThresholdMode())
	require.Equal(t, "5000", settings.GetHourlyCapSwap()["BTC-USDT-SWAP"], "标的键存成大写")
	require.Equal(t, "0.3", settings.GetRestingRatio())
	require.Equal(t, "100000", settings.GetMaxChildNotional(), "没写的项显示默认值")
	got, err := handler.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{LogicalAccountId: "logical-1"})
	require.NoError(t, err)
	require.Equal(t, "EQUITY_RATIO", got.GetLogicalAccount().GetOrderSettings().GetRebalanceThresholdMode())
}

func TestUpdateOrderSettingsRejectsInvalidValues(t *testing.T) {
	handler, ctx := newSettingsHandler(t)
	for name, settings := range map[string]*tradepb.OrderSettings{
		"挂单比例大于 1": {RestingRatio: "1.5"},
		"未知阈值模式":   {RebalanceThresholdMode: "SOMETIMES"},
		"空设置里的坏数字": {MinNotionalSpot: "abc"},
		"重复标的键":    {HourlyCapSpot: map[string]string{"btc-usdt-spot": "1", "BTC-USDT-SPOT": "2"}},
	} {
		response, err := handler.UpdateLogicalAccountOrderSettings(ctx, &tradepb.UpdateLogicalAccountOrderSettingsReq{
			LogicalAccountId: "logical-1", Settings: settings,
		})
		require.NoError(t, err, name)
		require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, response.GetRetInfo().GetCode(), name)
	}
	// 被拒绝的写入不改变已保存的设置。
	got, err := handler.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{LogicalAccountId: "logical-1"})
	require.NoError(t, err)
	require.Equal(t, "0", got.GetLogicalAccount().GetOrderSettings().GetRestingRatio())
}

func TestUpdateFundSettingsValidatesStablecoinMargin(t *testing.T) {
	handler, ctx := newSettingsHandlerFor(t, tradepb.ExecutionMode_EXECUTION_MODE_LIVE)
	update := func(settings *tradepb.FundSettings) *tradepb.UpdateLogicalAccountFundSettingsRsp {
		response, err := handler.UpdateLogicalAccountFundSettings(ctx, &tradepb.UpdateLogicalAccountFundSettingsReq{
			LogicalAccountId: "logical-1", Settings: settings,
		})
		require.NoError(t, err)
		return response
	}
	for name, settings := range map[string]*tradepb.FundSettings{
		"比例和不为 1":    {StablecoinMargin: map[string]string{"USDT": "0.5", "USDC": "0.4"}},
		"不含 USDT":    {StablecoinMargin: map[string]string{"USDC": "0.5", "FDUSD": "0.5"}},
		"预留比例大于等于 1": {ReserveRatio: "1"},
		"资产重复":       {MarginCoins: map[string]string{"ETH": "1"}, ArbitrageBaseAssets: []string{"eth"}},
	} {
		require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, update(settings).GetRetInfo().GetCode(), name)
	}
	response := update(&tradepb.FundSettings{
		ReserveRatio: "0.05", BnbDiscount: true, BnbReserveAmount: "200", DustConvertEnabled: true,
		StablecoinMargin: map[string]string{"usdt": "0.6", "usdc": "0.4"},
		MarginCoins:      map[string]string{"eth": "100"}, ArbitrageBaseAssets: []string{"btc"},
	})
	require.Equal(t, tradepb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode(), response.GetRetInfo().GetMsg())
	fund := response.GetLogicalAccount().GetFundSettings()
	require.True(t, fund.GetBnbDiscount())
	require.Equal(t, "0.6", fund.GetStablecoinMargin()["USDT"])
	require.Equal(t, "100", fund.GetMarginCoins()["ETH"])
	require.Equal(t, []string{"BTC"}, fund.GetArbitrageBaseAssets())
	require.Equal(t, int32(15), fund.GetDustConvertIntervalDays(), "没写的间隔显示默认 15 天")
}

func TestSettingsRPCRequiresSpaceAndSettings(t *testing.T) {
	handler := &LogicalAccountServer{}
	response, err := handler.UpdateLogicalAccountOrderSettings(context.Background(), &tradepb.UpdateLogicalAccountOrderSettingsReq{LogicalAccountId: "x", Settings: &tradepb.OrderSettings{}})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_NO_PERMISSION, response.GetRetInfo().GetCode())
	response2, err := handler.UpdateLogicalAccountFundSettings(spacecontext.WithSpaceID(context.Background(), "space-1"), &tradepb.UpdateLogicalAccountFundSettingsReq{LogicalAccountId: "x"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, response2.GetRetInfo().GetCode())
}

// 模拟盘组合没有资金维护任务：BNB 抵扣、BNB 保留、小额兑换和稳定币保证金写不进去，其他项照常。
func TestPaperLogicalAccountRejectsLiveOnlyFundSettings(t *testing.T) {
	handler, ctx := newSettingsHandler(t)
	update := func(settings *tradepb.FundSettings) *tradepb.UpdateLogicalAccountFundSettingsRsp {
		response, err := handler.UpdateLogicalAccountFundSettings(ctx, &tradepb.UpdateLogicalAccountFundSettingsReq{LogicalAccountId: "logical-1", Settings: settings})
		require.NoError(t, err)
		return response
	}
	for name, settings := range map[string]*tradepb.FundSettings{
		"BNB 抵扣": {BnbDiscount: true},
		"BNB 保留": {BnbReserveAmount: "100"},
		"小额兑换":   {DustConvertEnabled: true},
		"稳定币保证金": {StablecoinMargin: map[string]string{"USDT": "0.5", "USDC": "0.5"}},
	} {
		require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, update(settings).GetRetInfo().GetCode(), name)
	}
	response := update(&tradepb.FundSettings{ReserveRatio: "0.05", MarginCoins: map[string]string{"ETH": "100"}, ArbitrageBaseAssets: []string{"BTC"}})
	require.Equal(t, tradepb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode(), response.GetRetInfo().GetMsg())
}

// 含非币安成员的组合不能开 BNB 抵扣、BNB 保留、小额兑换、稳定币保证金；保证金币不能写成组合的结算币。
func TestFundSettingsAreRestrictedToBinanceMembersAndSettlementAsset(t *testing.T) {
	handler, ctx := newSettingsHandlerFor(t, tradepb.ExecutionMode_EXECUTION_MODE_LIVE)
	require.NoError(t, handler.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "okx-1", Name: "okx", Exchange: "OKX", Markets: []string{"SPOT"},
			ExecutionMode: "LIVE", AccountMode: "OKX_UNIFIED", Environment: "PRODUCTION", CredentialSecretID: "secret-okx",
			SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "okx-1", Enabled: true, Priority: 1})
	}))
	update := func(settings *tradepb.FundSettings) tradepb.ErrorCode {
		response, err := handler.UpdateLogicalAccountFundSettings(ctx, &tradepb.UpdateLogicalAccountFundSettingsReq{LogicalAccountId: "logical-1", Settings: settings})
		require.NoError(t, err)
		return response.GetRetInfo().GetCode()
	}
	for name, settings := range map[string]*tradepb.FundSettings{
		"BNB 抵扣": {BnbDiscount: true},
		"BNB 保留": {BnbReserveAmount: "100"},
		"小额兑换":   {DustConvertEnabled: true},
		"稳定币保证金": {StablecoinMargin: map[string]string{"USDT": "0.5", "USDC": "0.5"}},
	} {
		require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, update(settings), "OKX 成员："+name)
	}
	require.Equal(t, tradepb.ErrorCode_SUCCESS, update(&tradepb.FundSettings{ReserveRatio: "0.05", MarginCoins: map[string]string{"ETH": "100"}}), "与交易所无关的项照常")
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, update(&tradepb.FundSettings{MarginCoins: map[string]string{"USDT": "100"}}), "结算币不能当保证金币")
}

// 存的设置读不出来时返回错误说明、不返回设置：界面不能按默认值显示（一保存就覆盖了原设置）。
// 只是不满足后来加的写入校验的旧设置，照常显示（和执行器一样宽松解析）。
func TestUnreadableStoredSettingsAreReportedNotShownAsDefaults(t *testing.T) {
	handler, ctx := newSettingsHandlerFor(t, tradepb.ExecutionMode_EXECUTION_MODE_LIVE)
	set := func(order, fund string) *tradepb.LogicalAccount {
		require.NoError(t, handler.Store.DBForTest().Exec(
			"UPDATE t_logical_accounts SET c_order_settings_json = ?, c_fund_settings_json = ?", order, fund).Error)
		got, err := handler.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{LogicalAccountId: "logical-1"})
		require.NoError(t, err)
		return got.GetLogicalAccount()
	}
	broken := set(`{"order_mode":"LIMIT"}`, `{"arbitrage_base_assets":"BTC"}`)
	require.Nil(t, broken.GetOrderSettings())
	require.Contains(t, broken.GetOrderSettingsError(), "下单方式")
	require.Nil(t, broken.GetFundSettings())
	require.NotEmpty(t, broken.GetFundSettingsError())

	old := set(`{}`, `{"stablecoin_margin":{"USDC":"0.5"},"bnb_reserve_amount":"5"}`)
	require.Empty(t, old.GetFundSettingsError(), "严格校验通不过但宽松解析读得出的旧设置照常显示")
	require.Equal(t, "5", old.GetFundSettings().GetBnbReserveAmount())
	require.Empty(t, old.GetOrderSettingsError())
}
