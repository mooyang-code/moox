package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

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

func (h *ExecutionServer) PlaceManualOrder(
	ctx context.Context,
	req *tradepb.PlaceManualOrderReq,
) (*tradepb.PlaceManualOrderRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.PlaceManualOrderRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	quantity, err := shared.ParseDecimal(req.GetQuantity())
	if err != nil {
		return &tradepb.PlaceManualOrderRsp{RetInfo: invalidInfo(err)}, nil
	}
	var limitPrice *shared.Decimal
	if req.LimitPrice != nil {
		value, parseErr := shared.ParseDecimal(req.GetLimitPrice())
		if parseErr != nil {
			return &tradepb.PlaceManualOrderRsp{
				RetInfo: invalidInfo(parseErr),
			}, nil
		}
		limitPrice = &value
	}
	instrumentID, err := h.accountInstrumentID(ctx, spaceID, req.GetTradingAccountId(), req.GetInstrumentId())
	if err != nil {
		return &tradepb.PlaceManualOrderRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	if h.PlaceManual == nil {
		err = fmt.Errorf("trade RPC: manual order service is not configured")
	}
	var action store.OperatorActionRecord
	var order store.OrderRecord
	if err == nil {
		action, order, err = h.PlaceManual(ctx, ManualOrderCommand{
			SpaceID: spaceID, ActionID: req.GetActionId(),
			TradingAccountID: req.GetTradingAccountId(),
			ClientOrderID:    req.GetClientOrderId(), InstrumentID: instrumentID,
			OrderType:    orderTypeFromPB(req.GetOrderType()),
			FillPolicy:   fillPolicyFromPB(req.GetFillPolicy()),
			Side:         sideFromPB(req.GetSide()),
			PositionSide: positionSideFromPB(req.GetPositionSide()),
			Quantity:     quantity, LimitPrice: limitPrice, Reason: req.GetReason(),
			DeadlineAt: req.GetDeadlineAt(),
		})
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
		err = fmt.Errorf("trade RPC: cancel service is not configured")
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
	record, err := h.Store.GetOperatorAction(ctx, spaceID, req.GetActionId())
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
	record, err := h.Store.GetLogicalAccountTarget(
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
	record, err := h.Store.GetOrder(ctx, spaceID, req.GetOrderId())
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
	if req.GetLogicalAccountId() != "" {
		if _, err = h.Store.GetLogicalAccount(
			ctx,
			spaceID,
			req.GetLogicalAccountId(),
		); err != nil {
			return &tradepb.ListOrdersRsp{RetInfo: errorInfo(err)}, nil
		}
	}
	if req.GetTradingAccountId() != "" {
		if _, err = h.Store.GetTradingAccount(
			ctx,
			spaceID,
			req.GetTradingAccountId(),
		); err != nil {
			return &tradepb.ListOrdersRsp{RetInfo: errorInfo(err)}, nil
		}
	}
	instrumentID, err := h.queryInstrumentID(ctx, spaceID, req.GetLogicalAccountId(), req.GetTradingAccountId(), req.GetInstrumentId())
	if err != nil {
		return &tradepb.ListOrdersRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := h.Store.ListOrders(ctx, spaceID, store.OrderQuery{
		LogicalAccountID: req.GetLogicalAccountId(),
		TradingAccountID: req.GetTradingAccountId(),
		InstrumentID:     instrumentID, State: normalized(req.GetState()),
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
	if _, err := h.Store.GetTradingAccount(
		ctx,
		spaceID,
		req.GetTradingAccountId(),
	); err != nil {
		return &tradepb.ListFillsRsp{RetInfo: errorInfo(err)}, nil
	}
	instrumentID, err := h.accountInstrumentID(ctx, spaceID, req.GetTradingAccountId(), req.GetInstrumentId())
	if err != nil {
		return &tradepb.ListFillsRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := h.Store.ListFills(ctx, spaceID, store.FillQuery{
		TradingAccountID: req.GetTradingAccountId(), OrderID: req.GetOrderId(),
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
	accountIDs, err := h.positionAccountIDs(ctx, spaceID, req)
	if err != nil {
		return &tradepb.ListPositionsRsp{RetInfo: errorInfo(err)}, nil
	}
	instrumentID, err := h.queryInstrumentID(ctx, spaceID, req.GetLogicalAccountId(), req.GetTradingAccountId(), req.GetInstrumentId())
	if err != nil {
		return &tradepb.ListPositionsRsp{RetInfo: instrumentErrorInfo(err)}, nil
	}
	positions := make([]*tradepb.Position, 0)
	for _, accountID := range accountIDs {
		records, listErr := h.Store.ListPositionsByInstrument(
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

func (h *ExecutionServer) positionAccountIDs(
	ctx context.Context,
	spaceID string,
	req *tradepb.ListPositionsReq,
) ([]string, error) {
	if req.GetLogicalAccountId() == "" {
		if _, err := h.Store.GetTradingAccount(
			ctx,
			spaceID,
			req.GetTradingAccountId(),
		); err != nil {
			return nil, err
		}
		return []string{req.GetTradingAccountId()}, nil
	}
	if _, err := h.Store.GetLogicalAccount(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
	); err != nil {
		return nil, err
	}
	members, err := h.Store.ListLogicalAccountMembers(
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
			"exchange account %s is not a member of logical account %s",
			req.GetTradingAccountId(),
			req.GetLogicalAccountId(),
		)
	}
	return ids, nil
}

// accountInstrumentID 按执行账户的市场标准化上游传入的标的 ID。
func (h *ExecutionServer) accountInstrumentID(ctx context.Context, spaceID, tradingAccountID, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	account, err := h.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return "", err
	}
	return exchange.NormalizeInstrumentID(raw, exchange.MarketType(account.MarketType))
}

// queryInstrumentID 给查询用：有账户信息时标准化，没有时按原值匹配。
func (h *ExecutionServer) queryInstrumentID(ctx context.Context, spaceID, logicalAccountID, tradingAccountID, raw string) (string, error) {
	switch {
	case raw == "":
		return "", nil
	case tradingAccountID != "":
		return h.accountInstrumentID(ctx, spaceID, tradingAccountID, raw)
	case logicalAccountID != "":
		account, err := h.Store.GetLogicalAccount(ctx, spaceID, logicalAccountID)
		if err != nil {
			return "", err
		}
		return exchange.NormalizeInstrumentID(raw, exchange.MarketType(account.MarketType))
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
