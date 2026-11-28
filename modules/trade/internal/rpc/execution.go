package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	positionapp "github.com/mooyang-code/moox/modules/trade/internal/application/position"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// SubmitOrderCommand 是人工下单的命令：给 LogicalAccountID 里的执行账户下一笔单。
type SubmitOrderCommand struct {
	SpaceID          string
	ActionID         string
	LogicalAccountID string
	TradingAccountID string
	ClientOrderID    string
	InstrumentID     string
	OrderType        exchange.OrderType
	FillPolicy       exchange.FillPolicy
	Side             exchange.Side
	PositionSide     exchange.PositionSide
	Quantity         shared.Decimal
	LimitPrice       *shared.Decimal
	Reason           string
	DeadlineAt       int64
}

type ExecutionServer struct {
	Store *store.Store
	// Submit 受理人工下单：按组合的控制模式决定要不要先暂停组合（见 operator.Service.PlaceOrder）。
	Submit func(context.Context, SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error)
	Cancel func(
		context.Context,
		string,
		string,
		string,
		string,
	) (store.OperatorActionRecord, store.OrderRecord, error)
	// Lanes 按标的返回执行器对目标的结论（targetapp.Executor.LaneStatuses）；为空时 GetLogicalAccountTarget 不带 lanes。
	Lanes func(context.Context, store.LogicalAccountTargetRecord) ([]targetapp.LaneStatus, error)
	// Positions 统一查询永续合约持仓与余额类市场（现货、股票）持币；为空时回退到只读 Store（无实时参考价）。
	Positions *positionapp.Query
}

// submitOrderCommand 把下单请求转成命令。失败时返回写好的错误信息，调用方直接放进响应。
func (h *ExecutionServer) submitOrderCommand(
	ctx context.Context, spaceID string, req *tradepb.SubmitOrderReq,
) (SubmitOrderCommand, *tradepb.RetInfo) {
	quantity, err := shared.ParseDecimal(req.GetQuantity())
	if err != nil {
		return SubmitOrderCommand{}, invalidInfo(err)
	}
	var limitPrice *shared.Decimal
	// limit_price 是 proto 可选字段：只有请求带了才解析，读取器分不出“未传”和“空串”。
	if req.LimitPrice != nil {
		value, parseErr := shared.ParseDecimal(req.GetLimitPrice())
		if parseErr != nil {
			return SubmitOrderCommand{}, invalidInfo(parseErr)
		}
		limitPrice = &value
	}
	instrumentID, err := h.requestInstrumentID(ctx, spaceID, req.GetTradingAccountId(), req.GetInstrumentId(), req.GetMarketType())
	if err != nil {
		return SubmitOrderCommand{}, instrumentErrorInfo(err)
	}
	return SubmitOrderCommand{
		SpaceID: spaceID, ActionID: req.GetActionId(), LogicalAccountID: req.GetLogicalAccountId(),
		TradingAccountID: req.GetTradingAccountId(),
		ClientOrderID:    req.GetClientOrderId(), InstrumentID: instrumentID,
		OrderType: orderTypeFromPB(req.GetOrderType()), FillPolicy: fillPolicyFromPB(req.GetFillPolicy()),
		Side: sideFromPB(req.GetSide()), PositionSide: positionSideFromPB(req.GetPositionSide()),
		Quantity: quantity, LimitPrice: limitPrice, Reason: req.GetReason(), DeadlineAt: req.GetDeadlineAt(),
	}, nil
}

func (h *ExecutionServer) CancelOrder(
	ctx context.Context,
	req *tradepb.CancelOrderReq,
) (*tradepb.CancelOrderRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.CancelOrderRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if h.Cancel == nil {
		err = fmt.Errorf("撤单服务未配置")
	}
	var action store.OperatorActionRecord
	var order store.OrderRecord
	if err == nil {
		action, order, err = h.Cancel(
			ctx,
			spaceID,
			req.GetActionId(),
			req.GetOrderId(),
			req.GetReason(),
		)
	}
	return &tradepb.CancelOrderRsp{
		RetInfo: errorInfo(err), Action: operatorActionToPB(action),
		Order: orderToPB(order),
	}, nil
}

func (h *ExecutionServer) GetOperatorAction(
	ctx context.Context,
	req *tradepb.GetOperatorActionReq,
) (*tradepb.GetOperatorActionRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetOperatorActionRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	record, err := h.Store.ReadOnly().GetOperatorAction(ctx, spaceID, req.GetActionId())
	return &tradepb.GetOperatorActionRsp{
		RetInfo: errorInfo(err), Action: operatorActionToPB(record),
	}, nil
}

func (h *ExecutionServer) GetLogicalAccountTarget(
	ctx context.Context,
	req *tradepb.GetLogicalAccountTargetReq,
) (*tradepb.GetLogicalAccountTargetRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetLogicalAccountTargetRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	record, err := h.Store.ReadOnly().GetLogicalAccountTarget(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
	)
	return &tradepb.GetLogicalAccountTargetRsp{
		RetInfo: errorInfo(err), Target: logicalAccountTargetToPB(record),
		Lanes: h.targetLanes(ctx, record, err),
	}, nil
}

