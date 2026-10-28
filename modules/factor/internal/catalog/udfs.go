package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorudf"
	"gorm.io/gorm"
)

// EngineView 返回管理端看到的引擎状态（心跳里的模块生命周期）：切换自定义算子版本之前据此确认引擎已暂停且本次暂停已收敛。
type EngineView func(ctx context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error)

// UDFVerifier 是发布时复核模块用的运行时与测试限制。它与引擎的运行时、试算的运行时分开：发布测试串行执行
// （持有全局 UDF 锁），有自己的时限与内存上限。
type UDFVerifier struct {
	Runtime     *factorudf.Runtime
	Limits      factorudf.Limits
	Conformance factorudf.ConformanceOptions
}

// WithUDFVerifier 配置发布复核。
func WithUDFVerifier(v UDFVerifier) Option { return func(s *Service) { s.verifier = &v } }

// WithEngineView 配置引擎状态来源（切换版本的前提检查用）。
func WithEngineView(view EngineView) Option { return func(s *Service) { s.engineView = view } }

// 引擎心跳的新鲜度：切换前提的“已暂停且已收敛”要来自足够新的心跳，并在提交前由之后的另一次心跳复核。
const (
	engineStatusMaxAge   = 30 * time.Second
	engineRecheckTimeout = 25 * time.Second
	engineRecheckPoll    = 250 * time.Millisecond
)

// ---------------------------------------------------------------- 发布

// PublishResult 是发布的结果。
type PublishResult struct {
	Name    string
	Hash    string
	Created bool
	Report  *factorudf.Report
}

// PublishUdf 复核并保存一个版本（未启用）：包结构、模块检查、元数据一致、重跑一致性测试并比对摘要；
// 通过后存为不可变版本；同一哈希重复上传只更新说明与示例。持有全局 UDF 锁，发布测试因此串行执行。
func (s *Service) PublishUdf(ctx context.Context, raw []byte) (*PublishResult, error) {
	if s.verifier == nil || s.verifier.Runtime == nil {
		return nil, errors.New("自定义算子发布复核没有配置")
	}
	pkg, err := factorudf.ReadPackage(raw, s.verifier.Limits)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidUdf, err)
	}
	info, err := factorudf.VerifyPackage(pkg, s.verifier.Limits)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidUdf, err)
	}
	if !pkg.Report.Passed {
		return nil, fmt.Errorf("%w: 包里的本地测试报告显示测试没有通过", ErrInvalidUdf)
	}
	unlock, err := s.locks.LockUdfContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	rt := s.verifier.Runtime
	module, err := rt.Load(ctx, pkg.Wasm)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidUdf, err)
	}
	// 发布复核用完即卸载：版本由引擎按需拉取，管理端不常驻实例。
	defer rt.Unload(ctx, info.Hash)
	report, err := factorudf.RunConformance(ctx, rt, module, pkg.Fixtures, s.verifier.Conformance)
	if err != nil {
		return nil, err
	}
	if !report.Passed {
		return nil, fmt.Errorf("%w: 服务端一致性测试未通过：%s", ErrInvalidUdf, describeFailures(report))
	}
	if report.Digest != pkg.Report.Digest {
		return nil, fmt.Errorf("%w: 本地与服务端的测试输出摘要不一致（本地 %s，服务端 %s）：同一个 wasm 在两种环境下算出了不同的结果，"+
			"通常是依赖了未规定的浮点或 NaN 行为", ErrInvalidUdf, short(pkg.Report.Digest), short(report.Digest))
	}
	manifest, err := json.Marshal(pkg.Manifest)
	if err != nil {
		return nil, err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	created, err := s.db.PutUdfVersion(ctx, store.UdfVersion{
		Hash: info.Hash, Name: info.Manifest.Name, Manifest: string(manifest),
		SourceRef: pkg.Manifest.Source.Repo + "@" + pkg.Manifest.Source.Commit, SDKVersion: info.Manifest.SDK,
		Toolchain: pkg.Manifest.Toolchain, Report: string(reportJSON), Wasm: pkg.Wasm,
	})
	if err != nil {
		return nil, err
	}
	return &PublishResult{Name: info.Manifest.Name, Hash: info.Hash, Created: created, Report: report}, nil
}

// ErrInvalidUdf 表示上传的自定义算子包不合格（包结构、模块检查或一致性测试没有通过）。
var ErrInvalidUdf = errors.New("自定义算子包不合格")

