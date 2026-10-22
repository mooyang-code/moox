package lifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 重置（设计第 4～6 节）：删除模块的业务数据与配置，回到新部署后、初始化之前的状态。
//
//	plan   ：只读。列出每一步、保留什么、不备份什么，给出**稳定的计划摘要**（只含删除范围，不含行数这类易变统计）和一次性令牌（10 分钟）；
//	apply  ：凭令牌发起，兑换成持久化的 operation_id；模块必须已暂停；备份 → 分步执行（每步完成就写日志）→ 回到未初始化、等用户 init；
//	续做   ：进程中途退出或某步失败后，模块停在 reset_failed；凭 operation_id 续做，跳过已完成的步骤，令牌过期不影响；
//	abort  ：reset_failed 下放弃，模块置为未初始化并保持「重置后等待初始化」，已做的步骤不会回来。
//
// 步骤必须幂等。重置在模块进程内、模块暂停期间执行，缓存与连接都由本进程管理，外部删文件会留下脏缓存。

// ResetScope 是重置范围：data 只清运行数据（保留配置，重新积累）；all 清运行数据和业务配置，回到新部署的状态。
type ResetScope string

const (
	ResetData ResetScope = "data"
	ResetAll  ResetScope = "all"
)

// Valid 报告范围取值是否合法。
func (s ResetScope) Valid() bool { return s == ResetData || s == ResetAll }

// ResetTokenTTL 是计划令牌的有效期。令牌只用于发起，发起后换成持久化的 operation_id 续做。
const ResetTokenTTL = 10 * time.Minute

// ResetStep 是重置的一个步骤。Run 必须幂等：重复执行结果相同。
type ResetStep struct {
	Name     string
	Describe string
	// Run 执行这一步；operationID 用来给备份目录命名。
	Run func(ctx context.Context, operationID string) error
}

// ResetPlanInfo 是模块给出的计划说明。
type ResetPlanInfo struct {
	// Keeps 是保留的东西（计入摘要）；Notes 是需要用户知道的说明（例如「不备份：Pebble 数据 2.1 GB」，计入摘要）。
	Keeps []string
	Notes []string
	// Stats 是行数、字节数这类易变统计，只展示、不计入摘要。
	Stats map[string]any
	// Blockers 非空表示不能执行（例如实盘账户还有持仓）。
	Blockers []string
}

// ResetSpec 是模块的重置实现。
type ResetSpec struct {
	// Plan 返回给定范围的说明与步骤；步骤的第一步通常是备份。plan 与 apply 都会调用它。
	Plan func(ctx context.Context, scope ResetScope) (ResetPlanInfo, []ResetStep, error)
}

