// Package lifecycle 是各模块共用的生命周期状态机（设计第 6 节）。
//
// 每个模块和它管理的对象（因子集合、策略实例……）都有同一套阶段：
//
//	未初始化 → 未就绪 / 就绪 → 预热 → 运行；运行中可以暂停、自动暂停、故障。
//
// 阶段只由两类事件推进：用户命令（init、start、confirm、pause、resume）和启动前检查的结果。
// 就绪是检查通过，不会自动进入预热；预热要用户 start，进入运行要用户 confirm。
// 运行中检查失败的对象只暂停它自己（自动暂停），检查重新通过后自动回到暂停前的阶段。
// 阶段持久化在模块自己的状态文件里，重启后跑检查，通过就回到重启前的阶段。
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Stage 是生命周期阶段。
type Stage string

const (
	StageUninitialized Stage = "uninitialized" // 灰：刚部署，初始化未完成
	StageNotReady      Stage = "not_ready"     // 红：检查有失败项
	StageReady         Stage = "ready"         // 蓝：检查全部通过，等待用户启动
	StageWarming       Stage = "warming"       // 黄：预热，用真实数据跑完整链路，不真实下单
	StageRunning       Stage = "running"       // 绿：用户确认后运行
	StagePaused        Stage = "paused"        // 橙：手动暂停，或受上游故障影响自动暂停
	StageFault         Stage = "fault"         // 红：运行中出现自身无法恢复的错误
)

// Color 返回阶段在首页的显示颜色。
func (s Stage) Color() string {
	switch s {
	case StageUninitialized:
		return "gray"
	case StageNotReady, StageFault:
		return "red"
	case StageReady:
		return "blue"
	case StageWarming:
		return "yellow"
	case StageRunning:
		return "green"
	case StagePaused:
		return "orange"
	}
	return "gray"
}

// Active 报告阶段是否在计算（预热与运行都在跑完整链路，区别只是预热不真实下单）。
func (s Stage) Active() bool { return s == StageWarming || s == StageRunning }

// CheckStatus 是一项检查的结果。
type CheckStatus string

const (
	CheckPass CheckStatus = "PASS"
	CheckWarn CheckStatus = "WARN"
	CheckFail CheckStatus = "FAIL"
)

// Check 是一项启动前检查的结果；失败项给出原因和处理办法。
type Check struct {
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message,omitempty"`
	// Remedy 是失败时的处理办法（例如要执行的命令）。
	Remedy string `json:"remedy,omitempty"`
}

// Passed 报告检查是否全部没有失败项（警告不挡路）。
func Passed(checks []Check) bool {
	for _, c := range checks {
		if c.Status == CheckFail {
			return false
		}
	}
	return true
}

// Command 是用户命令。
type Command string

const (
	CmdInit    Command = "init"
	CmdCheck   Command = "check"
	CmdStart   Command = "start"
	CmdConfirm Command = "confirm"
	CmdPause   Command = "pause"
	CmdResume  Command = "resume"
)

// Ref 指向一个对象；Kind 为空表示模块自身。
type Ref struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
}

func (r Ref) key() string { return r.Kind + "/" + r.ID }

// Object 是一个对象（或模块自身）的当前状态。
type Object struct {
	Ref
	Stage  Stage  `json:"stage"`
	Color  string `json:"color"`
	Reason string `json:"reason,omitempty"`
	// Since 是进入当前阶段的时间。
	Since time.Time `json:"since"`
	// AutoPaused 为真表示暂停是检查失败触发的，检查通过后自动恢复；Resume 是暂停前的阶段。
	AutoPaused bool  `json:"auto_paused,omitempty"`
	Resume     Stage `json:"resume,omitempty"`
	// Recovered 为真表示这次是重启后自动回到原阶段（首页提示「已自动恢复」）。
	Recovered bool    `json:"recovered,omitempty"`
	Checks    []Check `json:"checks,omitempty"`
}

