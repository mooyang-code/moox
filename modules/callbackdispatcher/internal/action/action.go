// Package action 是系统动作的框架：处理器的注册点，以及系统动作调用 Trade 用的客户端接口。
//
// 第一版的系统动作“卖出策略持有的下架币”（执行计划第 5 节，任务 C6）依赖交易计划 T27 的 Trade 接口，本轮没有实现：
// 这里只定义好接口。C6 的做法是：
//  1. 在 trade.go 的 TradeClient 之上实现处理器，经网关（配置里的 gateway_client）调用 Trade 的 BlockInstrument、
//     UnblockInstrument、ListBlockedInstruments、ListInstrumentHolders、FlattenInstrument、GetOperatorAction；
//  2. 在 bootstrap 里用 Registry.Register(store.SystemActionSellDelistedHoldings, handler) 注册；
//  3. 处理器的 Step 每次推进一步，用 Outcome.NextRunAt 记下下一次该推进的时间（等到下架前 24 小时、2 小时、下架后核对），
//     轮次和步骤存在 t_delist_actions（Store.UpsertDelistAction）；
//  4. 发现“当前有策略持有”时调用 telemetry.NoteDelistDetected，产生 delist_detected 关注项。
//
// 没有注册处理器的系统动作，工作线程不会领取对应的执行记录（Store.ClaimDue 按已注册的动作过滤），
// 记录停在待处理，处理器注册之后立即被处理，不会丢，也不会空转。
package action

import (
	"context"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
)

// Outcome 是系统动作推进一步之后的状态。
type Outcome struct {
	// Status 是 store.StatusPending（还没完，等 NextRunAt 再推进）、StatusSucceeded、StatusFailed 或 StatusCanceled。
	Status string
	// NextRunAt 在 Status 为待处理时必填：下一次该推进的时间。
	NextRunAt time.Time
	// Error 是失败原因，或者推进过程中值得记下的说明。
	Error string
}

// Handler 处理一种系统动作。处理器自己负责幂等：同一条执行记录的同一步可能被重复推进（进程重启、重试）。
type Handler interface {
	// Step 推进一条执行记录一步。返回错误表示这一步暂时没做成，工作线程稍后重试；
	// 需要停下来（用完重试次数、核对失败）时返回 Status 为 StatusFailed 的 Outcome。
	Step(ctx context.Context, execution store.Execution, now time.Time) (Outcome, error)
	// OnCancel 在用户取消一条还没结束的记录时善后（下架：解除禁止买入）。返回错误则取消不生效，用户可以重试。
	OnCancel(ctx context.Context, execution store.Execution) error
}

// Registry 是系统动作到处理器的映射。
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry 创建空的注册表。
func NewRegistry() *Registry {
	return &Registry{handlers: map[string]Handler{}}
}

// Register 注册一种系统动作的处理器。
func (r *Registry) Register(systemAction string, handler Handler) {
	r.handlers[systemAction] = handler
}

// Handler 返回系统动作的处理器。
func (r *Registry) Handler(systemAction string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	handler, ok := r.handlers[systemAction]
	return handler, ok
}

// Actions 返回已经注册了处理器的系统动作。
func (r *Registry) Actions() []string {
	if r == nil {
		return nil
	}
	actions := make([]string, 0, len(r.handlers))
	for action := range r.handlers {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	return actions
}
