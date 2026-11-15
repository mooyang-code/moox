package fetchplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// planCloud 是对账器用到的云端能力：云函数的读回与写入（写入只经操作表），加上按包名取最新的可用代码包。
type planCloud interface {
	cloud.Functions
	// LatestPackage 返回空间某个包名最新的可用代码包；没有时返回 nil。
	LatestPackage(ctx context.Context, spaceID, packageName string) (*cloud.Package, error)
}

// functionTarget 是一个定时函数在某个阶段应有的计划与 cron。cron 为空表示定时器停用（退役的函数保留过渡环境、只停定时器）。
type functionTarget struct {
	Name   string
	Region string
	Plan   *marketfetch.Plan
	Cron   string
}

// planStage 是计划推进的两个阶段。
const (
	planStageTransition = "transition"
	planStageFinal      = "final"
)

// planOpKey 是计划推进的操作用途：带计划号与阶段，只有用途相同的成功结果才算这一阶段的确认。
func planOpKey(planID int64, stage string) string {
	return fmt.Sprintf("plan=%d|stage=%s", planID, stage)
}

// stageTarget 是计划推进传入阶段时的期望：过渡阶段用过渡计划与过渡 cron；最终阶段用最终计划与最终 cron，
// 退役的函数（没有最终计划）保持过渡计划、cron 为空。
func stageTarget(content *FetchPlanContent, function *PlanFunctionChange, stage string) functionTarget {
	target := functionTarget{Name: function.Name, Region: function.Region, Plan: content.TransitionalPlan(function), Cron: content.TransitionalCron(function)}
	if stage == planStageFinal {
		if final := content.FinalPlan(function); final != nil {
			target.Plan, target.Cron = final, content.FinalCron(function)
		} else {
			target.Cron = ""
		}
	}
	return target
}

// confirmedTargets 按已经确认的状态算出每个定时函数应有的内容（读回修复、恢复启用、出口去重后重建、升级失败退回 Ensure 用）：
// 未生效计划的 Finalized 里有它就用最终阶段；Transitioned 里有它就用过渡阶段；都没有就用最近一份已生效的计划。
// 已生效计划里也没有的函数（还没被推进到）不在结果里，等计划推进。
func (r *PlanReconciler) confirmedTargets(ctx context.Context) (map[string]functionTarget, error) {
	targets := map[string]functionTarget{}
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil {
		return nil, err
	}
	if applied != nil {
		content, err := decodeFetchPlanContent(applied.Plan)
		if err != nil {
			return nil, err
		}
		functions, _ := content.Applied()
		for name, function := range scfFunctions(functions) {
			targets[name] = functionTarget{Name: name, Region: function.Region, Plan: function.Plan, Cron: function.Cron}
		}
	}
	open, err := r.Store.Open(ctx, r.SpaceID)
	if err != nil {
		return nil, err
	}
	if open != nil {
		content, err := decodeFetchPlanContent(open.Plan)
		if err != nil {
			return nil, err
		}
		progress, err := decodeFetchPlanProgress(open.Progress)
		if err != nil {
			return nil, err
		}
		for name, function := range content.Functions {
			if !function.IsSCF() {
				continue
			}
			switch {
			case progress.Finalized[name]:
				targets[name] = stageTarget(content, function, planStageFinal)
			case progress.Transitioned[name]:
				targets[name] = stageTarget(content, function, planStageTransition)
			}
		}
	}
	return targets, nil
}

// functionRef 返回定时函数在云上的位置。
func (r *PlanReconciler) functionRef(name, region string) cloud.FunctionRef {
	return cloud.FunctionRef{AccountID: r.Deploy.CloudAccountID, Region: region, Namespace: r.Deploy.NamespaceFor(name), Name: name}
}

