// Package sysctl 是系统级暂停与恢复的编排（设计 2.1、8.1）：按模块依赖顺序逐个调用各模块自己的生命周期接口。
//
// 编排器自己不持有业务状态，阶段以各模块持久化的状态为准，所以 Pause / Resume 都是幂等的：
// 命令行中途断开、笔记本关机，再执行一次就按各模块当前状态继续。
//
//	暂停：下游到上游逐个暂停（先停会下单的，再停会产生数据的），每个模块等到真正停下（在途操作结束）再停下一个；
//	恢复：先「追赶」后「放行」，中间有一道栅栏——
//	      追赶：按依赖让各模块进入恢复追赶（上游追平后才启动下游：采集补采完，因子才同步；因子追平，策略才开始记录周期），
//	      栅栏：所有模块都追平（caught_up）之前谁也不放行；
//	      放行：按上游到下游放行（Trade 先于策略，保证策略放行后的第一批目标有地方接收）。
package sysctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
)

// 模块名（和各模块在 Monitor 里的组件 ID 一致）。
const (
	Collector    = "collector"
	FactorEngine = "factor-engine"
	Strategy     = "strategy"
	Trade        = "trade"
)

// pauseOrder 是暂停顺序：下游到上游。
var pauseOrder = []string{Strategy, Trade, FactorEngine, Collector}

// startDeps 是恢复追赶的启动依赖：上游追平后才启动下游。Trade 与行情链路无关，一开始就追赶。
var startDeps = map[string][]string{
	Collector:    nil,
	FactorEngine: {Collector},
	Strategy:     {FactorEngine},
	Trade:        nil,
}

// releaseOrder 是放行顺序：上游到下游，Trade 先于策略。
var releaseOrder = []string{Collector, FactorEngine, Trade, Strategy}

// selfReleasing 报告模块追平后是否自己放行，不等栅栏。采集只拉取和写入行情，没有下单、发目标、发告警这类副作用，
// 而它的追赶目标是固定的（恢复开始的时刻）：实时采集在放行前保持关闭，如果等所有模块追平再放行，
// 这段等待期间的行情又成了新缺口，下游的追赶目标会一直被推远。所以采集补齐暂停区间就恢复实时采集，
// 等待栅栏的是有副作用的模块（因子发布结果、Trade 与策略下单）。
func selfReleasing(name string) bool { return name == Collector }

// Lifecycle 是一个模块的生命周期接口（*lifecycle.Client 实现）。
type Lifecycle interface {
	Status(ctx context.Context) (lifecycle.Snapshot, error)
	Exec(ctx context.Context, req lifecycle.CommandRequest) (lifecycle.Object, error)
	Catchup(ctx context.Context) (lifecycle.CatchupReport, error)
}

// Module 是参与编排的一个模块。
type Module struct {
	Name string
	API  Lifecycle
}

// Orchestrator 编排系统级暂停与恢复。
type Orchestrator struct {
	// Modules 是参与编排的模块；没有出现的模块（没有配置地址）被跳过。
	Modules []Module
	// By 是操作者，写进暂停详情。
	By string
	// Poll 是轮询间隔（默认 5 秒）；Sleep 可替换（测试用）。
	Poll  time.Duration
	Sleep func(ctx context.Context, d time.Duration) error
	// Out 接收进度输出，可为 nil。
	Out io.Writer
}

func (o *Orchestrator) logf(format string, args ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", args...)
	}
}

