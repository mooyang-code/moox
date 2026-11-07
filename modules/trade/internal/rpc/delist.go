package rpc

import (
	"context"
	"errors"
	"strings"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// FlattenInstrumentCommand 是对一个组合发起的单标的下架清仓：标的已经按交易所换算成内部标的 ID。
type FlattenInstrumentCommand struct {
	SpaceID          string
	ActionID         string
	LogicalAccountID string
	Exchange         string
	InstrumentID     string
	Reason           string
}

// FlattenInstrumentFunc 发起下架清仓。unblocked 为真表示标的已被解除禁止而提前结束。
type FlattenInstrumentFunc func(ctx context.Context, command FlattenInstrumentCommand) (action store.OperatorActionRecord, unblocked bool, err error)

// ReduceLogicalAccountFunc 发起按比例减仓。
type ReduceLogicalAccountFunc func(ctx context.Context, spaceID, actionID, logicalAccountID string, ratio shared.Decimal, reason string) (store.OperatorActionRecord, error)

func delistKey(exchange tradepb.Exchange, market tradepb.MarketType, symbol string) delistapp.Key {
	return delistapp.Key{
		Exchange:       string(exchangeFromPB(exchange)),
		MarketType:     string(marketFromPB(market)),
		ExchangeSymbol: symbol,
	}
}

// BlockInstrument 把标的写进下架名单。全局方法，不检查空间。
func (s *ConsoleServer) BlockInstrument(ctx context.Context, req *tradepb.BlockInstrumentReq) (*tradepb.BlockInstrumentRsp, error) {
	if err := validatePB(req); err != nil {
		return &tradepb.BlockInstrumentRsp{RetInfo: invalidInfo(err)}, nil
	}
	if s.Delist == nil {
		return &tradepb.BlockInstrumentRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	result, err := s.Delist.Block(ctx, delistKey(req.GetExchange(), req.GetMarketType(), req.GetExchangeSymbol()), req.GetReason(), req.GetEventId())
	return &tradepb.BlockInstrumentRsp{
		RetInfo: errorInfo(err), InstrumentUnknown: result.InstrumentUnknown, Created: result.Created,
	}, nil
}

// UnblockInstrument 解除禁止。全局方法，不检查空间。
func (s *ConsoleServer) UnblockInstrument(ctx context.Context, req *tradepb.UnblockInstrumentReq) (*tradepb.UnblockInstrumentRsp, error) {
	if err := validatePB(req); err != nil {
		return &tradepb.UnblockInstrumentRsp{RetInfo: invalidInfo(err)}, nil
	}
	if s.Delist == nil {
		return &tradepb.UnblockInstrumentRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	removed, err := s.Delist.Unblock(ctx, delistKey(req.GetExchange(), req.GetMarketType(), req.GetExchangeSymbol()))
	return &tradepb.UnblockInstrumentRsp{RetInfo: errorInfo(err), Removed: removed}, nil
}

// ListBlockedInstruments 列出下架名单。全局方法，不检查空间。
func (s *ConsoleServer) ListBlockedInstruments(ctx context.Context, req *tradepb.ListBlockedInstrumentsReq) (*tradepb.ListBlockedInstrumentsRsp, error) {
	if s.Delist == nil {
		return &tradepb.ListBlockedInstrumentsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	records, err := s.Delist.List(ctx)
	if err != nil {
		return &tradepb.ListBlockedInstrumentsRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ListBlockedInstrumentsRsp{RetInfo: success(), Instruments: make([]*tradepb.BlockedInstrument, 0, len(records))}
	for _, record := range records {
		rsp.Instruments = append(rsp.Instruments, &tradepb.BlockedInstrument{
			Exchange: exchangeToPB(record.Exchange), MarketType: marketToPB(record.MarketType), ExchangeSymbol: record.ExchangeSymbol,
			InstrumentId: record.InstrumentID, BaseAsset: record.BaseAsset, Reason: record.Reason,
			SourceEventId: record.SourceEventID, BlockedAt: record.BlockedAt,
		})
	}
	return rsp, nil
}

// ListInstrumentHolders 跨空间列出策略持有该标的的执行账户。全局方法，不检查空间；
// 没有持有者时 holders 是空数组（经网关的 JSON 序列化不会省略它）。
func (s *ConsoleServer) ListInstrumentHolders(ctx context.Context, req *tradepb.ListInstrumentHoldersReq) (*tradepb.ListInstrumentHoldersRsp, error) {
	if err := validatePB(req); err != nil {
		return &tradepb.ListInstrumentHoldersRsp{RetInfo: invalidInfo(err)}, nil
	}
	if s.Delist == nil {
		return &tradepb.ListInstrumentHoldersRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	result, err := s.Delist.Holders(ctx, delistKey(req.GetExchange(), req.GetMarketType(), req.GetExchangeSymbol()), req.GetPaperOnly())
	if err != nil {
		return &tradepb.ListInstrumentHoldersRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ListInstrumentHoldersRsp{
		RetInfo: success(), InstrumentUnknown: result.InstrumentUnknown,
		Holders: make([]*tradepb.InstrumentHolder, 0, len(result.Holders)),
	}
	for _, holder := range result.Holders {
		rsp.Holders = append(rsp.Holders, &tradepb.InstrumentHolder{
			SpaceId: holder.SpaceID, LogicalAccountId: holder.LogicalAccountID, TradingAccountId: holder.TradingAccountID,
			Quantity: holder.Quantity, Dust: holder.Dust, Paper: holder.Paper, Status: holder.Status, Note: holder.Note,
		})
	}
	for _, skipped := range result.SkippedAccounts {
		rsp.SkippedAccounts = append(rsp.SkippedAccounts, &tradepb.SkippedAccount{TradingAccountId: skipped.TradingAccountID, Reason: skipped.Reason})
	}
	rsp.Notes = result.Notes
	return rsp, nil
}

// FlattenInstrument 在调用时带的空间里，对组合中属于这个交易所的成员发起单标的下架清仓。
func (s *ConsoleServer) FlattenInstrument(ctx context.Context, req *tradepb.FlattenInstrumentReq) (*tradepb.FlattenInstrumentRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.FlattenInstrumentRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Delist == nil || s.FlattenInstrumentAction == nil || s.Store == nil {
		return &tradepb.FlattenInstrumentRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	// 先按空间读一次组合，保证只能操作自己空间的组合。
	if _, err := s.Store.GetLogicalAccount(ctx, spaceID, req.GetLogicalAccountId()); err != nil {
		return &tradepb.FlattenInstrumentRsp{RetInfo: errorInfo(err)}, nil
	}
	key := delistKey(req.GetExchange(), req.GetMarketType(), req.GetExchangeSymbol())
	instrumentID, err := s.Delist.ResolveInstrumentID(ctx, spaceID, req.GetLogicalAccountId(), key)
	if err != nil {
		return &tradepb.FlattenInstrumentRsp{RetInfo: errorInfo(err)}, nil
	}
	action, unblocked, err := s.FlattenInstrumentAction(ctx, FlattenInstrumentCommand{
		SpaceID: spaceID, ActionID: req.GetActionId(), LogicalAccountID: req.GetLogicalAccountId(),
		Exchange: key.Exchange, InstrumentID: instrumentID, Reason: req.GetReason(),
	})
	if errors.Is(err, operatorapp.ErrLogicalAccountBusy) {
		// 忙：别的清仓在跑或拿不到组合锁。不返回动作（返回的可能是别人的），事件中心稍后再试，不计入重试次数。
		return &tradepb.FlattenInstrumentRsp{RetInfo: errorInfo(err), Busy: true}, nil
	}
	return &tradepb.FlattenInstrumentRsp{RetInfo: errorInfo(err), Action: operatorActionToPB(action), Unblocked: unblocked}, nil
}

// ReduceLogicalAccount 按比例减仓：先暂停组合、撤全部订单，再把现货和合约同比例减掉。
func (s *ConsoleServer) ReduceLogicalAccount(ctx context.Context, req *tradepb.ReduceLogicalAccountReq) (*tradepb.ReduceLogicalAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ReduceLogicalAccountRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.ReduceAccount == nil {
		return &tradepb.ReduceLogicalAccountRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	ratio, err := shared.ParseDecimal(strings.TrimSpace(req.GetReduceRatio()))
	if err != nil {
		return &tradepb.ReduceLogicalAccountRsp{RetInfo: invalidInfo(err)}, nil
	}
	action, err := s.ReduceAccount(ctx, spaceID, req.GetActionId(), req.GetLogicalAccountId(), ratio, req.GetReason())
	return &tradepb.ReduceLogicalAccountRsp{RetInfo: errorInfo(err), Action: operatorActionToPB(action)}, nil
}
