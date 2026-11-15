// Package lifecycle 是各模块共用的模块级生命周期：暂停、恢复追赶与放行（设计 3.8）。
//
// 模块只有三种状态：运行、暂停、恢复追赶。
//
//   - 暂停分两种：手动暂停只有用户 resume 才恢复，可以带定时恢复；系统暂停由系统级暂停动作带下来，系统恢复只恢复这一类，
//     不动用户先前单独手动暂停的模块。
//   - 恢复是两段式：resume 先进入恢复追赶（补齐暂停期间缺失的数据，不计算、不下单），追平后 release 放行回到运行；
//     单个模块的恢复在追平后自动放行，系统级恢复由编排器等所有模块追平后统一放行。
//   - 状态持久化在模块自己的状态文件里。恢复追赶是进程内的任务，不跨重启续做：重启后回到暂停，再 resume 即可。
//
// 模块自己的就绪判断（数据库、依赖是否连上）和它管理的对象（因子任务、策略实例）能不能计算，都不在这里，由各模块自己判断。
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Stage 是模块的状态。
type Stage string

const (
	StageRunning  Stage = "running"  // 运行
	StagePaused   Stage = "paused"   // 暂停：不计算新周期、不下单
	StageResuming Stage = "resuming" // 恢复追赶：补齐暂停期间缺失的数据，不计算、不下单
)

// Command 是用户命令。
type Command string

const (
	CmdPause  Command = "pause"
	CmdResume Command = "resume"
	// CmdRelease 放行：恢复追赶已追平的模块回到运行。
	CmdRelease Command = "release"
)

// PauseKind 是暂停的类型：手动暂停只有用户 resume 才恢复；系统暂停是系统级暂停动作带下来的，系统恢复只恢复这一类。
type PauseKind string

const (
	PauseManual PauseKind = "manual"
	PauseSystem PauseKind = "system"
)

// rank 是暂停类型的优先级：已有高优先级的暂停时，低优先级的暂停不覆盖它。
func (k PauseKind) rank() int {
	switch k {
	case PauseManual:
		return 2
	case PauseSystem:
		return 1
	}
	return 0
}

