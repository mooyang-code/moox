package rpc

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/wallet"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// ResolveWalletTransfer 人工结束一条超过 10 分钟还没对账出结果的钱包划转记录；已经结束的记录原样返回。
func (h *AccountServer) ResolveWalletTransfer(
	ctx context.Context,
	req *tradepb.ResolveWalletTransferReq,
) (*tradepb.ResolveWalletTransferRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ResolveWalletTransferRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	result, err := wallet.Resolve(ctx, h.Store, spaceID, req.GetTransferId(), req.GetResult(), req.GetNote(), time.Now())
	if err != nil {
		return &tradepb.ResolveWalletTransferRsp{RetInfo: errorInfo(err)}, nil
	}
	return &tradepb.ResolveWalletTransferRsp{RetInfo: success(), State: result.State, AlreadySettled: result.AlreadySettled}, nil
}