// Snapshot 是模块的状态快照：模块自身、各对象，以及由对象推导的汇总阶段。
type Snapshot struct {
	Module  string   `json:"module"`
	Self    Object   `json:"self"`
	Objects []Object `json:"objects,omitempty"`
	// Stage 是汇总阶段：模块自身检查不过是未就绪；否则按对象推导（有故障取故障，有暂停取暂停……）。
	Stage     Stage     `json:"stage"`
	Color     string    `json:"color"`
	Degraded  bool      `json:"degraded,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Hooks 是模块提供的检查与动作。
type Hooks struct {
	// CheckModule 检查模块自身（本地存储布局、依赖是否可达……）。
	CheckModule func(ctx context.Context) []Check
	// CheckObject 检查一个对象能不能运行（数据源就绪、上游没有断档……）。
	CheckObject func(ctx context.Context, ref Ref) []Check
	// Objects 列出当前存在的对象（目录里启用的因子集合、策略实例……）。
	Objects func() []Ref
	// Init 执行初始化动作（可为 nil）；ref 为空表示模块自身。
	Init func(ctx context.Context, ref Ref) error
	// OnChange 在阶段变化后调用（日志、指标），可为 nil；在持有控制器锁时不会被调用。
	OnChange func(ref Ref, from, to Stage, reason string)
}

// Options 是控制器的参数。
type Options struct {
	Module string
	// StateFile 是阶段持久化文件；为空时只在内存里保存。
	StateFile string
	Now       func() time.Time
	Hooks     Hooks
}

// Controller 管理一个模块的生命周期。
type Controller struct {
	opts Options

	mu      sync.Mutex
	self    *record
	objects map[string]*record
	checks  map[string][]Check
}

type record struct {
	Ref
	Stage      Stage     `json:"stage"`
	Reason     string    `json:"reason,omitempty"`
	Since      time.Time `json:"since"`
	AutoPaused bool      `json:"auto_paused,omitempty"`
	Resume     Stage     `json:"resume,omitempty"`
	Recovered  bool      `json:"recovered,omitempty"`
	// Persisted 记录这个对象重启前所处的阶段（等待启动后检查）。
	Pending Stage `json:"pending,omitempty"`
}

// ErrNotAllowed 表示命令在当前阶段不允许。
type ErrNotAllowed struct {
	Ref     Ref
	Command Command
	Stage   Stage
	Why     string
}

func (e *ErrNotAllowed) Error() string {
	name := e.Ref.key()
	if e.Ref == (Ref{}) {
		name = "模块"
	}
	return fmt.Sprintf("%s 处于 %s，不能执行 %s：%s", name, e.Stage, e.Command, e.Why)
}

// New 创建控制器并读取持久化的阶段。重启前处于预热、运行、暂停的对象先标记为待恢复，Recover 跑完检查后才回到原阶段。
func New(opts Options) (*Controller, error) {
	if opts.Module == "" {
		return nil, errors.New("lifecycle: module 不能为空")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &Controller{opts: opts, objects: map[string]*record{}, checks: map[string][]Check{}}
	c.self = &record{Stage: StageUninitialized, Since: opts.Now().UTC()}
	if opts.StateFile == "" {
		return c, nil
	}
	raw, err := os.ReadFile(opts.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取生命周期状态 %s：%w", opts.StateFile, err)
	}
	var saved struct {
		Self    record   `json:"self"`
		Objects []record `json:"objects"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, fmt.Errorf("解析生命周期状态 %s：%w", opts.StateFile, err)
	}
	restore := func(r record) *record {
		out := r
		if r.Stage.Active() || r.Stage == StagePaused {
			// 重启后先未就绪，跑完检查再回到原阶段。
			out.Pending = r.Stage
			if r.Stage == StagePaused {
				out.Pending = r.Stage
			}
			out.Stage = StageNotReady
			out.Reason = "重启后等待启动前检查"
		}
		return &out
	}
	self := restore(saved.Self)
	c.self = self
	for _, o := range saved.Objects {
		rec := restore(o)
		c.objects[rec.key()] = rec
	}
	return c, nil
}

func (c *Controller) save() error {
	if c.opts.StateFile == "" {
		return nil
	}
	state := struct {
		Self    record   `json:"self"`
		Objects []record `json:"objects"`
	}{Self: *c.self}
	// 持久化的是「重启前所处的阶段」：等待恢复的对象保存它的待恢复阶段。
	persist := func(r *record) record {
		out := *r
		if r.Pending != "" && r.Stage == StageNotReady {
			out.Stage = r.Pending
			out.Pending = ""
		}
		return out
	}
	state.Self = persist(c.self)
	keys := make([]string, 0, len(c.objects))
	for k := range c.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		state.Objects = append(state.Objects, persist(c.objects[k]))
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.opts.StateFile), 0o750); err != nil {
		return err
	}
	tmp := c.opts.StateFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.opts.StateFile)
}