func describeFailures(r *factorudf.Report) string {
	var parts []string
	for _, f := range r.Failures() {
		parts = append(parts, f.Name+"："+f.Detail)
	}
	return strings.Join(parts, "；")
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// ---------------------------------------------------------------- 切换

// Prerequisite 是切换的一项前提及其检查结果。
type Prerequisite struct {
	Name   string
	OK     bool
	Detail string
}

// UdfImpact 是切换对一个因子定义的影响：新旧的 definition_hash、回看与输入列，以及引用它的成员。
type UdfImpact struct {
	FactorID          string
	OldDefinitionHash string
	NewDefinitionHash string
	OldLookback       int
	NewLookback       int
	InputColumns      []string
	Members           []string // task/member_key
	Error             string   // 用新版本重新编译或成员校验失败的原因
}

// ActivateResult 是 ActivateUdf 的结果。
type ActivateResult struct {
	Name          string
	OldHash       string
	NewHash       string
	DryRun        bool
	Applied       bool
	Impacts       []UdfImpact
	Tasks         []string
	Instances     []string
	Prerequisites []Prerequisite
	Notes         []string
}

// ActivateUdf 切换名称启用的版本。切换在因子引擎模块暂停期间进行：影响清单（引用它的定义 → 成员 → 因子任务 → 策略实例）、
// 前提一（引擎已暂停且本次暂停已收敛，提交前由之后的另一次心跳复核）、前提二（受影响任务没有已领取且未结束的补算作业）、
// 用新版本重新编译并重做成员校验、报告新旧 definition_hash 与回看。dryRun 只返回影响清单与各前提的检查结果。
// 没有任何已启用成员引用它时（例如首次启用）没有计算在用旧版本，不要求引擎暂停。
func (s *Service) ActivateUdf(ctx context.Context, name, hash string, dryRun bool) (*ActivateResult, error) {
	unlock, err := s.locks.LockUdfContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	current, versions, err := s.db.GetUdf(ctx, name)
	if err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, errors.New("要启用的版本哈希不能为空")
	}
	found := false
	for _, v := range versions {
		found = found || v.Hash == hash
	}
	if !found {
		return nil, fmt.Errorf("%w: version %s is not a version of udf %q", store.ErrConflict, hash, name)
	}
	result := &ActivateResult{Name: name, OldHash: current.ActiveHash, NewHash: hash, DryRun: dryRun}
	if current.ActiveHash == hash {
		result.Notes = append(result.Notes, "这个版本已经是启用版本，没有变化")
		result.Prerequisites = []Prerequisite{}
		return result, nil
	}

	refs, err := s.db.UdfReferences(ctx, name)
	if err != nil {
		return nil, err
	}
	// 引用清单在全局 UDF 锁下是稳定的（改变引用关系的入口都要先取它）；成员的增删与启停、任务的启停另有各自的锁，
	// 所以再按锁顺序取引用它的定义的锁与受影响任务的锁（定义锁 → 任务锁，各自按 ID 升序）：校验与提交期间不会有成员或任务
	// 在别处启用，“没有已启用成员所以不要求暂停引擎”的判断才不会被并发的启用推翻。
	sortedRefs := append([]string(nil), refs...)
	sort.Strings(sortedRefs)
	for _, factorID := range sortedRefs {
		unlockFactor, err := s.locks.LockFactorContext(ctx, factorID)
		if err != nil {
			return nil, err
		}
		defer unlockFactor()
	}
	usages, err := s.db.ListUsages(ctx, refs...)
	if err != nil {
		return nil, err
	}
	taskIDs := map[string]bool{}
	for _, list := range usages {
		for _, usage := range list {
			taskIDs[usage.TaskID] = true
		}
	}
	sortedTasks := make([]string, 0, len(taskIDs))
	for id := range taskIDs {
		sortedTasks = append(sortedTasks, id)
	}
	sort.Strings(sortedTasks)
	for _, taskID := range sortedTasks {
		unlockTask, err := s.locks.LockContext(ctx, taskID)
		if err != nil {
			return nil, err
		}
		defer unlockTask()
	}
	taskSet := map[string]domain.FactorTask{}
	enabledImpact := false
	var impactErrs []string
	for _, factorID := range refs {
		impact, err := s.impactOf(ctx, factorID, name, hash, usages[factorID], taskSet, &enabledImpact)
		if err != nil {
			return nil, err
		}
		if impact.Error != "" {
			impactErrs = append(impactErrs, factorID+"："+impact.Error)
		}
		result.Impacts = append(result.Impacts, impact)
	}
	instances := map[string]bool{}
	for id, task := range taskSet {
		result.Tasks = append(result.Tasks, id)
		if task.BoundInstanceID != "" {
			instances[task.BoundInstanceID] = true
		}
	}
	sort.Strings(result.Tasks)
	for id := range instances {
		result.Instances = append(result.Instances, id)
	}
	sort.Strings(result.Instances)
	s.lookbackNotes(result)

	result.Prerequisites = s.prerequisites(ctx, enabledImpact, result.Tasks)
	recompiled := Prerequisite{Name: "用新版本重新编译并重做成员校验", OK: len(impactErrs) == 0}
	if !recompiled.OK {
		recompiled.Detail = strings.Join(impactErrs, "；")
	}
	result.Prerequisites = append(result.Prerequisites, recompiled)
	if dryRun {
		return result, nil
	}
	for _, p := range result.Prerequisites {
		if !p.OK {
			return result, fmt.Errorf("%w: 前提不满足：%s：%s", store.ErrConflict, p.Name, p.Detail)
		}
	}
	if enabledImpact {
		// 提交前复核：等一次新的心跳，确认引擎仍然处于同一次暂停且已收敛。
		if err := s.recheckEngine(ctx); err != nil {
			return result, fmt.Errorf("%w: 前提不满足：提交前复核引擎状态：%v", store.ErrConflict, err)
		}
		again := s.prerequisites(ctx, enabledImpact, result.Tasks)
		for _, p := range again {
			if !p.OK {
				return result, fmt.Errorf("%w: 前提不满足：%s：%s", store.ErrConflict, p.Name, p.Detail)
			}
		}
	}
	if err := s.db.SetActiveUdf(ctx, name, hash); err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}

