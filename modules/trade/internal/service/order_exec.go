package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 阶段4：交易执行编排（现货 MVP）。
//
//   - 下单(PlaceOrderExec)：计算冻结币种/金额 → AdjustFrozen 预冻结 → 落库 PENDING + 审计(place,处理中)
//     → 调适配层下单 → 回填 exchange_order_id/status + 审计结果。适配层失败：解冻 + REJECTED + 审计失败。
//   - 成交回填(ApplyFills)：每笔 fill 解冻对应冻结额、计入所得资产与手续费流水、推进订单状态。
//   - 撤单(CancelOrderExec)：适配层撤单 → 解冻剩余冻结额 → CANCELED + 审计。
//   - 改单(AmendOrderExec)：适配层改单 → 更新价/量 + 审计。
//   - 合约(swap)暂不做现货式冻结，由持仓保证金维护。
//
// 冻结额不单独落库，按订单当前状态（qty - filled_qty）重算，保证冻结/解冻可对冲。

// splitSymbol 按常见计价币后缀拆分交易对。
func splitSymbol(symbol string) (base, quote string) {
	for _, q := range []string{"USDT", "USDC", "FDUSD", "TUSD", "BTC", "ETH", "BNB"} {
		if strings.HasSuffix(symbol, q) && len(symbol) > len(q) {
			return symbol[:len(symbol)-len(q)], q
		}
	}
	return symbol, ""
}

// freezeCost 计算现货下单需冻结的币种与金额。
// buy  -> 冻结计价币，金额 = price*qty（市价买单 price 为空/0 时用 amount）。
// sell -> 冻结基础币，金额 = qty。
func freezeCost(side, symbol, price, qty, amount string) (string, string, error) {
	base, quote := splitSymbol(symbol)
	if quote == "" {
		return "", "", fmt.Errorf("cannot infer quote currency from symbol %q", symbol)
	}
	if side == "sell" {
		if base == "" {
			return "", "", fmt.Errorf("cannot infer base currency from symbol %q", symbol)
		}
		return base, qty, nil
	}
	if amount != "" && (price == "" || price == "0") {
		return quote, amount, nil
	}
	cost, err := mulSvc(price, qty)
	if err != nil {
		return "", "", err
	}
	return quote, cost, nil
}

// remainingFreeze 按订单当前状态重算剩余冻结额（用于撤单/失败解冻）。
func remainingFreeze(o *Order) (string, string, error) {
	remQty, err := subSvc(o.Quantity, o.FilledQty)
	if err != nil {
		return "", "", err
	}
	if remQty == "0" || remQty == "" {
		return "", "", nil
	}
	return freezeCost(o.Side, o.Symbol, o.Price, remQty, o.Amount)
}

// recordOp 追加操作审计（失败不阻断主流程）。
func (s *OrderService) recordOp(ctx context.Context, spaceID string, op *OrderOperation) {
	if op == nil {
		return
	}
	if op.OpID == "" {
		op.OpID = genID("op")
	}
	if op.CreatedAt.IsZero() {
		op.CreatedAt = time.Now()
	}
	_ = s.store.AppendOrderOperation(ctx, spaceID, op)
}

// unfreeze 解冻订单当前剩余冻结额（仅现货/杠杆）。
func (s *OrderService) unfreeze(ctx context.Context, spaceID string, o *Order) error {
	if o.MarketType != "spot" && o.MarketType != "margin" {
		return nil
	}
	cur, amt, err := remainingFreeze(o)
	if err != nil {
		return err
	}
	if cur == "" || amt == "" || amt == "0" {
		return nil
	}
	return s.store.AdjustFrozen(ctx, spaceID, o.AccountID, cur, "-"+amt)
}

