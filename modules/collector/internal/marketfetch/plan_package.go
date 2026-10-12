package marketfetch

import (
	"context"
	"fmt"
	"strings"
	"time"
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

// refreshPackage 解析并缓存最新可用代码包。解析失败时沿用上一次的结果（云端暂时不可读不应让函数停摆），
// 一次也没解析成功过则返回错误：没有代码包就不能创建或升级函数。
func (r *PlanReconciler) refreshPackage(ctx context.Context) error {
	if strings.TrimSpace(r.Deploy.PackageName) == "" {
		return nil
	}
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
		return fmt.Errorf("resolve code package %q: %w", r.Deploy.PackageName, err)
	}
	r.resolvedPackage, r.resolvedAt = id, r.now()
	return nil
}
