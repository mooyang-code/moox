package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

type HandlerConfig struct {
	NakDelay          time.Duration
	PeriodBudgetMin   time.Duration
	PeriodBudgetMax   time.Duration
	ReadColumnTimeout time.Duration
}

type Handler struct {
	sets     trigger.TaskLocator
	store    trigger.PeriodStore
	runner   trigger.PipelineRunner
	runners  *trigger.TaskRunners
	registry *events.Registry
	initErr  error
	cfg      HandlerConfig
	// continuity 非空时按连续水位逐期推进（设计见 docs/计划/因子连续水位详细设计.md）。
	continuity *Continuity
	// gate 非空时，任务没有处于预热或运行（生命周期门禁）就不计算：事件直接 ACK，水位不前进，
	// 启动之后由下一个事件和追赶循环从水位下一期补算。
	gate func(taskID string) bool
	// catchupGate 非空时，实时门禁关闭的任务在恢复追赶中仍可推进连续水位（只写历史周期）。
	catchupGate func(taskID string) bool
	// begin 非空时取代 gate/catchupGate 的二次检查：先登记在途、再检查门禁，返回的函数在这次计算结束后注销。
	// live 为真是实时事件（只看实时门禁），否则是追赶推进（实时门禁或追赶门禁放行都可以）。
	begin func(ctx context.Context, taskID string, live bool) (func(), bool)
	// params 在开算一期之前取得任务各可变成员用的参数（设计 6.3）；为空时全部按成员自己的参数计算。
	params trigger.ParamSelector
}

// SetParamSelector 设置取参来源；必须在开始消费之前调用。
func (h *Handler) SetParamSelector(params trigger.ParamSelector) { h.params = params }

// SetGate 设置生命周期门禁；必须在开始消费之前调用。
func (h *Handler) SetGate(gate func(taskID string) bool) { h.gate = gate }

// SetBegin 设置登记式门禁；任务串行队列里的闭包在真正执行前用它再检查一次。必须在开始消费之前调用。
func (h *Handler) SetBegin(begin func(ctx context.Context, taskID string, live bool) (func(), bool)) {
	h.begin = begin
}

// enter 在写结果之前登记并检查门禁；没有设置 begin 时沿用 gate/catchupGate。
func (h *Handler) enter(ctx context.Context, taskID string, live bool) (func(), bool) {
	if h.begin != nil {
		return h.begin(ctx, taskID, live)
	}
	open := h.gate == nil || h.gate(taskID)
	if !open && !live && h.catchupGate != nil {
		open = h.catchupGate(taskID)
	}
	if !open {
		return nil, false
	}
	return func() {}, true
}

// SetCatchupGate 设置恢复追赶的门禁：Advance（连续水位追赶）在实时门禁关闭时，只要追赶门禁放行就可以推进。
func (h *Handler) SetCatchupGate(gate func(taskID string) bool) { h.catchupGate = gate }

// SetContinuity 打开连续水位推进；必须在开始消费之前调用。
func (h *Handler) SetContinuity(continuity *Continuity) { h.continuity = continuity }

// Advance 在任务的通道里按连续水位推进一次（追赶循环与补齐通知调用）。
func (h *Handler) Advance(ctx context.Context, taskID string) error {
	if h == nil || h.continuity == nil {
		return nil
	}
	if h.gate != nil && !h.gate(taskID) && (h.catchupGate == nil || !h.catchupGate(taskID)) {
		return nil
	}
	_, err := h.runners.Do(ctx, taskID, func(runnerCtx context.Context) (pipeline.Outcome, error) {
		// 排队期间可能已经暂停：真正执行前登记并再检查一次门禁。
		release, ok := h.enter(runnerCtx, taskID, false)
		if !ok {
			return pipeline.Outcome{}, nil
		}
		defer release()
		return pipeline.Outcome{}, h.continuity.advanceTask(runnerCtx, h, taskID)
	})
	return err
}

func NewHandler(sets trigger.TaskLocator, periodStore trigger.PeriodStore, runner trigger.PipelineRunner, locks trigger.TaskLocks, cfg HandlerConfig) *Handler {
	if cfg.NakDelay <= 0 {
		cfg.NakDelay = 10 * time.Second
	}
	if cfg.PeriodBudgetMin <= 0 {
		cfg.PeriodBudgetMin = time.Minute
	}
	if cfg.PeriodBudgetMax <= 0 {
		cfg.PeriodBudgetMax = 15 * time.Minute
	}
	if cfg.PeriodBudgetMin > cfg.PeriodBudgetMax {
		cfg.PeriodBudgetMin = cfg.PeriodBudgetMax
	}
	if cfg.ReadColumnTimeout <= 0 {
		cfg.ReadColumnTimeout = 20 * time.Second
	}
	registry, initErr := events.DefaultRegistry()
	return &Handler{
		sets: sets, store: periodStore, runner: runner, runners: trigger.NewTaskRunners(locks), registry: registry, initErr: initErr, cfg: cfg,
	}
}

