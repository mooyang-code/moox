package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"trpc.group/trpc-go/trpc-go/log"
)

// 主动预热：引擎成为活动引擎（启动、接管租约）或者目录里出现新的因子集之后，立刻为实时缓存里还没有的因子集排队后台预热，
// 不等该集合的第一个实时周期到达才开始。否则那一期的全部标的都还在预热中，结果只能跳过：1h 集合在 16:10 重启，
// 要到 17:00 那一期才开始预热，这一期整根缺失。预热仍走后台预热的读取节流；之后哪些标的需要再预热由实时周期自己判断，
// 来不及预热而跳过的周期记入账本，由跳过恢复补算（见 gapfill.go）。

const (
	prewarmInterval = time.Minute
	prewarmTimeout  = time.Minute
)

type prewarmCatalog interface {
	SetIDs() []string
	SetByID(setID string) (domain.EngineSet, SetLookup)
}

type prewarmWindows interface {
	WarmupStatuses() []pipeline.WarmupStatus
	Prewarm(plan pipeline.Plan) int
}

type prewarmer struct {
	sets    prewarmCatalog
	source  SourceDataset
	windows prewarmWindows
	active  func() bool
	now     func() time.Time
	kick    chan struct{}
}

func newPrewarmer(sets prewarmCatalog, source SourceDataset, windows prewarmWindows, active func() bool) *prewarmer {
	return &prewarmer{sets: sets, source: source, windows: windows, active: active, now: time.Now, kick: make(chan struct{}, 1)}
}

// Kick 让预热循环尽快检查一次，不阻塞。引擎成为活动引擎、目录变化时调用。
func (p *prewarmer) Kick() {
	if p == nil {
		return
	}
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// Run 启动时、每次 Kick 以及每隔 prewarmInterval 检查一次，直到 ctx 结束；排队失败的集合（例如 Storage 暂时不可用）下次再试。
func (p *prewarmer) Run(ctx context.Context) {
	ticker := time.NewTicker(prewarmInterval)
	defer ticker.Stop()
	for {
		p.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		case <-ticker.C:
		}
	}
}

// once 为目录里实时缓存还没有的因子集排队预热。实时缓存里已有的集合（实时周期到过或已经预热过）不再处理，
// 所以稳定运行时每轮只是内存里的比对。
func (p *prewarmer) once(ctx context.Context) {
	if p.active != nil && !p.active() {
		return
	}
	known := make(map[string]struct{})
	for _, status := range p.windows.WarmupStatuses() {
		known[status.SetID] = struct{}{}
	}
	for _, setID := range p.sets.SetIDs() {
		if ctx.Err() != nil || (p.active != nil && !p.active()) {
			return
		}
		if _, ok := known[setID]; ok {
			continue
		}
		set, lookup := p.sets.SetByID(setID)
		if lookup != SetFound {
			continue
		}
		if err := p.prewarmSet(ctx, set); err != nil && ctx.Err() == nil {
			log.WarnContextf(ctx, "factor_prewarm_failed set_id=%s error=%v", setID, err)
		}
	}
}

// prewarmSet 按实时周期的方式为一个集合构造计划（同样的回看窗口、源列与标的范围），交给后台预热。
func (p *prewarmer) prewarmSet(ctx context.Context, set domain.EngineSet) error {
	ctx, cancel := context.WithTimeout(ctx, prewarmTimeout)
	defer cancel()
	clock, err := periodclock.ForSpace(set.Set.SpaceID)
	if err != nil {
		return err
	}
	period, err := prewarmPeriod(clock, set.Set.Freq, p.now())
	if err != nil {
		return err
	}
	columns, err := p.source.DatasetColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
	if err != nil {
		return fmt.Errorf("list source dataset columns: %w", err)
	}
	universe, err := setSubjects(ctx, p.source, set, nil)
	if err != nil {
		return err
	}
	plan, err := pipeline.BuildLivePlan(clock, pipeline.LiveInput{
		Set: set.Set, Factors: set.Factors, PeriodTime: period, Universe: universe, CarryColumns: columns,
	})
	if err != nil {
		return err
	}
	if queued := p.windows.Prewarm(plan); queued > 0 {
		log.InfoContextf(ctx, "factor_prewarm_queued set_id=%s subjects=%d window=%s/%s", set.Set.SetID, queued,
			plan.TargetStart.UTC().Format(time.RFC3339), plan.TargetEnd.UTC().Format(time.RFC3339))
	}
	return nil
}

// prewarmPeriod 是预热计划的目标周期：最近一根已收盘 K 线的前一根。预热窗口只读到早已入库的 K 线，不会把还没采到的
// 最新一根当成空洞；下一个实时周期离窗口终点只有一两根，在实时增量读取的范围内。
func prewarmPeriod(clock periodclock.Clock, freq string, now time.Time) (time.Time, error) {
	duration, err := clock.Duration(freq)
	if err != nil {
		return time.Time{}, err
	}
	// 时钟的周期以 Unix 纪元为锚；time.Truncate 以公元元年为锚，两者相差整数天，对支持的频率结果一致，再由 Align 校验。
	aligned, err := clock.Align(now.UTC().Truncate(duration), freq)
	if err != nil {
		return time.Time{}, err
	}
	return aligned.Add(-2 * duration), nil
}