// impactOf 用新版本重新编译一个引用它的定义，并对每个引用它的成员重做成员校验（规则同 UpdateFactor）。
func (s *Service) impactOf(ctx context.Context, factorID, name, hash string, usages []domain.FactorUsage,
	tasks map[string]domain.FactorTask, enabledImpact *bool) (UdfImpact, error) {
	impact := UdfImpact{FactorID: factorID}
	old, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return impact, err
	}
	impact.OldDefinitionHash = domain.DefinitionHash(old)
	if c := old.Compiled(); c != nil {
		impact.OldLookback = c.Lookback
	}
	replaced := old
	env, err := s.db.ResolveUDFs(ctx, domain.UDFNames(old.Spec), map[string]string{name: hash})
	if err != nil {
		impact.Error = err.Error()
		return impact, nil
	}
	replaced.UDFs = env
	newDef, err := domain.CompileDefinition(replaced)
	if err != nil {
		impact.Error = "用新版本重新编译失败：" + err.Error()
		return impact, nil
	}
	impact.NewDefinitionHash = domain.DefinitionHash(newDef)
	impact.NewLookback = newDef.LookbackPeriods
	impact.InputColumns = newDef.InputColumns
	for _, usage := range usages {
		impact.Members = append(impact.Members, usage.TaskID+"/"+usage.MemberKey)
		task, err := s.db.GetTask(ctx, usage.TaskID)
		if err != nil {
			return impact, err
		}
		tasks[task.TaskID] = task
		if usage.Status == domain.MemberStatusEnabled && task.Status == domain.TaskStatusEnabled {
			*enabledImpact = true
		}
		member, err := s.db.GetMember(ctx, usage.TaskID, usage.MemberKey)
		if err != nil {
			return impact, err
		}
		if err := domain.CheckMemberParams(newDef, member.Params); err != nil {
			impact.Error = fmt.Sprintf("任务 %s 的成员 %s 的参数在新版本下不再合法：%v", usage.TaskID, usage.MemberKey, err)
			return impact, nil
		}
		if _, err := s.validateMembership(ctx, task, domain.MemberDef{Key: usage.MemberKey, Factor: newDef, Kind: member.Kind, Params: member.Params}); err != nil {
			impact.Error = fmt.Sprintf("任务 %s 的成员 %s 校验失败：%v", usage.TaskID, usage.MemberKey, err)
			return impact, nil
		}
	}
	return impact, nil
}