// PlaceOrderExec 下单编排。
func (s *OrderService) PlaceOrderExec(ctx context.Context, spaceID, channelID string, req *exchange.PlaceOrderReq, operator string) (*Order, error) {
	if req == nil || channelID == "" || req.Symbol == "" || (req.Quantity == "" && req.Amount == "") {
		return nil, ErrInvalidParam
	}
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return nil, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return nil, err
	}
	if req.ClientOrderID == "" {
		req.ClientOrderID = genID("ord")
	}
	o := &Order{
		OrderID:       genID("o"),
		ClientOrderID: req.ClientOrderID,
		AccountID:     ch.AccountID,
		ChannelID:     ch.ChannelID,
		Exchange:      ch.Exchange,
		Symbol:        req.Symbol,
		MarketType:    ch.MarketType,
		Side:          string(req.Side),
		PosSide:       req.PosSide,
		OrderType:     string(req.Type),
		TimeInForce:   req.TimeInForce,
		Price:         req.Price,
		Quantity:      req.Quantity,
		Amount:        req.Amount,
		ReduceOnly:    req.ReduceOnly,
		TriggerPrice:  req.TriggerPrice,
		Source:        "api",
		Status:        int(exchange.StatusPending),
	}

	// 现货：预冻结
	if ch.MarketType == "spot" || ch.MarketType == "margin" {
		cur, amt, ferr := freezeCost(string(req.Side), req.Symbol, req.Price, req.Quantity, req.Amount)
		if ferr != nil {
			return nil, fmt.Errorf("freeze cost: %w", ferr)
		}
		if amt != "" && amt != "0" {
			if err := s.store.AdjustFrozen(ctx, spaceID, ch.AccountID, cur, amt); err != nil {
				return nil, fmt.Errorf("freeze balance: %w", err)
			}
		}
	}

	// 落库 PENDING + 审计
	if err := s.store.SaveOrder(ctx, spaceID, o); err != nil {
		_ = s.unfreeze(ctx, spaceID, o)
		return nil, err
	}
	reqBody, _ := json.Marshal(req)
	s.recordOp(ctx, spaceID, &OrderOperation{
		AccountID: o.AccountID, ChannelID: o.ChannelID, OrderID: o.OrderID,
		OpType: "place", Request: string(reqBody), OpStatus: 0, Operator: operator,
	})

	// 适配层下单
	start := time.Now()
	res, err := adapter.PlaceOrder(ctx, cred, req)
	latency := time.Since(start).Milliseconds()
	op := &OrderOperation{
		AccountID: o.AccountID, ChannelID: o.ChannelID, OrderID: o.OrderID,
		OpType: "place", OpStatus: 1, LatencyMS: latency, Operator: operator,
	}
	if err != nil {
		_ = s.unfreeze(ctx, spaceID, o)
		o.Status = int(exchange.StatusRejected)
		o.RejectReason = err.Error()
		_ = s.store.UpdateOrder(ctx, spaceID, o)
		op.OpStatus = 2
		op.ErrorCode = "adapter_place"
		op.ErrorMessage = err.Error()
		s.recordOp(ctx, spaceID, op)
		return o, err
	}
	o.ExchangeOrderID = res.ExchangeOrderID
	o.Status = int(res.Status)
	o.SubmittedAt = time.Now()
	_ = s.store.UpdateOrder(ctx, spaceID, o)
	op.Response = fmt.Sprintf(`{"exchange_order_id":%q,"status":%d}`, res.ExchangeOrderID, res.Status)
	s.recordOp(ctx, spaceID, op)
	return o, nil
}

// CancelOrderExec 撤单编排。
func (s *OrderService) CancelOrderExec(ctx context.Context, spaceID, channelID string, req *exchange.CancelOrderReq, operator string) (*Order, error) {
	if req == nil || channelID == "" || (req.OrderID == "" && req.ClientOrderID == "") {
		return nil, ErrInvalidParam
	}
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return nil, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return nil, err
	}
	o, err := s.store.GetOrder(ctx, spaceID, req.OrderID, req.ClientOrderID)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := adapter.CancelOrder(ctx, cred, req)
	latency := time.Since(start).Milliseconds()
	op := &OrderOperation{
		AccountID: o.AccountID, ChannelID: o.ChannelID, OrderID: o.OrderID,
		OpType: "cancel", OpStatus: 1, LatencyMS: latency, Operator: operator,
	}
	if err != nil {
		op.OpStatus = 2
		op.ErrorCode = "adapter_cancel"
		op.ErrorMessage = err.Error()
		s.recordOp(ctx, spaceID, op)
		return o, err
	}
	o.Status = int(res.Status)
	o.FinishedAt = time.Now()
	_ = s.store.UpdateOrder(ctx, spaceID, o)
	_ = s.unfreeze(ctx, spaceID, o)
	s.recordOp(ctx, spaceID, op)
	return o, nil
}

// AmendOrderExec 改单编排。
func (s *OrderService) AmendOrderExec(ctx context.Context, spaceID, channelID string, req *exchange.AmendOrderReq, operator string) (*Order, error) {
	if req == nil || channelID == "" || (req.OrderID == "" && req.ClientOrderID == "") {
		return nil, ErrInvalidParam
	}
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return nil, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return nil, err
	}
	o, err := s.store.GetOrder(ctx, spaceID, req.OrderID, req.ClientOrderID)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := adapter.AmendOrder(ctx, cred, req)
	latency := time.Since(start).Milliseconds()
	op := &OrderOperation{
		AccountID: o.AccountID, ChannelID: o.ChannelID, OrderID: o.OrderID,
		OpType: "amend", OpStatus: 1, LatencyMS: latency, Operator: operator,
	}
	if err != nil {
		op.OpStatus = 2
		op.ErrorMessage = err.Error()
		s.recordOp(ctx, spaceID, op)
		return o, err
	}
	if req.NewPrice != "" {
		o.Price = req.NewPrice
	}
	if req.NewQuantity != "" {
		o.Quantity = req.NewQuantity
	}
	o.Status = int(res.Status)
	_ = s.store.UpdateOrder(ctx, spaceID, o)
	s.recordOp(ctx, spaceID, op)
	return o, nil
}