// PauseInfo 是暂停详情：类型、原因、操作者与开始时间。
type PauseInfo struct {
	Kind   PauseKind `json:"kind"`
	Reason string    `json:"reason,omitempty"`
	// By 是操作者（Admin 用户、cli:主机、monitor:mute……）。
	By    string    `json:"by,omitempty"`
	Since time.Time `json:"since"`
	// ExpiresAt 非零表示到点自动恢复（由 Refresh 检查）。
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Catchup 是恢复追赶的进度，由模块的 Catchup 钩子报告。
type Catchup struct {
	// Since 是暂停起点（最早的那次暂停），追赶从这里开始核对。
	Since time.Time `json:"since,omitzero"`
	// Target 是追赶目标（最新已完成周期）；追赶期间会前移，追平指在放行那一刻无缺口。
	Target time.Time `json:"target,omitzero"`
	Done   int64     `json:"done,omitempty"`
	Total  int64     `json:"total,omitempty"`
	// CaughtUp 为真表示已追平。
	CaughtUp bool `json:"caught_up"`
	// BlockedBy 是被阻塞的原因（例如某个任务还没追平、上游模块尚未追平）。
	BlockedBy string `json:"blocked_by,omitempty"`
	// Unfillable 是暂停区间里确认无法补采的周期数（源头确实没有）。
	Unfillable int `json:"unfillable,omitempty"`
	// Message 是人读的进度说明。
	Message string `json:"message,omitempty"`
}

// State 是模块的状态快照（健康检查响应的 details.lifecycle、状态接口都用它）。
type State struct {
	Module string `json:"module"`
	Stage  Stage  `json:"stage"`
	Reason string `json:"reason,omitempty"`
	// Since 是进入当前状态的时间。
	Since time.Time `json:"since"`
	// Pause 是暂停详情（暂停与恢复追赶时存在）；PausedSince 是最早一次暂停的时间，恢复追赶据此核对缺失区间。
	Pause       *PauseInfo `json:"pause,omitempty"`
	PausedSince time.Time  `json:"paused_since,omitzero"`
	// QuiescedAt 是本次暂停收敛完成的时间：暂停命令成功后等到在途操作结束（没有 Quiesce 钩子时即刻）才写入，离开暂停时清空。
	// 只有它不早于 Pause.Since 才算本次暂停已收敛，上一轮暂停留下的值不能证明这一轮。
	QuiescedAt time.Time `json:"quiesced_at,omitzero"`
	// ReleasedAt 是最近一次放行的时间：下单类模块据此只对放行之后收盘的周期出目标，放行之前的周期只记录、不求值。
	ReleasedAt time.Time `json:"released_at,omitzero"`
	// Catchup 是恢复追赶的进度（恢复追赶中）。
	Catchup *Catchup `json:"catchup,omitempty"`
}

// Quiesced 报告本次暂停是否已经收敛：处于暂停、有暂停详情，并且收敛时间不早于本次暂停的开始时间。
// 没有 Quiesce 钩子的模块，暂停命令成功即写入收敛时间。
func (s State) Quiesced() bool {
	return s.Stage == StagePaused && s.Pause != nil && !s.QuiescedAt.IsZero() && !s.QuiescedAt.Before(s.Pause.Since)
}

// Hooks 是模块提供的动作。
type Hooks struct {
	// Catchup 报告恢复追赶的进度；since 是最早一次暂停的时间。为 nil 表示没有数据需要追赶，恢复后立即视为追平。
	// 钩子应只写历史数据，不下单、不发目标；放行前要再核的条件（例如模块就绪）也由它报告。
	Catchup func(ctx context.Context, since time.Time) (Catchup, error)
	// Quiesce 在暂停之后调用（门禁已经关闭）：等在途的写操作结束（例如 Trade 已提交的订单请求）。
	// 返回错误表示在途操作没有在时限内结束；模块仍然是暂停，调用方可以稍后再确认。为 nil 表示没有需要等待的在途操作。
	Quiesce func(ctx context.Context) error
	// OnChange 在状态变化后调用（日志、指标、把暂停落到模块自己的开关上），可为 nil；不在持有控制器锁时调用。
	// 重启后载入的不是运行时，Run 开始时会以 from 为空调用一次，让模块按载入的状态重新落一遍。
	OnChange func(from, to Stage, reason string)
}

// Options 是控制器的参数。
type Options struct {
	Module string
	// QuiesceTimeout 是暂停后等待在途操作结束的时限；为 0 时取 30 秒。
	QuiesceTimeout time.Duration
	// StateFile 是状态持久化文件；为空时只在内存里保存。
	StateFile string
	Now       func() time.Time
	Hooks     Hooks
	// Audit 接收审计事件（暂停与恢复等命令），含操作者与原因；模块把它接到自己的日志，可为 nil。调用时不持有控制器锁。
	Audit func(event string)
}

// Controller 管理一个模块的生命周期。
type Controller struct {
	opts Options

	mu  sync.Mutex
	rec record
	// 以下是进程内的恢复追赶状态，重启后清零。
	catchup     *Catchup
	autoRelease bool // 追平后自动放行（单模块恢复）；系统级恢复为 false，等编排器统一 release
}

// record 是持久化的状态。
type record struct {
	Stage       Stage      `json:"stage"`
	Reason      string     `json:"reason,omitempty"`
	Since       time.Time  `json:"since"`
	Pause       *PauseInfo `json:"pause,omitempty"`
	PausedSince time.Time  `json:"paused_since,omitzero"`
	QuiescedAt  time.Time  `json:"quiesced_at,omitzero"`
	ReleasedAt  time.Time  `json:"released_at,omitzero"`
}

// ErrNotAllowed 表示命令在当前状态不允许。
type ErrNotAllowed struct {
	Command Command
	Stage   Stage
	Why     string
}

func (e *ErrNotAllowed) Error() string {
	return fmt.Sprintf("模块处于 %s，不能执行 %s：%s", e.Stage, e.Command, e.Why)
}

// New 创建控制器并读取持久化的状态。重启前在恢复追赶的模块回到暂停（暂停起点与暂停详情不变）。
func New(opts Options) (*Controller, error) {
	if opts.Module == "" {
		return nil, errors.New("lifecycle: module 不能为空")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &Controller{opts: opts, rec: record{Stage: StageRunning, Since: opts.Now().UTC()}}
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
	var saved record
	if err := json.Unmarshal(raw, &saved); err != nil {
		return nil, fmt.Errorf("解析生命周期状态 %s：%w", opts.StateFile, err)
	}
	switch saved.Stage {
	case StagePaused, StageResuming:
		if saved.Pause == nil {
			saved.Pause = &PauseInfo{Kind: PauseManual, Since: saved.Since}
		}
		if saved.Stage == StageResuming {
			// 恢复追赶是进程内的任务，不跨重启续做：回到暂停，暂停起点不变，重启后再 resume。
			saved.Stage, saved.Reason, saved.Since = StagePaused, pauseReason(saved.Pause.Kind, saved.Pause.Reason), opts.Now().UTC()
		}
		c.rec = saved
	default:
		// 运行（以及没写过状态的文件）：保留最近一次放行的时间。
		c.rec.ReleasedAt = saved.ReleasedAt
	}
	return c, nil
}

func (c *Controller) save() error {
	if c.opts.StateFile == "" {
		return nil
	}
	raw, err := json.MarshalIndent(c.rec, "", "  ")
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

type change struct {
	from, to Stage
	reason   string
}

// set 变更状态并记下变化；调用方持有锁。
func (c *Controller) set(to Stage, reason string, changes *[]change) {
	from := c.rec.Stage
	c.rec.Reason = reason
	if from == to {
		return
	}
	c.rec.Stage = to
	if to != StagePaused {
		c.rec.QuiescedAt = time.Time{}
	}
	c.rec.Since = c.opts.Now().UTC()
	*changes = append(*changes, change{from: from, to: to, reason: reason})
}

func (c *Controller) notify(changes []change) {
	if c.opts.Hooks.OnChange == nil {
		return
	}
	for _, ch := range changes {
		c.opts.Hooks.OnChange(ch.from, ch.to, ch.reason)
	}
}

// audit 记一条审计事件。
func (c *Controller) audit(format string, args ...any) {
	if c.opts.Audit != nil {
		c.opts.Audit(fmt.Sprintf(format, args...))
	}
}

// ---------------------------------------------------------------- 查询

// State 返回模块的状态快照。
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state()
}

// state 生成快照；调用方持有锁。快照不能暴露内部指针：调用方在锁外编码快照时会与改写竞争。
func (c *Controller) state() State {
	s := State{
		Module: c.opts.Module, Stage: c.rec.Stage, Reason: c.rec.Reason, Since: c.rec.Since,
		PausedSince: c.rec.PausedSince, QuiescedAt: c.rec.QuiescedAt, ReleasedAt: c.rec.ReleasedAt,
	}
	if c.rec.Pause != nil {
		pause := *c.rec.Pause
		s.Pause = &pause
	}
	if c.catchup != nil {
		cu := *c.catchup
		s.Catchup = &cu
	}
	return s
}

// Stage 返回模块的当前状态。
func (c *Controller) Stage() Stage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rec.Stage
}

// Running 报告模块是否在运行：处理实时周期、领取后台作业之前用它做门禁。
func (c *Controller) Running() bool { return c.Stage() == StageRunning }

// Halted 报告模块是否被有意停下（暂停或恢复追赶）：下单、接收目标、修改接口、定时任务用它做门禁。
func (c *Controller) Halted() bool { return c.Stage() != StageRunning }

// ReleasedAt 返回最近一次放行的时间（没有放行过为零）。
func (c *Controller) ReleasedAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rec.ReleasedAt
}

