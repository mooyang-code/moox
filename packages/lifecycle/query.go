package lifecycle

import (
	"context"
	"sort"
	"time"
)

// ---------------------------------------------------------------- 查询与门禁

// Stage 返回对象的当前阶段（不存在时为未初始化）。
func (c *Controller) Stage(ref Ref) Stage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref == (Ref{}) {
		return c.self.Stage
	}
	if r := c.objects[ref.key()]; r != nil {
		return r.Stage
	}
	return StageUninitialized
}

// moduleOpen 报告模块自身是否放行实时计算：没有控制意图，且自身阶段是就绪、预热或运行。
// 自身未初始化、未就绪、暂停、恢复追赶、重置、故障时一律不放行，不沿用「本来就不计算」的隐含假设。
// 调用方持有锁。
func (c *Controller) moduleOpen() bool {
	if c.self.Hold != HoldNone {
		return false
	}
	switch c.self.Stage {
	case StageReady, StageWarming, StageRunning:
		return true
	}
	return false
}

// Allowed 报告对象是否在做实时计算（预热或运行）：处理周期事件前用它做门禁。
// ref 为空时指模块自身（没有对象的模块，例如 Trade）：自身处于预热或运行。
// 模块自身暂停、恢复追赶、重置或故障时整个模块都不计算，不管对象各自处于什么阶段。
func (c *Controller) Allowed(ref Ref) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.moduleOpen() {
		return false
	}
	if ref == (Ref{}) {
		return c.self.Stage.Active()
	}
	if r := c.objects[ref.key()]; r != nil {
		return r.Stage.Active()
	}
	return false
}

// CatchupAllowed 报告对象是否可以做恢复追赶（只写历史数据：补采进来的增量同步、历史周期补算……）。
//
//	模块恢复追赶中   ：参与本次恢复的对象（手动暂停的对象继续排除）；
//	模块就绪/预热/运行：处于恢复追赶的单个对象；
//	模块暂停          ：只有处于恢复追赶的单个对象（resume --force 的例外，不放开整个模块）；
//	重置、故障、未初始化、未就绪：一律不允许。
//
// ref 为空指模块自身。
func (c *Controller) CatchupAllowed(ref Ref) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.self.Hold == HoldPostReset {
		return false
	}
	switch c.self.Stage {
	case StageResuming:
		if ref == (Ref{}) {
			return true
		}
		o := c.objects[ref.key()]
		return o != nil && (o.catchupMember || o.Stage == StageResuming)
	case StageReady, StageWarming, StageRunning, StagePaused:
		if ref == (Ref{}) {
			return false
		}
		o := c.objects[ref.key()]
		return o != nil && o.Stage == StageResuming
	}
	return false
}

// Halted 报告 ref（空表示模块自身）是否被有意停下：暂停、恢复追赶、重置、重置失败、故障，或者带着控制意图。
// 它和 Allowed 不同：自身未初始化、未就绪不算「有意停下」。Trade 这类下单的模块用它做门禁——
// 就绪检查的抖动不应该让执行和目标接收停掉，只有用户（或系统）有意停下才停。
func (c *Controller) Halted(ref Ref) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.self.Hold != HoldNone {
		return true
	}
	switch c.self.Stage {
	case StagePaused, StageResuming, StageResetting, StageResetFailed, StageFault:
		return true
	}
	if ref == (Ref{}) {
		return false
	}
	o := c.objects[ref.key()]
	return o != nil && (o.Stage == StagePaused || o.Stage == StageResuming || o.Stage == StageFault)
}

// Warming 报告对象是否处于预热（预热期间不真实下单）。
func (c *Controller) Warming(ref Ref) bool { return c.Stage(ref) == StageWarming }

// effective 是考虑模块自身状态后的有效阶段；调用方持有锁。
func (c *Controller) effective(r *record) Stage {
	if r == c.self {
		return r.Stage
	}
	if !r.Stage.Active() {
		return r.Stage
	}
	switch c.self.Stage {
	case StagePaused, StageResuming, StageResetting, StageResetFailed, StageFault:
		return c.self.Stage
	}
	if c.self.Hold == HoldSystemPause {
		return StagePaused
	}
	return r.Stage
}

func (c *Controller) object(r *record) Object {
	obj := Object{
		Ref: r.Ref, Stage: r.Stage, Effective: c.effective(r), Color: c.effective(r).Color(), Reason: r.Reason, Since: r.Since,
		Pause: r.Pause, PausedSince: r.PausedSince, Resume: r.Resume, Recovered: r.Recovered, Hold: r.Hold,
		Checks: append([]Check(nil), c.checks[r.key()]...),
	}
	if r.catchup != nil {
		cu := *r.catchup
		obj.Catchup = &cu
	}
	return obj
}

// Snapshot 返回模块的状态快照。
func (c *Controller) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := Snapshot{Module: c.opts.Module, Self: c.object(c.self), UpdatedAt: c.opts.Now().UTC()}
	keys := make([]string, 0, len(c.objects))
	for k := range c.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		snap.Objects = append(snap.Objects, c.object(c.objects[k]))
	}
	snap.Stage, snap.Reason, snap.Degraded = c.summarize(snap)
	snap.Color = snap.Stage.Color()
	return snap
}

