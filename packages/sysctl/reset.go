package sysctl

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
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
)

// 系统级重置编排（设计第 6 节）。分两段：
//
//	plan：连接每个模块取各自的重置计划，汇总阻止条件，得到系统摘要和 10 分钟有效的一次性令牌（只用于发起）；
//	apply：校验令牌与（生产环境的）系统名，写系统日志，先系统暂停，再按顺序逐个模块重置：
//	       下游到上游，Storage 在 Archive 之后、Admin 业务部分最后。模块重置前再取一次该模块的计划，
//	       摘要和 plan 时不一致就停下，所以系统令牌过期不影响后面的模块；
//	       失败或中断后凭系统 operation_id 续做，已完成的步骤跳过，不自动回滚。
//
// Storage、EventBus 与 Admin 业务部分是外部流程（要在各自的主机上执行，编排器没有登录那些主机的能力），
// 作为「外部步骤」写进计划：编排器停在那一步，列出要执行的命令，执行完用 --external-done 标记后继续。

// 参与重置的模块名（和各模块的生命周期名一致）。
const (
	FactorMgr = "factor-mgr"
	Archive   = "archive-mgr"
	Monitor   = "monitor"
)

// resetOrder 是模块重置顺序：下游到上游，先清下游，避免上游清空时下游仍在读写。
var resetOrder = []string{Strategy, Trade, FactorEngine, FactorMgr, Collector, Archive, Monitor}

// resetPauseOrder 是重置前的暂停顺序：先让 Monitor 静默告警，再下游到上游。
var resetPauseOrder = []string{Monitor, Strategy, Trade, FactorEngine, FactorMgr, Collector, Archive}

// ResetAPI 是模块的重置接口（用重置专用密钥的 *lifecycle.Client 实现）。
type ResetAPI interface {
	ResetPlan(ctx context.Context, scope lifecycle.ResetScope) (lifecycle.ResetPlan, error)
	ResetApply(ctx context.Context, req lifecycle.ResetApplyRequest) (lifecycle.ResetStatus, error)
	ResetStatus(ctx context.Context) (lifecycle.ResetStatus, error)
}

// ExternalStep 是不在模块进程内的重置步骤（Storage、EventBus、Admin 业务部分）。
type ExternalStep struct {
	Name     string
	Describe string
	// After 是它排在哪个模块之后（resetOrder 里的名字）；空表示最后。
	After string
	// Commands 是人工要执行的命令（Run 为空时展示给用户）。
	Commands []string
	// Run 不为空时由编排器自动执行；为空表示人工执行，编排器停下等 --external-done。
	Run func(ctx context.Context) error
}

// ResetConfig 是系统重置的配置。
type ResetConfig struct {
	// StateDir 放 plan.json 与 journal.json（0600），重置可以跨命令续做。
	StateDir string
	// SystemName 是系统名；Production 为真时 apply 要逐字输入它。
	SystemName string
	Production bool
	// External 是外部步骤。
	External []ExternalStep
	// BlockerWait 是模块重置前等待暂时性阻止条件消失的时限（默认 3 分钟）：
	// 系统暂停刚结束时，Collector 的 SCF 定时器还没确认停用、在途操作还没落地，这类阻止条件几秒到几分钟内会自己消失。
	// 超时仍有阻止条件才失败（实盘持仓这类不会自己消失的，也只是多等这一段时间）。
	BlockerWait time.Duration
	// Now 可替换（测试用）。
	Now func() time.Time
}