func (c *Controller) recordFor(ref Ref) *record {
	if ref == (Ref{}) {
		return c.self
	}
	r := c.objects[ref.key()]
	if r == nil {
		r = &record{Ref: ref, Stage: StageUninitialized, Since: c.opts.Now().UTC()}
		c.objects[ref.key()] = r
	}
	return r
}

type change struct {
	ref      Ref
	from, to Stage
	reason   string
}

// set 变更阶段并记下变化；调用方持有锁。
func (c *Controller) set(r *record, to Stage, reason string, changes *[]change) {
	if r.Stage == to && r.Reason == reason {
		return
	}
	from := r.Stage
	r.Stage, r.Reason = to, reason
	if from != to {
		r.Since = c.opts.Now().UTC()
		*changes = append(*changes, change{ref: r.Ref, from: from, to: to, reason: reason})
	}
}

func (c *Controller) notify(changes []change) {
	if c.opts.Hooks.OnChange == nil {
		return
	}
	for _, ch := range changes {
		c.opts.Hooks.OnChange(ch.ref, ch.from, ch.to, ch.reason)
	}
}

// ---------------------------------------------------------------- 检查与推导

func (c *Controller) runChecks(ctx context.Context, ref Ref) []Check {
	if ref == (Ref{}) {
		if c.opts.Hooks.CheckModule != nil {
			return c.opts.Hooks.CheckModule(ctx)
		}
		return nil
	}
	if c.opts.Hooks.CheckObject != nil {
		return c.opts.Hooks.CheckObject(ctx, ref)
	}
	return nil
}

// Refresh 对模块与所有对象跑一遍检查并推导阶段，周期调用：
//   - 模块自身第一次检查时自动初始化；对象要用户 init 之后才进入检查；
//   - 未初始化之外的未就绪 / 就绪按检查结果互相转换（就绪不会自动进入预热）；
//   - 预热、运行中检查失败的对象自动暂停，检查重新通过后自动回到暂停前的阶段；
//   - 重启后待恢复的对象检查通过就回到原阶段。
func (c *Controller) Refresh(ctx context.Context) {
	// 模块自身是基础设施，第一次检查时自动初始化；需要用户 init 的是它管理的对象。
	c.mu.Lock()
	selfUninit := c.self.Stage == StageUninitialized
	c.mu.Unlock()
	if selfUninit {
		_, _ = c.initCmd(ctx, Ref{})
	}
	refs := []Ref{{}}
	if c.opts.Hooks.Objects != nil {
		refs = append(refs, c.opts.Hooks.Objects()...)
	}
	results := make(map[string][]Check, len(refs))
	for _, ref := range refs {
		results[ref.key()] = c.runChecks(ctx, ref)
	}
	c.mu.Lock()
	var changes []change
	known := map[string]bool{}
	for _, ref := range refs {
		known[ref.key()] = true
		r := c.recordFor(ref)
		checks := results[ref.key()]
		c.checks[ref.key()] = checks
		c.apply(r, checks, &changes)
	}
	// 目录里已经没有的对象：移除（停用、删除）。
	for k, r := range c.objects {
		if !known[k] {
			delete(c.objects, k)
			delete(c.checks, k)
			changes = append(changes, change{ref: r.Ref, from: r.Stage, to: StageUninitialized, reason: "对象已移除"})
		}
	}
	_ = c.save()
	c.mu.Unlock()
	c.notify(changes)
}

// apply 按检查结果推进一个记录的阶段；调用方持有锁。
func (c *Controller) apply(r *record, checks []Check, changes *[]change) {
	passed := Passed(checks)
	failure := firstFailure(checks)
	switch r.Stage {
	case StageUninitialized:
		// 未初始化要用户执行 init 之后才进入检查。
	case StageNotReady, StageReady:
		switch {
		case !passed:
			if r.Pending != "" {
				r.Reason = failure
			} else {
				c.set(r, StageNotReady, failure, changes)
			}
		case r.Pending != "":
			// 重启后检查通过：回到重启前的阶段，首页提示已自动恢复。
			back := r.Pending
			r.Pending, r.Recovered = "", true
			c.set(r, back, "重启后自动恢复", changes)
		default:
			c.set(r, StageReady, "", changes)
		}
	case StageWarming, StageRunning:
		if !passed {
			// 只暂停受影响的对象，记下暂停前的阶段。
			r.Resume, r.AutoPaused = r.Stage, true
			c.set(r, StagePaused, failure, changes)
		}
	case StagePaused:
		if r.AutoPaused && passed {
			back := r.Resume
			r.AutoPaused, r.Resume = false, ""
			c.set(r, back, "检查恢复，自动回到暂停前的阶段", changes)
		} else if r.AutoPaused {
			r.Reason = failure
		}
	case StageFault:
		// 故障要用户 resume（检查通过后）才恢复。
	}
}

