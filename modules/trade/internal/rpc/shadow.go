package rpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// defaultShadowTargets 是不填 limit 时列出的影子目标条数。
const defaultShadowTargets = 20

// ListShadowTargets 列出试运行中的策略实例最近的影子目标（批 4 T4）：管理台确认实盘前先看它们，moox-cli trade shadow list 同样读这里。
// 经只读视图读库，不和交易执行的写入排同一个连接。
func (s *ConsoleServer) ListShadowTargets(
	ctx context.Context,
	req *tradepb.ListShadowTargetsReq,
) (*tradepb.ListShadowTargetsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListShadowTargetsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.ListShadowTargetsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = defaultShadowTargets
	}
	records, err := s.Store.ReadOnly().ListShadowTargets(ctx, spaceID, req.GetInstanceId(), limit)
	if err != nil {
		return &tradepb.ListShadowTargetsRsp{RetInfo: errorInfo(err)}, nil
	}
	targets := make([]*tradepb.ShadowTarget, 0, len(records))
	for _, record := range records {
		target, err := shadowTargetToPB(record)
		if err != nil {
			return &tradepb.ListShadowTargetsRsp{RetInfo: errorInfo(err)}, nil
		}
		targets = append(targets, target)
	}
	return &tradepb.ListShadowTargetsRsp{RetInfo: success(), Targets: targets}, nil
}

// shadowTargetToPB 把一条影子目标记录转成响应：权重、参考价、换算出的数量在库里是 JSON，按写入时的形状解开。
func shadowTargetToPB(record store.TargetHistoryRecord) (*tradepb.ShadowTarget, error) {
	var weights []struct {
		InstrumentID string `json:"instrument_id"`
		TargetWeight string `json:"target_weight"`
		Hold         bool   `json:"hold"`
	}
	if err := json.Unmarshal([]byte(record.WeightsJSON), &weights); err != nil {
		return nil, fmt.Errorf("影子目标 %s 的权重无法解析：%w", record.TargetID, err)
	}
	var prices map[string]struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal([]byte(record.ReferencePricesJSON), &prices); err != nil {
		return nil, fmt.Errorf("影子目标 %s 的参考价无法解析：%w", record.TargetID, err)
	}
	var quantities []store.InstrumentQuantity
	if err := json.Unmarshal([]byte(record.QuantityTargetsJSON), &quantities); err != nil {
		return nil, fmt.Errorf("影子目标 %s 的目标数量无法解析：%w", record.TargetID, err)
	}
	out := &tradepb.ShadowTarget{
		TargetId: record.TargetID, LogicalAccountId: record.LogicalAccountID, InstanceId: record.InstanceID,
		SessionId: record.SessionID, SessionStartedAt: record.SessionStartedAt, StrategyId: record.StrategyID,
		BarEndTime: record.BarEndTime, ValidUntil: record.ValidUntil, Equity: record.Equity, EquitySourceTime: record.EquitySourceTime,
		ReferencePrices: make(map[string]string, len(prices)), RecordedAt: record.RecordedAt,
	}
	for _, weight := range weights {
		out.Weights = append(out.Weights, &tradepb.ShadowWeight{InstrumentId: weight.InstrumentID, TargetWeight: weight.TargetWeight, Hold: weight.Hold})
	}
	for instrumentID, price := range prices {
		out.ReferencePrices[instrumentID] = price.Price
	}
	for _, quantity := range quantities {
		out.Targets = append(out.Targets, &tradepb.InstrumentQuantity{InstrumentId: quantity.InstrumentID, Quantity: quantity.Quantity, Hold: quantity.Hold})
	}
	return out, nil
}