func (c ResetConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ModuleResetPlan 是一个模块的计划（令牌不进系统计划，模块重置前会重新取）。
type ModuleResetPlan struct {
	Name     string   `json:"name"`
	Steps    []string `json:"steps"`
	Keeps    []string `json:"keeps,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Blockers []string `json:"blockers,omitempty"`
	Digest   string   `json:"digest"`
	// Stats 是行数、字节数等易变统计，只展示。
	Stats map[string]any `json:"stats,omitempty"`
}

// ExternalPlan 是外部步骤的计划项。
type ExternalPlan struct {
	Name     string   `json:"name"`
	Describe string   `json:"describe"`
	After    string   `json:"after,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Manual   bool     `json:"manual"`
}

// SystemResetPlan 是系统重置计划。
type SystemResetPlan struct {
	Scope      lifecycle.ResetScope `json:"scope"`
	SystemName string               `json:"system_name"`
	Production bool                 `json:"production"`
	Modules    []ModuleResetPlan    `json:"modules"`
	External   []ExternalPlan       `json:"external,omitempty"`
	// Skipped 是没有配置地址、不参与的模块。
	Skipped  []string  `json:"skipped,omitempty"`
	Blockers []string  `json:"blockers,omitempty"`
	Digest   string    `json:"digest"`
	Token    string    `json:"token,omitempty"`
	Expires  time.Time `json:"expires_at,omitempty"`
}

// ResetStepState 是系统日志里的一步。
type ResetStepState struct {
	Kind     string   `json:"kind"` // module | external
	Name     string   `json:"name"`
	Describe string   `json:"describe,omitempty"`
	Digest   string   `json:"digest,omitempty"`
	State    string   `json:"state"` // pending | running | done | failed | awaiting
	OpID     string   `json:"module_operation_id,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// ResetJournal 是系统重置的持久化日志。
type ResetJournal struct {
	OperationID string               `json:"operation_id"`
	Scope       lifecycle.ResetScope `json:"scope"`
	SystemName  string               `json:"system_name"`
	By          string               `json:"by,omitempty"`
	State       string               `json:"state"` // running | failed | waiting_external | done
	Paused      bool                 `json:"paused"`
	StartedAt   time.Time            `json:"started_at"`
	FinishedAt  time.Time            `json:"finished_at,omitempty"`
	Error       string               `json:"error,omitempty"`
	Steps       []ResetStepState     `json:"steps"`
}

// ResetApplyRequest 是 apply 的参数：新发起带 Token（生产环境还要 ConfirmName），续做带 OperationID。
type ResetApplyRequest struct {
	Token       string
	ConfirmName string
	OperationID string
	// ExternalDone 是人工已经执行完的外部步骤。
	ExternalDone []string
}

// ErrAwaitingExternal 表示停在人工执行的外部步骤上；日志里有要执行的命令。
var ErrAwaitingExternal = errors.New("等待人工执行外部步骤")

func (o *Orchestrator) resetModules() []Module {
	var out []Module
	for _, name := range resetOrder {
		if m, ok := o.module(name); ok {
			out = append(out, m)
		}
	}
	return out
}

func (o *Orchestrator) planPath() string    { return filepath.Join(o.Reset.StateDir, "plan.json") }
func (o *Orchestrator) journalPath() string { return filepath.Join(o.Reset.StateDir, "journal.json") }

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadResetJournal 读取系统重置日志；没有时返回 nil。
func (o *Orchestrator) LoadResetJournal() (*ResetJournal, error) {
	var j ResetJournal
	if err := readJSON(o.journalPath(), &j); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return &j, nil
}

func (o *Orchestrator) saveJournal(j *ResetJournal) error { return writeJSON(o.journalPath(), j) }

func (o *Orchestrator) externalAfter(module string) []ExternalStep {
	var out []ExternalStep
	for _, e := range o.Reset.External {
		if e.After == module {
			out = append(out, e)
		}
	}
	return out
}

// externalLast 是没有指定位置的外部步骤，排在所有模块之后。
func (o *Orchestrator) externalLast() []ExternalStep {
	var out []ExternalStep
	for _, e := range o.Reset.External {
		if e.After == "" {
			out = append(out, e)
		}
	}
	return out
}

// PlanReset 取各模块的重置计划并汇总（只读）。令牌和计划写进 StateDir，apply 凭它发起。
func (o *Orchestrator) PlanReset(ctx context.Context, scope lifecycle.ResetScope) (SystemResetPlan, error) {
	if scope != lifecycle.ResetData && scope != lifecycle.ResetAll {
		return SystemResetPlan{}, fmt.Errorf("范围只能是 data 或 all，收到 %q", scope)
	}
	if o.Reset.StateDir == "" {
		return SystemResetPlan{}, errors.New("没有配置系统重置的状态目录")
	}
	if o.Reset.Production && o.Reset.SystemName == "" {
		return SystemResetPlan{}, errors.New("生产环境需要系统名（--system-name 或 MOOX_SYSTEM_NAME），apply 时要逐字输入它确认")
	}
	if j, err := o.LoadResetJournal(); err != nil {
		return SystemResetPlan{}, err
	} else if j != nil && j.State != "done" {
		return SystemResetPlan{}, fmt.Errorf("有未完成的系统重置 %s（%s）：用 apply --operation-id 续做", j.OperationID, j.State)
	}
	plan := SystemResetPlan{Scope: scope, SystemName: o.Reset.SystemName, Production: o.Reset.Production}
	mods := o.resetModules()
	if len(mods) == 0 {
		return plan, errors.New("没有配置任何可重置模块的地址")
	}
	configured := map[string]bool{}
	for _, m := range mods {
		configured[m.Name] = true
		mp, err := o.planModule(ctx, m, scope)
		if err != nil {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s：%v", m.Name, err))
			continue
		}
		for _, b := range mp.Blockers {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s：%s", m.Name, b))
		}
		plan.Modules = append(plan.Modules, mp)
	}
	for _, name := range resetOrder {
		if !configured[name] {
			plan.Skipped = append(plan.Skipped, name)
		}
	}
	for _, e := range o.Reset.External {
		plan.External = append(plan.External, ExternalPlan{Name: e.Name, Describe: e.Describe, After: e.After, Commands: e.Commands, Manual: e.Run == nil})
	}
	plan.Digest = planDigest(plan)
	if len(plan.Blockers) == 0 {
		plan.Token = randomToken()
		plan.Expires = o.Reset.now().Add(lifecycle.ResetTokenTTL)
	}
	if err := writeJSON(o.planPath(), plan); err != nil {
		return plan, fmt.Errorf("保存计划：%w", err)
	}
	return plan, nil
}

// planModule 取一个模块的计划；模块处于重置中或重置失败时直接报错。
func (o *Orchestrator) planModule(ctx context.Context, m Module, scope lifecycle.ResetScope) (ModuleResetPlan, error) {
	mp, _, err := o.planModuleToken(ctx, m, scope)
	return mp, err
}

// planModuleToken 同 planModule，另外返回模块给出的一次性令牌（发起模块重置用）。
func (o *Orchestrator) planModuleToken(ctx context.Context, m Module, scope lifecycle.ResetScope) (ModuleResetPlan, string, error) {
	if m.Reset == nil {
		return ModuleResetPlan{}, "", errors.New("没有配置重置接口（缺重置专用密钥）")
	}
	snap, err := m.API.Status(ctx)
	if err != nil {
		return ModuleResetPlan{}, "", fmt.Errorf("读取状态：%w", err)
	}
	if st := snap.Self.Stage; st == lifecycle.StageResetting || st == lifecycle.StageResetFailed {
		return ModuleResetPlan{}, "", fmt.Errorf("模块处于%s，先处理上一次的重置", st)
	}
	p, err := m.Reset.ResetPlan(ctx, scope)
	if err != nil {
		return ModuleResetPlan{}, "", fmt.Errorf("取重置计划：%w", err)
	}
	mp := ModuleResetPlan{Name: m.Name, Keeps: p.Keeps, Notes: p.Notes, Blockers: p.Blockers, Digest: p.Digest, Stats: p.Stats}
	for _, s := range p.Steps {
		mp.Steps = append(mp.Steps, s.Name+"："+s.Describe)
	}
	return mp, p.Token, nil
}

func planDigest(p SystemResetPlan) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s\n", p.Scope, p.SystemName)
	for _, m := range p.Modules {
		fmt.Fprintf(h, "m|%s|%s\n", m.Name, m.Digest)
	}
	names := make([]string, 0, len(p.External))
	for _, e := range p.External {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	fmt.Fprintf(h, "e|%s\n", strings.Join(names, ","))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ApplyReset 发起或续做系统重置，同步执行到完成、失败或停在外部步骤。
func (o *Orchestrator) ApplyReset(ctx context.Context, req ResetApplyRequest) (*ResetJournal, error) {
	j, err := o.openJournal(req)
	if err != nil {
		return nil, err
	}
	for _, name := range req.ExternalDone {
		found := false
		for i := range j.Steps {
			if j.Steps[i].Kind == "external" && j.Steps[i].Name == name {
				found = true
				if j.Steps[i].State != "done" {
					j.Steps[i].State, j.Steps[i].Error = "done", ""
				}
			}
		}
		if !found {
			return j, fmt.Errorf("没有叫 %q 的外部步骤", name)
		}
	}
	return j, o.run(ctx, j)
}

// openJournal 新发起时校验令牌和系统名并建日志，续做时读取已有日志。
func (o *Orchestrator) openJournal(req ResetApplyRequest) (*ResetJournal, error) {
	if req.OperationID != "" {
		j, err := o.LoadResetJournal()
		if err != nil {
			return nil, err
		}
		if j == nil || j.OperationID != req.OperationID {
			return nil, fmt.Errorf("没有找到系统重置 %s 的日志", req.OperationID)
		}
		if j.State == "done" {
			return j, errors.New("这次系统重置已经完成")
		}
		j.State, j.Error = "running", ""
		return j, nil
	}
	if j, err := o.LoadResetJournal(); err != nil {
		return nil, err
	} else if j != nil && j.State != "done" {
		return nil, fmt.Errorf("有未完成的系统重置 %s（%s）：用 --operation-id 续做", j.OperationID, j.State)
	}
	var plan SystemResetPlan
	if err := readJSON(o.planPath(), &plan); err != nil {
		return nil, errors.New("没有可用的计划：先执行 system reset plan")
	}
	switch {
	case plan.Token == "" || req.Token == "" || plan.Token != req.Token:
		return nil, errors.New("令牌不对或已经用过：重新执行 system reset plan")
	case o.Reset.now().After(plan.Expires):
		return nil, errors.New("令牌已过期：重新执行 system reset plan")
	case len(plan.Blockers) > 0:
		return nil, errors.New("计划里有阻止重置的条件：重新执行 system reset plan 查看")
	case plan.Production && req.ConfirmName != plan.SystemName:
		return nil, fmt.Errorf("生产环境需要逐字输入系统名确认（--confirm-name %s）", plan.SystemName)
	}
	// 令牌只能用一次：先作废再开始。
	plan.Token = ""
	if err := writeJSON(o.planPath(), plan); err != nil {
		return nil, err
	}
	j := &ResetJournal{
		OperationID: "sysreset-" + o.Reset.now().UTC().Format("20060102T150405") + "-" + randomToken()[:6],
		Scope:       plan.Scope, SystemName: plan.SystemName, By: o.By, State: "running", StartedAt: o.Reset.now().UTC(),
	}
	digests := map[string]string{}
	for _, m := range plan.Modules {
		digests[m.Name] = m.Digest
	}
	// 外部步骤的说明和命令以 plan 时为准（命令随范围不同），位置以配置为准。
	planned := map[string]ExternalPlan{}
	for _, e := range plan.External {
		planned[e.Name] = e
	}
	externalStep := func(e ExternalStep) ResetStepState {
		if p, ok := planned[e.Name]; ok {
			e.Describe, e.Commands = p.Describe, p.Commands
		}
		return externalState(e)
	}
	for _, name := range resetOrder {
		if d, ok := digests[name]; ok {
			j.Steps = append(j.Steps, ResetStepState{Kind: "module", Name: name, Digest: d, State: "pending"})
		}
		for _, e := range o.externalAfter(name) {
			j.Steps = append(j.Steps, externalStep(e))
		}
	}
	for _, e := range o.externalLast() {
		j.Steps = append(j.Steps, externalStep(e))
	}
	if err := o.saveJournal(j); err != nil {
		return nil, err
	}
	return j, nil
}

func externalState(e ExternalStep) ResetStepState {
	return ResetStepState{Kind: "external", Name: e.Name, Describe: e.Describe, Commands: e.Commands, State: "pending"}
}

func (o *Orchestrator) external(name string) (ExternalStep, bool) {
	for _, e := range o.Reset.External {
		if e.Name == name {
			return e, true
		}
	}
	return ExternalStep{}, false
}

// run 依次执行日志里没完成的步骤。
func (o *Orchestrator) run(ctx context.Context, j *ResetJournal) error {
	fail := func(step *ResetStepState, err error) error {
		step.State, step.Error = "failed", err.Error()
		j.State, j.Error = "failed", fmt.Sprintf("%s：%v", step.Name, err)
		_ = o.saveJournal(j)
		return fmt.Errorf("系统重置 %s 在 %s 失败：%w（修好后用 --operation-id %s 续做）", j.OperationID, step.Name, err, j.OperationID)
	}
	if !j.Paused {
		o.logf("系统暂停")
		for _, name := range resetPauseOrder {
			m, ok := o.module(name)
			if !ok || !hasStep(j, name) {
				continue
			}
			_, err := m.API.Exec(ctx, lifecycle.CommandRequest{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem, Reason: "系统重置 " + j.OperationID, By: o.By})
			if err != nil && !strings.Contains(err.Error(), "已暂停，但在途操作没有结束") {
				j.State, j.Error = "failed", fmt.Sprintf("暂停 %s：%v", name, err)
				_ = o.saveJournal(j)
				return fmt.Errorf("暂停 %s：%w", name, err)
			}
			o.logf("%s：已暂停", name)
		}
		j.Paused = true
		if err := o.saveJournal(j); err != nil {
			return err
		}
	}
	for i := range j.Steps {
		step := &j.Steps[i]
		if step.State == "done" {
			continue
		}
		switch step.Kind {
		case "module":
			if err := o.resetModuleStep(ctx, j, step); err != nil {
				return fail(step, err)
			}
		default:
			ext, ok := o.external(step.Name)
			if !ok {
				return fail(step, errors.New("外部步骤不在当前配置里"))
			}
			if ext.Run == nil {
				step.State = "awaiting"
				j.State = "waiting_external"
				_ = o.saveJournal(j)
				o.logf("等待人工执行外部步骤 %s：%s", step.Name, step.Describe)
				return fmt.Errorf("%w：%s（执行完后用 --operation-id %s --external-done %s 继续）", ErrAwaitingExternal, step.Name, j.OperationID, step.Name)
			}
			step.State = "running"
			_ = o.saveJournal(j)
			if err := ext.Run(ctx); err != nil {
				return fail(step, err)
			}
			step.State = "done"
		}
		if err := o.saveJournal(j); err != nil {
			return err
		}
	}
	j.State, j.FinishedAt = "done", o.Reset.now().UTC()
	return o.saveJournal(j)
}

func hasStep(j *ResetJournal, module string) bool {
	for _, s := range j.Steps {
		if s.Kind == "module" && s.Name == module {
			return true
		}
	}
	return false
}

// freshPlan 重新取模块的计划；有阻止条件时在 BlockerWait 内重试（暂时性的会自己消失），超时仍有才失败。
func (o *Orchestrator) freshPlan(ctx context.Context, m Module, scope lifecycle.ResetScope) (ModuleResetPlan, string, error) {
	wait := o.Reset.BlockerWait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	deadline := time.Now().Add(wait)
	for {
		mp, token, err := o.planModuleToken(ctx, m, scope)
		if err != nil {
			return mp, "", err
		}
		if len(mp.Blockers) == 0 {
			return mp, token, nil
		}
		if time.Now().After(deadline) {
			return mp, "", fmt.Errorf("现在不能重置：%s", strings.Join(mp.Blockers, "；"))
		}
		o.logf("%s：等待阻止条件消失：%s", m.Name, strings.Join(mp.Blockers, "；"))
		if err := o.sleep(ctx); err != nil {
			return mp, "", err
		}
	}
}

// resetModuleStep 重置一个模块：新步骤先取新计划核对摘要再发起；已经发起过的凭模块 operation_id 续做。
func (o *Orchestrator) resetModuleStep(ctx context.Context, j *ResetJournal, step *ResetStepState) error {
	m, ok := o.module(step.Name)
	if !ok || m.Reset == nil {
		return errors.New("模块没有配置重置接口")
	}
	if step.OpID == "" {
		fresh, token, err := o.freshPlan(ctx, m, j.Scope)
		if err != nil {
			return err
		}
		if fresh.Digest != step.Digest {
			return errors.New("模块的重置计划和 plan 时不一致（配置或表结构变了）：重新执行 system reset plan")
		}
		st, err := m.Reset.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: token, Scope: j.Scope, By: o.By})
		if err != nil {
			return err
		}
		step.OpID, step.State = st.Operation.OperationID, "running"
		if err := o.saveJournal(j); err != nil {
			return err
		}
		o.logf("%s：开始重置 %s", step.Name, step.OpID)
	} else {
		st, err := m.Reset.ResetStatus(ctx)
		if err != nil {
			return err
		}
		if st.Operation != nil && st.Operation.OperationID == step.OpID && st.Operation.State == "failed" {
			if _, err := m.Reset.ResetApply(ctx, lifecycle.ResetApplyRequest{OperationID: step.OpID, By: o.By}); err != nil {
				return err
			}
			o.logf("%s：续做重置 %s", step.Name, step.OpID)
		}
	}
	for {
		st, err := m.Reset.ResetStatus(ctx)
		if err != nil {
			return err
		}
		if op := st.Operation; op != nil && op.OperationID == step.OpID {
			switch op.State {
			case "done":
				step.State, step.Error = "done", ""
				o.logf("%s：重置完成", step.Name)
				return nil
			case "failed":
				return fmt.Errorf("模块重置失败：%s", op.Error)
			case "aborted":
				return errors.New("模块重置被放弃了")
			}
		} else if st.LastReset != nil && st.LastReset.OperationID == step.OpID {
			step.State = "done"
			return nil
		}
		if err := o.sleep(ctx); err != nil {
			return err
		}
	}
}
