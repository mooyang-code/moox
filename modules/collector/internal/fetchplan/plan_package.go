package fetchplan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
)

// planPackageRefreshInterval 是重新解析最新代码包的最短间隔。
const planPackageRefreshInterval = 30 * time.Second

// packageID 返回最近一次按包名解析出的最新可用代码包 ID（升级检查、读回比对与环境大小估算用）。
// 真正写给函数的代码包在操作开始时现取（latestPackage），不用这里的缓存。补采协程与对账协程并发调用，所以受锁保护。
func (r *PlanReconciler) packageID() string {
	r.packageMu.Lock()
	defer r.packageMu.Unlock()
	if r.resolvedPackage == nil {
		return ""
	}
	return r.resolvedPackage.PackageID
}

// LatestPackageID 返回最近一次解析出的最新可用代码包 ID（运行状态页判断函数的代码包是否最新）；没解析到时为空。
func (r *PlanReconciler) LatestPackageID() string { return r.packageID() }

// errNoCodePackage 表示需要代码包（创建或升级云函数）但一个也没解析到。
var errNoCodePackage = errors.New("no code package")

// planPackageWarnInterval 是"解析不到代码包"这条警告的最短间隔：没有云函数需要它时对账每分钟都会走到这里。
const planPackageWarnInterval = time.Hour

// latestPackage 现取最新的可用代码包（操作开始时调用）：排队期间可能上传了新包，旧包也可能已被清理（每个包名只留最新 3 个）。
func (r *PlanReconciler) latestPackage(ctx context.Context) (cloud.Package, error) {
	name := strings.TrimSpace(r.Deploy.PackageName)
	if name == "" {
		return cloud.Package{}, fmt.Errorf("%w: scf_plan has no package_name for space %s", errNoCodePackage, r.SpaceID)
	}
	pkg, err := r.Cloud.LatestPackage(ctx, name)
	if err != nil {
		return cloud.Package{}, fmt.Errorf("%w: resolve code package %q: %w", errNoCodePackage, name, err)
	}
	if pkg == nil {
		return cloud.Package{}, fmt.Errorf("%w: no available code package named %q; upload one with `moox-cli collector function upload`", errNoCodePackage, name)
	}
	r.packageMu.Lock()
	r.resolvedPackage, r.resolvedAt, r.packageErr = pkg, r.now(), nil
	r.packageMu.Unlock()
	return *pkg, nil
}

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
	fresh := r.resolvedPackage != nil && r.now().Sub(r.resolvedAt) < planPackageRefreshInterval
	r.packageMu.Unlock()
	if fresh {
		return nil
	}
	_, err := r.latestPackage(ctx)
	if err == nil {
		return nil
	}
	r.packageMu.Lock()
	defer r.packageMu.Unlock()
	if r.resolvedPackage != nil {
		// 沿用旧的结果，下一轮再试。
		r.resolvedAt = r.now().Add(planPackageRefreshInterval - 5*time.Second)
		return nil
	}
	r.packageErr = err
	return err
}

// refreshPackageIfAvailable 是对账每一轮的包解析：代码包只在创建或升级云函数时才用得上，
// 计划里没有云函数（例如全部是推送数据集）时，没有代码包不应该挡住计划的推进。
// 解析不到只按间隔记警告，真正要创建函数时由操作报 package。
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

// packageError 返回解析不到代码包的原因。
func (r *PlanReconciler) packageError() error {
	r.packageMu.Lock()
	cause := r.packageErr
	r.packageMu.Unlock()
	if cause == nil {
		cause = fmt.Errorf("%w: resolve code package %q: not resolved yet", errNoCodePackage, r.Deploy.PackageName)
	}
	return cause
}

// packageGate 在没有可用代码包时判断对账能否继续。云上已经有这个空间的计划函数时，它们要升级到最新的代码包，
// 继续下发新环境会让旧代码读不了，所以仍然拦住；一个函数也没有（例如数据集全部是推送）就放行。
func (r *PlanReconciler) packageGate(ctx context.Context) error {
	if strings.TrimSpace(r.Deploy.PackageName) == "" || strings.TrimSpace(r.Deploy.CloudAccountID) == "" || strings.TrimSpace(r.packageID()) != "" {
		return nil
	}
	for _, region := range r.planRegions() {
		refs, err := r.Cloud.List(ctx, r.Deploy.CloudAccountID, region, r.fleetPrefix())
		if err != nil {
			return r.fail("cloud", fmt.Errorf("cannot tell whether cloud functions need the missing code package: %w", err))
		}
		for _, ref := range refs {
			// 只看本空间的函数：前缀相同的别的空间（crypto 与 crypto-x）不算。
			if r.ownsFunctionName(ref.Name) {
				return r.fail("package", r.packageError())
			}
		}
	}
	return nil
}

// fleetPrefix 是本空间采集计划函数的名字前缀。
func (r *PlanReconciler) fleetPrefix() string {
	return "moox-fetch" + marketfetch.PlanFleetMarker + planNameSanitizer.Replace(strings.ToLower(r.SpaceID)) + "-"
}