// desiredSpec 是一个定时函数期望内容的唯一出处：环境（基础项加受管项）、内存、超时、VPC、定时器。
// 代码包不在这里定：操作开始时现取最新的可用包（见 withPackage），排队期间上传的新包也能用上。
func (r *PlanReconciler) desiredSpec(target functionTarget) (cloud.FunctionSpec, error) {
	network, ok := r.Deploy.Networks[target.Region]
	if !ok || network.VPCID == "" || network.SubnetID == "" {
		return cloud.FunctionSpec{}, fmt.Errorf("region %s has no VPC and subnet configured", target.Region)
	}
	managed, err := r.functionEnvironment(target.Region, target.Plan)
	if err != nil {
		return cloud.FunctionSpec{}, err
	}
	environment := make(map[string]string, len(managed)+len(r.Deploy.BaseEnvironment))
	for key, value := range r.Deploy.BaseEnvironment {
		environment[key] = value
	}
	for key, value := range managed {
		environment[key] = value
	}
	return cloud.FunctionSpec{
		Ref: r.functionRef(target.Name, target.Region), Environment: environment, MemoryMB: int64(r.memoryMB()),
		TimeoutS: tencent.CollectorTimerTimeoutSeconds, VPCID: network.VPCID, SubnetID: network.SubnetID,
		Timer: &cloud.Timer{Cron: target.Cron, Enabled: target.Cron != ""},
	}, nil
}

func (r *PlanReconciler) memoryMB() int {
	if r.Deploy.MemoryMB > 0 {
		return r.Deploy.MemoryMB
	}
	return 64
}

// withPackage 在操作开始时取最新的可用代码包填进期望内容，并记在操作上。
func (r *PlanReconciler) withPackage(ctx context.Context, op *functionOp, spec cloud.FunctionSpec) (cloud.FunctionSpec, error) {
	pkg, err := r.latestPackage(ctx)
	if err != nil {
		return spec, err
	}
	spec.Package, op.packageID = pkg, pkg.PackageID
	return spec, nil
}

// pausingHook 返回给 Ensure 的暂停判断：空间正在暂停时把这次操作记成“定时器被改成停用”，结果不算本阶段的确认。
func (r *PlanReconciler) pausingHook(op *functionOp) func() bool {
	return func() bool {
		if r.pausing.Load() {
			op.timerSuppressed.Store(true)
			return true
		}
		return false
	}
}

// startEnsure 起一个“按期望内容 Ensure”的操作（定时函数，可能打开定时器）。
func (r *PlanReconciler) startEnsure(key string, spec cloud.FunctionSpec) (*functionOp, error) {
	ref := spec.Ref
	return r.ops().start(refKey(ref), key, true, func(ctx context.Context, op *functionOp) error {
		spec, err := r.withPackage(ctx, op, spec)
		if err != nil {
			return err
		}
		spec.Pausing = r.pausingHook(op)
		if err := r.Cloud.Ensure(ctx, spec); err != nil {
			return err
		}
		r.noteWritten(ref, op.packageID)
		return nil
	})
}

// startDisableTimer 起一个“只停定时器”的操作：退役、暂停、计划外的函数都走它，不走 Ensure（函数卡在失败状态时也能停）。
// 函数已经不在云上时算成功。
func (r *PlanReconciler) startDisableTimer(ref cloud.FunctionRef, key string) (*functionOp, error) {
	return r.ops().start(refKey(ref), key, false, func(ctx context.Context, op *functionOp) error {
		if err := r.Cloud.SetTimer(ctx, ref, cloud.Timer{}, ""); err != nil && !errors.Is(err, cloud.ErrFunctionNotFound) {
			return err
		}
		return nil
	})
}

// startUpgrade 起一个“只换代码”的操作：环境取云上读回的值，不动定时器。UpgradeCode 失败而函数处于失败状态
// （UpdateFailed、CreateFailed）时退回 Ensure（只有 Ensure 会重做更新、删掉重建）；fallback 为空表示没有可退回的期望内容。
func (r *PlanReconciler) startUpgrade(ref cloud.FunctionRef, fallback *cloud.FunctionSpec) (*functionOp, error) {
	return r.ops().start(refKey(ref), opKeyUpgrade, fallback != nil && fallback.Timer != nil, func(ctx context.Context, op *functionOp) error {
		pkg, err := r.latestPackage(ctx)
		if err != nil {
			return err
		}
		op.packageID = pkg.PackageID
		err = r.Cloud.UpgradeCode(ctx, ref, pkg)
		if err == nil {
			r.noteWritten(ref, pkg.PackageID)
			return nil
		}
		if fallback == nil || ctx.Err() != nil || errors.Is(err, cloud.ErrFunctionNotFound) {
			return err
		}
		state, readErr := r.Cloud.DescribeCode(ctx, ref)
		if readErr != nil || !state.Exists || !strings.Contains(strings.ToLower(state.Status), "failed") {
			return err
		}
		spec := *fallback
		spec.Package = pkg
		spec.Pausing = r.pausingHook(op)
		if ensureErr := r.Cloud.Ensure(ctx, spec); ensureErr != nil {
			return fmt.Errorf("升级失败（%v），退回 Ensure 也失败: %w", err, ensureErr)
		}
		r.noteWritten(ref, pkg.PackageID)
		return nil
	})
}

