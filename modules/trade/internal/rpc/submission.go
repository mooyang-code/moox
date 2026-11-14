package rpc

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// SubmitOrder 是人工下单的唯一入口：策略驱动的组合先暂停再下单，自主下单的组合直接受理，由应用层按控制模式决定。
func (h *ExecutionServer) SubmitOrder(ctx context.Context, req *tradepb.SubmitOrderReq) (*tradepb.SubmitOrderRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.SubmitOrderRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	command, info := h.submitOrderCommand(ctx, spaceID, req)
	if info != nil {
		return &tradepb.SubmitOrderRsp{RetInfo: info}, nil
	}
	if h.Submit == nil {
		err = fmt.Errorf("人工下单服务未配置")
	}
	var action store.OperatorActionRecord
	var order store.OrderRecord
	if err == nil {
		action, order, err = h.Submit(ctx, command)
	}
	return &tradepb.SubmitOrderRsp{RetInfo: errorInfo(err), Action: operatorActionToPB(action), Order: orderToPB(order)}, nil
}
