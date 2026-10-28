package lifecycle

import (
	"context"
	"errors"
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

// StageSince 返回对象进入当前阶段的时间（对象不存在时为零值）。
func (c *Controller) StageSince(ref Ref) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref == (Ref{}) {
		return c.self.Since
	}
	if r := c.objects[ref.key()]; r != nil {
		return r.Since
	}
	return time.Time{}
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

// ModuleOpen 报告模块自身是否放行（没有控制意图，且自身阶段是就绪、预热或运行）：后台的领取类动作（例如补算领取）用它做门禁。
func (c *Controller) ModuleOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trackOpen()
	return c.moduleOpen()
}

// ModuleGeneration 返回模块的暂停代次：模块每次从放行变为不放行加一。
// 在途的长任务（补算作业）记下领取时的代次，代次变化说明期间发生过暂停，不能再沿用旧的执行上下文写结果。
func (c *Controller) ModuleGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trackOpen()
	return c.generation
}

// trackOpen 在模块由放行变为不放行时推进代次；调用方持有锁。每次状态变更落盘前与每次查询时都会调用，
// 所以两次观察之间的任何一次关闭都会被计入。
func (c *Controller) trackOpen() {
	open := c.moduleOpen()
	if c.wasOpen && !open {
		c.generation++
	}
	c.wasOpen = open
}

// Quiesced 报告对象本次暂停是否已经收敛：处于暂停、有暂停详情，并且收敛时间不早于本次暂停的开始时间。
// 没有 Quiesce 钩子的模块，暂停命令成功即写入收敛时间。
func (o Object) Quiesced() bool {
	return o.Stage == StagePaused && o.Pause != nil && !o.QuiescedAt.IsZero() && !o.QuiescedAt.Before(o.Pause.Since)
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
	// 重启后待恢复的暂停（启动前检查还没通过）仍然是被有意停下的：不能因为检查暂时不过就放开写路径。
	if c.self.Pending == StagePaused {
		return true
	}
	if ref != (Ref{}) {
		if o := c.objects[ref.key()]; o != nil && o.Pending == StagePaused {
			return true
		}
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

// ReleasedAt 返回对象最近一次放行的时间（没有放行过为零）。
func (c *Controller) ReleasedAt(ref Ref) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ref == (Ref{}) {
		return c.self.ReleasedAt
	}
	if r := c.objects[ref.key()]; r != nil {
		return r.ReleasedAt
	}
	return time.Time{}
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
		Pause: r.Pause, PausedSince: r.PausedSince, QuiescedAt: r.QuiescedAt, Resume: r.Resume, Recovered: r.Recovered, Hold: r.Hold, ReleasedAt: r.ReleasedAt, LastReset: r.LastReset,
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
func (c *Controller) Recover(ctx context.Context) {
	c.Refresh(ctx)
	c.AutoAdvance(ctx)
}

// AutoAdvance 把 AutoStart 钩子认定的对象从未初始化推进到预热：未初始化先 init，就绪再 start；
// 预热中的对象在 AutoConfirm 钩子认可时自动确认进入运行。
// 其他阶段（未就绪、预热、运行、暂停、恢复追赶、重置、故障）不动；命令被拒绝（模块重置中、等待重置后初始化、
// 模块自身没有放行）时静默跳过，下一轮刷新再试。
func (c *Controller) AutoAdvance(ctx context.Context) {
	if c.opts.Hooks.AutoStart == nil || c.opts.Hooks.Objects == nil {
		return
	}
	for _, ref := range c.opts.Hooks.Objects() {
		if ctx.Err() != nil {
			return
		}
		if !c.opts.Hooks.AutoStart(ref) {
			continue
		}
		if c.Stage(ref) == StageUninitialized {
			if _, err := c.Command(ctx, CmdInit, ref); err != nil {
				c.autoAdvanceFailed(ref, CmdInit, err)
				continue
			}
		}
		if c.Stage(ref) == StageReady {
			if _, err := c.Command(ctx, CmdStart, ref); err != nil {
				c.autoAdvanceFailed(ref, CmdStart, err)
			}
		}
		if c.opts.Hooks.AutoConfirm != nil && c.Stage(ref) == StageWarming && c.opts.Hooks.AutoConfirm(ref, c.StageSince(ref)) {
			if _, err := c.Command(ctx, CmdConfirm, ref); err != nil {
				c.autoAdvanceFailed(ref, CmdConfirm, err)
			}
		}
	}
}

// autoAdvanceFailed 记录自动推进失败：被门禁拒绝是预期的等待，不记审计；其他错误（初始化失败）记审计，下一轮重试。
func (c *Controller) autoAdvanceFailed(ref Ref, cmd Command, err error) {
	var notAllowed *ErrNotAllowed
	if errors.As(err, &notAllowed) {
		return
	}
	c.audit("lifecycle_auto_%s_failed module=%s ref=%s/%s error=%q", cmd, c.opts.Module, ref.Kind, ref.ID, err.Error())
}

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
			c.AutoAdvance(ctx)
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
	// LastReset 是最近一次完成的重置。
	LastReset *ResetRecord `json:"last_reset,omitempty"`
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
		Self: snap.Self.Stage, Pause: snap.Self.Pause, Hold: snap.Self.Hold, Catchup: snap.Self.Catchup, LastReset: snap.Self.LastReset,
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
