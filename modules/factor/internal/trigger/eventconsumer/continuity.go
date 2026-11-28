package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultMaxMissingRatio = 0.1
	defaultMaxPeriods      = 30
)

// LocalRows 判断没有事件可用的周期哪些标的已经到了：先把这些标的增量同步到本地，再看本地这一期有没有真实的行
// （localdata.Data.RealRowsAt）。
type LocalRows interface {
	RealRowsAt(ctx context.Context, set domain.FactorTask, subjects []string, period time.Time) (map[string]bool, error)
}

// ContinuityConfig 是连续水位的参数。
type ContinuityConfig struct {
	Store *watermark.Store
	Local LocalRows
	// MaxMissingRatio 是缺失阈值：未确认缺失的失败标的 ÷ 期望标的超过它的周期视为无效，阻塞水位（默认 10%）；
	// SetRatios 按任务 ID 覆盖。
	MaxMissingRatio float64
	SetRatios       map[string]float64
	// MaxPeriods 是一次推进最多处理的周期数，剩下的交给追赶循环。
	MaxPeriods int
	Now        func() time.Time
}

// Continuity 维护每个任务的连续水位：从水位下一期逐期处理到最新已知周期，前面有还没补到的无效周期就停住，
// 水位不前进；当期缺失在阈值以内时先算已到的标的，缺的记进空洞账本由补采后的只填充补算补上。
type Continuity struct {
	cfg ContinuityConfig

	mu sync.Mutex
	// absent 是补采确认行情源没有的 (任务, 周期) 标的：不计入缺失，也不阻塞。
	absent map[string]map[int64]absentEntry
}

type absentEntry struct {
	subjects map[string]struct{}
	until    time.Time
}

// NewContinuity 构造连续水位推进。
func NewContinuity(cfg ContinuityConfig) (*Continuity, error) {
	if cfg.Store == nil || cfg.Local == nil {
		return nil, errors.New("continuity requires a watermark store and local rows")
	}
	if cfg.MaxMissingRatio <= 0 {
		cfg.MaxMissingRatio = defaultMaxMissingRatio
	}
	if cfg.MaxPeriods <= 0 {
		cfg.MaxPeriods = defaultMaxPeriods
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Continuity{cfg: cfg, absent: map[string]map[int64]absentEntry{}}, nil
}

// States 返回全部任务的推进状态（心跳上报与指标用）。
func (c *Continuity) States() []watermark.State { return c.cfg.Store.All() }

// DeleteTask 清理已删除任务的水位记录（含持久化文件）与内存中的确认缺失表。
func (c *Continuity) DeleteTask(taskID string) (bool, error) {
	c.mu.Lock()
	delete(c.absent, taskID)
	c.mu.Unlock()
	return c.cfg.Store.Delete(taskID)
}

// RetainTasks 仅保留 keep 中的任务，删除其余任务的落盘水位记录与内存确认缺失表。
func (c *Continuity) RetainTasks(keep map[string]struct{}) ([]string, error) {
	c.mu.Lock()
	for taskID := range c.absent {
		if _, ok := keep[taskID]; !ok {
			delete(c.absent, taskID)
		}
	}
	c.mu.Unlock()
	return c.cfg.Store.Retain(keep)
}

// NoteAbsent 记下补采确认行情源没有的标的（K 线补写事件带来），30 分钟后过期。
func (c *Continuity) NoteAbsent(taskID string, from, to time.Time, step time.Duration, subjects []string) {
	if len(subjects) == 0 || step <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.absent[taskID] == nil {
		c.absent[taskID] = map[int64]absentEntry{}
	}
	until := c.cfg.Now().Add(30 * time.Minute)
	for period := from.UTC(); period.Before(to); period = period.Add(step) {
		entry := c.absent[taskID][period.Unix()]
		if entry.subjects == nil {
			entry.subjects = map[string]struct{}{}
		}
		for _, subject := range subjects {
			entry.subjects[strings.TrimSpace(subject)] = struct{}{}
		}
		entry.until = until
		c.absent[taskID][period.Unix()] = entry
	}
}

// absentAt 返回这一期确认没有的标的（复制一份：NoteAbsent 在补写事件的消费协程里会修改同一个任务）。
func (c *Continuity) absentAt(taskID string, period time.Time) map[string]struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	for key, entry := range c.absent[taskID] {
		if now.After(entry.until) {
			delete(c.absent[taskID], key)
		}
	}
	entry := c.absent[taskID][period.Unix()]
	subjects := make(map[string]struct{}, len(entry.subjects))
	for subject := range entry.subjects {
		subjects[subject] = struct{}{}
	}
	return subjects
}