// lookbackNotes 在回看变长时提示需要同步调整的引擎侧配置（源数据保留期、窗口与内存预算）。
func (s *Service) lookbackNotes(r *ActivateResult) {
	var grown []string
	for _, impact := range r.Impacts {
		if impact.NewLookback > impact.OldLookback {
			grown = append(grown, fmt.Sprintf("%s（%d → %d）", impact.FactorID, impact.OldLookback, impact.NewLookback))
		}
	}
	if len(grown) > 0 {
		r.Notes = append(r.Notes, "回看变长："+strings.Join(grown, "、")+"。请确认源数据的保留期能覆盖新回看，并让引擎配置的 "+
			"window.lookback、window.max_memory、memory.total_budget、compute.max_memory 也装得下，否则恢复后任务会停在预热阶段")
	}
}

// prerequisites 检查切换的前提一与前提二。没有已启用成员受影响时两项都不要求。
func (s *Service) prerequisites(ctx context.Context, enabledImpact bool, tasks []string) []Prerequisite {
	out := []Prerequisite{s.enginePaused(ctx, enabledImpact), s.noRunningJobs(ctx, tasks)}
	return out
}

func (s *Service) enginePaused(ctx context.Context, required bool) Prerequisite {
	p := Prerequisite{Name: "因子引擎模块已暂停且本次暂停已收敛"}
	if !required {
		// 数据库里没有已启用的成员引用它，不等于引擎已经停止使用：停用刚发生、引擎还没同步目录时，它仍按缓存的目录运行旧版本。
		// 引擎在线且目录还没同步到最新时同样要求暂停。
		if s.engineView != nil {
			if info, _, seen, err := s.engineView(ctx); err == nil && seen && info.Online && !info.CatalogInSync {
				required = true
			}
		}
	}
	if !required {
		p.OK, p.Detail = true, "没有已启用的成员引用它，引擎的目录也已同步，没有计算在用旧版本，不要求暂停引擎"
		return p
	}
	if s.engineView == nil {
		p.Detail = "没有配置引擎状态来源"
		return p
	}
	info, status, seen, err := s.engineView(ctx)
	switch {
	case err != nil:
		p.Detail = "读取引擎状态失败：" + err.Error()
	case !seen:
		p.Detail = "没有收到过引擎心跳，无法确认引擎已暂停"
	case !info.Online:
		p.Detail = "引擎离线（心跳已过期）：先让引擎上线并暂停，再切换"
	case time.Since(info.LastHeartbeatAt) > engineStatusMaxAge:
		p.Detail = fmt.Sprintf("引擎最近一次心跳是 %s 前，状态不够新", time.Since(info.LastHeartbeatAt).Round(time.Second))
	case status.Lifecycle.Stage != "paused":
		p.Detail = fmt.Sprintf("因子引擎模块处于 %q，不是暂停：先执行 moox-cli lifecycle pause --endpoint <因子引擎健康检查地址> 并等待收敛", status.Lifecycle.Stage)
	case !status.Lifecycle.Quiesced:
		p.Detail = "引擎已暂停，但本次暂停还没有收敛（在途的计算还没结束）：等待片刻再试"
	default:
		p.OK, p.Detail = true, fmt.Sprintf("引擎已暂停，收敛于 %s", status.Lifecycle.QuiescedAt.Format(time.RFC3339))
	}
	return p
}

func (s *Service) noRunningJobs(ctx context.Context, tasks []string) Prerequisite {
	p := Prerequisite{Name: "受影响任务没有已领取且未结束的补算作业"}
	var running []string
	for _, taskID := range tasks {
		jobs, err := s.db.ListRecalcJobs(ctx, store.RecalcJobFilter{TaskID: taskID, Statuses: []string{store.RecalcStatusRunning}})
		if err != nil {
			p.Detail = "读取补算作业失败：" + err.Error()
			return p
		}
		for _, job := range jobs {
			running = append(running, job.JobID+"（任务 "+taskID+"）")
		}
	}
	if len(running) > 0 {
		p.Detail = "有已领取的补算作业，先用 CancelRecalcJob 取消（引擎暂停期间不会领取新作业）：" + strings.Join(running, "、")
		return p
	}
	p.OK = true
	return p
}

