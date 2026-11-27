package fetchplan

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// verifyUntouched 在有未生效计划时，对不受该计划影响的已生效函数做云端校验。
func (r *PlanReconciler) verifyUntouched(ctx context.Context, open *store.FetchPlanRecord) error {
	if !r.verifyRequested.Load() && !r.lastVerified.IsZero() && r.now().Sub(r.lastVerified) < planVerifyInterval {
		return nil
	}
	applied, err := r.Store.LatestApplied(ctx, r.SpaceID)
	if err != nil || applied == nil {
		return err
	}
	appliedContent, err := decodeFetchPlanContent(applied.Plan)
	if err != nil {
		return err
	}
	openContent, err := decodeFetchPlanContent(open.Plan)
	if err != nil {
		return err
	}
	previous, _ := appliedContent.Applied()
	previous = scfFunctions(previous)
	untouched := map[string]*PlanFunction{}
	for name, function := range previous {
		change := openContent.Functions[name]
		if change != nil && (change.Push || change.Create) {
			continue
		}
		untouched[name] = function
	}
	return r.verifyFleet(ctx, untouched)
}

// upgradeLaggingFunctions 是两分钟一次的代码包升级检查：不做完整读回，优先用内存里最近一次读回的包 ID，没有时只调 DescribeCode。
// 落后的函数分批起 UpgradeCode 操作（只换代码，环境取云上读回的值，不动定时器），不等它们结束。
func (r *PlanReconciler) upgradeLaggingFunctions(ctx context.Context) error {
	pkg := strings.TrimSpace(r.packageID())
	if pkg == "" {
		return nil
	}
	refs, err := r.timerFunctionRefs(ctx)
	if err != nil {
		return err
	}
	targets, err := r.confirmedTargets(ctx)
	if err != nil {
		return err
	}
	started := 0
	for _, name := range sortedKeys(refs) {
		if started >= runtimeConfigBatchSize {
			break // 每次检查最多一批，下一次检查接着升级
		}
		if r.ops().busy(refKey(refs[name])) {
			continue
		}
		readback, ok := r.cachedReadback(refs[name])
		if !ok || readback.Error != "" {
			state, err := r.Cloud.DescribeCode(ctx, refs[name])
			if err != nil {
				log.WarnContextf(ctx, "collector fetch plan cannot read the code package of %s space=%s: %v", name, r.SpaceID, err)
				continue
			}
			readback = FunctionReadback{FunctionState: state, CodeOnly: true}
			r.storeReadback(refs[name], readback)
		}
		if !readback.Exists || strings.TrimSpace(readback.PackageID) == "" || readback.PackageID == pkg {
			continue
		}
		var fallback *cloud.FunctionSpec
		if target, ok := targets[name]; ok {
			if spec, err := r.desiredSpec(target); err == nil {
				fallback = &spec
			}
		}
		if _, err := r.startUpgrade(refs[name], fallback); err == nil {
			started++
		}
	}
	if started > 0 {
		log.WarnContextf(ctx, "collector fetch plan upgrading function code space=%s package=%s functions=%d", r.SpaceID, pkg, started)
	}
	return nil
}

// timerFunctionRefs 返回已生效计划与未生效计划里的全部定时函数在云上的位置。
func (r *PlanReconciler) timerFunctionRefs(ctx context.Context) (map[string]cloud.FunctionRef, error) {
	refs := map[string]cloud.FunctionRef{}
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
				refs[name] = r.functionRef(name, function.Region)
			}
		}
	}
	return refs, nil
}

