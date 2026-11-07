// Package delist 是系统动作“卖出策略持有的下架币”的处理器（执行计划第 5 节，任务 C6）。
//
// 处理器不在内存里留状态：每次 Step 都从事件内容（标的、下架时间 T、范围）和 t_delist_actions 里已存的步骤推导出现在该做什么，
// 时间只看传进来的 now。所以进程重启、卡住后被放回、下架时间被合并更新，都只是“用新的输入再推导一次”。
//
// 时间线（T 是下架时间）：
//  1. 发现就调 BlockInstrument 禁止买入（轮次 0），之后每次推进前先用 ListBlockedInstruments 确认标的还在名单里，被手工解除就停止并置为已取消；
//  2. T−24 小时：按（空间、组合账户）聚合持有者，逐个调 FlattenInstrument（轮次 1），每 30 秒查一次结果；
//  3. T−2 小时：复查（轮次 2），再找一次持有者，仍有持有的用新的操作号再平一轮；轮次之间互不阻挡；
//  4. T：所有平仓一律停下，不再下单；T 之后 10 分钟核对（轮次 3），仍有持有的置为失败并告警，零头只告警一次。
//
// 并发：同一标的的“写名单 + 复查记录状态”与 OnCancel 的“查别的未结束执行 + 解除禁止”靠进程内的按标的锁串行；
// 锁只包这两段，查持有者、平仓、轮询都不在锁里，取消不会排在 Trade 的慢调用后面。
package delist

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// pollInterval 是查平仓结果、Trade 忙时再试的间隔；忙不计入重试次数。
	pollInterval = 30 * time.Second
	// maxRetries 是平仓失败（FAILED、调用出错）的重试上限，计数的重试按指数退避，用完这个组合这一轮置为失败。
	maxRetries = 10
	// maxBackoff 是重试间隔的上限。
	maxBackoff = 30 * time.Minute
	// transientRetry 是 Trade 暂时不可达（读名单、查持有者出错）时再试的间隔。
	transientRetry = time.Minute
	// tradeTimeout 是调用 Trade 的时限。OnCancel 的 ctx 没有截止时间，必须自己加；Step 的 ctx 总共 5 分钟。
	tradeTimeout = 30 * time.Second
	// minStepBudget 是 Step 的剩余时间低于它时不再发起新的 Trade 调用，剩下的留给下一次推进。
	minStepBudget = 45 * time.Second
)

// Handler 是下架处理器。
type Handler struct {
	Store *store.Store
	Trade action.TradeClient
	// Now 为空时用系统时钟；测试注入模拟时钟。
	Now func() time.Time
	// TradeTimeout 是单次调用 Trade 的时限，为 0 时用 30 秒。
	TradeTimeout time.Duration

	locks keyedMutex
	// loadExecution 为空时读库；测试用它模拟“写名单后复查记录状态失败”。
	loadExecution func(ctx context.Context, executionID string) (store.Execution, error)
}

var _ action.Handler = (*Handler)(nil)

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) tradeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := h.TradeTimeout
	if timeout <= 0 {
		timeout = tradeTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// keyedMutex 按键加互斥锁，没人用的键会被清掉。
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*lockEntry
}

type lockEntry struct {
	mutex sync.Mutex
	refs  int
}

// Lock 加锁并返回解锁函数。
func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*lockEntry{}
	}
	entry := k.locks[key]
	if entry == nil {
		entry = &lockEntry{}
		k.locks[key] = entry
	}
	entry.refs++
	k.mu.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// OnCancel 在记录已被取消之后善后：解除禁止买入。幂等；可能被快速连点两次而同时执行，靠按标的的锁串行。
func (h *Handler) OnCancel(ctx context.Context, execution store.Execution) error {
	tgt, err := parseTarget(execution)
	if err != nil {
		// 事件内容坏了就从没写过名单，没有要解除的。
		log.WarnContextf(ctx, "取消下架记录 %s：%v，没有要解除的禁止", execution.ExecutionID, err)
		return nil
	}
	unlock := h.locks.Lock(tgt.subject())
	defer unlock()
	steps, err := h.Store.ListDelistActions(ctx, execution.ExecutionID)
	if err != nil {
		return fmt.Errorf("读取下架步骤失败: %w", err)
	}
	return h.release(ctx, execution, tgt, hasBlockStep(steps))
}

// hasBlockStep 报告这条记录有没有禁止买入那一行：没有说明它从来没有尝试过写名单。
// 那一行在调用 Trade 之前就存下，所以“Trade 已写入但响应丢了、进程退出”之类的情况下，这一行也在。
func hasBlockStep(steps []store.DelistAction) bool {
	for _, step := range steps {
		if step.Kind == store.DelistKindBlock && step.Round == RoundBlock {
			return true
		}
	}
	return false
}

// blockOwnerMarker 是写进名单原因里的执行记录标记。名单遇到重复写入时保留第一个写入者的原因，所以名单里这一项的原因带着谁的标记，
// 就说明是谁写的；不能靠写名单时 Trade 返回的 created 记：写的响应丢了再重试，拿到的就是 created=false。
// 只按执行记录 ID 匹配（原因里的下架时间在改期后会变）。
func blockOwnerMarker(executionID string) string {
	return "事件中心执行记录 " + executionID + "）"
}