// summarize 推导汇总阶段：模块自身没有通过检查、重置中、暂停、恢复追赶时，汇总就是模块自身的阶段；
// 有对象时，故障 > 未就绪 > 暂停 > 恢复追赶 > 预热 > 运行 > 就绪 > 未初始化。
// 降级只算「不是用户意图」的情形：自动暂停、故障、未就绪；手动和系统暂停是用户意图，恢复追赶是预期的过渡，都不算降级。
func (c *Controller) summarize(s Snapshot) (Stage, string, bool) {
	switch s.Self.Stage {
	case StageUninitialized, StageNotReady, StageFault, StageResetting, StageResetFailed:
		return s.Self.Stage, s.Self.Reason, s.Self.Stage == StageResetFailed
	case StagePaused, StageResuming:
		return s.Self.Stage, s.Self.Reason, s.Self.Stage == StagePaused && s.Self.Pause != nil && s.Self.Pause.Kind == PauseAuto
	}
	if s.Self.Hold == HoldSystemPause {
		return StagePaused, s.Self.Reason, false
	}
	if len(s.Objects) == 0 {
		return s.Self.Stage, s.Self.Reason, false
	}
	rank := map[Stage]int{StageFault: 7, StageNotReady: 6, StagePaused: 5, StageResuming: 4, StageWarming: 3, StageRunning: 2, StageReady: 1, StageUninitialized: 0}
	worst, reason := s.Objects[0].Stage, s.Objects[0].Reason
	attention, degraded, running, warming := false, false, 0, false
	for _, o := range s.Objects {
		if rank[o.Stage] > rank[worst] {
			worst, reason = o.Stage, o.Reason
		}
		switch o.Stage {
		case StagePaused:
			attention = true
			degraded = degraded || o.Pause == nil || o.Pause.Kind == PauseAuto
		case StageFault, StageNotReady:
			attention, degraded = true, true
		case StageResuming:
			attention = true
		}
		if o.Stage.Active() {
			running++
			warming = warming || o.Stage == StageWarming
		}
	}
	// 有对象在跑、另一些对象暂停、恢复追赶、故障：汇总取在跑的阶段（首页显示「运行中」，降级时附「降级」）。
	if attention && running > 0 {
		best := StageRunning
		if warming {
			best = StageWarming
		}
		return best, reason, degraded
	}
	return worst, reason, degraded
}

// Recover 在模块启动完成后调用一次：立刻跑检查，让重启前处于预热、运行的对象在检查通过后回到原阶段。
func (c *Controller) Recover(ctx context.Context) { c.Refresh(ctx) }

// Run 按 interval 周期调用 Refresh，直到 ctx 结束。
func (c *Controller) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Refresh(ctx)
		}
	}
}

// Compact 是放进 /healthz 响应（details.lifecycle）的精简快照：Monitor 据此汇总首页状态。
// 只列出需要关注的对象（暂停、恢复追赶、故障、未就绪），运行与预热的对象只计数，响应体不随对象数增长。
type Compact struct {
	Stage    Stage  `json:"stage"`
	Color    string `json:"color"`
	Degraded bool   `json:"degraded,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Self 是模块自身的阶段；Pause 是模块自身的暂停详情；Hold 是控制意图。
	Self  Stage      `json:"self"`
	Pause *PauseInfo `json:"pause,omitempty"`
	Hold  Hold       `json:"hold,omitempty"`
	// Catchup 是模块自身的恢复追赶进度（模块恢复追赶中）。
	Catchup *Catchup `json:"catchup,omitempty"`
	// Counts 是各阶段的对象数；PauseKinds 是暂停的对象（含模块自身）按类型计数。
	Counts     map[Stage]int     `json:"counts,omitempty"`
	PauseKinds map[PauseKind]int `json:"pause_kinds,omitempty"`
	Attention  []Object          `json:"attention,omitempty"`
	// Recovered 是重启后自动回到原阶段的对象数（首页提示「已自动恢复」）。
	Recovered int `json:"recovered,omitempty"`
}

// maxAttention 是精简快照里最多列出的需要关注的对象数。
const maxAttention = 6

// Compact 返回精简快照。
func (c *Controller) Compact() Compact {
	snap := c.Snapshot()
	out := Compact{
		Stage: snap.Stage, Color: snap.Color, Degraded: snap.Degraded, Reason: snap.Reason,
		Self: snap.Self.Stage, Pause: snap.Self.Pause, Hold: snap.Self.Hold, Catchup: snap.Self.Catchup,
		Counts: map[Stage]int{},
	}
	countPause := func(o Object) {
		if o.Stage == StagePaused && o.Pause != nil {
			if out.PauseKinds == nil {
				out.PauseKinds = map[PauseKind]int{}
			}
			out.PauseKinds[o.Pause.Kind]++
		}
	}
	countPause(snap.Self)
	for _, o := range snap.Objects {
		out.Counts[o.Stage]++
		countPause(o)
		if o.Recovered {
			out.Recovered++
		}
		attn := o.Stage == StagePaused || o.Stage == StageResuming || o.Stage == StageFault || o.Stage == StageNotReady
		if attn && len(out.Attention) < maxAttention {
			o.Checks = failedChecks(o.Checks)
			out.Attention = append(out.Attention, o)
		}
	}
	return out
}

func failedChecks(checks []Check) []Check {
	var out []Check
	for _, c := range checks {
		if c.Status == CheckFail {
			out = append(out, c)
		}
	}
	return out
}
