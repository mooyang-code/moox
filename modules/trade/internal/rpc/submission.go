package rpc

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

func (h *ExecutionServer) SubmitOrder(ctx context.Context, req *tradepb.SubmitOrderReq) (*tradepb.SubmitOrderRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.SubmitOrderRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	quantity, err := shared.ParseDecimal(req.GetQuantity())
	if err != nil {
		return &tradepb.SubmitOrderRsp{RetInfo: invalidInfo(err)}, nil
	}
	var limitPrice *shared.Decimal
	if req.LimitPrice != nil {
		value, parseErr := shared.ParseDecimal(req.GetLimitPrice())
		if parseErr != nil {
			return &tradepb.SubmitOrderRsp{RetInfo: invalidInfo(parseErr)}, nil
		}
		limitPrice = &value
	}
	instrumentID, err := h.requestInstrumentID(ctx, spaceID, req.GetTradingAccountId(), req.GetInstrumentId(), req.GetMarketType())
	if err != nil {
		return &tradepb.SubmitOrderRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	if h.SubmitOrdinary == nil {
		err = fmt.Errorf("普通下单服务未配置")
	}
	var action store.OperatorActionRecord
	var order store.OrderRecord
	if err == nil {
		action, order, err = h.SubmitOrdinary(ctx, SubmitOrderCommand{
			LogicalAccountID: req.GetLogicalAccountId(),
			ManualOrderCommand: ManualOrderCommand{
				SpaceID: spaceID, ActionID: req.GetActionId(), TradingAccountID: req.GetTradingAccountId(),
				ClientOrderID: req.GetClientOrderId(), InstrumentID: instrumentID,
				OrderType: orderTypeFromPB(req.GetOrderType()), FillPolicy: fillPolicyFromPB(req.GetFillPolicy()),
				Side: sideFromPB(req.GetSide()), PositionSide: positionSideFromPB(req.GetPositionSide()),
				Quantity: quantity, LimitPrice: limitPrice, Reason: req.GetReason(), DeadlineAt: req.GetDeadlineAt(),
			},
		})
	}
	return &tradepb.SubmitOrderRsp{RetInfo: errorInfo(err), Action: operatorActionToPB(action), Order: orderToPB(order)}, nil
}