// roundWait 是一轮里起完操作后最多等多久：本轮剩余时间减 5 秒；没有截止时间时最多等 opWaitMax。
func (r *PlanReconciler) roundWait(ctx context.Context) time.Duration {
	limit := r.opWaitMax
	if limit <= 0 {
		limit = 50 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if room := time.Until(deadline) - 5*time.Second; room < limit {
			limit = room
		}
	}
	return limit
}

// opFailure 把操作的错误归到失败原因：配额超限报 quota，没有代码包报 package，其余报 cloud。
func (r *PlanReconciler) opFailure(err error) error {
	switch {
	case errors.Is(err, cloud.ErrQuota):
		return r.fail("quota", err)
	case errors.Is(err, errNoCodePackage):
		return r.fail("package", err)
	default:
		return r.fail("cloud", err)
	}
}

// ----- 读回缓存 -----

// FunctionReadback 是一个函数最近一次的云上读回（只在内存里，重启即空）。环境已经去掉密钥项。
type FunctionReadback struct {
	cloud.FunctionState
	// CodeOnly 表示这次只读了代码包（DescribeCode），没读定时器。
	CodeOnly bool
	// Error 是这次没读到的原因。
	Error string
}

func (r *PlanReconciler) storeReadback(ref cloud.FunctionRef, readback FunctionReadback) {
	name := refKey(ref)
	r.readMu.Lock()
	defer r.readMu.Unlock()
	if r.readbacks == nil {
		r.readbacks = map[string]FunctionReadback{}
	}
	if readback.CodeOnly {
		// 只读代码包时保留上一次完整读回里的定时器。
		if previous, ok := r.readbacks[name]; ok && !previous.CodeOnly && previous.Timer != nil {
			readback.Timer, readback.CodeOnly = previous.Timer, false
		}
	}
	r.readbacks[name] = readback
}

// noteWritten 在操作成功写入代码包之后更新读回缓存里的代码包，两分钟一次的升级检查据此跳过刚升级过的函数。
func (r *PlanReconciler) noteWritten(ref cloud.FunctionRef, packageID string) {
	name := refKey(ref)
	r.readMu.Lock()
	defer r.readMu.Unlock()
	if readback, ok := r.readbacks[name]; ok && packageID != "" {
		readback.Exists, readback.PackageID = true, packageID
		r.readbacks[name] = readback
	}
}

// TimerReadback 返回定时函数（按函数名与地域，命名空间按部署参数算出）最近一次的读回；没有读回时第二个返回值为 false。
func (r *PlanReconciler) TimerReadback(name, region string) (FunctionReadback, bool) {
	return r.cachedReadback(r.functionRef(name, region))
}

func (r *PlanReconciler) cachedReadback(ref cloud.FunctionRef) (FunctionReadback, bool) {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	readback, ok := r.readbacks[refKey(ref)]
	return readback, ok
}

// describe 完整读回一个函数并记进缓存。
func (r *PlanReconciler) describe(ctx context.Context, ref cloud.FunctionRef) (cloud.FunctionState, error) {
	state, err := r.Cloud.Describe(ctx, ref)
	if err != nil {
		r.storeReadback(ref, FunctionReadback{FunctionState: cloud.FunctionState{ReadAt: r.now()}, Error: err.Error()})
		return state, err
	}
	r.storeReadback(ref, FunctionReadback{FunctionState: state})
	return state, nil
}