// release 解除禁止买入。调用时必须已持有这个标的的锁。attempted 为假表示这条记录从没尝试过写名单。
//   - 真实下架（ALL）：尝试过写名单就一律解除。同标的的演练还没结束时，它下一次推进前查名单发现不在了会自己停下，代价只是一次模拟盘演练；
//     不解除的话，已发起的平仓会继续卖（Trade 只在解除禁止时才停下清仓）。
//   - 演练（PAPER_ONLY）：只解除“自己写进名单的那一条”——名单里这一项的原因带着自己的执行记录 ID，并且同标的没有未取消的真实下架
//     （含已结束的，结束的真实下架按设计保留名单）。名单里没有这一项就没有要解除的。否则演练的结束或取消会把真实下架的名单解除，
//     持有这个币的组合就卡在被禁止里。
//
// Trade 拒绝参数（ErrInvalidRequest）说明名单里本来就不会有它，当作没有要解除的，不能让记录永远卡在善后上。
func (h *Handler) release(ctx context.Context, execution store.Execution, tgt target, attempted bool) error {
	if !attempted {
		return nil
	}
	if tgt.PaperOnly {
		owned, err := h.ownsListedBlock(ctx, execution, tgt)
		if err != nil {
			return err
		}
		if !owned {
			return nil
		}
		others, err := h.Store.CountNonCanceledAllBySubject(ctx, tgt.subject(), execution.ExecutionID)
		if err != nil {
			return fmt.Errorf("查同一标的的真实下架失败: %w", err)
		}
		if others > 0 {
			return nil
		}
	}
	callCtx, cancel := h.tradeCtx(ctx)
	defer cancel()
	if err := h.Trade.UnblockInstrument(callCtx, tgt.Instrument); err != nil && !errors.Is(err, action.ErrInvalidRequest) {
		return fmt.Errorf("解除禁止买入失败: %w", err)
	}
	return nil
}

// ownsListedBlock 查名单里这个标的的这一项是不是这条记录写的。
func (h *Handler) ownsListedBlock(ctx context.Context, execution store.Execution, tgt target) (bool, error) {
	callCtx, cancel := h.tradeCtx(ctx)
	defer cancel()
	blocked, err := h.Trade.ListBlockedInstruments(callCtx)
	if err != nil {
		if errors.Is(err, action.ErrInvalidRequest) {
			return false, nil
		}
		return false, fmt.Errorf("读取下架名单失败: %w", err)
	}
	for _, item := range blocked {
		if normalizeRef(item.Instrument) == tgt.Instrument {
			return strings.Contains(item.Reason, blockOwnerMarker(execution.ExecutionID)), nil
		}
	}
	return false, nil
}

// Step 推进一条下架执行记录。
func (h *Handler) Step(ctx context.Context, execution store.Execution, now time.Time) (action.Outcome, error) {
	tgt, err := parseTarget(execution)
	if err != nil {
		return action.Outcome{Status: store.StatusFailed, Error: err.Error()}, nil
	}
	steps, err := h.Store.ListDelistActions(ctx, execution.ExecutionID)
	if err != nil {
		return action.Outcome{}, fmt.Errorf("读取下架步骤失败: %w", err)
	}
	r := &run{h: h, execution: execution, target: tgt, now: now, steps: map[stepKey]store.DelistAction{}}
	for _, step := range steps {
		r.steps[keyOf(step)] = step
	}
	if tgt.PaperOnly {
		if block, ok := r.steps[stepKey{Round: RoundBlock, Kind: store.DelistKindBlock}]; ok && block.ReleaseVerdict != "" {
			if block.PlannedDelistAt == tgt.DelistAt.UnixMilli() {
				// 演练核对已有结论、只剩解除没做成：不再走整条时间线（先查名单会发现名单里已经没有这一项而把记录置为已取消，结论就丢了），
				// 也不重新核对，只重试解除，成功后返回已存下的结论。
				return r.retryRelease(ctx, block)
			}
			// 结论是按别的下架时间得出的：重试解除期间同标的又来了一次演练登记、下架时间不同，合并进了这条记录。
			// 旧结论作废，清掉上一次演练的步骤，重新写名单、按新的下架时间重走时间线（名单可能已被解除，不能当作手工解除）。
			r.restart = true
		}
	}
	outcome, err := r.step(ctx)
	if err != nil || !tgt.PaperOnly || r.stopped || (outcome.Status != store.StatusSucceeded && outcome.Status != store.StatusFailed) {
		return outcome, err
	}
	// 演练（PAPER_ONLY）的标的是真实存在、并不会下架的：记录一结束就解除禁止，否则全局的禁止会一直留着，实盘再也买不了它，
	// 而已结束的记录不能再取消。release 只解除自己写的那一项，同标的还有未取消的真实下架时不解除。真实下架结束后名单保留（标的确实下架了）。
	unlock := h.locks.Lock(tgt.subject())
	defer unlock()
	block, hasBlock := r.steps[stepKey{Round: RoundBlock, Kind: store.DelistKindBlock}]
	if hasBlock && !block.SelfReleased {
		// 解除之前先记下“本记录自己解除了名单”：之后记录被重新打开（这一步进行中合并了新的登记、核对失败后重新执行），
		// 下一次推进查名单发现没有这一项时，知道是自己解除的，重新写名单，而不是当作手工解除把记录置为已取消。
		block.SelfReleased = true
		if err := r.save(ctx, block); err != nil {
			return action.Outcome{}, err
		}
	}
	if releaseErr := h.release(ctx, execution, tgt, hasBlock); releaseErr != nil {
		// 记下结论和它对应的下架时间：之后每次重试只重试解除；下架时间被合并改了，结论就作废。
		if hasBlock {
			block.ReleaseVerdict, block.Error, block.PlannedDelistAt = outcome.Status, outcome.Error, tgt.DelistAt.UnixMilli()
			if err := r.save(ctx, block); err != nil {
				log.WarnContextf(ctx, "记录演练解除待重试的结论失败: %v", err)
			}
		}
		r.problem("演练结束后解除禁止买入失败，稍后重试: %v", releaseErr)
		r.wakeAt(now.Add(transientRetry))
		return r.pending(), nil
	}
	return outcome, nil
}