// verifyFleet 每小时读回一次已生效计划的函数：不存在、受管环境（分配哈希、外部入口三项）或定时器（cron、开关、可用状态、
// Qualifier、Message）不一致的起 Ensure（期望内容按已确认的状态算）；只是代码包落后的起 UpgradeCode。
// 同时停掉计划外的同前缀函数的定时器。有函数这次没读到或修复还在跑时，5 分钟后再读，不宣布核对完成。
func (r *PlanReconciler) verifyFleet(ctx context.Context, applied map[string]*PlanFunction) error {
	requested := r.verifyRequested.Load()
	if !requested && !r.lastVerified.IsZero() && r.now().Sub(r.lastVerified) < planVerifyInterval {
		return nil
	}
	r.verifyRequested.Store(false)
	if len(applied) == 0 {
		// 没有已生效的云函数（例如全部切到了推送）：计划外的同前缀函数照样要停。
		if err := r.disableUnplanned(ctx); err != nil {
			log.WarnContextf(ctx, "collector fetch plan cannot list unplanned functions space=%s: %v", r.SpaceID, err)
			r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
			return nil
		}
		r.lastVerified = r.now()
		return nil
	}
	targets, err := r.confirmedTargets(ctx)
	if err != nil {
		return r.fail("store", err)
	}
	pkg := strings.TrimSpace(r.packageID())
	ops := r.ops()
	var repair, upgrade, present []string
	missing, unverified := false, 0
	specs := map[string]cloud.FunctionSpec{}
	for _, name := range sortedKeys(applied) {
		target, ok := targets[name]
		if !ok {
			continue
		}
		spec, err := r.desiredSpec(target)
		if err != nil {
			return r.fail("environment", fmt.Errorf("function %s: %w", name, err))
		}
		specs[name] = spec
		if ops.busy(refKey(spec.Ref)) {
			unverified++
			continue
		}
		state, err := r.describe(ctx, spec.Ref)
		if err != nil {
			log.WarnContextf(ctx, "collector fetch plan cannot read back %s space=%s: %v", name, r.SpaceID, err)
			unverified++
			continue
		}
		switch {
		case !state.Exists:
			missing = true
			repair = append(repair, name)
		case managedDrift(state.Environment, spec.Environment) || !TimerMatches(state.Timer, target.Cron):
			present = append(present, name)
			repair = append(repair, name)
		case pkg != "" && state.PackageID != pkg:
			present = append(present, name)
			upgrade = append(upgrade, name)
		default:
			present = append(present, name)
		}
	}
	if missing {
		// 云端函数被删要重建：新建出来的函数同样要做一次出口 IP 去重。
		r.Egress.Arm()
	}
	if err := r.disableUnplanned(ctx); err != nil {
		log.WarnContextf(ctx, "collector fetch plan cannot list unplanned functions space=%s: %v", r.SpaceID, err)
		unverified++
	}
	// 没有其他问题时才做出口 IP 去重：共用 IP 的函数先删再按期望内容重建；重建后几分钟再探测下一轮。
	var egress []string
	if len(repair) == 0 && len(upgrade) == 0 && unverified == 0 {
		egress = r.Egress.Next(ctx, r, r.SpaceID, present, r.now())
	}
	if len(repair) == 0 && len(upgrade) == 0 && len(egress) == 0 {
		if unverified > 0 || r.Egress.Pending() {
			r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
			return nil
		}
		r.lastVerified = r.now()
		return nil
	}
	log.WarnContextf(ctx, "collector fetch plan repairing space=%s repair=%s upgrade=%s egress=%s", r.SpaceID, strings.Join(repair, ","), strings.Join(upgrade, ","), strings.Join(egress, ","))
	type pending struct {
		op  *functionOp
		key string
	}
	var started []pending
	startOp := func(op *functionOp, err error, key string) {
		if err == nil {
			started = append(started, pending{op: op, key: key})
		} else {
			unverified++
		}
	}
	for _, name := range repair {
		op, err := r.startEnsure(opKeyRepair, specs[name])
		startOp(op, err, opKeyRepair)
	}
	for _, name := range upgrade {
		spec := specs[name]
		op, err := r.startUpgrade(spec.Ref, &spec)
		startOp(op, err, opKeyUpgrade)
	}
	for _, name := range egress {
		spec := specs[name]
		op, err := r.ops().start(refKey(spec.Ref), opKeyEgress, true, func(ctx context.Context, op *functionOp) error {
			if err := r.Cloud.Delete(ctx, spec.Ref); err != nil {
				return err
			}
			spec, err := r.withPackage(ctx, op, spec)
			if err != nil {
				return err
			}
			spec.Pausing = r.pausingHook(op)
			return r.Cloud.Ensure(ctx, spec)
		})
		startOp(op, err, opKeyEgress)
	}
	ops.wait(ctx, func() []*functionOp {
		out := make([]*functionOp, 0, len(started))
		for _, item := range started {
			out = append(out, item.op)
		}
		return out
	}(), r.roundWait(ctx))
	var firstErr error
	failed := 0
	for _, item := range started {
		done := ops.take(item.op.name, item.key)
		switch {
		case done == nil:
			unverified++ // 还在跑：结束前不会重复起，5 分钟后再读回
		case done.err != nil:
			failed++
			if firstErr == nil {
				firstErr = fmt.Errorf("%s %s: %w", item.key, item.op.name, done.err)
			}
		}
	}
	if failed > 0 {
		// 有失败项：不更新读回时间，下一轮继续修复。
		return r.opFailure(fmt.Errorf("repair of drifted or missing functions did not fully succeed (%d failed): %w", failed, firstErr))
	}
	if unverified > 0 || len(egress) > 0 || r.Egress.Pending() {
		r.lastVerified = r.now().Add(planVerifyRetry - planVerifyInterval)
		return nil
	}
	r.lastVerified = r.now()
	return nil
}

// disableUnplanned 列出计划外的同前缀函数并停掉它们的定时器，不删除：旧部署留下的函数会继续按旧计划拉行情源，
// 和新函数挤同一批出口 IP 的限流额度，还占函数个数与命名空间配额。由人用 collector function delete 清理。
func (r *PlanReconciler) disableUnplanned(ctx context.Context) error {
	planned, err := r.plannedNames(ctx)
	if err != nil {
		return err
	}
	unplanned, err := r.scanUnplanned(ctx, planned)
	if err != nil {
		return err
	}
	for _, ref := range unplanned {
		if r.ops().busy(refKey(ref)) {
			continue
		}
		// 定时器已经停用（或没有）的不再发停用，也不记日志：每小时重发一遍会刷屏。读不到就照常发一次停用。
		if state, err := r.describe(ctx, ref); err == nil && (!state.Exists || state.Timer == nil || !state.Timer.Enabled) {
			continue
		}
		// 结果在下一轮由 collectFinishedOps 取走：成功才记“已停”，失败按 cloud 报。
		_, _ = r.startDisableTimer(ref, opKeyUnplanned)
	}
	return nil
}