func (h *Handler) Close() {
	if h != nil && h.runners != nil {
		h.runners.Close()
	}
}

func (h *Handler) RunnerStatuses() []trigger.RunnerStatus {
	if h == nil || h.runners == nil {
		return nil
	}
	return h.runners.Status()
}

func (h *Handler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == nil || h.sets == nil || h.store == nil || h.runner == nil || h.runners == nil || h.registry == nil {
		if h != nil && h.initErr != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("initialize factor period event registry: %w", h.initErr)}
		}
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor period handler is not initialized")}
	}
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor period delivery is nil")}
	}
	if delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("decode dataset period event: %w", delivery.DecodeError)}
	}
	message, payload, err := events.DecodeDatasetPeriodCompletedWithContentType(
		h.registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType,
	)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("reject dataset period event: %w", err)}
	}
	if message == nil || payload == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("dataset period event is empty")}
	}

	event := periodEvent{
		periodTime: time.Unix(payload.GetPeriodTime(), 0).UTC(), eventID: message.GetEventId(),
		universe: payload.GetUniverseSubjectIds(), failed: payload.GetFailedSubjects(),
	}
	// 同一个源数据集可以有多个任务：事件同时分给每个启用的任务，各自在自己的串行通道里计算（同一任务内部的期次仍按顺序），
	// 全部返回后再汇总错误。任一任务遇到基础设施错误时整条事件重投；已经算完的任务靠“同一触发事件的 marker 已存在”跳过。
	sets, lookupErr := h.sets.EnabledTasksByDataset(ctx, message.GetSpaceId(), payload.GetDatasetId(), payload.GetFrequency())
	if lookupErr != nil {
		err = fmt.Errorf("%w: lookup enabled factor tasks: %v", storageio.ErrInfra, lookupErr)
	}
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	taskErrs := make([]error, len(sets))
	var wg sync.WaitGroup
	for index, engineTask := range sets {
		set, members := engineTask.Set, engineTask.Members
		if !engineTask.ResultReady {
			taskErrs[index] = fmt.Errorf("%w: result dataset of factor task %s is not ready", storageio.ErrInfra, set.TaskID)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, setErr := h.runners.Do(ctx, set.TaskID, func(runnerCtx context.Context) (pipeline.Outcome, error) {
				if set.Status != domain.TaskStatusEnabled {
					return pipeline.Outcome{}, nil
				}
				if set.TaskID == "" || set.SpaceID != message.GetSpaceId() || set.SourceDatasetID != payload.GetDatasetId() || set.Freq != payload.GetFrequency() {
					return pipeline.Outcome{}, fmt.Errorf("enabled factor task identity does not match period event")
				}
				release, ok := h.enter(runnerCtx, set.TaskID, true)
				if !ok {
					return pipeline.Outcome{}, nil
				}
				defer release()
				// 任务与成员在入队之前取得，排队期间可能暂停、切换版本又恢复：登记之后按目录当前的内容重新取一次，
				// 不能用跨过暂停的旧快照写结果。
				if fresh, found, lookupErr := h.sets.EnabledTask(runnerCtx, set.TaskID); lookupErr != nil {
					return pipeline.Outcome{}, fmt.Errorf("%w: reload factor task %s: %v", storageio.ErrInfra, set.TaskID, lookupErr)
				} else if !found {
					return pipeline.Outcome{}, nil
				} else {
					set, members = fresh.Set, fresh.Members
				}
				if h.continuity != nil {
					return pipeline.Outcome{}, h.continuity.onEvent(runnerCtx, h, set, members, event)
				}
				return h.computePeriod(runnerCtx, set, members, event.periodTime, event.universe, event.failed, event.eventID)
			})
			if setErr != nil {
				taskErrs[index] = fmt.Errorf("factor task %s: %w", set.TaskID, setErr)
			}
		}()
	}
	wg.Wait()
	errs = append(errs, taskErrs...)
	err = errors.Join(errs...)
	if err == nil {
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}
	if errors.Is(err, storageio.ErrInfra) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		log.WarnContextf(ctx, "factor_period_retry dataset_id=%s period_time=%d error=%v", payload.GetDatasetId(), payload.GetPeriodTime(), err)
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: h.cfg.NakDelay, Err: err}
	}
	log.ErrorContextf(ctx, "factor_period_rejected dataset_id=%s period_time=%d error=%v", payload.GetDatasetId(), payload.GetPeriodTime(), err)
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
}