func (c *Continuity) ratioFor(taskID string) float64 {
	if ratio, ok := c.cfg.SetRatios[taskID]; ok && ratio > 0 {
		return ratio
	}
	return c.cfg.MaxMissingRatio
}

// onEvent 记下事件带来的最新周期与期望标的，然后推进。
func (c *Continuity) onEvent(ctx context.Context, h *Handler, set domain.FactorTask, members []domain.MemberDef, event periodEvent) error {
	step, err := c.note(set, event, true)
	if err != nil {
		return err
	}
	return c.advance(ctx, h, set, members, step, &event)
}

// note 只记下事件带来的最新周期（追赶目标）与期望序列，不推进；返回任务的周期步长。
// 任务还没就绪（例如引擎重启后窗口载入中）时到达的实时事件只调用它（create 为 false）：就绪之后由追赶循环从水位下一期补算，
// 不必等下一条实时事件。create 为 false 时不给还没有水位记录的任务建立记录（与 NoteTarget 同一条规则）：
// 从没算过的任务，连续性从它就绪后的第一条实时事件起保证，之前的历史用补算作业。
func (c *Continuity) note(set domain.FactorTask, event periodEvent, create bool) (time.Duration, error) {
	// 连续水位全部在轴上：事件周期在入口已换到轴上，轴是连续的。
	clock := periodclock.Continuous{}
	step, err := clock.Duration(set.Freq)
	if err != nil {
		return 0, err
	}
	period, err := clock.Align(event.periodTime, set.Freq)
	if err != nil {
		return 0, err
	}
	state, ok := c.cfg.Store.Get(set.TaskID)
	if !ok || state.CommittedThrough.IsZero() {
		if !create {
			return step, nil
		}
		// 第一次见到这个任务：从这一期起保证连续，不回头补历史（历史用补算作业）。
		state = watermark.State{TaskID: set.TaskID, CommittedThrough: period.Add(-step)}
	}
	state.SpaceID, state.DatasetID, state.Freq = set.SpaceID, set.SourceDatasetID, set.Freq
	if period.After(state.Target) {
		state.Target = period
	}
	if len(event.universe) > 0 && !period.Before(state.Target) {
		state.UniverseSubjects = append([]string(nil), event.universe...)
	}
	if err := c.cfg.Store.Put(state); err != nil {
		return 0, fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
	}
	return step, nil
}

// NoteTarget 显式把任务的追赶目标推到 period（取已有目标与 period 中较晚的）。
// 暂停期间的实时事件在连续水位目标更新之前就被 ACK，目标会停在暂停前；恢复追赶时由这里显式建立目标，
// 不等下一条实时事件。任务还没有水位记录（从未跑过实时周期）时不建立：连续性从第一次实时事件起保证。
func (c *Continuity) NoteTarget(taskID string, period time.Time) error {
	if err := c.cfg.Store.RaiseTarget(taskID, period); err != nil {
		return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
	}
	return nil
}

// advanceTask 在没有事件时推进（追赶循环、K 线补写事件）。
func (c *Continuity) advanceTask(ctx context.Context, h *Handler, taskID string) error {
	state, ok := c.cfg.Store.Get(taskID)
	if !ok || !state.Behind() {
		return nil
	}
	engineTask, found, err := h.sets.EnabledTask(ctx, taskID)
	if err != nil || !found || engineTask.Set.Status != domain.TaskStatusEnabled || !engineTask.ResultReady {
		return err
	}
	set, members := engineTask.Set, engineTask.Members
	step, err := periodclock.Continuous{}.Duration(set.Freq)
	if err != nil {
		return err
	}
	return c.advance(ctx, h, set, members, step, nil)
}