// ---------------------------------------------------------------- 周期推进

// Refresh 推进定时恢复与恢复追赶，周期调用。
func (c *Controller) Refresh(ctx context.Context) {
	c.resumeExpired(ctx)
	c.settle(ctx)
}

// Run 先按载入的状态通知一次模块（见 Hooks.OnChange），再按 interval 周期调用 Refresh，直到 ctx 结束。
func (c *Controller) Run(ctx context.Context, interval time.Duration) {
	c.mu.Lock()
	restored := c.rec
	c.mu.Unlock()
	if restored.Stage != StageRunning && c.opts.Hooks.OnChange != nil {
		c.opts.Hooks.OnChange("", restored.Stage, restored.Reason)
	}
	c.Refresh(ctx)
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

// resumeExpired 恢复到点的定时暂停：和手动 resume 一样先进入恢复追赶，追平后自动放行。
func (c *Controller) resumeExpired(ctx context.Context) {
	now := c.opts.Now()
	c.mu.Lock()
	p := c.rec.Pause
	due := c.rec.Stage == StagePaused && p != nil && p.Kind != PauseSystem && !p.ExpiresAt.IsZero() && !now.Before(p.ExpiresAt)
	c.mu.Unlock()
	if due {
		_, _ = c.Execute(ctx, Request{Command: CmdResume, By: "lifecycle:expires"})
	}
}