// recheckEngine 等待一次比当前状态更新的心跳，确认引擎仍处于同一次暂停且已收敛。
func (s *Service) recheckEngine(ctx context.Context) error {
	if s.engineView == nil {
		return errors.New("没有配置引擎状态来源")
	}
	first, firstStatus, seen, err := s.engineView(ctx)
	if err != nil || !seen {
		return fmt.Errorf("读取引擎状态失败：%v", err)
	}
	deadline := time.Now().Add(engineRecheckTimeout)
	for time.Now().Before(deadline) {
		info, status, seen, err := s.engineView(ctx)
		if err != nil || !seen {
			return fmt.Errorf("读取引擎状态失败：%v", err)
		}
		if info.LastHeartbeatAt.After(first.LastHeartbeatAt) {
			if !status.Lifecycle.Quiesced || status.Lifecycle.Stage != "paused" || !status.Lifecycle.QuiescedAt.Equal(firstStatus.Lifecycle.QuiescedAt) {
				return errors.New("引擎在复核期间离开了暂停状态或重新暂停过")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(engineRecheckPoll):
		}
	}
	return errors.New("等不到引擎的新心跳")
}

// ---------------------------------------------------------------- 定义变更与锁

// lockUdfIfReferenced 在新旧定义文本任一方引用自定义算子时先取全局 UDF 锁（锁顺序：UDF 锁 → 定义锁 → 任务锁）。
// 绑定解析与最终的编译校验都在锁内完成，引用清单直到事务提交之前不会变化。返回的函数释放锁，没有取锁时是空操作。
func (s *Service) lockUdfIfReferenced(ctx context.Context, factorID, newSpec string) (func(), error) {
	needed := len(domain.UDFNames(newSpec)) > 0
	if !needed && factorID != "" {
		if old, err := s.db.GetFactor(ctx, factorID); err == nil {
			needed = len(domain.UDFNames(old.Spec)) > 0
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if !needed {
		return func() {}, nil
	}
	return s.locks.LockUdfContext(ctx)
}

// withUDFs 按当前启用版本解析定义引用的自定义算子，填进定义（绑定随定义走完编译、传输与持久化）。
func (s *Service) withUDFs(ctx context.Context, def domain.FactorDef) (domain.FactorDef, error) {
	names := domain.UDFNames(def.Spec)
	if len(names) == 0 {
		return def, nil
	}
	env, err := s.db.ResolveUDFs(ctx, names, nil)
	if err != nil {
		return def, err
	}
	def.UDFs = env
	return def, nil
}

// ---------------------------------------------------------------- 查询

// UdfInfo 是一个自定义算子的完整视图：名称、版本（不含 wasm）与引用它的因子定义。
type UdfInfo struct {
	Name       store.UdfName
	Versions   []store.UdfVersion
	Referenced []string
}

// ListUdfs 返回全部自定义算子。
func (s *Service) ListUdfs(ctx context.Context) ([]UdfInfo, error) {
	names, err := s.db.ListUdfs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UdfInfo, 0, len(names))
	for _, n := range names {
		info, err := s.GetUdf(ctx, n.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// GetUdf 返回一个自定义算子。
func (s *Service) GetUdf(ctx context.Context, name string) (UdfInfo, error) {
	n, versions, err := s.db.GetUdf(ctx, strings.TrimSpace(name))
	if err != nil {
		return UdfInfo{}, err
	}
	refs, err := s.db.UdfReferences(ctx, n.Name)
	if err != nil {
		return UdfInfo{}, err
	}
	return UdfInfo{Name: n, Versions: versions, Referenced: refs}, nil
}

// UdfModule 按版本哈希返回 wasm 字节（引擎拉取模块用）。
func (s *Service) UdfModule(ctx context.Context, hash string) (name string, wasm []byte, err error) {
	v, err := s.db.GetUdfVersion(ctx, strings.TrimSpace(hash), true)
	if err != nil {
		return "", nil, err
	}
	return v.Name, v.Wasm, nil
}

// UDFEnvFor 返回 spec 引用的自定义算子的解析环境：overrides 是请求级覆盖（名称 → 版本哈希），其余取当前启用版本。
func (s *Service) UDFEnvFor(ctx context.Context, spec string, overrides map[string]string) (map[string]factorexpr.UDF, error) {
	names := domain.UDFNames(spec)
	return s.db.ResolveUDFs(ctx, names, overrides)
}

// EnabledUDFEnv 返回全部已启用版本（函数参考与控制台补全用）。
func (s *Service) EnabledUDFEnv(ctx context.Context) (factorexpr.UDFs, error) {
	return s.db.EnabledUDFEnv(ctx)
}
