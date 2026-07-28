package rpc

import (
	"context"
	"strings"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

type ExecutionServer struct {
	Orders  *orderapp.Service
	Targets targetapp.Submission
	Prices  targetapp.PriceSource
	Store   *store.Store
}

func (h *ExecutionServer) PlaceOrder(
	ctx context.Context,
	req *tradepb.PlaceOrderReq,
) (*tradepb.PlaceOrderRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.PlaceOrderRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetExchangeAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.PlaceOrderRsp{RetInfo: errorInfo(err)}, nil
	}
	quantity, err := shared.ParseDecimal(req.GetQuantity())
	if err != nil {
		return &tradepb.PlaceOrderRsp{RetInfo: invalidInfo(err)}, nil
	}
	quote, err := h.Prices.LatestPrice(ctx, req.GetExchangeAccountId(), req.GetSymbol())
	if err != nil {
		return &tradepb.PlaceOrderRsp{RetInfo: errorInfo(err)}, nil
	}
	var limitPrice *shared.Decimal
	if req.LimitPrice != nil {
		value, parseErr := shared.ParseDecimal(req.GetLimitPrice())
		if parseErr != nil {
			return &tradepb.PlaceOrderRsp{RetInfo: invalidInfo(parseErr)}, nil
		}
		limitPrice = &value
	}
	source := strings.TrimSpace(req.GetSource())
	if source == "" {
		source = "RPC"
	}
	spec := orderdomain.OrderSpec{
		ExchangeAccountID: req.GetExchangeAccountId(),
		ClientOrderID:     req.GetClientOrderId(), Symbol: req.GetSymbol(),
		OrderType:    orderTypeFromPB(req.GetOrderType()),
		TimeInForce:  timeInForceFromPB(req.GetTimeInForce()),
		Side:         sideFromPB(req.GetSide()),
		PositionSide: positionSideFromPB(req.GetPositionSide()),
		Quantity:     quantity, LimitPrice: limitPrice,
		ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
		ReduceOnly: req.GetReduceOnly(), Source: source,
		StrategyExecutionID: req.GetStrategyExecutionId(),
	}
	placed, err := h.Orders.Place(ctx, spaceID, spec)
	if err == nil {
		placed, err = h.Orders.Submit(ctx, spaceID, string(placed.ID))
	}
	record, getErr := h.Store.GetOrder(ctx, spaceID, string(placed.ID))
	if err == nil {
		err = getErr
	}
	return &tradepb.PlaceOrderRsp{RetInfo: errorInfo(err), Order: orderToPB(record)}, nil
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
	if _, err := h.Store.GetOrder(ctx, spaceID, req.GetOrderId()); err != nil {
		return &tradepb.CancelOrderRsp{RetInfo: errorInfo(err)}, nil
	}
	err = h.cancelStoredOrder(ctx, spaceID, req.GetOrderId())
	record, getErr := h.Store.GetOrder(ctx, spaceID, req.GetOrderId())
	if err == nil {
		err = getErr
	}
	return &tradepb.CancelOrderRsp{RetInfo: errorInfo(err), Order: orderToPB(record)}, nil
}

func (h *ExecutionServer) CancelAllOrders(
	ctx context.Context,
	req *tradepb.CancelAllOrdersReq,
) (*tradepb.CancelAllOrdersRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.CancelAllOrdersRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetExchangeAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.CancelAllOrdersRsp{RetInfo: errorInfo(err)}, nil
	}
	var canceled int32
	records, _, listErr := h.Store.ListOrders(ctx, spaceID, store.OrderQuery{
		ExchangeAccountID: req.GetExchangeAccountId(), Symbol: req.GetSymbol(),
		OnlyOpen: true, Limit: 1000,
	})
	if listErr != nil {
		return &tradepb.CancelAllOrdersRsp{
			RetInfo: errorInfo(listErr), CanceledCount: canceled,
		}, nil
	}
	for _, record := range records {
		if cancelErr := h.cancelStoredOrder(ctx, spaceID, record.OrderID); cancelErr != nil {
			return &tradepb.CancelAllOrdersRsp{
				RetInfo: errorInfo(cancelErr), CanceledCount: canceled,
			}, nil
		}
		canceled++
	}
	return &tradepb.CancelAllOrdersRsp{RetInfo: success(), CanceledCount: canceled}, nil
}

func (h *ExecutionServer) cancelStoredOrder(
	ctx context.Context,
	spaceID string,
	orderID string,
) error {
	record, err := h.Store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return err
	}
	state := orderdomain.State(record.State)
	if state.Terminal() {
		return nil
	}
	switch state {
	case orderdomain.Pending:
		_, err = h.Orders.DiscardPending(ctx, spaceID, orderID)
		return err
	case orderdomain.Submitting, orderdomain.SubmitUnknown:
		resolved, resolveErr := h.Orders.ResolveUnknown(ctx, spaceID, orderID)
		if resolveErr != nil {
			return resolveErr
		}
		if resolved.State.Terminal() {
			return nil
		}
		if resolved.State == orderdomain.Pending {
			_, err = h.Orders.DiscardPending(ctx, spaceID, orderID)
			return err
		}
	case orderdomain.Canceling, orderdomain.CancelUnknown:
		_, err = h.Orders.RecoverCancel(ctx, spaceID, orderID)
		return err
	}
	_, err = h.Orders.Cancel(ctx, spaceID, orderID)
	return err
}