// targetLanes 按标的给出执行器的结论。目标已收敛时整页都到位，不用算；成员或设置读不出来时不给，页面按未知显示。
func (h *ExecutionServer) targetLanes(ctx context.Context, record store.LogicalAccountTargetRecord, getErr error) []*tradepb.TargetLane {
	if h.Lanes == nil || getErr != nil || record.TargetID == "" || record.Status == targetapp.StatusConverged {
		return nil
	}
	statuses, err := h.Lanes(ctx, record)
	if err != nil {
		return nil
	}
	lanes := make([]*tradepb.TargetLane, 0, len(statuses))
	for _, status := range statuses {
		lanes = append(lanes, &tradepb.TargetLane{
			InstrumentId: status.InstrumentID, State: targetLaneStateToPB(status.State), Reason: status.Reason,
		})
	}
	return lanes
}

func targetLaneStateToPB(state targetapp.LaneState) tradepb.TargetLaneState {
	switch state {
	case targetapp.LaneWillOrder:
		return tradepb.TargetLaneState_TARGET_LANE_STATE_WILL_ORDER
	case targetapp.LaneInPlace:
		return tradepb.TargetLaneState_TARGET_LANE_STATE_IN_PLACE
	case targetapp.LaneBelowMinNotional:
		return tradepb.TargetLaneState_TARGET_LANE_STATE_BELOW_MIN_NOTIONAL
	case targetapp.LaneMarketDeferred:
		return tradepb.TargetLaneState_TARGET_LANE_STATE_MARKET_DEFERRED
	case targetapp.LaneBelowLot:
		return tradepb.TargetLaneState_TARGET_LANE_STATE_BELOW_LOT
	case targetapp.LaneBlocked:
		return tradepb.TargetLaneState_TARGET_LANE_STATE_BLOCKED
	}
	return tradepb.TargetLaneState_TARGET_LANE_STATE_UNKNOWN
}

func (h *ExecutionServer) GetOrder(
	ctx context.Context,
	req *tradepb.GetOrderReq,
) (*tradepb.GetOrderRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetOrderRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	record, err := h.Store.ReadOnly().GetOrder(ctx, spaceID, req.GetOrderId())
	return &tradepb.GetOrderRsp{RetInfo: errorInfo(err), Order: orderToPB(record)}, nil
}

