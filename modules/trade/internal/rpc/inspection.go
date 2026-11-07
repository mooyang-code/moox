package rpc

import (
	"context"
	"errors"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// InspectionFollowUp 在对已有账户重新检测后处理结果：账户类型变了或出现阻断项时暂停所在组合并告警。
type InspectionFollowUp func(ctx context.Context, spaceID, tradingAccountID string, result accountapp.ReinspectResult) error

// InspectExchangeCredential 只读检测一把交易所密钥，不落库。密钥是全局资源，不要求空间。
func (h *AccountServer) InspectExchangeCredential(
	ctx context.Context,
	req *tradepb.InspectExchangeCredentialReq,
) (*tradepb.InspectExchangeCredentialRsp, error) {
	if err := validatePB(req); err != nil {
		return &tradepb.InspectExchangeCredentialRsp{RetInfo: invalidInfo(err)}, nil
	}
	inspection, err := h.Accounts.InspectCredential(
		ctx, exchangeFromPB(req.GetExchange()), environmentFromPB(req.GetEnvironment()),
		req.GetCredentialSecretId(), marketsFromPB(req.GetMarkets()),
	)
	if err != nil {
		return &tradepb.InspectExchangeCredentialRsp{RetInfo: errorInfo(err)}, nil
	}
	return &tradepb.InspectExchangeCredentialRsp{RetInfo: success(), Inspection: inspectionToPB(inspection)}, nil
}

// ReinspectTradingAccount 对已有账户重新检测，结果写回账户类型和检测结果。
func (h *AccountServer) ReinspectTradingAccount(
	ctx context.Context,
	req *tradepb.ReinspectTradingAccountReq,
) (*tradepb.ReinspectTradingAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ReinspectTradingAccountRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId()); err != nil {
		return &tradepb.ReinspectTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	result, err := h.Accounts.Reinspect(ctx, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ReinspectTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	if h.AfterInspection != nil {
		if followErr := h.AfterInspection(ctx, spaceID, req.GetTradingAccountId(), result); followErr != nil {
			return &tradepb.ReinspectTradingAccountRsp{RetInfo: errorInfo(followErr)}, nil
		}
	}
	record, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	rsp := &tradepb.ReinspectTradingAccountRsp{
		RetInfo: errorInfo(err), Inspection: inspectionToPB(result.Inspection),
		Account: accountToPB(record), ModeChanged: result.ModeChanged,
	}
	if result.Inconclusive {
		// 检测没有成功：保留了上次的结果，告诉调用方这次没有结论。
		rsp.RetInfo = errorInfo(errors.Join(accountapp.ErrInspectionUnavailable, result.Cause))
	}
	return rsp, nil
}

func inspectionToPB(value exchange.Inspection) *tradepb.ExchangeInspection {
	findings := func(items []exchange.InspectionFinding) []*tradepb.InspectionFinding {
		out := make([]*tradepb.InspectionFinding, 0, len(items))
		for _, item := range items {
			out = append(out, &tradepb.InspectionFinding{Code: item.Code, Message: item.Message})
		}
		return out
	}
	result := &tradepb.ExchangeInspection{
		AccountMode: string(value.AccountMode), Skipped: value.Skipped, CanWithdraw: value.CanWithdraw,
		CanTradeSpot: value.CanTradeSpot, CanTradeSwap: value.CanTradeSwap, UniversalTransfer: value.UniversalTransfer,
		IpRestricted: value.IPRestricted, RawPermissions: value.RawPermissions, OkxAcctLv: value.OKXAcctLv,
		PositionMode: string(value.PositionMode), MultiAssetsMargin: value.MultiAssetsMargin,
		AvailableMarkets: marketsToPB(value.AvailableMarkets),
		// 阻断项按检测时请求的市场算；没有请求市场（预览）时 Blockers 就是按全部可开通市场算的。
		Blockers: findings(value.Blockers), Warnings: findings(value.Warnings),
		ConsecutiveFailures: int32(value.ConsecutiveFailures), LastFailure: value.LastFailure,
	}
	if !value.InspectedAt.IsZero() {
		result.InspectedAt = value.InspectedAt.UnixMilli()
	}
	return result
}
