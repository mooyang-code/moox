package rpc

import (
	"context"
	"fmt"

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
	command, info := h.manualOrderCommand(ctx, spaceID, req, req.LimitPrice != nil)
	if info != nil {
		return &tradepb.SubmitOrderRsp{RetInfo: info}, nil
	}
	if h.SubmitOrdinary == nil {
		err = fmt.Errorf("普通下单服务未配置")
	}
	var action store.OperatorActionRecord
	var order store.OrderRecord
	if err == nil {
		action, order, err = h.SubmitOrdinary(ctx, SubmitOrderCommand{
			LogicalAccountID:   req.GetLogicalAccountId(),
			ManualOrderCommand: command,
		})
	}
	return &tradepb.SubmitOrderRsp{RetInfo: errorInfo(err), Action: operatorActionToPB(action), Order: orderToPB(order)}, nil
}
