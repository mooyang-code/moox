// Package lifecycle 是各模块共用的生命周期状态机（设计第 6 节）。
//
// 每个模块和它管理的对象（因子集合、策略实例……）都有同一套阶段：
//
//	未初始化 → 未就绪 / 就绪 → 预热 → 运行；运行中可以暂停、自动暂停、故障。
//
// 阶段只由两类事件推进：用户命令（init、start、confirm、pause、resume、release）和启动前检查的结果。
// 就绪是检查通过，不会自动进入预热；预热要用户 start，进入运行要用户 confirm。
// 运行中检查失败的对象只暂停它自己（自动暂停），检查重新通过后自动回到暂停前的阶段。
// 恢复是两段式：resume 先进入恢复追赶（补齐暂停期间缺失的数据，不计算、不下单），追平后 release 放行才回到暂停前的阶段；
// 单个模块的恢复在追平后自动放行，系统级恢复由编排器等所有模块追平后统一放行。
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
	StageResuming      Stage = "resuming"      // 青：恢复追赶，补齐暂停期间缺失的数据；不计算、不下单
	StageResetting     Stage = "resetting"     // 紫：重置进行中
	StageResetFailed   Stage = "reset_failed"  // 红：重置某一步失败，模块保持停用
	StageFault         Stage = "fault"         // 红：运行中出现自身无法恢复的错误
)