func (h *ExecutionServer) ListOrders(
	ctx context.Context,
	req *tradepb.ListOrdersReq,
) (*tradepb.ListOrdersRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListOrdersRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	st := h.Store.ReadOnly()
	if req.GetLogicalAccountId() != "" {
		if _, err = st.GetLogicalAccount(
			ctx,
			spaceID,
			req.GetLogicalAccountId(),
		); err != nil {
			return &tradepb.ListOrdersRsp{RetInfo: errorInfo(err)}, nil
		}
	}
	if req.GetTradingAccountId() != "" {
		if _, err = st.GetTradingAccount(
			ctx,
			spaceID,
			req.GetTradingAccountId(),
		); err != nil {
			return &tradepb.ListOrdersRsp{RetInfo: errorInfo(err)}, nil
		}
	}
	instrumentID, err := queryInstrumentID(ctx, st, spaceID, req.GetLogicalAccountId(), req.GetTradingAccountId(), req.GetInstrumentId())
	if err != nil {
		return &tradepb.ListOrdersRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := st.ListOrders(ctx, spaceID, store.OrderQuery{
		LogicalAccountID: req.GetLogicalAccountId(),
		TradingAccountID: req.GetTradingAccountId(),
		InstrumentID:     instrumentID, State: normalized(req.GetState()), OwnerType: req.GetOwnerType(),
		OnlyOpen: req.GetOnlyOpen(), StartTime: req.GetStartTime(),
		EndTime: req.GetEndTime(), Offset: page.offset, Limit: page.size,
	})
	orders := make([]*tradepb.Order, 0, len(records))
	for _, record := range records {
		orders = append(orders, orderToPB(record))
	}
	return &tradepb.ListOrdersRsp{
		RetInfo: errorInfo(err), Orders: orders,
		PageResult: pageResult(page, total),
	}, nil
}

func (h *ExecutionServer) ListFills(
	ctx context.Context,
	req *tradepb.ListFillsReq,
) (*tradepb.ListFillsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListFillsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	st := h.Store.ReadOnly()
	if req.GetLogicalAccountId() != "" {
		if _, err := st.GetLogicalAccount(ctx, spaceID, req.GetLogicalAccountId()); err != nil {
			return &tradepb.ListFillsRsp{RetInfo: errorInfo(err)}, nil
		}
	}
	if req.GetTradingAccountId() != "" {
		if _, err := st.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId()); err != nil {
			return &tradepb.ListFillsRsp{RetInfo: errorInfo(err)}, nil
		}
	}
	instrumentID, err := queryInstrumentID(ctx, st, spaceID, req.GetLogicalAccountId(), req.GetTradingAccountId(), req.GetInstrumentId())
	if err != nil {
		return &tradepb.ListFillsRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := st.ListFills(ctx, spaceID, store.FillQuery{
		TradingAccountID: req.GetTradingAccountId(), LogicalAccountID: req.GetLogicalAccountId(), OrderID: req.GetOrderId(),
		InstrumentID: instrumentID, StartTime: req.GetStartTime(),
		EndTime: req.GetEndTime(), Offset: page.offset, Limit: page.size,
	})
	fills := make([]*tradepb.Fill, 0, len(records))
	for _, record := range records {
		fills = append(fills, fillToPB(record))
	}
	return &tradepb.ListFillsRsp{
		RetInfo: errorInfo(err), Fills: fills,
		PageResult: pageResult(page, total),
	}, nil
}

func (h *ExecutionServer) ListPositions(
	ctx context.Context,
	req *tradepb.ListPositionsReq,
) (*tradepb.ListPositionsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListPositionsRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	query := h.Positions
	if query == nil && h.Store != nil {
		query = &positionapp.Query{Store: h.Store.ReadOnly()}
	}
	if query == nil {
		return &tradepb.ListPositionsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	records, err := query.List(ctx, positionapp.ListRequest{
		SpaceID:          spaceID,
		LogicalAccountID: req.GetLogicalAccountId(),
		TradingAccountID: req.GetTradingAccountId(),
		InstrumentID:     req.GetInstrumentId(),
	})
	if errors.Is(err, positionapp.ErrNotLogicalMember) {
		return &tradepb.ListPositionsRsp{RetInfo: invalidInfo(err)}, nil
	}
	if err != nil {
		return &tradepb.ListPositionsRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	positions := make([]*tradepb.Position, 0, len(records))
	for _, record := range records {
		positions = append(positions, positionToPB(record))
	}
	return &tradepb.ListPositionsRsp{
		RetInfo: success(), Positions: positions,
	}, nil
}

// normalizeForMarkets 按可选的市场集合标准化上游传入的标的 ID：只有一个市场时按它标准化；
// 有多个市场时，标的 ID 必须自带市场后缀（例如 BTC-USDT-SWAP），否则无法判断是哪个市场。
func normalizeForMarkets(raw string, markets []string) (string, error) {
	switch len(markets) {
	case 0:
		return "", fmt.Errorf("%w: %q 没有可交易的市场", exchange.ErrInvalidInstrumentID, raw)
	case 1:
		return exchange.NormalizeInstrumentID(raw, exchange.MarketType(markets[0]))
	}
	for _, market := range markets {
		if id, err := exchange.NormalizeInstrumentID(raw, exchange.MarketType(market)); err == nil &&
			strings.HasSuffix(strings.ToUpper(strings.TrimSpace(raw)), "-"+exchange.InstrumentSuffix(market)) {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: 开通了多个市场 %v，标的 %q 必须带市场后缀", exchange.ErrInvalidInstrumentID, markets, raw)
}

// requestInstrumentID 按请求显式给的市场标准化标的 ID：账户必须开通了这个市场。没给市场时回到账户开通的市场
// （只开通一个时按它，开通多个时标的 ID 必须自带后缀）。
func (h *ExecutionServer) requestInstrumentID(
	ctx context.Context, spaceID, tradingAccountID, raw string, market string,
) (string, error) {
	if market == "" || raw == "" {
		return accountInstrumentID(ctx, h.Store, spaceID, tradingAccountID, raw)
	}
	account, err := h.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return "", err
	}
	explicit := exchange.MarketType(market)
	if !account.SupportsMarket(string(explicit)) {
		return "", fmt.Errorf("%w: 账户没有开通市场 %s", exchange.ErrInvalidInstrumentID, explicit)
	}
	return exchange.NormalizeInstrumentID(raw, explicit)
}

// accountInstrumentID 按执行账户已开通的市场标准化上游传入的标的 ID。
func accountInstrumentID(ctx context.Context, st *store.Store, spaceID, tradingAccountID, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	account, err := st.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return "", err
	}
	return normalizeForMarkets(raw, account.Markets)
}

// queryInstrumentID 给查询用：有账户信息时标准化，没有时按原值匹配。
func queryInstrumentID(ctx context.Context, st *store.Store, spaceID, logicalAccountID, tradingAccountID, raw string) (string, error) {
	switch {
	case raw == "":
		return "", nil
	case tradingAccountID != "":
		return accountInstrumentID(ctx, st, spaceID, tradingAccountID, raw)
	case logicalAccountID != "":
		markets, err := st.LogicalAccountMarkets(ctx, spaceID, logicalAccountID)
		if err != nil {
			return "", err
		}
		return normalizeForMarkets(raw, markets)
	default:
		return raw, nil
	}
}

func instrumentErrorInfo(err error) *tradepb.RetInfo {
	if errors.Is(err, exchange.ErrInvalidInstrumentID) {
		return invalidInfo(err)
	}
	return errorInfo(err)
}