// retryRelease 只重试演练结束后的解除；成功后清除“待重试解除”的标记，返回已存下的结论。
func (r *run) retryRelease(ctx context.Context, block store.DelistAction) (action.Outcome, error) {
	unlock := r.h.locks.Lock(r.target.subject())
	defer unlock()
	if err := r.h.release(ctx, r.execution, r.target, true); err != nil {
		r.problem("演练结束后解除禁止买入失败，稍后重试: %v", err)
		r.wakeAt(r.now.Add(transientRetry))
		return r.pending(), nil
	}
	verdict, message := block.ReleaseVerdict, block.Error
	block.ReleaseVerdict, block.Error = "", ""
	if err := r.save(ctx, block); err != nil {
		return action.Outcome{}, err
	}
	if verdict == store.StatusFailed {
		return action.Outcome{Status: store.StatusFailed, Error: message}, nil
	}
	return action.Outcome{Status: store.StatusSucceeded}, nil
}

// run 是一次 Step 的状态：已存的步骤、这次发现的问题和下一次该醒来的时间。
type run struct {
	h         *Handler
	execution store.Execution
	target    target
	now       time.Time
	steps     map[stepKey]store.DelistAction
	// wake 是下一次该推进的时间，取各步骤要求的最早的一个。
	wake time.Time
	// problems 是这次推进遇到的暂时性问题（Trade 不可达、读写名单或查询出错），原样带给执行记录的失败说明；
	// 工作线程按它是否为空累计连续暂时失败次数。Trade 忙、别的操作号正在平属于正常等待，不放进来，只写在那一行上。
	problems []string
	// stopped 为真表示执行记录已经被取消：不再发起新的 Trade 调用，直接返回。
	stopped bool
	// restart 为真表示演练结束后又被重新打开（见 Step 和 step）：这次推进清掉上一次演练的各轮步骤，重新写名单，按现在的下架时间重走时间线。
	restart bool
}

func (r *run) wakeAt(at time.Time) {
	if r.wake.IsZero() || at.Before(r.wake) {
		r.wake = at
	}
}

