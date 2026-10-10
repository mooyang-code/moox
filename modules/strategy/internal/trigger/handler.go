package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/storagepb"
)

// ViewDataReadyConsumerName 是默认的 durable 消费者名。
const ViewDataReadyConsumerName = "strategy_view_data_ready_v1"

// ErrInvalidEvent 表示事件本身不可处理（处理器未配置或事件为空），重投无意义。
var ErrInvalidEvent = errors.New("事件不可处理")

const (
	// SkipInfraRetryExhausted 表示读输入的基础设施错误超过尝试预算。
	SkipInfraRetryExhausted = "infra_retry_exhausted"
	// DefaultAttemptBudget 是同一事件对同一实例的默认尝试次数。
	DefaultAttemptBudget = 5
	// ValidBars 是结果有效期的 bar 数。
	ValidBars = 2

	maxStaleRereads = 3
	maxCASRetries   = 2
)

// Loader 为实例装配一期输入。
type Loader interface {
	LoadBar(ctx context.Context, spaceID string, resolved input.Resolved, program *dsl.Program, bar input.Bar) (input.Loaded, error)
}

// Observer 接收每个处理周期的结果，用于指标与 Monitor 上报。
type Observer interface {
	ObservePeriod(instance store.Instance, bar string, barEnd time.Time, result, reason string)
}

// Handler 处理 ViewDataReady：遍历绑定该 View 的启用实例，错误汇总，全部终态落库后才 ACK。
type Handler struct {
	Store         *store.Store
	Loader        Loader
	Now           func() time.Time
	AttemptBudget int
	Observer      Observer
	Logf          func(format string, args ...any)

	mu       sync.Mutex
	programs map[string]*runtime
	attempts map[string]int
	// exhausted 记录尝试预算已耗尽、但 skipped(infra_retry_exhausted) 尚未落库的（事件, 实例）及其概述：
	// 终态写入失败后重投时直接重试这条终态，不能重新读取输入、重新获得一整轮预算。
	exhausted map[string]string
	// dropped 记录本进程内已丢弃过的（事件, 实例）：同一事件因其他实例需要重投而再次投递时，不重复记日志与计数。
	dropped map[string]struct{}
}