// TimerMatches 判断读回的定时器是不是期望的样子：cron 为空时不能启用；否则 cron、开关、可用状态、Qualifier、Message 都要对。
// 对账器的读回修复与运行状态页的“云上状态”一列用同一个口径。
func TimerMatches(timer *cloud.TimerState, cron string) bool {
	if cron == "" {
		return timer == nil || !timer.Enabled
	}
	return timer != nil && timer.Enabled && timer.Cron == cron && strings.EqualFold(timer.AvailableStatus, "available") &&
		timer.Qualifier == timerTriggerQualifier && timer.Message == timerTriggerMessage
}

// managedDrift 判断读回的受管环境（分配哈希、Access 入口与调用方身份）与期望不一致。
func managedDrift(environment, want map[string]string) bool {
	for _, key := range []string{"MOOX_MARKET_FETCH_ASSIGNMENT_HASH", "MOOX_ACCESS_ADDRESS", "MOOX_ACCESS_ID", "MOOX_CALLER_KEY_ID"} {
		if environment[key] != want[key] {
			return true
		}
	}
	return false
}

// ----- 计划外的同前缀函数 -----

// ownsFunctionName 判断函数名符合本空间 PlanFunctionName 的完整格式（前缀、本空间配置里的某个市场、该市场的某个地域、三位以上序号），
// 并且不属于其他已配置空间按同样规则能生成的名字（空间 crypto 与 crypto-x 的前缀会互相误中）。
func (r *PlanReconciler) ownsFunctionName(name string) bool {
	if !matchesFleetName(r.SpaceID, r.Options.Markets, name) {
		return false
	}
	for space, markets := range r.OtherSpaces {
		if !strings.EqualFold(space, r.SpaceID) && matchesFleetName(space, markets, name) {
			return false
		}
	}
	return true
}

// ownsBackfillName 判断函数名是本空间配置里某个市场、某个地域的补采函数名，并且不是其他已配置空间的补采函数名。
func (r *PlanReconciler) ownsBackfillName(name string) bool {
	if !matchesBackfillName(r.SpaceID, r.Options.Markets, name) {
		return false
	}
	for space, markets := range r.OtherSpaces {
		if !strings.EqualFold(space, r.SpaceID) && matchesBackfillName(space, markets, name) {
			return false
		}
	}
	return true
}

func matchesBackfillName(spaceID string, markets map[string][]string, name string) bool {
	for market, regions := range markets {
		for _, region := range regions {
			if BackfillFunctionName(spaceID, market, region) == name {
				return true
			}
		}
	}
	return false
}

// matchesFleetName 判断 name 是不是 PlanFunctionName(spaceID, 某个市场, 该市场的某个地域, 某个序号)。
func matchesFleetName(spaceID string, markets map[string][]string, name string) bool {
	cut := strings.LastIndex(name, "-")
	if cut < 0 || len(name)-cut-1 < 3 {
		return false
	}
	index, err := strconv.Atoi(name[cut+1:])
	if err != nil || index < 0 {
		return false
	}
	for market, regions := range markets {
		for _, region := range regions {
			if PlanFunctionName(spaceID, market, region, index) == name {
				return true
			}
		}
	}
	return false
}

// planRegions 返回本空间配置涉及的全部地域。
func (r *PlanReconciler) planRegions() []string {
	seen := map[string]struct{}{}
	var regions []string
	for _, list := range r.Options.Markets {
		for _, region := range list {
			if _, ok := seen[region]; !ok {
				seen[region] = struct{}{}
				regions = append(regions, region)
			}
		}
	}
	sort.Strings(regions)
	return regions
}

// plannedNames 返回本空间计划里应有的全部云函数（键是 refKey：地域/命名空间/函数名）：已生效计划、未生效计划里的定时函数，
// 以及各市场的补采函数。同名函数在别的命名空间里的副本（例如命名空间配置改过之后留下的）不算计划内。
func (r *PlanReconciler) plannedNames(ctx context.Context) (map[string]cloud.FunctionRef, error) {
	names := map[string]cloud.FunctionRef{}
	for _, load := range []func(context.Context, string) (*store.FetchPlanRecord, error){r.Store.LatestApplied, r.Store.Open} {
		record, err := load(ctx, r.SpaceID)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		content, err := decodeFetchPlanContent(record.Plan)
		if err != nil {
			return nil, err
		}
		for name, function := range content.Functions {
			if function.IsSCF() {
				ref := r.functionRef(name, function.Region)
				names[refKey(ref)] = ref
			}
		}
	}
	for market := range r.Options.Markets {
		if region, ok := r.BackfillRegion(market); ok {
			ref := r.backfillRef(market, region)
			names[refKey(ref)] = ref
		}
	}
	return names, nil
}