func (r *run) problem(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

// pending 返回“还没完，等 wake 再推进”的结果。
func (r *run) pending() action.Outcome {
	wake := r.wake
	if wake.IsZero() || wake.Before(r.now) {
		wake = r.now.Add(time.Second)
	}
	return action.Outcome{Status: store.StatusPending, NextRunAt: wake, Error: joinLines(r.problems)}
}

// save 写一个步骤并记进本次状态。
func (r *run) save(ctx context.Context, step store.DelistAction) error {
	step.ExecutionID = r.execution.ExecutionID
	step.UpdatedAt = r.h.now().UnixMilli()
	if err := r.h.Store.UpsertDelistAction(ctx, step); err != nil {
		return fmt.Errorf("保存下架步骤失败: %w", err)
	}
	r.steps[keyOf(step)] = step
	return nil
}

func (r *run) step(ctx context.Context) (action.Outcome, error) {
	blockKey := stepKey{Round: RoundBlock, Kind: store.DelistKindBlock}
	if block, ok := r.steps[blockKey]; ok && block.Status == store.DelistSucceeded && !r.restart {
		// 每次推进前先查名单：已被手工解除的（误报、交易所改期），停止后续所有步骤。
		callCtx, cancel := r.h.tradeCtx(ctx)
		blocked, err := r.h.Trade.ListBlockedInstruments(callCtx)
		cancel()
		if err != nil {
			r.problem("读取下架名单失败，稍后重试: %v", err)
			r.wakeAt(r.now.Add(transientRetry))
			return r.pending(), nil
		}
		listed := slices.ContainsFunc(blocked, func(item action.BlockedInstrument) bool { return normalizeRef(item.Instrument) == r.target.Instrument })
		switch {
		case !listed && !block.SelfReleased:
			return action.Outcome{Status: store.StatusCanceled, Error: "禁止买入已被手工解除，下架处理已停止"}, nil
		case !listed:
			// 名单是这条演练结束时自己解除的，之后记录又被重新打开（合并了新的登记）：清掉上一次演练的步骤，重新写名单，按新的下架时间重走时间线。
			r.restart = true
		case block.SelfReleased:
			// 名单里还有这一项（解除没生效，或别人写的）：之后再不见了就是被手工解除的。
			block.SelfReleased = false
			if err := r.save(ctx, block); err != nil {
				return action.Outcome{}, err
			}
		}
	}
	if r.restart {
		if err := r.clearPreviousRun(ctx); err != nil {
			return action.Outcome{}, err
		}
	}
	if block, ok := r.steps[blockKey]; !ok || block.Status != store.DelistSucceeded || r.restart {
		outcome, finished, err := r.ensureBlocked(ctx)
		if finished || err != nil {
			return outcome, err
		}
	}

	if !r.now.Before(r.target.DelistAt) {
		return r.afterDelist(ctx)
	}
	for _, round := range []struct {
		number int
		due    time.Time
	}{{RoundFlatten, r.target.flattenAt()}, {RoundReview, r.target.reviewAt()}} {
		if err := r.flattenRound(ctx, round.number, round.due); err != nil {
			return action.Outcome{}, err
		}
		if r.stopped {
			return action.Outcome{Status: store.StatusCanceled}, nil
		}
	}
	// 到了下架时间平仓一律停下。
	r.wakeAt(r.target.DelistAt)
	return r.pending(), nil
}

// clearPreviousRun 删掉上一次演练的各轮步骤（平仓、复查、核对），只留禁止买入那一行：新的一次演练从头开始，
// 上一次留下的失败、只平了一部分的行也不会让关注项一直提醒到新的核对。
func (r *run) clearPreviousRun(ctx context.Context) error {
	if err := r.h.Store.DeleteDelistStepsExceptBlock(ctx, r.execution.ExecutionID); err != nil {
		return fmt.Errorf("清除上一次演练的步骤失败: %w", err)
	}
	for key := range r.steps {
		if key.Round != RoundBlock || key.Kind != store.DelistKindBlock {
			delete(r.steps, key)
		}
	}
	return nil
}

// ensureBlocked 写入下架名单。finished 为真时 outcome 就是这一步的最终结果（已取消、已结束、失败或等稍后重试）；
// 否则名单已写好，继续往下推进。
func (r *run) ensureBlocked(ctx context.Context) (outcome action.Outcome, finished bool, err error) {
	unlock := r.h.locks.Lock(r.target.subject())
	unlocked := false
	releaseLock := func() {
		if !unlocked {
			unlocked = true
			unlock()
		}
	}
	defer releaseLock()

	blockKey := stepKey{Round: RoundBlock, Kind: store.DelistKindBlock}
	step := r.steps[blockKey]
	step.Round, step.Kind, step.Exchange = RoundBlock, store.DelistKindBlock, r.target.Instrument.Exchange
	step.Reason = blockReason(r.execution, r.target)
	// 重新写名单就不再是“自己解除过”，旧的解除待重试结论（连同它对应的下架时间）也作废。
	step.SelfReleased, step.ReleaseVerdict, step.PlannedDelistAt = false, "", 0

	// 调用 Trade 之前先把这一行存下：Trade 可能已经写入而响应丢了（超时、网关抖动），或者写入之后进程就退出了，
	// 之后取消和演练结束靠这一行知道“这条记录尝试过写名单”，再按名单里原因带的执行记录 ID 判断是不是自己写的。
	step.Status, step.Error, step.Result = store.DelistPending, "", "正在写入禁止名单"
	step.Attempts++
	if err := r.save(ctx, step); err != nil {
		return action.Outcome{}, false, err
	}

	callCtx, cancel := r.h.tradeCtx(ctx)
	result, blockErr := r.h.Trade.BlockInstrument(callCtx, r.target.Instrument, step.Reason, r.execution.EventID)
	cancel()
	if blockErr != nil {
		step.Status, step.Error = store.DelistPending, blockErr.Error()
		if errors.Is(blockErr, action.ErrInvalidRequest) {
			step.Status = store.DelistFailed
			if err := r.save(ctx, step); err != nil {
				return action.Outcome{}, false, err
			}
			return action.Outcome{Status: store.StatusFailed, Error: "写入下架名单被 Trade 拒绝: " + blockErr.Error()}, true, nil
		}
		if err := r.save(ctx, step); err != nil {
			return action.Outcome{}, false, err
		}
		r.problem("写入下架名单失败，稍后重试: %v", blockErr)
		r.wakeAt(r.now.Add(transientRetry))
		return r.pending(), true, nil
	}

	// 记下“名单已经写了”（还不算成功），再复查记录状态。
	step.Status, step.Error = store.DelistPending, ""
	step.Result = "名单已写入，正在确认执行记录状态"
	if err := r.save(ctx, step); err != nil {
		return action.Outcome{}, false, err
	}

	// 写名单之后复查记录状态：取消发生在 Step 执行期间时，名单是在取消之后才写进去的，必须补一次解除。
	fresh, getErr := r.recheck(ctx)
	if getErr != nil {
		// 没法确认记录状态：不记步骤成功，下次推进会重新写（幂等）并再复查。记录要是其实已经取消，Step 的结果会被丢掉，
		// 没有下一次推进，所以顺手把“善后未完成”标记写回库（记录没取消时什么也不做），用户能看到“重试善后”。
		message := "取消后的善后可能没做完：写名单后复查记录状态失败（" + getErr.Error() + "），请点“重试善后”"
		if _, markErr := r.h.Store.MarkCleanupPendingIfCanceled(context.WithoutCancel(ctx), r.execution.ExecutionID, message, r.h.now().UnixMilli()); markErr != nil {
			log.WarnContextf(ctx, "记录善后可能没做完失败: %v", markErr)
		}
		r.problem("复查执行记录状态失败，稍后重试: %v", getErr)
		r.wakeAt(r.now.Add(transientRetry))
		return r.pending(), true, nil
	}
	if fresh.Status == store.StatusCanceled {
		if releaseErr := r.h.release(ctx, fresh, r.target, true); releaseErr != nil {
			// 记录已经不是执行中，SaveResult 会丢掉结果：直接把“善后未完成”标记写回库，用户才看得到“重试善后”。
			message := "取消后的善后失败：" + releaseErr.Error()
			if _, markErr := r.h.Store.MarkCleanupPendingIfCanceled(context.WithoutCancel(ctx), fresh.ExecutionID, message, r.h.now().UnixMilli()); markErr != nil {
				log.ErrorContextf(ctx, "记录取消善后失败的原因失败: %v", markErr)
			}
		}
		r.stopped = true
		return action.Outcome{Status: store.StatusCanceled}, true, nil
	}

	step.Status, step.Error = store.DelistSucceeded, ""
	switch {
	case result.InstrumentUnknown:
		step.Result = "Trade 没有这个标的（没有开通对应市场的账户），已写入禁止名单，没有持仓要处理"
	case result.Created:
		step.Result = "已写入禁止名单，策略不能再买入"
	default:
		step.Result = "标的已在禁止名单里"
	}
	if err := r.save(ctx, step); err != nil {
		return action.Outcome{}, false, err
	}
	if result.InstrumentUnknown {
		return action.Outcome{Status: store.StatusSucceeded}, true, nil
	}
	// 锁只包“写名单 + 复查”：查持有者是慢调用，不能让取消排在它后面。
	releaseLock()
	r.noteDetected(ctx)
	return action.Outcome{}, false, nil
}

// recheck 读最新的执行记录，失败时短暂重试几次。
func (r *run) recheck(ctx context.Context) (store.Execution, error) {
	var (
		fresh store.Execution
		err   error
	)
	for attempt := 0; attempt < 3; attempt++ {
		if r.h.loadExecution != nil {
			fresh, err = r.h.loadExecution(ctx, r.execution.ExecutionID)
		} else {
			fresh, err = r.h.Store.GetExecution(ctx, r.execution.ExecutionID)
		}
		if err == nil {
			return fresh, nil
		}
		select {
		case <-ctx.Done():
			return fresh, err
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fresh, err
}

// noteDetected 发现下架时，用只读的 ListInstrumentHolders 查当前有没有策略持有，有就产生 delist_detected 关注项。查不到不影响处理。
func (r *run) noteDetected(ctx context.Context) {
	result, err := r.listHolders(ctx)
	if err != nil {
		log.WarnContextf(ctx, "下架 %s：查持有者失败，不产生发现通知: %v", r.target.subject(), err)
		return
	}
	live := filterLive(result.Holders)
	if len(live) == 0 {
		// 没查到持有者，但有账户没能判断：不能当作没有持有，同样提醒一次，免得漏掉。
		if len(result.Skipped) > 0 {
			telemetry.NoteDelistDetected(fmt.Sprintf("%s 将于 %s 下架，%d 个账户没能判断是否持有，请人工确认", r.target.subject(), r.target.DelistAt.UTC().Format("2006-01-02 15:04 UTC"), len(result.Skipped)), r.now)
		}
		return
	}
	telemetry.NoteDelistDetected(fmt.Sprintf("%s 将于 %s 下架，%d 个组合持有", r.target.subject(), r.target.DelistAt.UTC().Format("2006-01-02 15:04 UTC"), len(groupHolders(live))), r.now)
}

// listHolders 查持有者。范围是 PAPER_ONLY 时自己再过滤一遍持有者，不依赖 Trade 守约。
func (r *run) listHolders(ctx context.Context) (action.HoldersResult, error) {
	callCtx, cancel := r.h.tradeCtx(ctx)
	defer cancel()
	result, err := r.h.Trade.ListInstrumentHolders(callCtx, r.target.Instrument, r.target.PaperOnly)
	if err != nil || !r.target.PaperOnly {
		return result, err
	}
	paper := result.Holders[:0:0]
	for _, holder := range result.Holders {
		if holder.Paper {
			paper = append(paper, holder)
		}
	}
	result.Holders = paper
	return result, nil
}

// describeSkipped 列出 Trade 没能判断的账户：它们不能当作没有持有，要人工确认。
func describeSkipped(skipped []action.SkippedAccount) string {
	if len(skipped) == 0 {
		return ""
	}
	parts := make([]string, 0, len(skipped))
	for _, item := range skipped {
		parts = append(parts, fmt.Sprintf("%s（%s）", item.TradingAccountID, item.Reason))
	}
	return fmt.Sprintf("有 %d 个账户没能判断持仓，需要人工确认：%s", len(skipped), strings.Join(parts, "、"))
}

// account 是一个（空间、组合账户）。
type account struct {
	SpaceID          string
	LogicalAccountID string
}

// groupHolders 把每个执行账户一行的持有者按（空间、组合账户）聚合，按名字排序。
func groupHolders(holders []action.Holder) []account {
	seen := map[account]bool{}
	var accounts []account
	for _, holder := range holders {
		key := account{SpaceID: holder.SpaceID, LogicalAccountID: holder.LogicalAccountID}
		if !seen[key] {
			seen[key] = true
			accounts = append(accounts, key)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].SpaceID != accounts[j].SpaceID {
			return accounts[i].SpaceID < accounts[j].SpaceID
		}
		return accounts[i].LogicalAccountID < accounts[j].LogicalAccountID
	})
	return accounts
}

func describeDust(holders []action.Holder) string {
	var parts []string
	for _, holder := range holders {
		if holder.Dust {
			parts = append(parts, fmt.Sprintf("%s/%s 数量 %s", holder.SpaceID, holder.LogicalAccountID, holder.Quantity))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "零头（不处理）：" + strings.Join(parts, "、")
}

// lowBudget 报告这次 Step 的剩余时间是否已经不够再发起 Trade 调用。
func (r *run) lowBudget(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	return (ok && time.Until(deadline) < minStepBudget) || ctx.Err() != nil
}

// roundRows 返回一轮里按 plannedAt 规划的各组合的行（不含汇总行），按名字排序。改期重开之前留下的旧行不算。
func (r *run) roundRows(round int, plannedAt int64) []store.DelistAction {
	var rows []store.DelistAction
	for key, step := range r.steps {
		if key.Round == round && key.Kind == store.DelistKindFlatten && (key.SpaceID != "" || key.LogicalAccountID != "") && step.PlannedDelistAt == plannedAt {
			rows = append(rows, step)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SpaceID != rows[j].SpaceID {
			return rows[i].SpaceID < rows[j].SpaceID
		}
		return rows[i].LogicalAccountID < rows[j].LogicalAccountID
	})
	return rows
}

// flattenRound 推进一轮平仓：到点后找持有者、给每个组合建一行（先存下操作号），再逐个发起并查结果。
// 汇总行记下这一轮是按哪个下架时间规划的：改期之后下架时间变了，而这一轮已经以失败或只完成一部分结束（用完重试、到了旧的下架时间被停下），
// 就按新时间重开，每个仍持有的组合换新的操作号再平；已经成功的一轮不重开。
func (r *run) flattenRound(ctx context.Context, round int, due time.Time) error {
	if r.now.Before(due) {
		r.wakeAt(due)
		return nil
	}
	plannedAt := r.target.DelistAt.UnixMilli()
	summaryKey := stepKey{Round: round, Kind: store.DelistKindFlatten}
	summary, started := r.steps[summaryKey]
	reopen := started && summary.PlannedDelistAt != plannedAt && terminal(summary.Status) && summary.Status != store.DelistSucceeded
	if !started || reopen {
		if r.lowBudget(ctx) {
			r.wakeAt(r.now.Add(5 * time.Second))
			return nil
		}
		listed, err := r.listHolders(ctx)
		if err != nil {
			r.problem("第 %d 轮：查持有者失败，稍后重试: %v", round, err)
			r.wakeAt(r.now.Add(transientRetry))
			return nil
		}
		holders := listed.Holders
		skipped := describeSkipped(listed.Skipped)
		accounts := groupHolders(filterLive(holders))
		for _, item := range accounts {
			row := store.DelistAction{
				Round: round, Kind: store.DelistKindFlatten, SpaceID: item.SpaceID, LogicalAccountID: item.LogicalAccountID,
				Exchange:         r.target.Instrument.Exchange,
				OperatorActionID: operatorActionID(r.execution.ExecutionID, round, item.SpaceID, item.LogicalAccountID, plannedAt),
				Reason:           flattenReason(r.execution, r.target, round),
				Status:           store.DelistPending, PlannedDelistAt: plannedAt,
			}
			// 重入时已按这个下架时间存下的行（含操作号、状态）不能被重置；改期前的旧行换成新的。
			if existing, exists := r.steps[keyOf(row)]; exists && existing.PlannedDelistAt == plannedAt {
				continue
			}
			if err := r.save(ctx, row); err != nil {
				return err
			}
		}
		summary = store.DelistAction{Round: round, Kind: store.DelistKindFlatten, Exchange: r.target.Instrument.Exchange, Status: store.DelistRunning, PlannedDelistAt: plannedAt}
		notes := strings.Join(listed.Notes, "；")
		// 有没能判断的账户时，这一轮不能算成功：汇总行带上说明，最终状态至少是“只完成一部分”，关注项会提醒人工确认。
		summary.Error = skipped
		summary.Result = joinLines(nonEmpty(fmt.Sprintf("持有的组合 %d 个", len(accounts)), describeDust(holders), notes))
		if len(accounts) == 0 {
			summary.Status = store.DelistSucceeded
			headline := "没有策略持有，无需平仓"
			if skipped != "" {
				summary.Status = store.DelistPartial
				headline = "没有查到策略持有，但有账户没能判断，不能当作没有持有"
			}
			summary.Result = joinLines(nonEmpty(headline, describeDust(holders), notes))
		}
		// 汇总行最后写：它按这个下架时间存在，说明这一轮的各组合行都已存好。
		if err := r.save(ctx, summary); err != nil {
			return err
		}
		if len(accounts) == 0 {
			return nil
		}
	}
	if terminal(summary.Status) {
		return nil
	}
	rows := r.roundRows(round, summary.PlannedDelistAt)
	for _, row := range rows {
		if r.stopped {
			return nil
		}
		if err := r.advanceRow(ctx, row); err != nil {
			return err
		}
	}
	return r.settleSummary(ctx, summary, rows)
}

// settleSummary 在这一轮的每个组合都有了结果之后，给汇总行记上最终状态。
func (r *run) settleSummary(ctx context.Context, summary store.DelistAction, rows []store.DelistAction) error {
	status := store.DelistSucceeded
	for _, row := range rows {
		current := r.steps[keyOf(row)]
		switch {
		case !terminal(current.Status):
			return nil
		case current.Status == store.DelistFailed:
			status = store.DelistFailed
		case current.Status == store.DelistPartial && status != store.DelistFailed:
			status = store.DelistPartial
		}
	}
	if status == store.DelistSucceeded && summary.Error != "" {
		status = store.DelistPartial
	}
	summary.Status = status
	return r.save(ctx, summary)
}

func filterLive(holders []action.Holder) []action.Holder {
	var live []action.Holder
	for _, holder := range holders {
		if !holder.Dust {
			live = append(live, holder)
		}
	}
	return live
}

func nonEmpty(values ...string) []string {
	var result []string
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

// advanceRow 推进一个组合的一轮平仓：待发起的发起，执行中的查结果。
func (r *run) advanceRow(ctx context.Context, row store.DelistAction) error {
	if terminal(row.Status) {
		return nil
	}
	if next := time.UnixMilli(row.NextCheckAt); row.NextCheckAt > 0 && next.After(r.now) {
		r.wakeAt(next)
		return nil
	}
	if r.lowBudget(ctx) {
		r.wakeAt(r.now.Add(5 * time.Second))
		return nil
	}
	// 每次发起或查询之前确认记录没有被取消：取消之后不再有新的 Trade 调用。
	fresh, err := r.h.Store.GetExecution(ctx, r.execution.ExecutionID)
	if err != nil {
		return fmt.Errorf("读取执行记录失败: %w", err)
	}
	if fresh.Status == store.StatusCanceled {
		r.stopped = true
		return nil
	}
	if row.Status == store.DelistRunning {
		return r.poll(ctx, row)
	}
	return r.launch(ctx, row)
}

// launch 发起平仓。过了下架时间不下单：每次发起之前都用当前时间再判断一次，不只在 Step 入口判断。
func (r *run) launch(ctx context.Context, row store.DelistAction) error {
	if !r.h.now().Before(r.target.DelistAt) {
		row.Status, row.Error = store.DelistFailed, "已到下架时间，不再下单"
		return r.save(ctx, row)
	}
	// 下架时间 T 作为截止时间传给 Trade：平仓跑到 T 还没做完时由 Trade 停止下单，不靠这里的下一次推进才发现。
	// 用这一行建立时的下架时间（操作号也由它算出）：截止时间是 Trade 判断同号请求是否一致的一部分，
	// 同一个操作号重发必须带同一个值，否则会被判成幂等冲突；改期后新建的行自带新的下架时间。
	// 一轮进行中下架时间被提前时，这一轮已有的行仍带原来的截止时间，到新的 T 由 afterDelist 停下这些行。
	deadline := row.PlannedDelistAt
	if deadline <= 0 {
		deadline = r.target.DelistAt.UnixMilli()
	}
	callCtx, cancel := r.h.tradeCtx(ctx)
	result, err := r.h.Trade.FlattenInstrument(callCtx, action.FlattenRequest{
		ActionID: row.OperatorActionID, SpaceID: row.SpaceID, LogicalAccountID: row.LogicalAccountID,
		Instrument: r.target.Instrument, Reason: row.Reason, DeadlineAt: deadline,
	})
	cancel()
	switch {
	case errors.Is(err, action.ErrTradeBusy):
		// 忙不计重试次数，稍后再试。
		row.Error = "Trade 正忙，稍后再试"
		row.NextCheckAt = r.now.Add(pollInterval).UnixMilli()
		r.wakeAt(r.now.Add(pollInterval))
		return r.save(ctx, row)
	case err != nil:
		return r.failed(ctx, row, "发起平仓失败: "+err.Error())
	case result.ActionID != "" && result.ActionID != row.OperatorActionID:
		// Trade 返回的是同组合同标的别的操作（别的记录的平仓还在跑）：不能把它当成自己的，更不能覆盖自己存下的操作号，
		// 否则这个号之后带着自己的说明重发会被判幂等冲突。按忙处理：稍后再试，不计重试次数。
		row.Error = "Trade 上同一个组合的同一个标的已有别的平仓在进行，稍后再试"
		row.NextCheckAt = r.now.Add(pollInterval).UnixMilli()
		r.wakeAt(r.now.Add(pollInterval))
		return r.save(ctx, row)
	}
	return r.apply(ctx, row, result)
}

// poll 查平仓的结果。查询出错不计这一行的重试次数，30 秒后再查；但算作这次推进的暂时失败。
func (r *run) poll(ctx context.Context, row store.DelistAction) error {
	callCtx, cancel := r.h.tradeCtx(ctx)
	result, err := r.h.Trade.GetOperatorAction(callCtx, row.SpaceID, row.OperatorActionID)
	cancel()
	if err != nil {
		row.Error = "查询平仓结果失败: " + err.Error()
		r.problem("%s/%s 第 %d 轮：查询平仓结果失败，稍后重试: %v", row.SpaceID, row.LogicalAccountID, row.Round, err)
		row.NextCheckAt = r.now.Add(pollInterval).UnixMilli()
		r.wakeAt(r.now.Add(pollInterval))
		return r.save(ctx, row)
	}
	return r.apply(ctx, row, result)
}

// apply 按 Trade 报告的操作状态更新步骤。
func (r *run) apply(ctx context.Context, row store.DelistAction, result action.OperatorAction) error {
	switch {
	case result.Unblocked:
		// Trade 发现标的已被解除禁止而提前结束了清仓：不是失败，也不要重试；下一次推进前查名单会把记录置为已取消。
		row.Status, row.Error, row.Result = store.DelistSucceeded, "", joinLines(nonEmpty("标的已被解除禁止，Trade 提前结束了清仓", result.Message))
		return r.save(ctx, row)
	case result.Status == action.OperatorCompleted:
		row.Status, row.Error, row.Result = store.DelistSucceeded, "", "平仓完成"
		return r.save(ctx, row)
	case result.Status == action.OperatorRunning:
		row.Status, row.Error = store.DelistRunning, ""
		row.NextCheckAt = r.now.Add(pollInterval).UnixMilli()
		r.wakeAt(r.now.Add(pollInterval))
		return r.save(ctx, row)
	case result.Status == action.OperatorPartial:
		// 同一个操作号再调很快又会得到 PARTIAL，重试没有用：这一轮这个组合就此结束，原因留给用户看，
		// 由下一轮复查换新的操作号再平。
		row.Status, row.Error = store.DelistPartial, joinLines(nonEmpty("平仓只完成了一部分，等下一轮复查再试", result.Message))
		return r.save(ctx, row)
	case result.Status == action.OperatorFailed:
		return r.failed(ctx, row, joinLines(nonEmpty("平仓失败", result.Message)))
	}
	return r.failed(ctx, row, fmt.Sprintf("Trade 返回了未知的操作状态 %q", result.Status))
}

// failed 记一次计数的失败：同一个操作号按指数退避重试，用完重试次数这个组合这一轮置为失败。
func (r *run) failed(ctx context.Context, row store.DelistAction, message string) error {
	row.Attempts++
	row.Error = message
	if row.Attempts >= maxRetries {
		row.Status = store.DelistFailed
		return r.save(ctx, row)
	}
	delay := min(time.Minute<<(row.Attempts-1), maxBackoff)
	row.Status = store.DelistPending
	row.NextCheckAt = r.now.Add(delay).UnixMilli()
	r.wakeAt(r.now.Add(delay))
	return r.save(ctx, row)
}

// afterDelist 是下架时间之后：所有还没结束的平仓一律停下，等核对时间到了再核对，只核对不下单。
func (r *run) afterDelist(ctx context.Context) (action.Outcome, error) {
	var open []store.DelistAction
	for key, step := range r.steps {
		if key.Kind == store.DelistKindFlatten && (key.SpaceID != "" || key.LogicalAccountID != "") && !terminal(step.Status) {
			open = append(open, step)
		}
	}
	for _, step := range open {
		step.Status, step.Error = store.DelistFailed, "已到下架时间，停止平仓"
		if err := r.save(ctx, step); err != nil {
			return action.Outcome{}, err
		}
	}
	// 没有汇总行的轮次（下架时间之后才收到）不补；有汇总行但没结束的，按结果收尾。
	for round := RoundFlatten; round <= RoundReview; round++ {
		summaryKey := stepKey{Round: round, Kind: store.DelistKindFlatten}
		summary, ok := r.steps[summaryKey]
		if !ok || terminal(summary.Status) {
			continue
		}
		rows := r.roundRows(round, summary.PlannedDelistAt)
		if err := r.settleSummary(ctx, summary, rows); err != nil {
			return action.Outcome{}, err
		}
	}
	verifyAt := r.target.verifyAt()
	if r.now.Before(verifyAt) {
		r.wakeAt(verifyAt)
		return r.pending(), nil
	}
	return r.verify(ctx)
}

// verify 核对：仍有非零头持有的置为失败并告警，零头列在结果里告警一次。
func (r *run) verify(ctx context.Context) (action.Outcome, error) {
	if r.stopped || r.lowBudget(ctx) {
		r.wakeAt(r.now.Add(5 * time.Second))
		return r.pending(), nil
	}
	listed, err := r.listHolders(ctx)
	if err != nil {
		r.problem("核对时查持有者失败，稍后重试: %v", err)
		r.wakeAt(r.now.Add(transientRetry))
		return r.pending(), nil
	}
	holders := listed.Holders
	skipped := describeSkipped(listed.Skipped)
	live := filterLive(holders)
	step := store.DelistAction{Round: RoundVerify, Kind: store.DelistKindVerify, Exchange: r.target.Instrument.Exchange}
	if existing, ok := r.steps[keyOf(step)]; ok {
		step.Attempts = existing.Attempts
	}
	step.Attempts++
	dust := describeDust(holders)
	detail := joinLines(nonEmpty(dust, strings.Join(listed.Notes, "；")))
	if len(live) > 0 || skipped != "" {
		var message string
		if len(live) > 0 {
			var parts []string
			for _, item := range groupHolders(live) {
				parts = append(parts, item.SpaceID+"/"+item.LogicalAccountID)
			}
			message = fmt.Sprintf("下架后核对：仍有 %d 个组合持有 %s：%s", len(parts), r.target.subject(), strings.Join(parts, "、"))
		}
		// 没能判断的账户不能当作没有持有：核对不通过，要人工确认。
		message = joinLines(nonEmpty(message, skipped))
		step.Status, step.Error, step.Result = store.DelistFailed, message, detail
		if err := r.save(ctx, step); err != nil {
			return action.Outcome{}, err
		}
		return action.Outcome{Status: store.StatusFailed, Error: message}, nil
	}
	step.Status, step.Result = store.DelistSucceeded, joinLines(nonEmpty("核对通过，没有策略持有", detail))
	if err := r.save(ctx, step); err != nil {
		return action.Outcome{}, err
	}
	if dust != "" {
		// 零头只告警一次，不处理。
		telemetry.Note(telemetry.AttentionActionFailed, fmt.Sprintf("%s 下架后留有零头，不会自动处理：%s", r.target.subject(), dust), r.now)
	}
	return action.Outcome{Status: store.StatusSucceeded}, nil
}
