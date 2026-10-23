package eventconsumer

import (
	"context"
	"errors"
	"fmt"
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
	sets     trigger.SetLocator
	store    trigger.PeriodStore
	runner   trigger.PipelineRunner
	lanes    *trigger.Lanes
	registry *events.Registry
	initErr  error
	cfg      HandlerConfig
	// continuity 非空时按连续水位逐期推进（设计见 docs/计划/因子连续水位详细设计.md）。
	continuity *Continuity
	// gate 非空时，集合没有处于预热或运行（生命周期门禁）就不计算：事件直接 ACK，水位不前进，
	// 启动之后由下一个事件和追赶循环从水位下一期补算。
	gate func(setID string) bool
	// catchupGate 非空时，实时门禁关闭的集合在恢复追赶中仍可推进连续水位（只写历史周期）。
	catchupGate func(setID string) bool
	// params 在开算一期之前冻结集合的参数（因子动态调参执行计划 2.4）；为空时全部按默认参数计算。
	params trigger.ParamFreezer
}

// SetParamFreezer 设置参数冻结；必须在开始消费之前调用。
func (h *Handler) SetParamFreezer(params trigger.ParamFreezer) { h.params = params }

// SetGate 设置生命周期门禁；必须在开始消费之前调用。
func (h *Handler) SetGate(gate func(setID string) bool) { h.gate = gate }

// SetCatchupGate 设置恢复追赶的门禁：Advance（连续水位追赶）在实时门禁关闭时，只要追赶门禁放行就可以推进。
func (h *Handler) SetCatchupGate(gate func(setID string) bool) { h.catchupGate = gate }

// SetContinuity 打开连续水位推进；必须在开始消费之前调用。
func (h *Handler) SetContinuity(continuity *Continuity) { h.continuity = continuity }

// Advance 在集合的通道里按连续水位推进一次（追赶循环与补齐通知调用）。
func (h *Handler) Advance(ctx context.Context, setID string) error {
	if h == nil || h.continuity == nil {
		return nil
	}
	if h.gate != nil && !h.gate(setID) && (h.catchupGate == nil || !h.catchupGate(setID)) {
		return nil
	}
	_, err := h.lanes.Do(ctx, setID, func(laneCtx context.Context) (pipeline.Outcome, error) {
		return pipeline.Outcome{}, h.continuity.advanceSet(laneCtx, h, setID)
	})
	return err
}

func NewHandler(sets trigger.SetLocator, periodStore trigger.PeriodStore, runner trigger.PipelineRunner, locks trigger.SetLocks, cfg HandlerConfig) *Handler {
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
		sets: sets, store: periodStore, runner: runner, lanes: trigger.NewLanes(locks), registry: registry, initErr: initErr, cfg: cfg,
	}
}

func (h *Handler) Close() {
	if h != nil && h.lanes != nil {
		h.lanes.Close()
	}
}

func (h *Handler) LaneStatuses() []trigger.LaneStatus {
	if h == nil || h.lanes == nil {
		return nil
	}
	return h.lanes.Status()
}

func (h *Handler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if h == nil || h.sets == nil || h.store == nil || h.runner == nil || h.lanes == nil || h.registry == nil {
		if h != nil && h.initErr != nil {
			return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("initialize factor period event registry: %w", h.initErr)}
		}
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor period handler is not initialized")}
	}
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("factor period delivery is nil")}
	}
	if delivery.DecodeError != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("decode collector period event: %w", delivery.DecodeError)}
	}
	message, payload, err := events.DecodeCollectorPeriodCompletedWithContentType(
		h.registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType,
	)
	if err != nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("reject collector period event: %w", err)}
	}
	if message == nil || payload == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("collector period event is empty")}
	}

	event := periodEvent{
		periodTime: time.Unix(payload.GetPeriodTime(), 0).UTC(), eventID: message.GetEventId(),
		universe: payload.GetUniverseSubjectIds(), failed: payload.GetFailedSubjects(),
	}
	// 同一个源数据集可以有多个集合（各策略实例的私有集合）：事件分给每个启用的集合，各自在自己的串行通道里计算。
	// 任一集合遇到基础设施错误时整条事件重投；已经算完的集合靠“同一触发事件的 marker 已存在”跳过。
	sets, lookupErr := h.sets.EnabledSetsByDataset(ctx, message.GetSpaceId(), payload.GetDatasetId(), payload.GetFrequency())
	if lookupErr != nil {
		err = fmt.Errorf("%w: lookup enabled factor sets: %v", storageio.ErrInfra, lookupErr)
	}
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, engineSet := range sets {
		set, members := engineSet.Set, engineSet.Members
		if !engineSet.ResultReady {
			errs = append(errs, fmt.Errorf("%w: result dataset of factor set %s is not ready", storageio.ErrInfra, set.SetID))
			continue
		}
		_, setErr := h.lanes.Do(ctx, set.SetID, func(laneCtx context.Context) (pipeline.Outcome, error) {
			if set.Status != domain.SetStatusEnabled {
				return pipeline.Outcome{}, nil
			}
			if set.SetID == "" || set.SpaceID != message.GetSpaceId() || set.SourceDatasetID != payload.GetDatasetId() || set.Freq != payload.GetFrequency() {
				return pipeline.Outcome{}, fmt.Errorf("enabled factor set identity does not match collector event")
			}
			if h.gate != nil && !h.gate(set.SetID) {
				return pipeline.Outcome{}, nil
			}
			if h.continuity != nil {
				return pipeline.Outcome{}, h.continuity.onEvent(laneCtx, h, set, members, event)
			}
			return h.computePeriod(laneCtx, set, members, event.periodTime, event.universe, event.failed, event.eventID)
		})
		if setErr != nil {
			errs = append(errs, fmt.Errorf("factor set %s: %w", set.SetID, setErr))
		}
	}
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

// computePeriod 计算一期：已经算过（同一触发事件的 marker 已存在）就跳过；开算之前先冻结这一期的参数
// （冻结是幂等的：重投、重启后重算拿到同一份）。
func (h *Handler) computePeriod(ctx context.Context, set domain.FactorSet, members []domain.MemberDef, period time.Time, universe, failed []string, triggerEventID string) (pipeline.Outcome, error) {
	exists, existsErr := h.store.ComputedExists(ctx, set.SpaceID, set.ResultDatasetID, triggerEventID, period.Unix())
	if existsErr != nil {
		return pipeline.Outcome{}, fmt.Errorf("%w: check computed factor period: %v", storageio.ErrInfra, existsErr)
	}
	if exists {
		return pipeline.Outcome{}, nil
	}
	var params map[string]domain.ParamFreeze
	if h.params != nil && len(members) > 0 {
		aligned := period
		if clock, err := periodclock.ForSpace(set.SpaceID); err == nil {
			if value, err := clock.Align(period, set.Freq); err == nil {
				aligned = value
			}
		}
		frozen, err := h.params.Freeze(ctx, set, members, aligned)
		if err != nil {
			return pipeline.Outcome{}, fmt.Errorf("%w: freeze factor params: %v", storageio.ErrInfra, err)
		}
		params = frozen
	}
	plan, err := h.livePlan(ctx, set, members, params, period, universe, failed, triggerEventID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	return h.runner.Run(ctx, plan)
}

func (h *Handler) livePlan(ctx context.Context, set domain.FactorSet, members []domain.MemberDef, params map[string]domain.ParamFreeze, period time.Time, universe, failed []string, triggerEventID string) (pipeline.Plan, error) {
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
