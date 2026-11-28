package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

// setupSCFPlanSummary 是 Timer 采集计划在初始化里的结果：每个空间的代码包是否就绪。
type setupSCFPlanSummary struct {
	Enabled bool                      `json:"enabled"`
	Status  string                    `json:"status"`
	Spaces  []setupSCFPlanSpaceResult `json:"spaces"`
}

type setupSCFPlanSpaceResult struct {
	SpaceID     string `json:"space_id"`
	PackageName string `json:"package_name"`
	PackageID   string `json:"package_id,omitempty"`
	// Action 是 exists（已有可用版本，不重复上传）、uploaded（首次上传）或 failed。
	Action  string `json:"action"`
	Warning string `json:"warning,omitempty"`
}

// scfPlanPackages 是初始化对代码包的操作，抽出来便于测试。
type scfPlanPackages interface {
	// Latest 返回指定名称的最新可用代码包（代码包全局共享，不分空间）。
	Latest(ctx context.Context, name string) (string, bool, error)
	// Upload 构建并上传一个代码包，返回 package_id。
	Upload(ctx context.Context, packageName string) (string, error)
}

// defaultSetupEnsureSCFPlan 保证每个参与 Timer 采集的空间都有可用的代码包：已有则沿用，没有则构建并上传。
// 之后的代码升级用 `moox-cli collector function upload`：Collector 按包名取最新版本，函数自动升级，不需要改配置。
// 代码包从 sourceRoot 下的 modules/collector 构建（产物包目录里按清单下载预编译的函数程序）。
// 构建或上传失败不阻断初始化（例如本机没有源码或 Go 工具链），结果记入摘要，补齐后重跑即可。
func defaultSetupEnsureSCFPlan(ctx context.Context, snapshot *setupSnapshot, sourceRoot string) (*setupSCFPlanSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("scf_plan: setup configuration is missing")
	}
	if !snapshot.Manifest.SCFPlan.Enabled {
		return nil, nil
	}
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return &setupSCFPlanSummary{Enabled: true, Status: "failed", Spaces: []setupSCFPlanSpaceResult{{Action: "failed", Warning: "open gateway failed"}}}, nil
	}
	defer gateway.Close()
	return ensureSCFPlanPackages(ctx, snapshot.Manifest, adminSCFPlanPackages{gateway: gateway, collectorRoot: filepath.Join(sourceRoot, "modules", "collector")}), nil
}

func ensureSCFPlanPackages(ctx context.Context, manifest *topology.Config, packages scfPlanPackages) *setupSCFPlanSummary {
	summary := &setupSCFPlanSummary{Enabled: true, Status: "ready"}
	if len(manifest.SCFPlan.Spaces) == 0 {
		return summary
	}
	// 所有空间共享同一个全局代码包名（默认 moox-collector-plan），初始化只查询和上传一次。
	packageName := manifest.SCFPlan.PackageName
	if packageName == "" {
		packageName = topology.DefaultSCFPackageName
	}
	pkgCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	id, found, err := packages.Latest(pkgCtx, packageName)
	var packageID, action, warning string
	switch {
	case err != nil:
		action, warning = "failed", "query code package failed: "+err.Error()
	case found:
		action, packageID = "exists", id
	default:
		uploaded, uploadErr := packages.Upload(pkgCtx, packageName)
		if uploadErr != nil {
			action, warning = "failed", "build or upload code package failed: "+uploadErr.Error()
		} else {
			action, packageID = "uploaded", uploaded
		}
	}
	cancel()
	if action == "failed" {
		summary.Status = "failed"
	}
	for _, space := range manifest.SCFPlan.Spaces {
		summary.Spaces = append(summary.Spaces, setupSCFPlanSpaceResult{
			SpaceID:     space.SpaceID,
			PackageName: packageName,
			PackageID:   packageID,
			Action:      action,
			Warning:     warning,
		})
	}
	return summary
}

type adminSCFPlanPackages struct {
	gateway rpcclient.GatewayForwarder
	// collectorRoot 是构建代码包用的 Collector 模块目录，取自这次安装的源目录，不看当前目录。
	collectorRoot string
}

func (a adminSCFPlanPackages) client() *rpcclient.Client {
	return &rpcclient.Client{Gateway: a.gateway}
}

func (a adminSCFPlanPackages) Latest(ctx context.Context, name string) (string, bool, error) {
	return a.client().LatestPackage(ctx, name)
}

func (a adminSCFPlanPackages) Upload(ctx context.Context, packageName string) (string, error) {
	version := "init-" + time.Now().UTC().Format("20060102T150405Z")
	result, err := packageCollectorFunction(ctx, collectorPackageOptions{CollectorRoot: a.collectorRoot, Version: version})
	if err != nil {
		return "", err
	}
	if err := collectorpackager.ValidateSCFPackageZip(result.Path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		return "", err
	}
	pkg, err := a.client().UploadPackage(ctx, &collectorpb.InitPackageUploadReq{
		PackageName: packageName, Version: version, Runtime: collectorFunctionRuntime,
		Filename: filepath.Base(result.Path),
	}, data)
	if err != nil {
		return "", err
	}
	return pkg.GetPackageId(), nil
}