// advance 从水位下一期逐期处理到最新已知周期（一次至多 MaxPeriods 期）。
func (c *Continuity) advance(ctx context.Context, h *Handler, set domain.FactorTask, members []domain.MemberDef, step time.Duration, event *periodEvent) error {
	state, _ := c.cfg.Store.Get(set.TaskID)
	for processed := 0; state.Behind() && processed < c.cfg.MaxPeriods; processed++ {
		period := state.CommittedThrough.Add(step)
		scope := periodScope{universe: state.UniverseSubjects}
		if event != nil && event.periodTime.Equal(period) {
			scope = event.scope()
		} else {
			missing, err := c.missingSubjects(ctx, set, period, scope.universe)
			if err != nil {
				return err
			}
			scope.failed = missing
		}
		expected, unconfirmed, confirmed, scope := c.scoped(set, period, scope)
		if reason := c.invalid(set.TaskID, expected, unconfirmed, confirmed); reason != "" {
			if state.BlockedSince.IsZero() || !state.BlockedPeriod.Equal(period) {
				if state.BlockedSince.IsZero() {
					state.BlockedSince = c.cfg.Now().UTC()
				}
				log.WarnContextf(ctx, "factor_watermark_blocked task_id=%s period=%s reason=%s missing=%d expected=%d", set.TaskID, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), reason, unconfirmed, expected)
			}
			state.BlockedPeriod, state.BlockedReason = period, reason
			if err := c.cfg.Store.Put(state); err != nil {
				return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
			}
			return nil
		}
		// 补采确认行情源没有的标的不参与计算：不读窗口、不记空洞账本、不补算。
		// 范围内的标的全部确认没有时这一期没有可算的，直接推进。
		if expected > 0 {
			if _, err := h.computePeriod(ctx, set, members, period, scope); err != nil {
				if !transient(err) {
					// 重试也不会通过的错误（例如完成标记超过体积上限、被 Storage 判为无效请求）：水位停在这一期，
					// 记成阻塞，让水位阻塞的监控看得到；之后的追赶照常重试，问题排除后自动解除。
					if state.BlockedSince.IsZero() || !state.BlockedPeriod.Equal(period) {
						if state.BlockedSince.IsZero() {
							state.BlockedSince = c.cfg.Now().UTC()
						}
						log.ErrorContextf(ctx, "factor_watermark_blocked task_id=%s period=%s reason=compute_failed error=%v", set.TaskID, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), err)
					}
					state.BlockedPeriod, state.BlockedReason = period, "compute_failed"
					if putErr := c.cfg.Store.Put(state); putErr != nil {
						return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, putErr)
					}
				}
				return err
			}
		}
		if !state.BlockedSince.IsZero() {
			log.InfoContextf(ctx, "factor_watermark_unblocked task_id=%s period=%s blocked_for=%s", set.TaskID, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), c.cfg.Now().Sub(state.BlockedSince).Truncate(time.Second))
		}
		state.CommittedThrough, state.BlockedSince, state.BlockedPeriod, state.BlockedReason = period, time.Time{}, time.Time{}, ""
		if err := c.cfg.Store.Put(state); err != nil {
			return fmt.Errorf("%w: save factor watermark: %v", storageio.ErrInfra, err)
		}
	}
	return nil
}

// scoped 整理一期的范围：扣除补采确认没有的标的，返回期望标的数与缺失标的数、确认没有的标的，以及真正要算的范围。
func (c *Continuity) scoped(set domain.FactorTask, period time.Time, scope periodScope) (expected, missing int, confirmed map[string]struct{}, out periodScope) {
	absent := c.absentAt(set.TaskID, period)
	confirmed = map[string]struct{}{}
	inScope := make(map[string]struct{}, len(scope.universe))
	for _, subject := range uniqueStrings(scope.universe) {
		if _, ok := absent[subject]; ok {
			confirmed[subject] = struct{}{}
			continue
		}
		out.universe = append(out.universe, subject)
		inScope[subject] = struct{}{}
	}
	for _, subject := range uniqueStrings(scope.failed) {
		if _, ok := inScope[subject]; ok {
			out.failed = append(out.failed, subject)
		}
	}
	return len(out.universe), len(out.failed), confirmed, out
}

// invalid 判断一期是否无效：不知道期望数量、零提交（全部缺失）、缺失超过阈值。
// 范围内的标的全部确认没有时不算无效（没有可算的，直接推进）。
func (c *Continuity) invalid(taskID string, expected, missing int, confirmed map[string]struct{}) string {
	switch {
	case expected == 0 && len(confirmed) > 0:
		return ""
	case expected == 0:
		return "no_universe"
	case missing == expected:
		return "no_rows"
	case float64(missing)/float64(expected) > c.ratioFor(taskID):
		return "missing_over_threshold"
	}
	return ""
}

// missingSubjects 返回期望标的里这一期在本地还没有真实行的（同步之后判断，见 LocalRows）。
func (c *Continuity) missingSubjects(ctx context.Context, set domain.FactorTask, period time.Time, universe []string) ([]string, error) {
	subjects := uniqueStrings(universe)
	if len(subjects) == 0 {
		return nil, nil
	}
	present, err := c.cfg.Local.RealRowsAt(ctx, set, subjects, period)
	if err != nil {
		return nil, fmt.Errorf("%w: 判断 %s 哪些标的已到：%v", storageio.ErrInfra, periodclock.FormatPeriod(set.SpaceID, set.Freq, period), err)
	}
	var missing []string
	for _, subject := range subjects {
		if !present[subject] {
			missing = append(missing, subject)
		}
	}
	return missing, nil
}

func periodStep(set domain.FactorTask) time.Duration {
	step, err := periodclock.Continuous{}.Duration(set.Freq)
	if err != nil {
		return time.Minute
	}
	return step
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
