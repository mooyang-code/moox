package fetchplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// errBackfillFunctionPending 表示补采函数还在创建或升级：调用方稍后重试。
var errBackfillFunctionPending = errors.New("补采函数还在创建或升级")

// DefaultBackfillReservedConcurrency 是补采函数默认的最大并发实例数，由平台的函数级预留并发硬性限制；
// 可用 scf_plan.backfill_reserved_concurrency 调大（历史补齐的并发是 地域数 ×（它 − 1），留 1 个给实时缺口的补采；账号的预留并发总额不够时不要调大，建函数会报配额超限）。
const DefaultBackfillReservedConcurrency = 5

// BackfillFunctionName 返回某市场、地域的补采函数名，名称含 PlanFleetMarker。
func BackfillFunctionName(spaceID, marketType, region string) string {
	region = strings.TrimPrefix(strings.ToLower(region), "ap-")
	return fmt.Sprintf("moox-fetch%sbackfill-%s-%s-%s", marketfetch.PlanFleetMarker, planNameSanitizer.Replace(strings.ToLower(spaceID)),
		planNameSanitizer.Replace(strings.ToLower(marketType)), planNameSanitizer.Replace(region))
}

// BackfillRegion 返回某市场补采函数所在的地域：配置了 backfill_region 就用它（与定时函数分开地域，补采重发不占定时函数
// 那批出口地址），否则是市场白名单中的第一个地域。
func (r *PlanReconciler) BackfillRegion(marketType string) (string, bool) {
	regions := r.Options.Markets[strings.ToLower(marketType)]
	if len(regions) == 0 {
		return "", false
	}
	if r.Options.BackfillRegion != "" {
		return r.Options.BackfillRegion, true
	}
	return regions[0], true
}

// BackfillRegions 返回某市场补采函数可用的全部地域；市场没有配云函数地域时为空。
// 配了 backfill_region 就只用它（补采与定时函数分开地域，补采的重发不占定时函数那批出口地址）；没配就是市场白名单里的全部地域。
// 平时的补采只用 BackfillRegion 一个地域；历史补齐把分块按这些地域轮流派发，出口地址分散，按出口 IP 限频的行情源能分摊开。
func (r *PlanReconciler) BackfillRegions(marketType string) []string {
	listed := r.Options.Markets[strings.ToLower(marketType)]
	if len(listed) == 0 {
		return nil
	}
	if r.Options.BackfillRegion != "" {
		return []string{r.Options.BackfillRegion}
	}
	seen := map[string]struct{}{}
	var regions []string
	for _, region := range listed {
		if _, ok := seen[region]; !ok {
			seen[region] = struct{}{}
			regions = append(regions, region)
		}
	}
	sort.Strings(regions)
	return regions
}

// BackfillReservedConcurrency 是本空间补采函数的最大并发实例数。
func (r *PlanReconciler) BackfillReservedConcurrency() int {
	if r.Options.BackfillReservedConcurrency > 0 {
		return r.Options.BackfillReservedConcurrency
	}
	return DefaultBackfillReservedConcurrency
}

// backfillPrefix 是本空间补采函数的名字前缀。
func (r *PlanReconciler) backfillPrefix() string {
	return fmt.Sprintf("moox-fetch%sbackfill-%s-", marketfetch.PlanFleetMarker, planNameSanitizer.Replace(strings.ToLower(r.SpaceID)))
}

// backfillRef 返回某市场补采函数在云上的位置（补采函数不分片，在配置的命名空间里）。
func (r *PlanReconciler) backfillRef(marketType, region string) cloud.FunctionRef {
	return cloud.FunctionRef{AccountID: r.Deploy.CloudAccountID, Region: region, Namespace: r.Deploy.Namespace, Name: BackfillFunctionName(r.SpaceID, marketType, region)}
}

// backfillSpec 是补采函数的期望内容：不带定时器与采集计划，同步调用，预留并发份内存硬性限制并发实例数。
func (r *PlanReconciler) backfillSpec(ref cloud.FunctionRef, access map[string]string) (cloud.FunctionSpec, error) {
	network, ok := r.Deploy.Networks[ref.Region]
	if !ok {
		return cloud.FunctionSpec{}, fmt.Errorf("region %s has no network configured", ref.Region)
	}
	if err := network.Check(); err != nil {
		return cloud.FunctionSpec{}, fmt.Errorf("region %s: %w", ref.Region, err)
	}
	environment := map[string]string{"MOOX_MARKET_FETCH_MODE": "backfill", "MOOX_FETCH_TIMEOUT_SECONDS": strconv.Itoa(tencent.CollectorTimerTimeoutSeconds)}
	for _, key := range []string{"MOOX_ENTRY_ADDRESS", "MOOX_ENTRY_HOST", "MOOX_CALLER", "MOOX_CALLER_KEY_ID", "MOOX_CALLER_KEY"} {
		if value, ok := access[key]; ok {
			environment[key] = value
		}
	}
	for key, value := range r.Deploy.BaseEnvironment {
		if _, set := environment[key]; !set {
			environment[key] = value
		}
	}
	memory := r.memoryMB()
	return cloud.FunctionSpec{
		Ref: ref, Environment: environment, MemoryMB: int64(memory), TimeoutS: tencent.CollectorTimerTimeoutSeconds,
		VPCID: network.VPCID, SubnetID: network.SubnetID, ReservedMem: int64(r.BackfillReservedConcurrency() * memory),
	}, nil
}