func firstFailure(checks []Check) string {
	for _, c := range checks {
		if c.Status == CheckFail {
			if c.Message != "" {
				return c.Name + "：" + c.Message
			}
			return c.Name
		}
	}
	return ""
}

// ---------------------------------------------------------------- 命令

// Command 执行一个用户命令并返回对象的新状态。ref 为空表示模块自身。
func (c *Controller) Command(ctx context.Context, cmd Command, ref Ref) (Object, error) {
	switch cmd {
	case CmdInit:
		return c.initCmd(ctx, ref)
	case CmdCheck:
		return c.checkCmd(ctx, ref)
	case CmdStart, CmdConfirm, CmdPause, CmdResume:
		return c.transition(ctx, cmd, ref)
	}
	return Object{}, fmt.Errorf("未知命令 %q", cmd)
}

func (c *Controller) initCmd(ctx context.Context, ref Ref) (Object, error) {
	if c.opts.Hooks.Init != nil {
		if err := c.opts.Hooks.Init(ctx, ref); err != nil {
			return Object{}, fmt.Errorf("初始化失败：%w", err)
		}
	}
	checks := c.runChecks(ctx, ref)
	c.mu.Lock()
	var changes []change
	r := c.recordFor(ref)
	c.checks[ref.key()] = checks
	if r.Stage == StageUninitialized || r.Stage == StageNotReady || r.Stage == StageReady {
		if Passed(checks) {
			c.set(r, StageReady, "", &changes)
		} else {
			c.set(r, StageNotReady, firstFailure(checks), &changes)
		}
	}
	_ = c.save()
	obj := c.object(r)
	c.mu.Unlock()
	c.notify(changes)
	return obj, nil
}

func (c *Controller) checkCmd(ctx context.Context, ref Ref) (Object, error) {
	checks := c.runChecks(ctx, ref)
	c.mu.Lock()
	var changes []change
	r := c.recordFor(ref)
	c.checks[ref.key()] = checks
	c.apply(r, checks, &changes)
	_ = c.save()
	obj := c.object(r)
	c.mu.Unlock()
	c.notify(changes)
	return obj, nil
}

func (c *Controller) transition(ctx context.Context, cmd Command, ref Ref) (Object, error) {
	checks := c.runChecks(ctx, ref)
	c.mu.Lock()
	var changes []change
	r := c.recordFor(ref)
	c.checks[ref.key()] = checks
	deny := func(why string) (Object, error) {
		obj := c.object(r)
		c.mu.Unlock()
		return obj, &ErrNotAllowed{Ref: ref, Command: cmd, Stage: r.Stage, Why: why}
	}
	switch cmd {
	case CmdStart:
		if r.Stage != StageReady {
			return deny("只有就绪的对象才能启动；先 init 并让检查通过")
		}
		if !Passed(checks) {
			return deny("检查有失败项：" + firstFailure(checks))
		}
		c.set(r, StageWarming, "", &changes)
	case CmdConfirm:
		if r.Stage != StageWarming {
			return deny("只有预热中的对象才能确认进入运行")
		}
		if !Passed(checks) {
			return deny("检查有失败项：" + firstFailure(checks))
		}
		c.set(r, StageRunning, "", &changes)
	case CmdPause:
		if !r.Stage.Active() {
			return deny("只有预热或运行中的对象才能暂停")
		}
		r.Resume, r.AutoPaused = r.Stage, false
		c.set(r, StagePaused, "手动暂停", &changes)
	case CmdResume:
		switch {
		case r.Stage == StagePaused || r.Stage == StageFault:
		default:
			return deny("只有暂停或故障的对象才能恢复")
		}
		if !Passed(checks) {
			return deny("检查有失败项：" + firstFailure(checks))
		}
		back := r.Resume
		if back == "" {
			back = StageReady
		}
		r.AutoPaused, r.Resume = false, ""
		c.set(r, back, "手动恢复", &changes)
	}
	_ = c.save()
	obj := c.object(r)
	c.mu.Unlock()
	c.notify(changes)
	return obj, nil
}