// periodEvent 是一条周期完成事件里引擎需要的部分。
type periodEvent struct {
	periodTime time.Time
	eventID    string
	universe   []string
	failed     []string
}

// computePeriod 计算一期：已经算过（同一触发事件的 marker 已存在）就跳过；开算之前先取得这一期的参数
// （取参是幂等的：重投、重启后重算拿到同一份，直到这一期的完成标记写出）。
func (h *Handler) computePeriod(ctx context.Context, set domain.FactorTask, members []domain.MemberDef, period time.Time, universe, failed []string, triggerEventID string) (pipeline.Outcome, error) {
	exists, existsErr := h.store.ComputedExists(ctx, set.SpaceID, set.ResultDatasetID, triggerEventID, period.Unix())
	if existsErr != nil {
		return pipeline.Outcome{}, fmt.Errorf("%w: check computed factor period: %v", storageio.ErrInfra, existsErr)
	}
	if exists {
		// 完成标记已经写出：只需清掉可能残留的取参进行中记录（写标记之后、清记录之前进程退出的情况）。
		h.finishParams(ctx, set, period)
		return pipeline.Outcome{}, nil
	}
	var params map[string]domain.ParamSnapshot
	if h.params != nil && len(members) > 0 {
		aligned := period
		if clock, err := periodclock.ForSpace(set.SpaceID); err == nil {
			if value, err := clock.Align(period, set.Freq); err == nil {
				aligned = value
			}
		}
		selected, err := h.params.Select(ctx, set, members, aligned)
		if err != nil {
			return pipeline.Outcome{}, fmt.Errorf("%w: select factor params: %v", storageio.ErrInfra, err)
		}
		params = selected
	}
	plan, err := h.livePlan(ctx, set, members, params, period, universe, failed, triggerEventID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	outcome, err := h.runner.Run(ctx, plan)
	if err != nil {
		return outcome, err
	}
	h.finishParams(ctx, set, period)
	return outcome, nil
}

// finishParams 在这一期的完成标记写成功后清掉取参的进行中记录；清不掉不影响这一期的结果（下次同一期不会再算，
// 之后的期次覆盖记录），只记日志。
func (h *Handler) finishParams(ctx context.Context, set domain.FactorTask, period time.Time) {
	if h.params == nil {
		return
	}
	aligned := period
	if clock, err := periodclock.ForSpace(set.SpaceID); err == nil {
		if value, err := clock.Align(period, set.Freq); err == nil {
			aligned = value
		}
	}
	if err := h.params.Done(ctx, set.TaskID, aligned); err != nil {
		log.ErrorContextf(ctx, "factor_param_inflight_clear_failed task_id=%s period=%s error=%v", set.TaskID, aligned.UTC().Format(time.RFC3339), err)
	}
}

func (h *Handler) livePlan(ctx context.Context, set domain.FactorTask, members []domain.MemberDef, params map[string]domain.ParamSnapshot, period time.Time, universe, failed []string, triggerEventID string) (pipeline.Plan, error) {
	clock, clockErr := periodclock.ForSpace(set.SpaceID)
	if clockErr != nil {
		return pipeline.Plan{}, clockErr
	}
	columnsCtx, cancel := context.WithTimeout(ctx, h.cfg.ReadColumnTimeout)
	carryColumns, columnsErr := h.store.DatasetColumns(columnsCtx, set.SpaceID, set.SourceDatasetID)
	cancel()
	if columnsErr != nil {
		return pipeline.Plan{}, fmt.Errorf("%w: read source dataset columns: %v", storageio.ErrInfra, columnsErr)
	}
	budget, budgetErr := periodBudget(clock, set.Freq, h.cfg.PeriodBudgetMin, h.cfg.PeriodBudgetMax)
	if budgetErr != nil {
		return pipeline.Plan{}, budgetErr
	}
	return pipeline.BuildLivePlan(clock, pipeline.LiveInput{
		Set: set, Members: members, Params: params, PeriodTime: period, Universe: universe, UpstreamFailed: failed,
		CarryColumns: carryColumns, TriggerEventID: triggerEventID, Budget: budget,
	})
}

func periodBudget(clock periodclock.Clock, freq string, minBudget, maxBudget time.Duration) (time.Duration, error) {
	duration, err := clock.Duration(freq)
	if err != nil {
		return 0, err
	}
	budget := duration * 2
	if budget < minBudget {
		budget = minBudget
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	return budget, nil
}

var _ jetstream.DeliveryHandler = (*Handler)(nil)
