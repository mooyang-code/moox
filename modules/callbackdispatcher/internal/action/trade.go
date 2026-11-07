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
	// Status 是执行账户状态：ENABLED、DISABLED、ERROR。停用和出错的账户照常列出（最需要人工处理），对它们平仓会失败，核对会报仍有持有。
	Status string
	// Note 是 Trade 对这一项的标注，例如“取不到报价，零头只按最小数量判断”。
	Note string
}

// SkippedAccount 是 Trade 查持有者时出错、没能判断的执行账户（资金设置读不出来、读受管资产出错等）。
type SkippedAccount struct {
	TradingAccountID string
	Reason           string
}

// HoldersResult 是 ListInstrumentHolders 的结果。
type HoldersResult struct {
	Holders []Holder
	// Skipped 不能当作没有持有：平仓轮次要提醒人工确认，下架后核对也不能因为“没人持有”就通过。
	Skipped []SkippedAccount
	// Notes 是明确不算持有的说明，例如标的的计价币不是账户的结算币（清仓不会卖它）。
	Notes []string
	// InstrumentUnknown 为真表示 Trade 没有这个标的，这时 Holders 为空，不算失败。
	InstrumentUnknown bool
}

// Trade 操作（单标的下架清仓）的状态。
const (
	OperatorRunning   = "RUNNING"
	OperatorCompleted = "COMPLETED"
	OperatorFailed    = "FAILED"
	// OperatorPartial 表示清仓只完成了一部分：最近一笔被交易所拒绝、连续多笔没有成交，或套利底仓的对冲腿下架，原因写在 Message 里。
	// 同一个操作号再调会很快又得到 PARTIAL，所以要重试必须换新的操作号。
	OperatorPartial = "PARTIAL"
)

// OperatorAction 是 Trade 的一次操作（单标的下架清仓）的结果。
type OperatorAction struct {
	// ActionID 是 Trade 的操作号。
	ActionID string
	// Status 是 OperatorRunning、OperatorCompleted、OperatorFailed 或 OperatorPartial。
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

// BlockResult 是写入下架名单的结果。
type BlockResult struct {
	// InstrumentUnknown 为真表示 Trade 不认识这个标的（没有开通对应市场的账户）：照常写入名单，ListInstrumentHolders 返回空，没有持仓要处理。
	InstrumentUnknown bool
	// Created 为假表示标的本来就在名单里，没有改动。
	Created bool
}

// ErrInvalidRequest 表示 Trade 认为请求本身不合法（参数、市场不支持）：重试没有用。
var ErrInvalidRequest = errors.New("Trade 拒绝了请求参数")

// ErrTradeBusy 表示 Trade 暂时忙（同组合有别的清仓在跑，或拿不到组合锁）：稍后再试，不计入重试次数。
// 实现方把 Trade 的“忙”返回码翻译成它。
var ErrTradeBusy = errors.New("Trade 正忙，稍后再试")

// TradeClient 是系统动作调用 Trade 的接口，按交易计划 4.7 的四个接口加两个查询。
// 这里的类型是事件中心自己的，处理器不依赖 Trade 的 proto 包；真实实现在 internal/tradegateway，经网关调用 Trade 的 RPC。
type TradeClient interface {
	// BlockInstrument 把标的写进下架名单（全局，不分空间）。Trade 不认识这个标的时照常写入，ListInstrumentHolders 返回空。
	BlockInstrument(ctx context.Context, instrument InstrumentRef, reason, eventID string) (BlockResult, error)
	// UnblockInstrument 解除禁止。
	UnblockInstrument(ctx context.Context, instrument InstrumentRef) error
	// ListBlockedInstruments 返回当前下架名单；每次推进前用它确认标的还在名单里。
	ListBlockedInstruments(ctx context.Context) ([]InstrumentRef, error)
	// ListInstrumentHolders 跨空间列出策略持有该标的的组合账户；paperOnly 为真只返回模拟盘。
	ListInstrumentHolders(ctx context.Context, instrument InstrumentRef, paperOnly bool) (HoldersResult, error)
	// FlattenInstrument 发起单标的下架清仓，返回 Trade 的操作（操作号、当前状态）。Trade 忙时返回 ErrTradeBusy。
	FlattenInstrument(ctx context.Context, request FlattenRequest) (OperatorAction, error)
	// GetOperatorAction 查询一次操作的结果。
	GetOperatorAction(ctx context.Context, spaceID, operatorActionID string) (OperatorAction, error)
}
