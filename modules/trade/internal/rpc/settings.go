package rpc

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ordersettings"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

func (h *LogicalAccountServer) UpdateLogicalAccountOrderSettings(
	ctx context.Context,
	req *tradepb.UpdateLogicalAccountOrderSettingsReq,
) (*tradepb.UpdateLogicalAccountOrderSettingsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.UpdateLogicalAccountOrderSettingsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	record, err := h.LogicalAccounts.UpdateOrderSettings(ctx, spaceID, req.GetLogicalAccountId(), orderSettingsFromPB(req.GetSettings()))
	return &tradepb.UpdateLogicalAccountOrderSettingsRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) UpdateLogicalAccountFundSettings(
	ctx context.Context,
	req *tradepb.UpdateLogicalAccountFundSettingsReq,
) (*tradepb.UpdateLogicalAccountFundSettingsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.UpdateLogicalAccountFundSettingsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	record, err := h.LogicalAccounts.UpdateFundSettings(ctx, spaceID, req.GetLogicalAccountId(), fundSettingsFromPB(req.GetSettings()))
	return &tradepb.UpdateLogicalAccountFundSettingsRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func orderSettingsFromPB(value *tradepb.OrderSettings) ordersettings.Document {
	return ordersettings.Document{
		RebalanceThresholdMode:  value.GetRebalanceThresholdMode(),
		RebalanceThresholdRatio: value.GetRebalanceThresholdRatio(),
		MinNotionalSpot:         value.GetMinNotionalSpot(),
		MinNotionalSwap:         value.GetMinNotionalSwap(),
		HourlyCapSpot:           value.GetHourlyCapSpot(),
		HourlyCapSwap:           value.GetHourlyCapSwap(),
		MaxChildNotional:        value.GetMaxChildNotional(),
		RandomizeSplit:          value.GetRandomizeSplit(),
		BatchIntervalSeconds:    int(value.GetBatchIntervalSeconds()),
		OrderMode:               value.GetOrderMode(),
		SlippageStep:            value.GetSlippageStep(),
		SlippageMax:             value.GetSlippageMax(),
		MaxAttempts:             int(value.GetMaxAttempts()),
		RestingRatio:            value.GetRestingRatio(),
		RestingTTLSeconds:       int(value.GetRestingTtlSeconds()),
	}
}

// orderSettingsToPB 把存储的设置转成当前生效的值（没写的项填默认值）；
// 存的内容读不出来时返回错误说明（界面据此提示并禁止保存，不能按默认值显示——用户一保存就把原设置覆盖了）。
func orderSettingsToPB(stored string) (*tradepb.OrderSettings, string) {
	settings, err := ordersettings.Parse(stored)
	if err != nil {
		return nil, err.Error()
	}
	value := settings.Document()
	return &tradepb.OrderSettings{
		RebalanceThresholdMode:  value.RebalanceThresholdMode,
		RebalanceThresholdRatio: value.RebalanceThresholdRatio,
		MinNotionalSpot:         value.MinNotionalSpot,
		MinNotionalSwap:         value.MinNotionalSwap,
		HourlyCapSpot:           value.HourlyCapSpot,
		HourlyCapSwap:           value.HourlyCapSwap,
		MaxChildNotional:        value.MaxChildNotional,
		RandomizeSplit:          value.RandomizeSplit,
		BatchIntervalSeconds:    int32(value.BatchIntervalSeconds),
		OrderMode:               value.OrderMode,
		SlippageStep:            value.SlippageStep,
		SlippageMax:             value.SlippageMax,
		MaxAttempts:             int32(value.MaxAttempts),
		RestingRatio:            value.RestingRatio,
		RestingTtlSeconds:       int32(value.RestingTTLSeconds),
	}, ""
}

func fundSettingsFromPB(value *tradepb.FundSettings) fundsettings.Document {
	return fundsettings.Document{
		ReserveRatio:            value.GetReserveRatio(),
		BNBDiscount:             value.GetBnbDiscount(),
		BNBReserveAmount:        value.GetBnbReserveAmount(),
		DustConvertEnabled:      value.GetDustConvertEnabled(),
		DustConvertIntervalDays: int(value.GetDustConvertIntervalDays()),
		StablecoinMargin:        value.GetStablecoinMargin(),
		MarginCoins:             value.GetMarginCoins(),
		ArbitrageBaseAssets:     value.GetArbitrageBaseAssets(),
		MarginRatioThreshold:    value.GetMarginRatioThreshold(),
		UnifiedMMRThreshold:     value.GetUnifiedMmrThreshold(),
		MarginReduceRatio:       value.GetMarginReduceRatio(),
		MarginMonitorDisabled:   value.GetMarginMonitorDisabled(),
	}
}

// fundSettingsToPB 用与执行器一致的宽松解析显示当前生效的值。
// 排除字段读不出来时返回错误说明、不返回设置；个别数值读不出来（宽松解析给出 warning）时设置照常返回，
// 但错误说明也要有：执行器按默认值处理（BNB 保留读不出来时按“已开启”处理），界面不能当没事，
// 否则用户一保存就把排除集合或 BNB 保留悄悄改掉。
func fundSettingsToPB(stored string) (*tradepb.FundSettings, string) {
	settings, warning, err := fundsettings.ParseLenient(stored)
	if err != nil {
		return nil, err.Error()
	}
	message := ""
	if warning != nil {
		message = warning.Error()
	}
	value := settings.Document()
	return &tradepb.FundSettings{
		ReserveRatio:            value.ReserveRatio,
		BnbDiscount:             value.BNBDiscount,
		BnbReserveAmount:        value.BNBReserveAmount,
		DustConvertEnabled:      value.DustConvertEnabled,
		DustConvertIntervalDays: int32(value.DustConvertIntervalDays),
		StablecoinMargin:        value.StablecoinMargin,
		MarginCoins:             value.MarginCoins,
		ArbitrageBaseAssets:     value.ArbitrageBaseAssets,
		MarginRatioThreshold:    value.MarginRatioThreshold,
		UnifiedMmrThreshold:     value.UnifiedMMRThreshold,
		MarginReduceRatio:       value.MarginReduceRatio,
		MarginMonitorDisabled:   value.MarginMonitorDisabled,
	}, message
}
