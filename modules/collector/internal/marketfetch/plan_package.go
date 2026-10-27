package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// planPackageRefreshInterval 是重新解析最新代码包的最短间隔。
const planPackageRefreshInterval = 30 * time.Second

// packageID 返回当前使用的代码包：按包名解析出的最新可用包（名称非空时），否则是固定的 PackageID。
// 补采协程与对账协程并发调用，所以解析结果受锁保护。
func (r *PlanReconciler) packageID() string {
	r.packageMu.Lock()
	defer r.packageMu.Unlock()
	if r.Deploy.PackageName != "" {
		return r.resolvedPackage
	}
	return r.Deploy.PackageID
}

// errNoCodePackage 表示需要代码包（创建或升级云函数）但一个也没解析到。
var errNoCodePackage = errors.New("no code package")

// planPackageWarnInterval 是"解析不到代码包"这条警告的最短间隔：没有云函数需要它时对账每分钟都会走到这里。
const planPackageWarnInterval = time.Hour

// refreshPackage 解析并缓存最新可用代码包。解析失败时沿用上一次的结果（云端暂时不可读不应让函数停摆），
// 一次也没解析成功过则返回错误：没有代码包就不能创建或升级函数。
func (r *PlanReconciler) refreshPackage(ctx context.Context) error {
	if strings.TrimSpace(r.Deploy.PackageName) == "" {
		return nil
	}
	// 解析整体串行：对账协程与多个补采协程并发刷新时，较早发出的旧查询结果不能晚返回后覆盖已缓存的新版本。
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.packageMu.Lock()
	fresh := r.resolvedPackage != "" && r.now().Sub(r.resolvedAt) < planPackageRefreshInterval
	r.packageMu.Unlock()
	if fresh {
		return nil
	}
	id, err := r.Cloud.LatestPackage(ctx, r.SpaceID, r.Deploy.PackageName)
	r.packageMu.Lock()
	defer r.packageMu.Unlock()
	if err != nil {
		if r.resolvedPackage != "" {
			// 沿用旧的结果，下一轮再试。
			r.resolvedAt = r.now().Add(planPackageRefreshInterval - 5*time.Second)
			return nil
		}
		r.packageErr = fmt.Errorf("resolve code package %q: %w", r.Deploy.PackageName, err)
		return r.packageErr
	}
	r.resolvedPackage, r.resolvedAt, r.packageErr = id, r.now(), nil
	return nil
}

// refreshPackageIfAvailable 是对账每一轮的包解析：代码包只在创建或升级云函数时才用得上，
// 计划里没有云函数（例如全部是推送数据集）时，没有代码包不应该挡住计划的推进。
// 解析不到只按间隔记警告，真正要创建函数时由 requirePackage 报错。
func (r *PlanReconciler) refreshPackageIfAvailable(ctx context.Context) {
	err := r.refreshPackage(ctx)
	if err == nil {
		return
	}
	r.packageMu.Lock()
	warn := r.packageWarnedAt.IsZero() || r.now().Sub(r.packageWarnedAt) >= planPackageWarnInterval
	if warn {
		r.packageWarnedAt = r.now()
	}
	r.packageMu.Unlock()
	if warn {
		log.WarnContextf(ctx, "collector fetch plan has no usable code package space=%s (only needed to create or upgrade cloud functions): %v", r.SpaceID, err)
	}
}

// requirePackage 返回创建或升级云函数要用的代码包；解析不到时报错，不用空包 ID 创建。
func (r *PlanReconciler) requirePackage() (string, error) {
	if id := strings.TrimSpace(r.packageID()); id != "" {
		return id, nil
	}
	r.packageMu.Lock()
	cause := r.packageErr
	r.packageMu.Unlock()
	if cause == nil {
		cause = fmt.Errorf("resolve code package %q: not resolved yet", r.Deploy.PackageName)
	}
	return "", fmt.Errorf("%w: %w", errNoCodePackage, cause)
}

// packageGate 在没有可用代码包时判断对账能否继续。云端已经有这个空间的计划函数时，它们要升级到配置的代码包，
// 继续下发新环境会让旧代码读不了，所以仍然拦住；一个函数也没有（例如数据集全部是推送）就放行。
func (r *PlanReconciler) packageGate(ctx context.Context) error {
	if strings.TrimSpace(r.Deploy.PackageName) == "" || strings.TrimSpace(r.packageID()) != "" {
		return nil
	}
	nodes, err := r.Cloud.ListTimerMarketFetchers(ctx, r.SpaceID)
	if err != nil {
		return r.fail("cloudfunction", fmt.Errorf("cannot tell whether cloud functions need the missing code package: %w", err))
	}
	// 创建中、还没标记就绪的预留也算：它们随后就绪时同样要升级到配置的代码包，不能在这个空档里放行。
	unready, err := r.Cloud.ListUnreadyMarketFetchers(ctx, r.SpaceID, "timer")
	if err != nil {
		return r.fail("cloudfunction", fmt.Errorf("cannot tell whether cloud functions need the missing code package: %w", err))
	}
	prefix := r.fleetPrefix()
	for _, node := range append(nodes, unready...) {
		if strings.HasPrefix(node.InstanceID, prefix) {
			_, packageErr := r.requirePackage()
			return r.fail("package", packageErr)
		}
	}
	return nil
}

// fleetPrefix 是本空间采集计划函数的名字前缀。
func (r *PlanReconciler) fleetPrefix() string {
	return "moox-fetch" + PlanFleetMarker + planNameSanitizer.Replace(strings.ToLower(r.SpaceID)) + "-"
}
