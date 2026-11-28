package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorudf"
	"gorm.io/gorm"
)

// UDFVerifier 是发布时复核模块用的运行时与测试限制。它与引擎的运行时、试算的运行时分开：发布测试串行执行
// （持有全局 UDF 锁），有自己的时限与内存上限。
type UDFVerifier struct {
	Runtime     *factorudf.Runtime
	Limits      factorudf.Limits
	Conformance factorudf.ConformanceOptions
}

// WithUDFVerifier 配置发布复核。
func WithUDFVerifier(v UDFVerifier) Option { return func(s *Service) { s.verifier = &v } }

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
	if pkg.Manifest.Source.Dirty {
		return nil, fmt.Errorf("%w: 包是在工作区有未提交改动时构建的，产物与记录的提交对不上：提交之后重新 build", ErrInvalidUdf)
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
	Name    string
	OldHash string
	NewHash string
	DryRun  bool
	Applied bool
	Impacts []UdfImpact
	Tasks   []string
	// Instances 是受影响任务关联的策略实例：只有声明了 factor_params 的实例会关联任务，只读任务的实例 factor-mgr 不知道，不在其中。
	Instances     []string
	Prerequisites []Prerequisite
	Notes         []string
}

// ActivateUdf 切换名称启用的版本：影响清单（引用它的定义 → 成员 → 因子任务 → 策略实例）、与 UpdateFactor 相同的两项前提
// （没有已启用的成员引用它、受影响任务没有已经开始的补算作业，见 definitionPrerequisites）、用新版本重新编译并重做成员校验、
// 报告新旧 definition_hash 与回看。dryRun 只返回影响清单与各前提的检查结果。
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
	// 在别处启用，“没有已启用的成员引用它”才不会被并发的启用推翻。补算作业只算已启用的成员，所以成员都停用后，
	// 新领取的作业也用不到这个定义；已经开始的作业由第二项前提挡住。
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
	var impactErrs []string
	for _, factorID := range refs {
		impact, err := s.impactOf(ctx, factorID, name, hash, usages[factorID], taskSet)
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

	var referencing []domain.FactorUsage
	for _, factorID := range refs {
		referencing = append(referencing, usages[factorID]...)
	}
	result.Prerequisites = s.definitionPrerequisites(ctx, referencing, result.Tasks)
	recompiled := Prerequisite{Name: "用新版本重新编译并重做成员校验", OK: len(impactErrs) == 0}
	if !recompiled.OK {
		recompiled.Detail = strings.Join(impactErrs, "；")
	}
	result.Prerequisites = append(result.Prerequisites, recompiled)
	if dryRun {
		return result, nil
	}
	if err := checkPrerequisites(result.Prerequisites); err != nil {
		return result, err
	}
	if err := s.db.SetActiveUdf(ctx, name, hash); err != nil {
		return result, err
	}
	result.Applied = true
	return result, nil
}

// impactOf 用新版本重新编译一个引用它的定义，并对每个引用它的成员重做成员校验（规则同 UpdateFactor）。
func (s *Service) impactOf(ctx context.Context, factorID, name, hash string, usages []domain.FactorUsage,
	tasks map[string]domain.FactorTask) (UdfImpact, error) {
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

// ---------------------------------------------------------------- 定义变更与锁

// definitionPrerequisites 是改变一个定义实际算法的两项前提，切换自定义算子版本（ActivateUdf）与改写定义（UpdateFactor）共用：
//   - 没有已启用的成员引用它：停用成员后实时计算不再用它，不需要暂停引擎；
//   - 受影响任务没有已经开始的补算作业：作业的定义在领取时下发，已经开始的作业归还或引擎重启后再领取会拿到新定义，
//     前半段旧算法、后半段新算法一起写进正式结果。“已经开始”指正在运行（引擎手里已有旧定义），或进度已越过起点（领取过又归还）。
func (s *Service) definitionPrerequisites(ctx context.Context, usages []domain.FactorUsage, tasks []string) []Prerequisite {
	return []Prerequisite{noEnabledMembers(usages), s.noStartedJobs(ctx, tasks)}
}

func noEnabledMembers(usages []domain.FactorUsage) Prerequisite {
	p := Prerequisite{Name: "没有已启用的成员引用它"}
	var enabled []string
	for _, usage := range usages {
		if usage.Status == domain.MemberStatusEnabled {
			enabled = append(enabled, usage.TaskID+"/"+usage.MemberKey)
		}
	}
	if len(enabled) > 0 {
		sort.Strings(enabled)
		p.Detail = "先停用这些成员（moox-cli factor member disable）：" + strings.Join(enabled, "、")
		return p
	}
	p.OK = true
	return p
}

func (s *Service) noStartedJobs(ctx context.Context, tasks []string) Prerequisite {
	p := Prerequisite{Name: "受影响任务没有已经开始的补算作业"}
	var started []string
	for _, taskID := range tasks {
		jobs, err := s.db.ListRecalcJobs(ctx, store.RecalcJobFilter{TaskID: taskID, Statuses: []string{store.RecalcStatusQueued, store.RecalcStatusRunning}})
		if err != nil {
			p.Detail = "读取补算作业失败：" + err.Error()
			return p
		}
		for _, job := range jobs {
			if job.Status == store.RecalcStatusRunning || job.ProgressTime > job.StartTime {
				started = append(started, job.JobID+"（任务 "+taskID+"）")
			}
		}
	}
	if len(started) > 0 {
		p.Detail = "先用 CancelRecalcJob 取消这些作业（改完再重新提交补算）：" + strings.Join(started, "、")
		return p
	}
	p.OK = true
	return p
}

// checkPrerequisites 返回第一项不满足的前提对应的冲突错误。
func checkPrerequisites(list []Prerequisite) error {
	for _, p := range list {
		if !p.OK {
			return fmt.Errorf("%w: 前提不满足：%s：%s", store.ErrConflict, p.Name, p.Detail)
		}
	}
	return nil
}

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
