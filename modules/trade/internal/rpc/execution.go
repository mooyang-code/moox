package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// errNotLogicalMember 是查询持仓时传了不属于该组合账户的交易账户：请求参数有误。
var errNotLogicalMember = errors.New("交易账户不是该组合账户的成员")

type ManualOrderCommand struct {
	SpaceID          string
	ActionID         string
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

type SubmitOrderCommand struct {
	ManualOrderCommand
	LogicalAccountID string
}

type ExecutionServer struct {
	Store          *store.Store
	SubmitOrdinary func(context.Context, SubmitOrderCommand) (store.OperatorActionRecord, store.OrderRecord, error)
	PlaceManual    func(
		context.Context,
		ManualOrderCommand,
	) (store.OperatorActionRecord, store.OrderRecord, error)
	Cancel func(
		context.Context,
		string,
		string,
		string,
		string,
	) (store.OperatorActionRecord, store.OrderRecord, error)
}

// manualOrderRequest 是 PlaceManualOrderReq 与 SubmitOrderReq 共有的字段读取。
type manualOrderRequest interface {
	GetActionId() string
	GetTradingAccountId() string
	GetClientOrderId() string
	GetInstrumentId() string
	GetMarketType() tradepb.MarketType
	GetOrderType() tradepb.OrderType
	GetFillPolicy() tradepb.FillPolicy
	GetSide() tradepb.OrderSide
	GetPositionSide() tradepb.PositionSide
	GetQuantity() string
	GetLimitPrice() string
	GetReason() string
	GetDeadlineAt() int64
}

// manualOrderCommand 把下单请求转成命令。hasLimitPrice 表示请求带了 limit_price（proto 可选字段，读取器分不出“未传”和“空串”）。
// 失败时返回写好的错误信息，调用方直接放进响应。
func (h *ExecutionServer) manualOrderCommand(
	ctx context.Context, spaceID string, req manualOrderRequest, hasLimitPrice bool,
) (ManualOrderCommand, *tradepb.RetInfo) {
	quantity, err := shared.ParseDecimal(req.GetQuantity())
	if err != nil {
		return ManualOrderCommand{}, invalidInfo(err)
	}
	var limitPrice *shared.Decimal
	if hasLimitPrice {
		value, parseErr := shared.ParseDecimal(req.GetLimitPrice())
		if parseErr != nil {
			return ManualOrderCommand{}, invalidInfo(parseErr)
		}
		limitPrice = &value
	}
	instrumentID, err := h.requestInstrumentID(ctx, spaceID, req.GetTradingAccountId(), req.GetInstrumentId(), req.GetMarketType())
	if err != nil {
		return ManualOrderCommand{}, instrumentErrorInfo(err)
	}
	return ManualOrderCommand{
		SpaceID: spaceID, ActionID: req.GetActionId(), TradingAccountID: req.GetTradingAccountId(),
		ClientOrderID: req.GetClientOrderId(), InstrumentID: instrumentID,
		OrderType: orderTypeFromPB(req.GetOrderType()), FillPolicy: fillPolicyFromPB(req.GetFillPolicy()),
		Side: sideFromPB(req.GetSide()), PositionSide: positionSideFromPB(req.GetPositionSide()),
		Quantity: quantity, LimitPrice: limitPrice, Reason: req.GetReason(), DeadlineAt: req.GetDeadlineAt(),
	}, nil
}

func (h *ExecutionServer) PlaceManualOrder(
	ctx context.Context,
	req *tradepb.PlaceManualOrderReq,
) (*tradepb.PlaceManualOrderRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.PlaceManualOrderRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	command, info := h.manualOrderCommand(ctx, spaceID, req, req.LimitPrice != nil)
	if info != nil {
		return &tradepb.PlaceManualOrderRsp{RetInfo: info}, nil
	}
	if h.PlaceManual == nil {
		err = fmt.Errorf("人工下单服务未配置")
	}
	var action store.OperatorActionRecord
	var order store.OrderRecord
	if err == nil {
		action, order, err = h.PlaceManual(ctx, command)
	}
	return &tradepb.PlaceManualOrderRsp{
		RetInfo: errorInfo(err), Action: operatorActionToPB(action),
		Order: orderToPB(order),
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
	}, nil
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
	st := h.Store.ReadOnly()
	accountIDs, err := positionAccountIDs(ctx, st, spaceID, req)
	if errors.Is(err, errNotLogicalMember) {
		return &tradepb.ListPositionsRsp{RetInfo: invalidInfo(err)}, nil
	}
	if err != nil {
		return &tradepb.ListPositionsRsp{RetInfo: errorInfo(err)}, nil
	}
	instrumentID, err := queryInstrumentID(ctx, st, spaceID, req.GetLogicalAccountId(), req.GetTradingAccountId(), req.GetInstrumentId())
	if err != nil {
		return &tradepb.ListPositionsRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	positions := make([]*tradepb.Position, 0)
	for _, accountID := range accountIDs {
		records, listErr := st.ListPositionsByInstrument(
			ctx,
			spaceID,
			accountID,
			instrumentID,
		)
		if listErr != nil {
			return &tradepb.ListPositionsRsp{
				RetInfo: errorInfo(listErr),
			}, nil
		}
		for _, record := range records {
			positions = append(positions, positionToPB(record))
		}
	}
	return &tradepb.ListPositionsRsp{
		RetInfo: success(), Positions: positions,
	}, nil
}

func positionAccountIDs(
	ctx context.Context,
	st *store.Store,
	spaceID string,
	req *tradepb.ListPositionsReq,
) ([]string, error) {
	if req.GetLogicalAccountId() == "" {
		if _, err := st.GetTradingAccount(
			ctx,
			spaceID,
			req.GetTradingAccountId(),
		); err != nil {
			return nil, err
		}
		return []string{req.GetTradingAccountId()}, nil
	}
	if _, err := st.GetLogicalAccount(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
	); err != nil {
		return nil, err
	}
	members, err := st.ListLogicalAccountMembers(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
		true,
	)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(members))
	for _, member := range members {
		if req.GetTradingAccountId() != "" &&
			member.TradingAccountID != req.GetTradingAccountId() {
			continue
		}
		ids = append(ids, member.TradingAccountID)
	}
	if req.GetTradingAccountId() != "" && len(ids) == 0 {
		return nil, fmt.Errorf(
			"%w：交易账户 %s，组合账户 %s",
			errNotLogicalMember,
			req.GetTradingAccountId(),
			req.GetLogicalAccountId(),
		)
	}
	return ids, nil
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
			strings.HasSuffix(strings.ToUpper(strings.TrimSpace(raw)), "-"+market) {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: 开通了多个市场 %v，标的 %q 必须带市场后缀", exchange.ErrInvalidInstrumentID, markets, raw)
}

// requestInstrumentID 按请求显式给的市场标准化标的 ID：账户必须开通了这个市场。没给市场时回到账户开通的市场
// （只开通一个时按它，开通多个时标的 ID 必须自带后缀）。
func (h *ExecutionServer) requestInstrumentID(
	ctx context.Context, spaceID, tradingAccountID, raw string, market tradepb.MarketType,
) (string, error) {
	if market == tradepb.MarketType_MARKET_TYPE_UNSPECIFIED || raw == "" {
		return accountInstrumentID(ctx, h.Store, spaceID, tradingAccountID, raw)
	}
	account, err := h.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return "", err
	}
	explicit := marketFromPB(market)
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