// Fault 报告对象运行中出现自身无法恢复的错误：进入故障，要用户 resume。
func (c *Controller) Fault(ref Ref, err error) {
	c.mu.Lock()
	r := c.recordFor(ref)
	var changes []change
	if r.Stage.Active() {
		r.Resume = r.Stage
	}
	c.set(r, StageFault, err.Error(), &changes)
	_ = c.save()
	c.mu.Unlock()
	c.notify(changes)
}

// ---------------------------------------------------------------- 查询

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

// Allowed 报告对象是否在计算（预热或运行）：处理周期事件前用它做门禁。
func (c *Controller) Allowed(ref Ref) bool { return c.Stage(ref).Active() }

// Warming 报告对象是否处于预热（预热期间不真实下单）。
func (c *Controller) Warming(ref Ref) bool { return c.Stage(ref) == StageWarming }

func (c *Controller) object(r *record) Object {
	return Object{
		Ref: r.Ref, Stage: r.Stage, Color: r.Stage.Color(), Reason: r.Reason, Since: r.Since,
		AutoPaused: r.AutoPaused, Resume: r.Resume, Recovered: r.Recovered, Checks: append([]Check(nil), c.checks[r.key()]...),
	}
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

// summarize 推导汇总阶段：模块自身没有通过检查就是未就绪 / 未初始化；有对象时，
// 故障 > 未就绪 > 暂停 > 预热 > 运行 > 就绪 > 未初始化；只要有对象没在预期阶段（暂停、故障、未就绪）就标记降级。
func (c *Controller) summarize(s Snapshot) (Stage, string, bool) {
	if s.Self.Stage == StageUninitialized || s.Self.Stage == StageNotReady || s.Self.Stage == StageFault {
		return s.Self.Stage, s.Self.Reason, false
	}
	if len(s.Objects) == 0 {
		return s.Self.Stage, s.Self.Reason, false
	}
	rank := map[Stage]int{StageFault: 6, StageNotReady: 5, StagePaused: 4, StageWarming: 3, StageRunning: 2, StageReady: 1, StageUninitialized: 0}
	worst, reason := s.Objects[0].Stage, s.Objects[0].Reason
	degraded := false
	running := 0
	for _, o := range s.Objects {
		if rank[o.Stage] > rank[worst] {
			worst, reason = o.Stage, o.Reason
		}
		if o.Stage == StagePaused || o.Stage == StageFault || o.Stage == StageNotReady {
			degraded = true
		}
		if o.Stage.Active() {
			running++
		}
	}
	// 有对象在跑、另一些对象暂停或故障：汇总取在跑的阶段，标记降级（首页显示「运行中 · 降级」）。
	if degraded && running > 0 {
		best := StageRunning
		for _, o := range s.Objects {
			if o.Stage == StageWarming {
				best = StageWarming
			}
		}
		return best, reason, true
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
// 只列出需要关注的对象（暂停、故障、未就绪），运行与预热的对象只计数，响应体不随对象数增长。
type Compact struct {
	Stage    Stage  `json:"stage"`
	Color    string `json:"color"`
	Degraded bool   `json:"degraded,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Self 是模块自身的阶段。
	Self Stage `json:"self"`
	// Counts 是各阶段的对象数。
	Counts    map[Stage]int `json:"counts,omitempty"`
	Attention []Object      `json:"attention,omitempty"`
	// Recovered 是重启后自动回到原阶段的对象数（首页提示「已自动恢复」）。
	Recovered int `json:"recovered,omitempty"`
}

// maxAttention 是精简快照里最多列出的需要关注的对象数。
const maxAttention = 6

// Compact 返回精简快照。
func (c *Controller) Compact() Compact {
	snap := c.Snapshot()
	out := Compact{Stage: snap.Stage, Color: snap.Color, Degraded: snap.Degraded, Reason: snap.Reason, Self: snap.Self.Stage, Counts: map[Stage]int{}}
	for _, o := range snap.Objects {
		out.Counts[o.Stage]++
		if o.Recovered {
			out.Recovered++
		}
		if (o.Stage == StagePaused || o.Stage == StageFault || o.Stage == StageNotReady) && len(out.Attention) < maxAttention {
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