// ResetPlan 是 plan 接口的返回。
type ResetPlan struct {
	Module string     `json:"module"`
	Scope  ResetScope `json:"scope"`
	Steps  []struct {
		Name     string `json:"name"`
		Describe string `json:"describe"`
	} `json:"steps"`
	Keeps    []string       `json:"keeps,omitempty"`
	Notes    []string       `json:"notes,omitempty"`
	Stats    map[string]any `json:"stats,omitempty"`
	Blockers []string       `json:"blockers,omitempty"`
	// Digest 是稳定摘要：模块、范围、步骤、保留与说明；令牌绑定它，计划变了令牌作废。
	Digest string `json:"digest"`
	// Token 与 ExpiresAt：发起 apply 用的一次性令牌。
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// ResetStepState 是日志里一个步骤的状态。
type ResetStepState struct {
	Name     string    `json:"name"`
	Describe string    `json:"describe,omitempty"`
	State    string    `json:"state"` // pending | done | failed
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at,omitempty"`
}

// ResetOperation 是一次重置的持久化日志（reset-journal.json）。
type ResetOperation struct {
	OperationID string           `json:"operation_id"`
	Scope       ResetScope       `json:"scope"`
	Digest      string           `json:"digest"`
	By          string           `json:"by,omitempty"`
	State       string           `json:"state"` // running | failed | done | aborted
	StartedAt   time.Time        `json:"started_at"`
	FinishedAt  time.Time        `json:"finished_at,omitempty"`
	Error       string           `json:"error,omitempty"`
	Steps       []ResetStepState `json:"steps"`
}

// ResetRecord 是最近一次完成的重置（放在模块自身记录里，首页显示「已重置·待初始化」）。
type ResetRecord struct {
	OperationID string     `json:"operation_id"`
	Scope       ResetScope `json:"scope"`
	By          string     `json:"by,omitempty"`
	At          time.Time  `json:"at"`
}

// ResetApplyRequest 是 apply 的请求：新发起带 Token + Scope，续做带 OperationID。
type ResetApplyRequest struct {
	Token       string     `json:"token,omitempty"`
	Scope       ResetScope `json:"scope,omitempty"`
	OperationID string     `json:"operation_id,omitempty"`
	By          string     `json:"by,omitempty"`
}

// ResetStatus 是 status 接口的返回。
type ResetStatus struct {
	Module    string          `json:"module"`
	Stage     Stage           `json:"stage"`
	Operation *ResetOperation `json:"operation,omitempty"`
	LastReset *ResetRecord    `json:"last_reset,omitempty"`
}

type resetState struct {
	mu     sync.Mutex // 串行化 apply / abort
	tokens map[string]resetToken
	op     *ResetOperation
	// running 为真表示后台正在执行步骤（进程内）。
	running bool
	done    chan struct{}
}

type resetToken struct {
	digest  string
	scope   ResetScope
	expires time.Time
}

func (c *Controller) journalPath() string {
	if c.opts.StateFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(c.opts.StateFile), "reset-journal.json")
}

func (c *Controller) loadJournal() error {
	path := c.journalPath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取重置日志 %s：%w", path, err)
	}
	var op ResetOperation
	if err := json.Unmarshal(raw, &op); err != nil {
		return fmt.Errorf("解析重置日志 %s：%w", path, err)
	}
	if op.State == "running" {
		// 进程在重置中途退出：停在失败，凭 operation_id 续做。
		op.State = "failed"
		if op.Error == "" {
			op.Error = "重置被重启中断"
		}
	}
	c.reset.op = &op
	return nil
}

