package action

import (
	"context"
	"errors"
)

// InstrumentRef 用（交易所、市场、交易所代码）定位一个标的。币安下架计划只给 FOOUSDT 这样的交易所代码，
// 内部标的 ID 由 Trade 按标的表换算。
type InstrumentRef struct {
	// Exchange 是小写交易所代码：binance、okx。
	Exchange string
	// Market 是 SPOT 或 SWAP。
	Market         string
	ExchangeSymbol string
}

// Holder 是持有某个标的的一个（空间、组合账户）。零头（Dust）不算持有。
type Holder struct {
	SpaceID          string
	LogicalAccountID string
	// Exchange 是持仓所在的交易所；混合交易所的组合里，下架清仓只平这个交易所的成员。
	Exchange string
	Quantity string
	Dust     bool
	// Paper 为真表示模拟盘组合。
	Paper bool
}

// OperatorAction 是 Trade 的一次操作（单标的下架清仓）的结果。
type OperatorAction struct {
	// Status 是 RUNNING、COMPLETED、FAILED 或 PARTIAL。
	Status string
	// Unblocked 为真表示 Trade 发现标的已经被解除禁止而提前结束了清仓（不算失败，也不要重试）。
	Unblocked bool
	Message   string
}

// FlattenRequest 是对一个组合账户发起的单标的下架清仓。
type FlattenRequest struct {
	// ActionID 在建动作时生成并存下，重试沿用同一个，Trade 据此幂等。
	ActionID         string
	SpaceID          string
	LogicalAccountID string
	Instrument       InstrumentRef
	Reason           string
}

// ErrTradeBusy 表示 Trade 暂时忙（同组合有别的清仓在跑，或拿不到组合锁）：稍后再试，不计入重试次数。
// 实现方把 Trade 的“忙”返回码翻译成它。
var ErrTradeBusy = errors.New("Trade 正忙，稍后再试")

// TradeClient 是系统动作调用 Trade 的接口，按交易计划 4.7 的四个接口加两个查询。
// 这里的类型是事件中心自己的：T27 的 RPC 还不存在，也避免依赖 Trade 的 proto 包。C6 写一个适配器，
// 把它们翻译成经网关调用 Trade 的 RPC。
type TradeClient interface {
	// BlockInstrument 把标的写进下架名单（全局，不分空间）。Trade 不认识这个标的时照常写入，ListInstrumentHolders 返回空。
	BlockInstrument(ctx context.Context, instrument InstrumentRef, reason, eventID string) error
	// UnblockInstrument 解除禁止。
	UnblockInstrument(ctx context.Context, instrument InstrumentRef) error
	// ListBlockedInstruments 返回当前下架名单；每次推进前用它确认标的还在名单里。
	ListBlockedInstruments(ctx context.Context) ([]InstrumentRef, error)
	// ListInstrumentHolders 跨空间列出策略持有该标的的组合账户；paperOnly 为真只返回模拟盘。
	ListInstrumentHolders(ctx context.Context, instrument InstrumentRef, paperOnly bool) ([]Holder, error)
	// FlattenInstrument 发起单标的下架清仓，返回 Trade 的操作号。Trade 忙时返回 ErrTradeBusy。
	FlattenInstrument(ctx context.Context, request FlattenRequest) (operatorActionID string, err error)
	// GetOperatorAction 查询一次操作的结果。
	GetOperatorAction(ctx context.Context, spaceID, operatorActionID string) (OperatorAction, error)
}