// Handle 处理一条事件。返回 nil 表示所有绑定实例的本期终态都已落库，可以 ACK；
// 返回错误表示至少一个实例需要重投；ErrInvalidEvent 表示事件本身不可处理。
func (h *Handler) Handle(ctx context.Context, message *eventpb.EventMessage, payload *storagepb.ViewDataReady) error {
	if h == nil || h.Store == nil || h.Loader == nil || message == nil || payload == nil {
		return fmt.Errorf("%w：处理器未配置或事件为空", ErrInvalidEvent)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.programs == nil {
		h.programs = make(map[string]*runtime)
	}
	if h.attempts == nil {
		h.attempts = make(map[string]int)
	}
	if h.exhausted == nil {
		h.exhausted = make(map[string]string)
	}
	instances, err := h.Store.EnabledInstancesByView(ctx, message.GetSpaceId(), payload.GetViewId())
	if err != nil {
		return fmt.Errorf("读取 View %s 的启用实例：%w", payload.GetViewId(), err)
	}
	// 同一事件内多个实例绑定同一 View：元数据（View、标的绑定、标签成员）只读一次。
	loader := h.Loader
	if scoped, ok := loader.(interface{ ForEvent() input.Loader }); ok {
		loader = scoped.ForEvent()
	}
	var retry []error
	for _, instance := range instances {
		if err := h.process(ctx, loader, instance, message, payload); err != nil {
			retry = append(retry, fmt.Errorf("实例 %s：%w", instance.InstanceID, err))
		}
	}
	if len(retry) > 0 {
		return errors.Join(retry...)
	}
	for key := range h.attempts {
		if strings.HasPrefix(key, message.GetEventId()+"\x00") {
			delete(h.attempts, key)
		}
	}
	for key := range h.exhausted {
		if strings.HasPrefix(key, message.GetEventId()+"\x00") {
			delete(h.exhausted, key)
		}
	}
	for key := range h.dropped {
		if strings.HasPrefix(key, message.GetEventId()+"\x00") {
			delete(h.dropped, key)
		}
	}
	return nil
}

// firstDrop 报告这条事件对这个实例的丢弃是否第一次发生（调用方持有 h.mu）。丢弃不落记录，没有别的依据去重：
// 同一事件因其他实例需要重投而再次投递时，不能把这个实例重复计入周期计数与模块健康失败。边界：记录在 Handle 返回
// nil 时清理，ACK 本身失败而重投时会再计一次；事件在确认前被流的保留策略清掉时条目留在内存里（只积累在重试中的事件
// 上，量很小），进程重启后清零。
func (h *Handler) firstDrop(eventID, instanceID string) bool {
	if h.dropped == nil {
		h.dropped = make(map[string]struct{})
	}
	key := eventID + "\x00" + instanceID
	if _, seen := h.dropped[key]; seen {
		return false
	}
	h.dropped[key] = struct{}{}
	return true
}

// period 是一个实例处理一期时的上下文。
type period struct {
	loader     Loader
	instance   store.Instance
	runtime    *runtime
	eventID    string
	viewID     string
	boundary   input.PeriodBoundaries
	validUntil time.Time
	input      store.InputRecord
}

func (h *Handler) process(ctx context.Context, loader Loader, instance store.Instance, message *eventpb.EventMessage, payload *storagepb.ViewDataReady) error {
	periodTime := time.Unix(payload.GetPeriodTime(), 0).UTC()
	rt, rtErr := h.runtimeFor(ctx, instance)
	var skip *input.SkipError
	if rtErr != nil && !errors.As(rtErr, &skip) {
		return rtErr
	}
	// 绑定快照（会话与实例上的副本）都无法解析：不知道日历、周期与触发类型，写不出正确的跳过记录，只记日志与计数。
	if rt == nil || rt.resolved.Bar == "" {
		if h.firstDrop(message.GetEventId(), instance.InstanceID) {
			h.logf("实例 %s 的绑定快照无法解析，本期丢弃：%v", instance.InstanceID, rtErr)
			h.observe(instance, strings.ToLower(payload.GetFrequency()), time.Time{}, store.StatusSkipped, input.SkipConfigError)
		}
		return nil
	}
	// 因子结果 View 只由 factor_period.computed 驱动，K 线 View 只由 collector.period.completed 驱动；实例绑定哪种 View
	// 在启用时就确定了，其他类型的完成事件不是这个实例的触发信号，先于其余处理忽略（编译失败的会话也带着快照里的类型）。
	if want := rt.resolved.CompletionKind; payload.GetCompletionKind() != want {
		h.logf("实例 %s 只接受 %s 触发，忽略 %s 事件 %s", instance.InstanceID, want, payload.GetCompletionKind(), message.GetEventId())
		return nil
	}
	calendar, bar := rt.resolved.Calendar, rt.resolved.Bar
	// 日历换算失败（例如 A 股内嵌日历已过期、周期不在交易日上）无法确定周期与有效期，只能 ACK：
	// 记日志并计入模块健康失败，不能悄悄丢掉这一期。
	boundary, err := input.FromStorageStart(calendar, bar, periodTime)
	if err != nil {
		if h.firstDrop(message.GetEventId(), instance.InstanceID) {
			h.logf("实例 %s 无法确定周期边界（%s/%s），本期丢弃：%v", instance.InstanceID, calendar, bar, err)
			h.observe(instance, bar, time.Time{}, store.StatusSkipped, input.SkipConfigError)
		}
		return nil
	}
	validUntil, err := advance(calendar, bar, boundary.BarEnd, ValidBars)
	if err != nil {
		if h.firstDrop(message.GetEventId(), instance.InstanceID) {
			h.logf("实例 %s 周期 %s 无法推算有效期（%s/%s），本期丢弃：%v", instance.InstanceID, input.BarEndLabel(calendar, bar, boundary.BarEnd), calendar, bar, err)
			h.observe(instance, bar, boundary.BarEnd, store.StatusSkipped, input.SkipConfigError)
		}
		return nil
	}
	p := &period{loader: loader, instance: instance, runtime: rt, eventID: message.GetEventId(), viewID: payload.GetViewId(), boundary: boundary, validUntil: validUntil}
	p.input = store.InputRecord{ViewID: payload.GetViewId(), Bar: bar, Calendar: calendar, BarStart: boundary.StorageStart, EventID: message.GetEventId()}
	if skip != nil {
		return h.commitSkipped(ctx, p, skip.Reason, skip.Detail)
	}
	latest, hasLatest, err := h.Store.LatestProcessed(ctx, instance.InstanceID, rt.sessionID)
	if err != nil {
		return err
	}
	if hasLatest && !boundary.BarEnd.After(latest.BarEndTime) {
		if boundary.BarEnd.Equal(latest.BarEndTime) {
			return nil
		}
		return h.commitSkipped(ctx, p, store.SkipOutOfOrder, fmt.Sprintf("已处理到 %s", input.BarEndLabel(calendar, bar, latest.BarEndTime)))
	}
	if !validUntil.After(h.now()) {
		return h.commitSkipped(ctx, p, store.SkipExpired, fmt.Sprintf("有效期 %s 已过", input.BarEndLabel(calendar, bar, validUntil)))
	}
	var adjacent *readiness.Record
	if len(rt.resolved.PreviousFactors) > 0 {
		previousEnd, err := advance(calendar, bar, boundary.BarEnd, -1)
		if err != nil {
			return h.commitSkipped(ctx, p, input.SkipConfigError, err.Error())
		}
		_, record, found, err := h.Store.AdjacentRecord(ctx, instance.InstanceID, p.viewID, bar, calendar, previousEnd, rt.resolved.PreviousFactors, rt.sessionID)
		if err != nil {
			return err
		}
		if found {
			adjacent = &readiness.Record{Factors: record.Factors}
		}
	}
	ready := readiness.Check(rt.resolved.ReadinessBinding(), adjacent, payload)
	p.input.Factors = ready.Hashes
	if ready.Reason != "" {
		return h.commitSkipped(ctx, p, ready.Reason, ready.Detail)
	}
	expected := ""
	if hasLatest {
		expected = latest.ResultID
	}
	for attempt := 0; ; attempt++ {
		decision, loaded, err := h.evaluate(ctx, p, payload, ready)
		if err != nil {
			return err
		}
		if decision == nil {
			return nil
		}
		if !validUntil.After(h.now()) {
			return h.commitSkipped(ctx, p, store.SkipExpired, "求值完成时有效期已过")
		}
		result, items, err := h.buildResult(p, *decision, loaded)
		if err != nil {
			return h.commitSkipped(ctx, p, input.SkipConfigError, err.Error())
		}
		_, created, err := h.Store.CommitResult(ctx, store.CommitRequest{Result: result, Items: items, ExpectedLatestResultID: &expected, Now: h.now()})
		switch {
		case err == nil:
			if created {
				h.observe(instance, bar, boundary.BarEnd, decision.Status, decision.SkipReason)
				h.updateHealth(ctx, instance.InstanceID, rt.sessionID, decision.Status, decision.SkipReason)
			}
			return nil
		case errors.Is(err, store.ErrResultCASConflict):
			latest, hasLatest, readErr := h.Store.LatestProcessed(ctx, instance.InstanceID, rt.sessionID)
			if readErr != nil {
				return readErr
			}
			if hasLatest && latest.BarEndTime.Equal(boundary.BarEnd) {
				return nil
			}
			if hasLatest && latest.BarEndTime.After(boundary.BarEnd) {
				return h.commitSkipped(ctx, p, store.SkipOutOfOrder, fmt.Sprintf("已处理到 %s", input.BarEndLabel(calendar, bar, latest.BarEndTime)))
			}
			expected = ""
			if hasLatest {
				expected = latest.ResultID
			}
			if attempt+1 >= maxCASRetries {
				return fmt.Errorf("结果提交连续冲突：%w", err)
			}
		case errors.Is(err, store.ErrResultExpired):
			return h.commitSkipped(ctx, p, store.SkipExpired, "提交时有效期已过")
		case errors.Is(err, store.ErrResultOlder):
			return h.commitSkipped(ctx, p, store.SkipOutOfOrder, "提交时已有更新的周期")
		case errors.Is(err, store.ErrResultInstanceNotActive):
			h.logf("实例 %s 在求值期间被停用，周期 %s 不再记录", instance.InstanceID, boundary.BarEnd.Format(time.RFC3339))
			return nil
		default:
			if store.IsPermanentWriteError(err) {
				// 结果本身无法写入（校验失败或违反表约束），重投也不会成功：记一条不带明细的 skipped(config_error) 后 ACK，
				// 不能让这条消息无限重投并压住同一消费者上的其他事件。
				h.logf("实例 %s 周期 %s 的结果无法写入：%v", instance.InstanceID, boundary.BarEnd.Format(time.RFC3339), err)
				return h.commitSkipped(ctx, p, input.SkipConfigError, "结果无法写入存储（确定性错误，详见策略模块日志）")
			}
			return fmt.Errorf("写入结果：%w", err)
		}
	}
}

// evaluate 读取输入并求值。返回 nil 决策表示本期已作为 skipped 落库。
func (h *Handler) evaluate(ctx context.Context, p *period, payload *storagepb.ViewDataReady, ready readiness.Result) (*engine.Decision, input.Loaded, error) {
	rt := p.runtime
	key := p.eventID + "\x00" + p.instance.InstanceID
	if detail, done := h.exhausted[key]; done {
		// 预算已耗尽：持续重试终态落库，成功之前不再读取输入。
		err := h.commitSkipped(ctx, p, SkipInfraRetryExhausted, detail)
		if err == nil {
			delete(h.exhausted, key)
		}
		return nil, input.Loaded{}, err
	}
	var loaded input.Loaded
	for reread := 0; ; reread++ {
		var err error
		loaded, err = p.loader.LoadBar(ctx, p.instance.SpaceID, rt.resolved, rt.program, input.Bar{BarStart: p.boundary.StorageStart, EventUniverse: eventUniverse(payload), Readiness: ready})
		if err == nil {
			break
		}
		if errors.Is(err, input.ErrStale) && reread < maxStaleRereads {
			// 索引已切换：丢弃事件内缓存的 View 元数据后整体重读。
			if cache, ok := p.loader.(interface{ Invalidate() }); ok {
				cache.Invalidate()
			}
			continue
		}
		var skip *input.SkipError
		if errors.As(err, &skip) {
			return nil, input.Loaded{}, h.commitSkipped(ctx, p, skip.Reason, skip.Detail)
		}
		h.attempts[key]++
		// 记录里只保存不含服务地址的概述，原始错误写日志。
		h.logf("实例 %s 周期 %s 读取输入失败（第 %d 次）：%v；原始错误：%v", p.instance.InstanceID, p.boundary.BarEnd.Format(time.RFC3339), h.attempts[key], err, input.RawCause(err))
		if h.attempts[key] >= h.budget() {
			delete(h.attempts, key)
			h.exhausted[key] = err.Error()
			commitErr := h.commitSkipped(ctx, p, SkipInfraRetryExhausted, err.Error())
			if commitErr == nil {
				delete(h.exhausted, key)
			}
			return nil, input.Loaded{}, commitErr
		}
		return nil, input.Loaded{}, fmt.Errorf("读取输入（第 %d 次）：%w", h.attempts[key], err)
	}
	previous := engine.State{}
	if last, ok, err := h.Store.LatestOk(ctx, p.instance.InstanceID, rt.sessionID); err != nil {
		return nil, input.Loaded{}, err
	} else if ok {
		if err := json.Unmarshal(last.RuleStatesJSON, &previous); err != nil {
			h.logf("实例 %s 上期结果 %s 的规则状态无法解析：%v", p.instance.InstanceID, last.ResultID, err)
			return nil, input.Loaded{}, h.commitSkipped(ctx, p, input.SkipConfigError, "上期结果的规则状态无法解析（记录已损坏），请停用后重新启用实例")
		}
	}
	decision, err := engine.Evaluate(rt.program, loaded.Frame, previous)
	if err != nil {
		return nil, input.Loaded{}, h.commitSkipped(ctx, p, input.SkipConfigError, err.Error())
	}
	return &decision, loaded, nil
}

// buildResult 把决策转成存储行；ok 且绑定账户的结果携带目标事件。
func (h *Handler) buildResult(p *period, decision engine.Decision, loaded input.Loaded) (store.Result, []store.ResultItem, error) {
	resultID, err := store.NewID()
	if err != nil {
		return store.Result{}, nil, err
	}
	targets, targetsJSON, err := EncodeTargets(decision.Targets)
	if err != nil {
		return store.Result{}, nil, err
	}
	states, err := json.Marshal(decision.State)
	if err != nil {
		return store.Result{}, nil, err
	}
	summary, err := EncodeSummary(decision.Summary, loaded.Sets.Notes)
	if err != nil {
		return store.Result{}, nil, err
	}
	record := p.input
	record.Detail = ""
	inputJSON, err := json.Marshal(record)
	if err != nil {
		return store.Result{}, nil, err
	}
	now := h.now()
	result := store.Result{
		ResultID: resultID, InstanceID: p.instance.InstanceID, SessionID: p.runtime.sessionID,
		BarEndTime: p.boundary.BarEnd, ValidUntil: p.validUntil, Status: decision.Status, SkipReason: decision.SkipReason, DSLHash: p.runtime.dslHash,
		InputJSON: inputJSON, TargetsJSON: targetsJSON, RuleStatesJSON: states, SummaryJSON: summary, PublishStatus: store.PublishNone, CreatedAt: now,
	}
	if decision.Status == engine.StatusOK && p.instance.LogicalAccountID != nil {
		event, err := MarshalTargetEvent(p.instance, result, targets)
		if err != nil {
			return store.Result{}, nil, err
		}
		result.EventData = event
		result.PublishStatus = store.PublishPending
	}
	if decision.Status == engine.StatusSkipped {
		result.TargetsJSON = json.RawMessage(`[]`)
	}
	return result, ResultItems(decision.Items), nil
}

// commitSkipped 写入一条 skipped 记录；实例已停用时视为无事可做。
func (h *Handler) commitSkipped(ctx context.Context, p *period, reason, detail string) error {
	if p.runtime == nil || p.runtime.sessionID == "" {
		return nil
	}
	resultID, err := store.NewID()
	if err != nil {
		return err
	}
	record := p.input
	record.Detail = detail
	inputJSON, err := json.Marshal(record)
	if err != nil {
		return err
	}
	result := store.Result{
		ResultID: resultID, InstanceID: p.instance.InstanceID, SessionID: p.runtime.sessionID,
		BarEndTime: p.boundary.BarEnd, ValidUntil: p.validUntil, Status: store.StatusSkipped, SkipReason: reason, DSLHash: p.runtime.dslHash,
		InputJSON: inputJSON, TargetsJSON: json.RawMessage(`[]`), RuleStatesJSON: json.RawMessage(`{}`), SummaryJSON: json.RawMessage(`{}`),
		PublishStatus: store.PublishNone, CreatedAt: h.now(),
	}
	_, created, err := h.Store.CommitResult(ctx, store.CommitRequest{Result: result, Now: h.now()})
	if err != nil {
		if errors.Is(err, store.ErrResultInstanceNotActive) {
			return nil
		}
		if store.IsPermanentWriteError(err) {
			// 连兜底的 skipped 也写不进去：重投不会成功，记错误日志并计入模块健康失败后确认，不能无限重投。
			if h.firstDrop(p.eventID, p.instance.InstanceID) {
				h.logf("实例 %s 周期 %s 的 skipped(%s) 记录无法写入，放弃本期：%v", p.instance.InstanceID, p.boundary.BarEnd.Format(time.RFC3339), reason, err)
				h.observe(p.instance, p.input.Bar, p.boundary.BarEnd, store.StatusSkipped, input.SkipConfigError)
			}
			return nil
		}
		return fmt.Errorf("写入 skipped(%s) 记录：%w", reason, err)
	}
	if created {
		h.observe(p.instance, p.input.Bar, p.boundary.BarEnd, store.StatusSkipped, reason)
		h.updateHealth(ctx, p.instance.InstanceID, p.runtime.sessionID, store.StatusSkipped, reason)
		h.logf("实例 %s 周期 %s 跳过（%s）：%s", p.instance.InstanceID, p.boundary.BarEnd.Format(time.RFC3339), reason, detail)
	}
	return nil
}

// degradingReasons 是需要人工处理的跳过原因（重新启用、修复 View 或补数据）：实例标记 degraded，下一个 ok 周期恢复。
// factor_changed 表示绑定的因子定义已变，只有重新启用才能恢复。
var degradingReasons = map[string]bool{
	input.SkipConfigError:         true,
	input.SkipHistoryInsufficient: true,
	input.SkipAmbiguousSeries:     true,
	readiness.ReasonFactorChanged: true,
}

// updateHealth 按本期结果更新实例健康：ok 恢复 degraded，需要人工处理的跳过原因标记 degraded。
// 只作用于仍以该会话启用的实例。
func (h *Handler) updateHealth(ctx context.Context, instanceID, sessionID, status, reason string) {
	switch {
	case status == engine.StatusOK:
		_ = h.Store.RecoverInstanceHealth(ctx, instanceID, sessionID, h.now())
	case degradingReasons[reason]:
		_ = h.Store.MarkInstanceDegraded(ctx, instanceID, sessionID, h.now())
	}
}

func (h *Handler) observe(instance store.Instance, bar string, barEnd time.Time, result, reason string) {
	if h.Observer != nil {
		h.Observer.ObservePeriod(instance, bar, barEnd, result, reason)
	}
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

func (h *Handler) budget() int {
	if h.AttemptBudget > 0 {
		return h.AttemptBudget
	}
	return DefaultAttemptBudget
}

func (h *Handler) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

// eventUniverse 返回事件的标的名单。ViewDataReady 总是携带名单，protobuf 解码会把空列表变成 nil，
// 这里改回空列表：空名单表示本期没有任何标的，按 no_data 跳过，而不是退化为数据集的全部标的。
func eventUniverse(payload *storagepb.ViewDataReady) []string {
	if universe := payload.GetUniverseSubjectIds(); universe != nil {
		return universe
	}
	return []string{}
}

func advance(calendar, bar string, at time.Time, n int) (time.Time, error) {
	return input.AdvanceBarEnd(calendar, bar, at, n)
}