func (o *Orchestrator) sleep(ctx context.Context) error {
	d := o.Poll
	if d <= 0 {
		d = 5 * time.Second
	}
	if o.Sleep != nil {
		return o.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (o *Orchestrator) module(name string) (Module, bool) {
	for _, m := range o.Modules {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

// ordered 按给定顺序返回已配置的模块。
func (o *Orchestrator) ordered(names []string) []Module {
	var out []Module
	for _, name := range names {
		if m, ok := o.module(name); ok {
			out = append(out, m)
		}
	}
	return out
}

// ModuleState 是一个模块的状态摘要。
type ModuleState struct {
	Name    string
	Stage   lifecycle.Stage
	Hold    lifecycle.Hold
	Pause   *lifecycle.PauseInfo
	Catchup *lifecycle.Catchup
	Message string
	// Err 非空表示读不到状态。
	Err error
}

// Status 读取全部模块的状态（上游到下游）。
func (o *Orchestrator) Status(ctx context.Context) []ModuleState {
	var out []ModuleState
	for _, m := range o.ordered(releaseOrder) {
		state := ModuleState{Name: m.Name}
		snap, err := m.API.Status(ctx)
		if err != nil {
			state.Err = err
		} else {
			state.Stage, state.Hold, state.Pause, state.Catchup = snap.Self.Stage, snap.Self.Hold, snap.Self.Pause, snap.Self.Catchup
			state.Message = snap.Self.Reason
		}
		out = append(out, state)
	}
	return out
}

// PauseResult 是系统暂停的结果。
type PauseResult struct {
	// Paused 是已处于暂停的模块（含之前就暂停的）；Warnings 是「已暂停，但在途操作没有结束」这类提示。
	Paused   []string
	Warnings []string
}

// Pause 系统暂停：下游到上游逐个暂停。某个模块失败就停在那里（不回滚已暂停的部分，暂停无损），返回已暂停的模块与错误；
// 重复执行幂等。
func (o *Orchestrator) Pause(ctx context.Context, reason string) (PauseResult, error) {
	var result PauseResult
	for _, m := range o.ordered(pauseOrder) {
		obj, err := m.API.Exec(ctx, lifecycle.CommandRequest{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem, Reason: reason, By: o.By})
		switch {
		case err == nil:
		case strings.Contains(err.Error(), "已暂停，但在途操作没有结束"):
			// 门禁已经关闭，只是在途操作还没结束：提示，继续停上游（上游停了不会再有新的数据进来）。
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s：%v", m.Name, err))
		default:
			return result, fmt.Errorf("暂停 %s：%w", m.Name, err)
		}
		o.logf("%s：已暂停（%s）", m.Name, describePause(obj))
		result.Paused = append(result.Paused, m.Name)
	}
	return result, nil
}

func describePause(obj lifecycle.Object) string {
	if obj.Pause == nil {
		return string(obj.Stage)
	}
	return string(obj.Pause.Kind)
}

// ResumeOptions 是系统恢复的选项。
type ResumeOptions struct {
	// Timeout 是等待追平的时限，0 表示一直等到 ctx 结束。超时只报告，不自动放行。
	Timeout time.Duration
	// ReleasePartial 为真时，超时后放行已追平的模块、让未追平的继续追赶（调用方要先让用户确认）。
	ReleasePartial bool
}

// ResumeResult 是系统恢复的结果。
type ResumeResult struct {
	Released []string
	// Pending 是没有放行的模块及原因（未追平、被手动暂停……）。
	Pending map[string]string
	// TimedOut 为真表示等待追平超时。
	TimedOut bool
}

type resumeState struct {
	started  bool
	skipped  string // 非空：不参与（手动暂停、已经在跑）
	caughtUp bool
	note     string
}

// Resume 系统恢复：先追赶，所有模块追平后再放行。
func (o *Orchestrator) Resume(ctx context.Context, opts ResumeOptions) (ResumeResult, error) {
	result := ResumeResult{Pending: map[string]string{}}
	mods := o.ordered(releaseOrder)
	if len(mods) == 0 {
		return result, errors.New("没有配置任何模块的生命周期地址")
	}
	states := map[string]*resumeState{}
	for _, m := range mods {
		states[m.Name] = &resumeState{}
	}
	var deadline time.Time
	if opts.Timeout > 0 {
		deadline = time.Now().Add(opts.Timeout)
	}

	// 追赶阶段：按依赖启动，轮询进度，直到全部追平。
	for {
		allCaught := true
		for _, m := range mods {
			st := states[m.Name]
			if st.skipped != "" {
				continue
			}
			if !st.started {
				if !o.depsCaughtUp(m.Name, states) {
					allCaught = false
					st.note = "等待上游追平"
					continue
				}
				skipped, err := o.startResume(ctx, m)
				if err != nil {
					return result, fmt.Errorf("恢复 %s：%w", m.Name, err)
				}
				if skipped != "" {
					st.skipped = skipped
					o.logf("%s：%s，不参与本次恢复", m.Name, skipped)
					continue
				}
				st.started = true
				o.logf("%s：进入恢复追赶", m.Name)
			}
			report, err := m.API.Catchup(ctx)
			if err != nil {
				return result, fmt.Errorf("读取 %s 的追赶进度：%w", m.Name, err)
			}
			st.caughtUp, st.note = report.CaughtUp && (report.Resuming || selfReleasing(m.Name)), catchupNote(report)
			if selfReleasing(m.Name) && !report.Resuming && report.CaughtUp {
				st.skipped = "已放行"
				if len(result.Released) == 0 || result.Released[len(result.Released)-1] != m.Name {
					result.Released = append(result.Released, m.Name)
					o.logf("%s：补齐暂停区间，已自行放行（恢复实时采集）", m.Name)
				}
				continue
			}
			if !st.caughtUp {
				allCaught = false
			}
		}
		if allCaught {
			break
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			result.TimedOut = true
			break
		}
		if err := o.sleep(ctx); err != nil {
			return o.partial(result, states, mods), err
		}
	}

	// 栅栏之后：放行。超时且没有选择部分放行时，什么都不放行。
	if result.TimedOut && !opts.ReleasePartial {
		return o.partial(result, states, mods), nil
	}
	for _, m := range mods {
		st := states[m.Name]
		if st.skipped != "" || !st.caughtUp {
			continue
		}
		if _, err := m.API.Exec(ctx, lifecycle.CommandRequest{Command: lifecycle.CmdRelease}); err != nil {
			// 放行前模块会再核一次：又出现缺口时被拒绝，已放行的上游保持运行，停在这一步，下次重试。
			return o.partial(result, states, mods), fmt.Errorf("放行 %s：%w", m.Name, err)
		}
		result.Released = append(result.Released, m.Name)
		st.caughtUp, st.skipped = false, "已放行"
		o.logf("%s：已放行", m.Name)
	}
	return o.partial(result, states, mods), nil
}

// partial 汇总没有放行的模块与原因。
func (o *Orchestrator) partial(result ResumeResult, states map[string]*resumeState, mods []Module) ResumeResult {
	for _, m := range mods {
		st := states[m.Name]
		switch {
		case st.skipped == "已放行":
		case st.skipped != "":
			result.Pending[m.Name] = st.skipped
		default:
			result.Pending[m.Name] = firstNonEmpty(st.note, "未追平")
		}
	}
	return result
}

func (o *Orchestrator) depsCaughtUp(name string, states map[string]*resumeState) bool {
	for _, dep := range startDeps[name] {
		st, ok := states[dep]
		if !ok {
			continue // 上游没有配置：不等
		}
		if st.skipped != "" {
			continue // 上游不参与（手动暂停、已经在跑）：不等
		}
		if !st.caughtUp {
			return false
		}
	}
	return true
}

// startResume 让模块进入恢复追赶。返回非空 skipped 表示模块不参与：手动暂停的、已经在跑的。
func (o *Orchestrator) startResume(ctx context.Context, m Module) (string, error) {
	snap, err := m.API.Status(ctx)
	if err != nil {
		return "", err
	}
	switch stage := snap.Self.Stage; {
	case stage == lifecycle.StageResuming:
		return "", nil // 上一次执行已经启动，继续等
	case stage == lifecycle.StagePaused && snap.Self.Pause != nil && snap.Self.Pause.Kind != lifecycle.PauseSystem:
		return "手动暂停（系统恢复不动它）", nil
	case stage != lifecycle.StagePaused && snap.Self.Hold != lifecycle.HoldSystemPause:
		return "没有处于暂停", nil
	}
	if _, err := m.API.Exec(ctx, lifecycle.CommandRequest{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: !selfReleasing(m.Name), By: o.By}); err != nil {
		return "", err
	}
	return "", nil
}

func catchupNote(report lifecycle.CatchupReport) string {
	switch {
	case report.CaughtUp && report.Resuming:
		return "已追平，待放行"
	case report.Self != nil && report.Self.Catchup != nil:
		return describeCatchup(*report.Self.Catchup)
	}
	for _, obj := range report.Objects {
		if obj.Catchup != nil && !obj.Catchup.CaughtUp {
			return obj.Kind + "/" + obj.ID + "：" + describeCatchup(*obj.Catchup)
		}
	}
	return "追赶中"
}

func describeCatchup(c lifecycle.Catchup) string {
	switch {
	case c.CaughtUp:
		return "已追平"
	case c.BlockedBy != "":
		return "被 " + c.BlockedBy + " 阻塞" + suffix(c.Message)
	case c.Total > 0:
		return fmt.Sprintf("%d/%d", c.Done, c.Total) + suffix(c.Message)
	case c.Message != "":
		return c.Message
	}
	return "追赶中"
}

func suffix(message string) string {
	if message == "" {
		return ""
	}
	return "（" + message + "）"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