func (h *ExecutionServer) SubmitTarget(
	ctx context.Context,
	req *tradepb.SubmitTargetReq,
) (*tradepb.SubmitTargetRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.SubmitTargetRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	targets := make([]execution.Target, 0, len(req.GetTargets()))
	for _, target := range req.GetTargets() {
		quantity, parseErr := shared.ParseDecimal(target.GetTargetQuantity())
		if parseErr != nil {
			return &tradepb.SubmitTargetRsp{RetInfo: invalidInfo(parseErr)}, nil
		}
		targets = append(targets, execution.Target{
			InstrumentID: target.GetInstrumentId(), Symbol: target.GetSymbol(),
			TargetQuantity: quantity,
		})
	}
	record, _, err := h.Targets.Accept(ctx, spaceID, execution.TargetIntent{
		ExecutionID: req.GetExecutionId(), StrategyRunID: req.GetStrategyRunId(),
		ExecutionBindingID: req.GetExecutionBindingId(),
		ExchangeAccountID:  req.GetExchangeAccountId(),
		CommandSequence:    req.GetCommandSequence(),
		NotAfter:           time.UnixMilli(req.GetNotAfter()).UTC(),
		DataRevision:       req.GetDataRevision(), Targets: targets,
	})
	return &tradepb.SubmitTargetRsp{
		RetInfo: errorInfo(err), Execution: targetToPB(record),
	}, nil
}

func (h *ExecutionServer) GetExecution(
	ctx context.Context,
	req *tradepb.GetExecutionReq,
) (*tradepb.GetExecutionRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetExecutionRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	record, err := h.Store.GetTargetExecution(ctx, spaceID, req.GetExecutionId())
	return &tradepb.GetExecutionRsp{
		RetInfo: errorInfo(err), Execution: targetToPB(record),
	}, nil
}

func (h *ExecutionServer) ListExecutions(
	ctx context.Context,
	req *tradepb.ListExecutionsReq,
) (*tradepb.ListExecutionsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListExecutionsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetExchangeAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.ListExecutionsRsp{RetInfo: errorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := h.Store.QueryTargetExecutions(
		ctx,
		spaceID,
		store.TargetExecutionQuery{
			ExchangeAccountID:  req.GetExchangeAccountId(),
			ExecutionBindingID: req.GetExecutionBindingId(),
			Status:             normalized(req.GetStatus()), Offset: page.offset, Limit: page.size,
		},
	)
	executions := make([]*tradepb.TargetExecution, 0, len(records))
	for _, record := range records {
		executions = append(executions, targetToPB(record))
	}
	return &tradepb.ListExecutionsRsp{
		RetInfo: errorInfo(err), Executions: executions,
		PageResult: pageResult(page, total),
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
	if _, err := h.Store.GetExchangeAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.ListOrdersRsp{RetInfo: errorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := h.Store.ListOrders(ctx, spaceID, store.OrderQuery{
		ExchangeAccountID: req.GetExchangeAccountId(), Symbol: req.GetSymbol(),
		State: normalized(req.GetState()), OnlyOpen: req.GetOnlyOpen(),
		StartTime: req.GetStartTime(), EndTime: req.GetEndTime(),
		Offset: page.offset, Limit: page.size,
	})
	orders := make([]*tradepb.Order, 0, len(records))
	for _, record := range records {
		orders = append(orders, orderToPB(record))
	}
	return &tradepb.ListOrdersRsp{
		RetInfo: errorInfo(err), Orders: orders, PageResult: pageResult(page, total),
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
	if _, err := h.Store.GetExchangeAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.ListFillsRsp{RetInfo: errorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	records, total, err := h.Store.ListFills(ctx, spaceID, store.FillQuery{
		ExchangeAccountID: req.GetExchangeAccountId(), OrderID: req.GetOrderId(),
		Symbol: req.GetSymbol(), StartTime: req.GetStartTime(),
		EndTime: req.GetEndTime(), Offset: page.offset, Limit: page.size,
	})
	fills := make([]*tradepb.Fill, 0, len(records))
	for _, record := range records {
		fills = append(fills, fillToPB(record))
	}
	return &tradepb.ListFillsRsp{
		RetInfo: errorInfo(err), Fills: fills, PageResult: pageResult(page, total),
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
		return &tradepb.ListPositionsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetExchangeAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.ListPositionsRsp{RetInfo: errorInfo(err)}, nil
	}
	records, err := h.Store.ListPositions(
		ctx,
		spaceID,
		req.GetExchangeAccountId(),
		req.GetSymbol(),
	)
	positions := make([]*tradepb.Position, 0, len(records))
	for _, record := range records {
		positions = append(positions, positionToPB(record))
	}
	return &tradepb.ListPositionsRsp{RetInfo: errorInfo(err), Positions: positions}, nil
}