// EnsureBackfillFunction 保证该市场的补采函数就绪（与定时函数同一代码包，不带定时器），返回函数名。
// 就绪缓存里已有它、并且代码包与外部入口指纹（外部入口与调用方身份）都没变就直接返回；否则没有操作在跑就起一个 Ensure，
// 本次调用等一会儿，做完了就返回函数名，还在做就返回“仍在创建”（errBackfillFunctionPending），调用方下一轮再试。
// 补采函数没有定时器，暂停期间照常创建（恢复追赶时系统暂停还开着，补采却要跑）。
func (r *PlanReconciler) EnsureBackfillFunction(ctx context.Context, marketType string) (string, error) {
	region, ok := r.BackfillRegion(marketType)
	if !ok {
		return "", fmt.Errorf("no SCF region is configured for market %s", marketType)
	}
	return r.EnsureBackfillFunctionIn(ctx, marketType, region)
}

// EnsureBackfillFunctionIn 保证该市场在指定地域的补采函数就绪，地域必须是 BackfillRegions 之一（历史补齐按地域轮流派发分块）。
func (r *PlanReconciler) EnsureBackfillFunctionIn(ctx context.Context, marketType, region string) (string, error) {
	if !slices.Contains(r.BackfillRegions(marketType), region) {
		return "", fmt.Errorf("region %s is not a backfill region of market %s", region, marketType)
	}
	if err := r.refreshPackage(ctx); err != nil {
		return "", err
	}
	if r.Entry == nil {
		return "", errors.New("SCF entry environment is required")
	}
	access, err := r.Entry(region)
	if err != nil {
		return "", err
	}
	ref := r.backfillRef(marketType, region)
	name := ref.Name
	fingerprint := backfillEntryFingerprint(access)
	key := opKeyBackfill + "|" + fingerprint
	r.readMu.Lock()
	if r.backfillRefs == nil {
		r.backfillRefs = map[string]cloud.FunctionRef{}
	}
	r.backfillRefs[name] = ref
	r.readMu.Unlock()
	r.backfillMu.Lock()
	// 持有补采锁之后才读取目标代码包：在锁外读取，较早读到旧包的调用等到锁后会把已升级的函数降回旧版本。
	pkg := r.packageID()
	ready := func(currentPkg string) bool {
		return currentPkg != "" && r.backfillReady[name] == currentPkg+"|"+fingerprint
	}
	settleLocked := func(op *functionOp) error {
		if op.err != nil {
			return fmt.Errorf("ensure backfill function %s: %w", name, op.err)
		}
		r.markBackfillReady(name, op.packageID+"|"+fingerprint)
		return nil
	}
	if ready(pkg) {
		r.backfillMu.Unlock()
		return name, nil
	}
	if op := r.ops().take(refKey(ref), key); op != nil {
		if err := settleLocked(op); err != nil {
			r.backfillMu.Unlock()
			return "", err
		}
		if ready(pkg) {
			r.backfillMu.Unlock()
			return name, nil
		}
	}
	if r.ops().busy(refKey(ref)) {
		r.backfillMu.Unlock()
		return "", fmt.Errorf("backfill function %s: %w", name, errBackfillFunctionPending)
	}
	spec, err := r.backfillSpec(ref, access)
	if err != nil {
		r.backfillMu.Unlock()
		return "", err
	}
	op, err := r.ops().start(refKey(ref), key, false, func(ctx context.Context, op *functionOp) error {
		spec, err := r.withPackage(ctx, op, spec)
		if err != nil {
			return err
		}
		return r.Cloud.Ensure(ctx, spec)
	})
	if err != nil {
		r.backfillMu.Unlock()
		return "", fmt.Errorf("backfill function %s: %w", name, err)
	}
	r.backfillMu.Unlock()

	// 在锁外等待云端操作完成（最长约 50s），避免阻塞其他地域/市场的补采调用及删除清理。
	r.ops().wait(ctx, []*functionOp{op}, r.roundWait(ctx))

	r.backfillMu.Lock()
	defer r.backfillMu.Unlock()
	done := r.ops().take(refKey(ref), key)
	if done == nil {
		if op.finished() && op.err != nil {
			return "", fmt.Errorf("ensure backfill function %s: %w", name, op.err)
		}
		// 等待结束与重新加锁之间可能已被其他并发调用取走并标记就绪；
		// 仅在缓存已确认匹配最新代码包与指纹时返回就绪，防止误报中途已被删除的函数。
		if ready(r.packageID()) {
			return name, nil
		}
		return "", fmt.Errorf("backfill function %s: %w", name, errBackfillFunctionPending)
	}
	if err := settleLocked(done); err != nil {
		return "", err
	}
	return name, nil
}

// backfillEntryFingerprint 由外部入口地址、实例、调用方与密钥 ID 算出指纹，不含密钥本身。
func backfillEntryFingerprint(access map[string]string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{access["MOOX_ENTRY_ADDRESS"], access["MOOX_ENTRY_HOST"], access["MOOX_CALLER"], access["MOOX_CALLER_KEY_ID"]}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// markBackfillReady 记下补采函数已确认的“代码包|外部入口指纹”。
func (r *PlanReconciler) markBackfillReady(name, key string) {
	if r.backfillReady == nil {
		r.backfillReady = map[string]string{}
	}
	r.backfillReady[name] = key
}