func (c *Controller) saveJournal(op *ResetOperation) error {
	path := c.journalPath()
	if path == "" || op == nil {
		return nil
	}
	raw, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func digestOf(module string, scope ResetScope, steps []ResetStep, info ResetPlanInfo) string {
	h := sha256.New()
	fmt.Fprintf(h, "module=%s\nscope=%s\n", module, scope)
	for _, s := range steps {
		fmt.Fprintf(h, "step=%s|%s\n", s.Name, s.Describe)
	}
	keeps := append([]string(nil), info.Keeps...)
	sort.Strings(keeps)
	for _, k := range keeps {
		fmt.Fprintf(h, "keep=%s\n", k)
	}
	for _, n := range info.Notes {
		fmt.Fprintf(h, "note=%s\n", n)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Controller) resetSpec() (*ResetSpec, error) {
	if c.opts.Hooks.Reset == nil || c.opts.Hooks.Reset.Plan == nil {
		return nil, fmt.Errorf("模块 %s 没有实现重置", c.opts.Module)
	}
	return c.opts.Hooks.Reset, nil
}

// ResetPlan 返回重置计划与一次性令牌（只读，不改变任何东西）。
func (c *Controller) ResetPlan(ctx context.Context, scope ResetScope) (ResetPlan, error) {
	spec, err := c.resetSpec()
	if err != nil {
		return ResetPlan{}, err
	}
	if !scope.Valid() {
		return ResetPlan{}, fmt.Errorf("未知的重置范围 %q（data | all）", scope)
	}
	info, steps, err := spec.Plan(ctx, scope)
	if err != nil {
		return ResetPlan{}, fmt.Errorf("生成重置计划：%w", err)
	}
	plan := ResetPlan{Module: c.opts.Module, Scope: scope, Keeps: info.Keeps, Notes: info.Notes, Stats: info.Stats, Blockers: info.Blockers}
	for _, s := range steps {
		plan.Steps = append(plan.Steps, struct {
			Name     string `json:"name"`
			Describe string `json:"describe"`
		}{s.Name, s.Describe})
	}
	plan.Digest = digestOf(c.opts.Module, scope, steps, info)
	if len(info.Blockers) == 0 {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return ResetPlan{}, err
		}
		plan.Token = hex.EncodeToString(raw)
		plan.ExpiresAt = c.opts.Now().UTC().Add(ResetTokenTTL)
		c.reset.mu.Lock()
		if c.reset.tokens == nil {
			c.reset.tokens = map[string]resetToken{}
		}
		for t, v := range c.reset.tokens { // 顺手清掉过期的
			if !v.expires.After(c.opts.Now()) {
				delete(c.reset.tokens, t)
			}
		}
		c.reset.tokens[plan.Token] = resetToken{digest: plan.Digest, scope: scope, expires: plan.ExpiresAt}
		c.reset.mu.Unlock()
	}
	return plan, nil
}

// ErrReset 表示重置请求被拒绝（令牌、前置条件、阶段……）。
type ErrReset struct{ Why string }

func (e *ErrReset) Error() string { return "重置被拒绝：" + e.Why }

func resetDenied(format string, args ...any) error {
	return &ErrReset{Why: fmt.Sprintf(format, args...)}
}

// ResetApply 发起或续做重置。校验通过后立即返回，步骤在后台执行（用 ResetStatus 查看进度）。
func (c *Controller) ResetApply(ctx context.Context, req ResetApplyRequest) (ResetStatus, error) {
	spec, err := c.resetSpec()
	if err != nil {
		return ResetStatus{}, err
	}
	c.reset.mu.Lock()
	defer c.reset.mu.Unlock()
	if c.reset.running {
		return ResetStatus{}, resetDenied("上一次重置仍在执行")
	}

	var op *ResetOperation
	var scope ResetScope
	resume := req.OperationID != ""
	if resume {
		op = c.reset.op
		if op == nil || op.OperationID != req.OperationID {
			return ResetStatus{}, resetDenied("没有这个重置操作 %q", req.OperationID)
		}
		if op.State != "failed" && op.State != "running" {
			return ResetStatus{}, resetDenied("操作 %s 已经 %s，不能续做", op.OperationID, op.State)
		}
		scope = op.Scope
	} else {
		scope = req.Scope
		if !scope.Valid() {
			return ResetStatus{}, resetDenied("未知的重置范围 %q（data | all）", scope)
		}
		if c.reset.op != nil && (c.reset.op.State == "failed" || c.reset.op.State == "running") {
			return ResetStatus{}, resetDenied("上一次重置 %s 没有完成：续做（operation_id）或放弃（abort）", c.reset.op.OperationID)
		}
	}

	info, steps, err := spec.Plan(ctx, scope)
	if err != nil {
		return ResetStatus{}, fmt.Errorf("生成重置计划：%w", err)
	}
	digest := digestOf(c.opts.Module, scope, steps, info)
	now := c.opts.Now().UTC()

	if !resume {
		tok, ok := c.reset.tokens[req.Token]
		switch {
		case req.Token == "" || !ok:
			return ResetStatus{}, resetDenied("缺少有效的令牌：先执行 reset plan")
		case !tok.expires.After(now):
			delete(c.reset.tokens, req.Token)
			return ResetStatus{}, resetDenied("令牌已过期（10 分钟）：重新执行 reset plan")
		case tok.scope != scope || tok.digest != digest:
			delete(c.reset.tokens, req.Token)
			return ResetStatus{}, resetDenied("计划已经变化：重新执行 reset plan")
		}
		if len(info.Blockers) > 0 {
			return ResetStatus{}, resetDenied("存在阻止重置的条件：%s", strings.Join(info.Blockers, "；"))
		}
		c.mu.Lock()
		stage := c.self.Stage
		c.mu.Unlock()
		if stage != StagePaused && stage != StageUninitialized {
			return ResetStatus{}, resetDenied("模块处于 %s：重置要求模块已暂停（或未初始化），先 pause", stage)
		}
		delete(c.reset.tokens, req.Token) // 单次使用：前置条件都满足、真正开始之后才消耗
		id := make([]byte, 8)
		if _, err := rand.Read(id); err != nil {
			return ResetStatus{}, err
		}
		op = &ResetOperation{OperationID: "reset-" + now.Format("20060102T150405") + "-" + hex.EncodeToString(id), Scope: scope, Digest: digest, By: req.By, State: "running", StartedAt: now}
		for _, s := range steps {
			op.Steps = append(op.Steps, ResetStepState{Name: s.Name, Describe: s.Describe, State: "pending"})
		}
	} else {
		if len(op.Steps) != len(steps) {
			return ResetStatus{}, resetDenied("模块的重置步骤和操作日志不一致（%d ≠ %d），无法续做：abort 后重新 plan", len(op.Steps), len(steps))
		}
		for i, s := range steps {
			if op.Steps[i].Name != s.Name {
				return ResetStatus{}, resetDenied("模块的重置步骤和操作日志不一致（第 %d 步 %s ≠ %s），无法续做：abort 后重新 plan", i+1, s.Name, op.Steps[i].Name)
			}
		}
		op.State, op.Error = "running", ""
	}

	c.mu.Lock()
	var changes []change
	c.set(c.self, StageResetting, "重置中："+op.OperationID, &changes)
	_ = c.save()
	c.mu.Unlock()
	c.notify(changes)
	c.reset.op = op
	if err := c.saveJournal(op); err != nil {
		return ResetStatus{}, fmt.Errorf("写重置日志：%w", err)
	}
	c.reset.running = true
	c.reset.done = make(chan struct{})
	if resume {
		c.audit("lifecycle_reset_resume module=%s operation_id=%s scope=%s by=%s", c.opts.Module, op.OperationID, op.Scope, req.By)
	} else {
		c.audit("lifecycle_reset_start module=%s operation_id=%s scope=%s by=%s digest=%s steps=%d", c.opts.Module, op.OperationID, op.Scope, req.By, op.Digest, len(op.Steps))
	}
	go c.runReset(context.WithoutCancel(ctx), op, steps)
	return c.resetStatusLocked(), nil
}

// runReset 在后台逐步执行；每步完成就写日志。
func (c *Controller) runReset(ctx context.Context, op *ResetOperation, steps []ResetStep) {
	defer func() {
		c.reset.mu.Lock()
		c.reset.running = false
		close(c.reset.done)
		c.reset.mu.Unlock()
	}()
	fail := func(i int, err error) {
		c.reset.mu.Lock()
		op.Steps[i].State, op.Steps[i].Error, op.Steps[i].At = "failed", err.Error(), c.opts.Now().UTC()
		op.State, op.Error = "failed", fmt.Sprintf("第 %d 步 %s 失败：%v", i+1, op.Steps[i].Name, err)
		_ = c.saveJournal(op)
		c.reset.mu.Unlock()
		c.audit("lifecycle_reset_failed module=%s operation_id=%s step=%s error=%v", c.opts.Module, op.OperationID, op.Steps[i].Name, err)
		c.mu.Lock()
		var changes []change
		c.set(c.self, StageResetFailed, op.Error, &changes)
		_ = c.save()
		c.mu.Unlock()
		c.notify(changes)
	}
	for i, step := range steps {
		c.reset.mu.Lock()
		done := op.Steps[i].State == "done"
		c.reset.mu.Unlock()
		if done {
			continue
		}
		if err := step.Run(ctx, op.OperationID); err != nil {
			fail(i, err)
			return
		}
		c.reset.mu.Lock()
		op.Steps[i].State, op.Steps[i].Error, op.Steps[i].At = "done", "", c.opts.Now().UTC()
		err := c.saveJournal(op)
		c.reset.mu.Unlock()
		if err != nil {
			fail(i, fmt.Errorf("写重置日志：%w", err))
			return
		}
		c.audit("lifecycle_reset_step module=%s operation_id=%s step=%s done=%d/%d", c.opts.Module, op.OperationID, step.Name, i+1, len(steps))
	}
	c.finishReset(op)
	c.audit("lifecycle_reset_done module=%s operation_id=%s scope=%s by=%s", c.opts.Module, op.OperationID, op.Scope, op.By)
}

// finishReset 完成重置：回到未初始化，保持「重置后等待初始化」，对象全部清掉（它们的定义已被删除）。
func (c *Controller) finishReset(op *ResetOperation) {
	now := c.opts.Now().UTC()
	c.reset.mu.Lock()
	op.State, op.FinishedAt, op.Error = "done", now, ""
	_ = c.saveJournal(op)
	c.reset.mu.Unlock()
	c.mu.Lock()
	var changes []change
	c.self.Pause, c.self.PausedSince, c.self.Resume = nil, time.Time{}, ""
	c.self.catchup, c.self.autoRelease, c.self.catchupMember = nil, false, false
	c.self.Hold = HoldPostReset
	c.self.LastReset = &ResetRecord{OperationID: op.OperationID, Scope: op.Scope, By: op.By, At: now}
	c.objects = map[string]*record{}
	c.checks = map[string][]Check{}
	c.set(c.self, StageUninitialized, "重置完成，等待初始化", &changes)
	_ = c.save()
	c.mu.Unlock()
	c.notify(changes)
}

// ResetWait 等待后台执行结束（测试与命令行轮询用）。
func (c *Controller) ResetWait(ctx context.Context) error {
	c.reset.mu.Lock()
	done := c.reset.done
	running := c.reset.running
	c.reset.mu.Unlock()
	if !running || done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResetAbort 放弃一次失败的重置：模块置为未初始化并保持「重置后等待初始化」，已做的步骤不会回来，
// 尚未清理的对象不会因此被放行。
func (c *Controller) ResetAbort(ctx context.Context, by string) (ResetStatus, error) {
	c.reset.mu.Lock()
	defer c.reset.mu.Unlock()
	if c.reset.running {
		return ResetStatus{}, resetDenied("重置仍在执行，不能放弃")
	}
	op := c.reset.op
	if op == nil || op.State != "failed" {
		return ResetStatus{}, resetDenied("没有失败的重置可以放弃")
	}
	op.State, op.FinishedAt = "aborted", c.opts.Now().UTC()
	if by != "" {
		op.By = by
	}
	_ = c.saveJournal(op)
	c.mu.Lock()
	var changes []change
	c.self.Hold = HoldPostReset
	c.set(c.self, StageUninitialized, "重置已放弃（"+op.OperationID+"），已做的步骤不会回来", &changes)
	_ = c.save()
	c.mu.Unlock()
	c.notify(changes)
	c.audit("lifecycle_reset_abort module=%s operation_id=%s by=%s", c.opts.Module, op.OperationID, by)
	return c.resetStatusLocked(), nil
}

// ResetStatus 返回当前重置状态。
func (c *Controller) ResetStatus() ResetStatus {
	c.reset.mu.Lock()
	defer c.reset.mu.Unlock()
	return c.resetStatusLocked()
}

func (c *Controller) resetStatusLocked() ResetStatus {
	c.mu.Lock()
	st := ResetStatus{Module: c.opts.Module, Stage: c.self.Stage, LastReset: c.self.LastReset}
	c.mu.Unlock()
	if c.reset.op != nil {
		cp := *c.reset.op
		cp.Steps = append([]ResetStepState(nil), c.reset.op.Steps...)
		st.Operation = &cp
	}
	return st
}