// Color 返回阶段在首页的显示颜色。
func (s Stage) Color() string {
	switch s {
	case StageUninitialized:
		return "gray"
	case StageNotReady, StageFault, StageResetFailed:
		return "red"
	case StageReady:
		return "blue"
	case StageWarming:
		return "yellow"
	case StageRunning:
		return "green"
	case StagePaused:
		return "orange"
	case StageResuming:
		return "cyan"
	case StageResetting:
		return "purple"
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
	// CmdRelease 放行：恢复追赶已追平的对象回到暂停前的阶段。
	CmdRelease Command = "release"
)

// PauseKind 是暂停的类型。手动暂停只有用户 resume 才会恢复；自动暂停是检查失败触发的，检查通过后自动恢复；
// 系统暂停是系统级暂停动作带下来的，系统恢复只恢复这一类，不动用户先前单独手动暂停的对象。
type PauseKind string

const (
	PauseManual PauseKind = "manual"
	PauseAuto   PauseKind = "auto"
	PauseSystem PauseKind = "system"
)

// rank 是暂停类型的优先级：已有高优先级的暂停时，低优先级的暂停不覆盖它。
func (k PauseKind) rank() int {
	switch k {
	case PauseManual:
		return 3
	case PauseSystem:
		return 2
	case PauseAuto:
		return 1
	}
	return 0
}

// PauseInfo 是暂停详情：类型、原因、操作者与开始时间。
type PauseInfo struct {
	Kind   PauseKind `json:"kind"`
	Reason string    `json:"reason,omitempty"`
	// By 是操作者（Admin 用户、cli:主机、monitor:auto……）。
	By    string    `json:"by,omitempty"`
	Since time.Time `json:"since"`
	// ExpiresAt 非零表示到点自动恢复（由 Refresh 检查）。
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Hold 是持久化在模块自身记录里的控制意图，不依赖阶段推导：系统暂停时即使模块自身检查失败（未就绪）也必须能建立门禁，
// 重置完成后必须等用户 init，Refresh 不得自动初始化模块自身。
type Hold string

const (
	HoldNone        Hold = ""
	HoldSystemPause Hold = "system_pause"
	HoldPostReset   Hold = "post_reset"
)

// Catchup 是恢复追赶的进度，由模块的 Catchup 钩子报告。
type Catchup struct {
	// Since 是暂停起点（最早的那次暂停），追赶从这里开始核对。
	Since time.Time `json:"since"`
	// Target 是追赶目标（最新已完成周期）；追赶期间会前移，追平指在放行那一刻无缺口。
	Target time.Time `json:"target,omitempty"`
	Done   int64     `json:"done,omitempty"`
	Total  int64     `json:"total,omitempty"`
	// CaughtUp 为真表示本对象已追平。
	CaughtUp bool `json:"caught_up"`
	// BlockedBy 是被阻塞的原因（例如上游模块尚未追平）。
	BlockedBy string `json:"blocked_by,omitempty"`
	// Unfillable 是暂停区间里确认无法补采的周期数（源头确实没有）。
	Unfillable int `json:"unfillable,omitempty"`
	// Message 是人读的进度说明。
	Message string `json:"message,omitempty"`
}

// Ref 指向一个对象；Kind 为空表示模块自身。
type Ref struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
}

func (r Ref) key() string { return r.Kind + "/" + r.ID }

// Object 是一个对象（或模块自身）的当前状态。
type Object struct {
	Ref
	Stage Stage `json:"stage"`
	// Effective 是考虑模块自身状态后的有效阶段：模块暂停、恢复追赶、重置时，处于计算中的对象实际都不在计算。
	Effective Stage  `json:"effective"`
	Color     string `json:"color"`
	Reason    string `json:"reason,omitempty"`
	// Since 是进入当前阶段的时间。
	Since time.Time `json:"since"`
	// Pause 是暂停详情（暂停与恢复追赶阶段存在）；PausedSince 是最早一次暂停的时间，恢复追赶据此核对缺失区间。
	Pause       *PauseInfo `json:"pause,omitempty"`
	PausedSince time.Time  `json:"paused_since,omitempty"`
	// Resume 是暂停前的阶段（放行后回到它）。
	Resume Stage `json:"resume,omitempty"`
	// Catchup 是恢复追赶的进度（恢复追赶阶段，或模块恢复追赶中参与追赶的对象）。
	Catchup *Catchup `json:"catchup,omitempty"`
	// Hold 只在模块自身记录里有意义。
	Hold Hold `json:"hold,omitempty"`
	// ReleasedAt 是最近一次放行的时间：下单类模块据此只对放行之后收盘的周期出目标，放行之前的周期只记录、不求值。
	ReleasedAt time.Time `json:"released_at,omitempty"`
	// LastReset 是最近一次完成的重置（只在模块自身记录里有意义）。
	LastReset *ResetRecord `json:"last_reset,omitempty"`
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
	// AutoStart 报告对象是否随创建自动推进到预热（init → start）：返回 true 的对象在未初始化、就绪时由周期刷新自动
	// init、start，不必用户逐个执行命令（例如策略实例的私有因子集、启用后的策略实例）。暂停、恢复追赶、重置、故障、未就绪
	// 的对象不动，预热到运行的确认（confirm）默认仍由用户执行（见 AutoConfirm）。为 nil 表示没有对象自动推进。
	AutoStart func(ref Ref) bool
	// AutoConfirm 报告预热中的对象能否自动确认进入运行；since 是对象进入预热的时间。只对 AutoStart 认定的对象调用，
	// 由周期刷新在每次推进时询问；证据（例如预热之后完整算完的一期）由模块自己保存。为 nil 表示不自动确认。
	AutoConfirm func(ref Ref, since time.Time) bool
	// Catchup 报告一个对象（ref 为空是模块自身）的恢复追赶进度；since 是最早一次暂停的时间。
	// 为 nil 表示没有数据需要追赶，恢复后立即视为追平。钩子应只写历史数据，不下单、不发目标。
	Catchup func(ctx context.Context, ref Ref, since time.Time) (Catchup, error)
	// AfterReset 在重置完成后用户第一次 init 模块自身（解除「重置后等待初始化」）时调用，可为 nil。
	// 模块用它放开重置期间一直保持关闭的东西（例如 Collector 的采集暂停开关）：重置结束不等于可以马上工作，要等用户明确 init。
	AfterReset func(ctx context.Context)
	// Reset 是模块的重置实现（见 reset.go）；为 nil 表示模块不支持重置。
	Reset *ResetSpec
	// Quiesce 在对象暂停之后调用（门禁已经关闭）：等待该对象在途的写操作结束（例如 Trade 已提交的订单请求）。
	// 返回错误表示在途操作没有在时限内结束；对象仍然是暂停，调用方可以稍后再确认。为 nil 表示没有需要等待的在途操作。
	Quiesce func(ctx context.Context, ref Ref) error
	// OnChange 在阶段变化后调用（日志、指标），可为 nil；在持有控制器锁时不会被调用。
	OnChange func(ref Ref, from, to Stage, reason string)
}

// Options 是控制器的参数。
type Options struct {
	Module string
	// QuiesceTimeout 是暂停后等待在途操作结束的时限；为 0 时取 30 秒。
	QuiesceTimeout time.Duration
	// StateFile 是阶段持久化文件；为空时只在内存里保存。
	StateFile string
	Now       func() time.Time
	Hooks     Hooks
	// Audit 接收审计事件（重置的开始、每一步、结束、失败、放弃，暂停与恢复命令），含操作者、范围和计划摘要；
	// 模块把它接到自己的日志，可为 nil。调用时不持有控制器锁。
	Audit func(event string)
}

// Controller 管理一个模块的生命周期。
type Controller struct {
	opts Options

	mu      sync.Mutex
	self    *record
	objects map[string]*record
	checks  map[string][]Check

	reset resetState
}

type record struct {
	Ref
	Stage       Stage        `json:"stage"`
	Reason      string       `json:"reason,omitempty"`
	Since       time.Time    `json:"since"`
	Pause       *PauseInfo   `json:"pause,omitempty"`
	PausedSince time.Time    `json:"paused_since,omitempty"`
	Resume      Stage        `json:"resume,omitempty"`
	Recovered   bool         `json:"recovered,omitempty"`
	Hold        Hold         `json:"hold,omitempty"`
	ReleasedAt  time.Time    `json:"released_at,omitempty"`
	LastReset   *ResetRecord `json:"last_reset,omitempty"`
	// 以下是进程内的恢复追赶状态，重启后清零（重启时恢复追赶回到暂停，要重新 resume）。
	catchup       *Catchup
	catchupMember bool // 模块恢复追赶时，参与本次恢复的原活动对象
	autoRelease   bool // 追平后自动放行（单模块恢复）；系统级恢复为 false，等编排器统一 release
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
	if err := c.loadJournal(); err != nil {
		return nil, err
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
		switch {
		case r.Stage.Active() || r.Stage == StagePaused:
			// 重启后先未就绪，跑完检查再回到原阶段。
			out.Pending = r.Stage
			out.Stage = StageNotReady
			out.Reason = "重启后等待启动前检查"
		case r.Stage == StageResuming:
			// 恢复追赶是进程内的任务，不跨重启续做：回到暂停，暂停起点不变，重启后再 resume。
			out.Pending = StagePaused
			out.Stage = StageNotReady
			out.Reason = "重启后等待启动前检查"
		case r.Stage == StageResetting:
			// 重置进行中进程退出：停在重置失败，等 reset 续做。
			out.Stage = StageResetFailed
			out.Reason = "重置被重启中断"
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
	selfUninit := c.self.Stage == StageUninitialized && c.self.Hold != HoldPostReset
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
	c.resumeExpired(ctx)
	c.settle(ctx)
}

// audit 记一条审计事件。
func (c *Controller) audit(format string, args ...any) {
	if c.opts.Audit != nil {
		c.opts.Audit(fmt.Sprintf(format, args...))
	}
}

// resumeExpired 恢复到点的定时暂停：和手动 resume 一样先进入恢复追赶，追平后自动放行。
// 对象在模块暂停期间不单独恢复（先等模块恢复）。
func (c *Controller) resumeExpired(ctx context.Context) {
	now := c.opts.Now()
	c.mu.Lock()
	var due []Ref
	expired := func(r *record) bool {
		return r.Stage == StagePaused && r.Pause != nil && !r.Pause.ExpiresAt.IsZero() && !now.Before(r.Pause.ExpiresAt) && r.Pause.Kind != PauseSystem
	}
	if expired(c.self) {
		due = append(due, Ref{})
	} else if c.self.Stage != StagePaused {
		for _, o := range c.objects {
			if expired(o) {
				due = append(due, o.Ref)
			}
		}
	}
	c.mu.Unlock()
	for _, ref := range due {
		// 检查有失败项等原因恢复不了时保持暂停，下一轮 Refresh 再试。
		_, _ = c.Execute(ctx, Request{Command: CmdResume, Ref: ref, By: "lifecycle:expires"})
	}
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
			r.Resume = r.Stage
			r.Pause = &PauseInfo{Kind: PauseAuto, Reason: failure, By: "lifecycle:auto", Since: c.opts.Now().UTC()}
			c.markPausedSince(r)
			c.set(r, StagePaused, failure, changes)
		}
	case StagePaused:
		if r.Pause != nil && r.Pause.Kind == PauseAuto && passed {
			// 检查恢复：走同一条恢复路径（追赶 → 放行），没有缺口时追赶瞬间完成。
			c.beginResume(r, true, changes)
			r.Reason = "检查恢复，自动回到暂停前的阶段"
		} else if r.Pause != nil && r.Pause.Kind == PauseAuto {
			r.Reason = failure
			r.Pause.Reason = failure
		}
	case StageResuming, StageResetting, StageResetFailed:
		// 恢复追赶由 settle 推进；重置由重置流程推进。
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