// ApplyFills 成交回填：追加成交明细、结算余额、推进订单状态。
// 由 WS 推送或定时对账调用。fills 中每笔需带 price/quantity/fee/fee_currency。
func (s *OrderService) ApplyFills(ctx context.Context, spaceID, orderID string, fills []*exchange.Trade) error {
	if orderID == "" || len(fills) == 0 {
		return ErrInvalidParam
	}
	o, err := s.store.GetOrder(ctx, spaceID, orderID, "")
	if err != nil {
		return err
	}
	base, quote := splitSymbol(o.Symbol)

	var newFilledQty, newFilledAmount, totalCost string
	svcTrades := make([]*Trade, 0, len(fills))
	for i, f := range fills {
		if f.TradeID == "" {
			f.TradeID = genID("tr")
		}
		cost, _ := mulSvc(f.Price, f.Quantity)
		if newFilledQty == "" {
			newFilledQty = f.Quantity
			newFilledAmount = cost
			totalCost = cost
		} else {
			newFilledQty, _ = addSvc(newFilledQty, f.Quantity)
			newFilledAmount, _ = addSvc(newFilledAmount, cost)
			totalCost, _ = addSvc(totalCost, cost)
		}

		// 解冻本次成交对应冻结额：buy 解冻 quote=cost；sell 解冻 base=qty
		var unfreezeCur, unfreezeAmt string
		if o.Side == "sell" {
			unfreezeCur, unfreezeAmt = base, f.Quantity
		} else {
			unfreezeCur, unfreezeAmt = quote, cost
		}
		if unfreezeAmt != "" && unfreezeAmt != "0" {
			_ = s.store.AdjustFrozen(ctx, spaceID, o.AccountID, unfreezeCur, "-"+unfreezeAmt)
		}
		// 计入所得资产
		var recvCur, recvAmt string
		if o.Side == "buy" {
			recvCur, recvAmt = base, f.Quantity
		} else {
			recvCur, recvAmt = quote, cost
		}
		_ = s.store.AppendFundFlows(ctx, spaceID, []*FundFlow{{
			FlowID: genID("flow"), AccountID: o.AccountID, Currency: recvCur,
			BizType: "trade", Direction: 1, Amount: recvAmt, RefType: "order", RefID: o.OrderID,
		}})
		// 手续费
		if f.Fee != "" && f.Fee != "0" && f.FeeCurrency != "" {
			_ = s.store.AppendFundFlows(ctx, spaceID, []*FundFlow{{
				FlowID: genID("flow"), AccountID: o.AccountID, Currency: f.FeeCurrency,
				BizType: "fee", Direction: -1, Amount: f.Fee, RefType: "order", RefID: o.OrderID,
			}})
		}

		tradedAt := time.Now()
		if f.TradedAt > 0 {
			tradedAt = time.UnixMilli(f.TradedAt)
		}
		svcTrades = append(svcTrades, &Trade{
			TradeID: f.TradeID, ExchangeTradeID: f.ExchangeTradeID, OrderID: o.OrderID,
			ExchangeOrderID: o.ExchangeOrderID, AccountID: o.AccountID, ChannelID: o.ChannelID,
			Exchange: o.Exchange, Symbol: o.Symbol, Side: o.Side,
			Price: f.Price, Quantity: f.Quantity, Amount: f.Amount,
			Fee: f.Fee, FeeCurrency: f.FeeCurrency, Role: f.Role, TradedAt: tradedAt,
		})
		_ = i
	}
	if err := s.store.AppendTrades(ctx, spaceID, svcTrades); err != nil {
		return err
	}

	o.FilledQty = newFilledQty
	o.FilledAmount = newFilledAmount
	if newFilledQty != "" && newFilledQty != "0" {
		o.AvgPrice, _ = divSvcSafe(totalCost, newFilledQty)
	}
	rem, _ := subSvc(o.Quantity, o.FilledQty)
	if rem == "0" || rem == "" {
		o.Status = int(exchange.StatusFilled)
		o.FinishedAt = time.Now()
	} else {
		o.Status = int(exchange.StatusPartiallyFilled)
	}
	return s.store.UpdateOrder(ctx, spaceID, o)
}