// scanUnplanned 按采集函数与补采函数两个前缀列出本空间各地域（全部命名空间，含分片命名空间）的函数，返回符合本空间命名格式、
// 又不在计划里的那些（包括命名空间或补采地域改过之后留下的旧副本）。
func (r *PlanReconciler) scanUnplanned(ctx context.Context, planned map[string]cloud.FunctionRef) ([]cloud.FunctionRef, error) {
	if strings.TrimSpace(r.Deploy.CloudAccountID) == "" {
		return nil, nil
	}
	var out []cloud.FunctionRef
	for _, region := range r.planRegions() {
		// 补采函数单独按它的前缀列：命名空间或市场的第一个地域改过之后留下的旧副本也要看得见、删得到。
		for _, prefix := range []string{r.fleetPrefix(), r.backfillPrefix()} {
			refs, err := r.Cloud.List(ctx, r.Deploy.CloudAccountID, region, prefix)
			if err != nil {
				return nil, err
			}
			for _, ref := range refs {
				if _, ok := planned[refKey(ref)]; ok || !r.ownsFunctionName(ref.Name) && !r.ownsBackfillName(ref.Name) {
					continue
				}
				out = append(out, ref)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	r.readMu.Lock()
	r.unplanned = append([]cloud.FunctionRef(nil), out...)
	r.readMu.Unlock()
	return out, nil
}

// UnplannedFunctions 返回最近一次读回时找到的计划外函数（只读内存，不发起读回；重启后到下一次读回之前为空）。
func (r *PlanReconciler) UnplannedFunctions() []cloud.FunctionRef {
	r.readMu.Lock()
	defer r.readMu.Unlock()
	return append([]cloud.FunctionRef(nil), r.unplanned...)
}

// ----- 给 RPC 的视图与操作 -----

// CloudFunctionView 是 ListCloudFunctions 的一项：计划里应有的内容、云上读回的状态、是否在计划外。不含任何密钥。
type CloudFunctionView struct {
	Name      string
	Region    string
	Namespace string
	// Kind 是 timer（定时采集函数）或 backfill（补采函数）。
	Kind string
	// Unplanned 表示符合本空间命名格式、但不在计划里（定时器已被停用，等人工删除）。
	Unplanned bool
	// ExpectedCron 为空表示定时器应当停用；ExpectedAssignmentHash 是期望的分配哈希；ExpectedPackageID 是最新的可用代码包。
	ExpectedCron           string
	ExpectedAssignmentHash string
	ExpectedPackageID      string
	// Readback 是这次的读回；Operation 是正在跑的操作用途（没有为空）。
	Readback  FunctionReadback
	Operation string
	// TimerMatches、PackageLatest、EnvironmentMatches 是读回与期望的比对结果。
	TimerMatches       bool
	PackageLatest      bool
	EnvironmentMatches bool
}

// ListCloudFunctions 读回本空间计划里的全部函数（定时函数与补采函数）和计划外的同前缀函数，返回期望与实际的对照。
// 读回结果同时刷新内存里的缓存（运行状态页的“云上状态”一列读它）。
func (r *PlanReconciler) ListCloudFunctions(ctx context.Context) ([]CloudFunctionView, error) {
	r.refreshPackageIfAvailable(ctx)
	pkg := r.packageID()
	expected, err := r.viewTargets(ctx)
	if err != nil {
		return nil, err
	}
	planned, err := r.plannedNames(ctx)
	if err != nil {
		return nil, err
	}
	running := r.ops().runningKeys()
	var views []CloudFunctionView
	for _, key := range sortedKeys(planned) {
		ref := planned[key]
		name := ref.Name
		view := CloudFunctionView{Name: name, Region: ref.Region, Namespace: ref.Namespace, Kind: "backfill", ExpectedPackageID: pkg, Operation: running[key]}
		target, timer := expected[name]
		if timer {
			view.Kind, view.ExpectedCron = "timer", target.Cron
			if environment, envErr := r.functionEnvironment(target.Region, target.Plan); envErr == nil {
				view.ExpectedAssignmentHash = environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
			}
		}
		state, readErr := r.describe(ctx, ref)
		view.Readback, _ = r.cachedReadback(ref)
		if readErr == nil && state.Exists {
			view.PackageLatest = pkg == "" || state.PackageID == pkg
			if timer {
				view.TimerMatches = TimerMatches(state.Timer, target.Cron)
				view.EnvironmentMatches = state.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] == view.ExpectedAssignmentHash
			} else {
				view.TimerMatches, view.EnvironmentMatches = state.Timer == nil || !state.Timer.Enabled, true
			}
		}
		views = append(views, view)
	}
	unplanned, err := r.scanUnplanned(ctx, planned)
	if err != nil {
		return nil, err
	}
	for _, ref := range unplanned {
		kind := "timer"
		if strings.HasPrefix(ref.Name, r.backfillPrefix()) {
			kind = "backfill"
		}
		view := CloudFunctionView{Name: ref.Name, Region: ref.Region, Namespace: ref.Namespace, Kind: kind, Unplanned: true, ExpectedPackageID: pkg, Operation: running[refKey(ref)]}
		if state, readErr := r.describe(ctx, ref); readErr == nil && state.Exists {
			view.TimerMatches = state.Timer == nil || !state.Timer.Enabled
			view.PackageLatest = pkg == "" || state.PackageID == pkg
		}
		view.Readback, _ = r.cachedReadback(ref)
		views = append(views, view)
	}
	return views, nil
}

// viewTargets 是状态展示用的期望：按已确认的状态算，未生效计划里还没确认的函数按它的过渡计划。
func (r *PlanReconciler) viewTargets(ctx context.Context) (map[string]functionTarget, error) {
	targets, err := r.confirmedTargets(ctx)
	if err != nil {
		return nil, err
	}
	open, err := r.Store.Open(ctx, r.SpaceID)
	if err != nil || open == nil {
		return targets, err
	}
	content, err := decodeFetchPlanContent(open.Plan)
	if err != nil {
		return nil, err
	}
	for name, function := range content.Functions {
		if _, ok := targets[name]; !ok && function.IsSCF() {
			targets[name] = stageTarget(content, function, planStageTransition)
		}
	}
	return targets, nil
}

// FunctionDeletion 是 DeleteCloudFunctions 里一个函数的结果。
type FunctionDeletion struct {
	Name      string
	Region    string
	Namespace string
	// Done 为 true 表示已从云上删掉；为 false 时 Error 说明原因（还在删时 Error 为空，稍后用 ListCloudFunctions 查看）。
	Done  bool
	Error string
}

// belongs 判断云上一个函数是不是本空间的：在计划里（按地域、命名空间、函数名），或者函数名符合本空间的命名格式
// （ownsFunctionName：排除其他已配置空间按同样规则能生成的名字），或者是本空间某个市场的补采函数（ownsBackfillName）。
func (r *PlanReconciler) belongs(ref cloud.FunctionRef, planned map[string]cloud.FunctionRef) bool {
	if _, ok := planned[refKey(ref)]; ok || r.ownsFunctionName(ref.Name) {
		return true
	}
	return r.ownsBackfillName(ref.Name)
}

// DeleteCloudFunctionsRequest 是 DeleteCloudFunctions 的条件：Names 与 Prefix 至少给一个；Namespace、Region 非空时只删这个命名空间、
// 这个地域里的（同名函数在旧命名空间的副本与在用的函数分得开）；DryRun 为 true 时只返回会删哪些，不删。
type DeleteCloudFunctionsRequest struct {
	Names     []string
	Prefix    string
	Namespace string
	Region    string
	DryRun    bool
}

// DeleteCloudFunctions 按名字或前缀删除本空间的云函数（经操作表）。计划里的函数删掉后，下一轮对账立即读回并重建。
// prefix 必须以本空间的函数名前缀（moox-fetch-plan-<空间>- 或补采函数前缀）开头。预览（DryRun）与实际删除用同一段匹配，
// 预览列出的就是实际会删的。只删本空间的函数（见 belongs）：前缀相同的别的空间（例如 crypto 与 crypto-x）的函数不碰，
// 按名字点到别的空间的函数直接拒绝。
func (r *PlanReconciler) DeleteCloudFunctions(ctx context.Context, req DeleteCloudFunctionsRequest) ([]FunctionDeletion, error) {
	prefix := strings.TrimSpace(req.Prefix)
	namespace, region := strings.TrimSpace(req.Namespace), strings.ToLower(strings.TrimSpace(req.Region))
	wanted := map[string]bool{}
	for _, name := range req.Names {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}
	if len(wanted) == 0 && prefix == "" {
		return nil, errors.New("要写明函数名或前缀")
	}
	if prefix != "" && !strings.HasPrefix(prefix, r.fleetPrefix()) && !strings.HasPrefix(prefix, r.backfillPrefix()) {
		return nil, fmt.Errorf("前缀 %q 不是本空间的函数名前缀（%s 或 %s）", prefix, r.fleetPrefix(), r.backfillPrefix())
	}
	regions := r.planRegions()
	if region != "" {
		if !slices.Contains(regions, region) {
			return nil, fmt.Errorf("地域 %q 不在空间 %s 的 scf_plan 配置里（%s）", region, r.SpaceID, strings.Join(regions, "、"))
		}
		regions = []string{region}
	}
	planned, err := r.plannedNames(ctx)
	if err != nil {
		return nil, err
	}
	var results []FunctionDeletion
	found := map[string]bool{}
	var refs []cloud.FunctionRef
	for _, region := range regions {
		listPrefix := prefix
		if listPrefix == "" {
			listPrefix = "moox-fetch"
		}
		listed, err := r.Cloud.List(ctx, r.Deploy.CloudAccountID, region, listPrefix)
		if err != nil {
			return nil, err
		}
		for _, ref := range listed {
			if prefix == "" && !wanted[ref.Name] || namespace != "" && ref.Namespace != namespace {
				continue
			}
			found[ref.Name] = true
			if !r.belongs(ref, planned) {
				if prefix == "" {
					results = append(results, FunctionDeletion{Name: ref.Name, Region: ref.Region, Namespace: ref.Namespace,
						Error: fmt.Sprintf("函数 %s 不属于空间 %s（不在它的计划里，也不符合它的命名格式），不删；要删就用它所属空间的命令", ref.Name, r.SpaceID)})
				}
				continue
			}
			refs = append(refs, ref)
		}
	}
	notFound := func() {
		for _, name := range sortedKeys(wanted) {
			if !found[name] {
				results = append(results, FunctionDeletion{Name: name, Error: "云上没有符合条件的这个函数"})
			}
		}
	}
	if req.DryRun {
		sort.Slice(refs, func(i, j int) bool { return refKey(refs[i]) < refKey(refs[j]) })
		for _, ref := range refs {
			results = append(results, FunctionDeletion{Name: ref.Name, Region: ref.Region, Namespace: ref.Namespace})
		}
		notFound()
		return results, nil
	}
	sort.Slice(refs, func(i, j int) bool { return refKey(refs[i]) < refKey(refs[j]) })
	type pending struct {
		index int
		op    *functionOp
	}
	var started []pending
	for _, ref := range refs {
		ref := ref
		op, err := r.ops().start(refKey(ref), opKeyDelete, false, func(ctx context.Context, _ *functionOp) error {
			if err := r.Cloud.Delete(ctx, ref); err != nil {
				return err
			}
			// 下一轮对账立即读回：计划里的函数随即重建，不用等一小时；补采函数的就绪缓存也作废，下一次补采重建。
			r.verifyRequested.Store(true)
			r.readMu.Lock()
			delete(r.readbacks, refKey(ref))
			r.unplanned = slices.DeleteFunc(r.unplanned, func(item cloud.FunctionRef) bool { return refKey(item) == refKey(ref) })
			if r.deletedFunctions == nil {
				r.deletedFunctions = map[string]bool{}
			}
			r.deletedFunctions[ref.Name] = true
			r.readMu.Unlock()
			r.backfillMu.Lock()
			delete(r.backfillReady, ref.Name)
			r.backfillMu.Unlock()
			return nil
		})
		result := FunctionDeletion{Name: ref.Name, Region: ref.Region, Namespace: ref.Namespace}
		if err != nil {
			result.Error = err.Error()
		} else {
			started = append(started, pending{index: len(results), op: op})
		}
		results = append(results, result)
	}
	ops := make([]*functionOp, 0, len(started))
	for _, item := range started {
		ops = append(ops, item.op)
	}
	r.ops().wait(ctx, ops, r.roundWait(ctx))
	for _, item := range started {
		if done := r.ops().take(item.op.name, opKeyDelete); done != nil {
			if done.err != nil {
				results[item.index].Error = done.err.Error()
			} else {
				results[item.index].Done = true
			}
		}
	}
	notFound()
	return results, nil
}

// EgressProbe 是一个函数的出口 IP 探测结果。
type EgressProbe struct {
	Name  string
	IP    string
	Error string
}

// ProbeFunctionEgress 探测本空间已生效计划里各定时函数（names 非空时只探测这些）此刻的公网出口 IP。只调用函数，不改云上状态。
func (r *PlanReconciler) ProbeFunctionEgress(ctx context.Context, names []string) ([]EgressProbe, error) {
	targets, err := r.confirmedTargets(ctx)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[strings.TrimSpace(name)] = true
	}
	var out []EgressProbe
	for _, name := range sortedKeys(targets) {
		if len(wanted) > 0 && !wanted[name] {
			continue
		}
		probe := EgressProbe{Name: name}
		if ip, err := r.ProbeEgressIP(ctx, r.SpaceID, name); err != nil {
			probe.Error = err.Error()
		} else {
			probe.IP = ip
		}
		out = append(out, probe)
	}
	return out, nil
}

// ProbeEgressIP 实现 egressbalance.Prober：同步调用函数的 egress_probe，返回它此刻的公网出口 IP。
func (r *PlanReconciler) ProbeEgressIP(ctx context.Context, _ string, name string) (string, error) {
	region := ""
	if targets, err := r.confirmedTargets(ctx); err == nil {
		region = targets[name].Region
	}
	if region == "" {
		return "", fmt.Errorf("函数 %s 不在已生效的计划里", name)
	}
	result, err := r.invoke(ctx, r.functionRef(name, region), map[string]any{"action": "egress_probe", "data": map[string]any{"node_id": name}})
	if err != nil {
		return "", err
	}
	data, _ := result.Result["data"].(map[string]any)
	if data == nil {
		if raw, ok := result.Result["raw"].(string); ok {
			var decoded map[string]any
			if json.Unmarshal([]byte(raw), &decoded) == nil {
				data, _ = decoded["data"].(map[string]any)
			}
		}
	}
	details, _ := data["details"].(map[string]any)
	ip, _ := details["public_ip"].(string)
	if strings.TrimSpace(ip) == "" {
		return "", fmt.Errorf("egress probe of %s returned no public ip", name)
	}
	return strings.TrimSpace(ip), nil
}

// InvokeBackfill 同步调用补采函数并等它返回（补采的 klinecheck.Invoker）。
func (r *PlanReconciler) InvokeBackfill(ctx context.Context, name string, event map[string]any) (marketfetch.InvocationResult, error) {
	r.readMu.Lock()
	ref, ok := r.backfillRefs[name]
	r.readMu.Unlock()
	if !ok {
		return marketfetch.InvocationResult{}, fmt.Errorf("补采函数 %s 还没有就绪", name)
	}
	return r.invoke(ctx, ref, event)
}

// invoke 同步调用函数，把函数的返回值解成对象（不是 JSON 对象时放在 raw 里）。
func (r *PlanReconciler) invoke(ctx context.Context, ref cloud.FunctionRef, event map[string]any) (marketfetch.InvocationResult, error) {
	result, err := r.Cloud.Invoke(ctx, ref, event, true)
	if err != nil {
		return marketfetch.InvocationResult{}, err
	}
	out := marketfetch.InvocationResult{RequestID: result.RequestID, Code: result.Code, Message: result.Message, DurationMS: result.Duration, BillDuration: result.BillDuration}
	if result.Code != 0 {
		return out, fmt.Errorf("SCF invocation failed: %s", result.Message)
	}
	out.Result = map[string]any{}
	if raw := strings.TrimSpace(result.ReturnResult); raw != "" {
		if json.Unmarshal([]byte(raw), &out.Result) != nil {
			out.Result = map[string]any{"raw": raw}
		}
	}
	return out, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
