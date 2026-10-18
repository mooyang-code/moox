package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
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
	// Latest 返回空间下指定名称的最新可用代码包。
	Latest(ctx context.Context, spaceID, name string) (string, bool, error)
	// Upload 为空间构建并上传一个代码包，返回 package_id。
	Upload(ctx context.Context, space setupconfig.SCFPlanSpace, cloudAccountID string) (string, error)
}

// defaultSetupEnsureSCFPlan 保证每个参与 Timer 采集的空间都有可用的代码包：已有则沿用，没有则构建并上传。
// 之后的代码升级用 `moox-cli collector function upload`：Collector 按包名取最新版本，函数自动升级，不需要改配置。
// 构建或上传失败不阻断初始化（例如本机没有源码或 Go 工具链），结果记入摘要，补齐后重跑即可。
func defaultSetupEnsureSCFPlan(ctx context.Context, snapshot *setupconfig.Snapshot) (*setupSCFPlanSummary, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("scf_plan: setup configuration is missing")
	}
	if !snapshot.Manifest.SCFPlan.Enabled {
		return nil, nil
	}
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return &setupSCFPlanSummary{Enabled: true, Status: "failed", Spaces: []setupSCFPlanSpaceResult{{Action: "failed", Warning: "open gateway failed"}}}, nil
	}
	defer gateway.Close()
	return ensureSCFPlanPackages(ctx, snapshot.Manifest, adminSCFPlanPackages{gateway: gateway}), nil
}

func ensureSCFPlanPackages(ctx context.Context, manifest setupconfig.Manifest, packages scfPlanPackages) *setupSCFPlanSummary {
	summary := &setupSCFPlanSummary{Enabled: true, Status: "ready"}
	account := manifest.SCFCloudAccount()
	for _, space := range manifest.SCFPlan.Spaces {
		result := setupSCFPlanSpaceResult{SpaceID: space.SpaceID, PackageName: space.PackageName}
		spaceCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		id, found, err := packages.Latest(spaceCtx, space.SpaceID, space.PackageName)
		switch {
		case err != nil:
			result.Action, result.Warning = "failed", "query code package failed: "+err.Error()
		case found:
			result.Action, result.PackageID = "exists", id
		default:
			uploaded, uploadErr := packages.Upload(spaceCtx, space, account.AccountID)
			if uploadErr != nil {
				result.Action, result.Warning = "failed", "build or upload code package failed: "+uploadErr.Error()
			} else {
				result.Action, result.PackageID = "uploaded", uploaded
			}
		}
		cancel()
		if result.Action == "failed" {
			summary.Status = "failed"
		}
		summary.Spaces = append(summary.Spaces, result)
	}
	return summary
}

type adminSCFPlanPackages struct {
	gateway adminclient.GatewayForwarder
}

func (a adminSCFPlanPackages) client(spaceID string) *adminclient.Client {
	return &adminclient.Client{Gateway: a.gateway, SpaceID: spaceID}
}

func (a adminSCFPlanPackages) Latest(ctx context.Context, spaceID, name string) (string, bool, error) {
	return a.client(spaceID).LatestPackage(ctx, name)
}

func (a adminSCFPlanPackages) Upload(ctx context.Context, space setupconfig.SCFPlanSpace, cloudAccountID string) (string, error) {
	version := "init-" + time.Now().UTC().Format("20060102T150405Z")
	result, err := packageCollectorFunction(ctx, collectorPackageOptions{SpaceID: space.SpaceID, Version: version})
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
	response, err := a.client(space.SpaceID).UploadPackage(ctx, adminclient.UploadPackageRequest{
		PackageName: space.PackageName, Version: version, Runtime: "Go1",
		PackageType: adminclient.ResolvePackageType("data_collector"), BizType: "market_fetcher",
		CloudAccountID: cloudAccountID, OriginalFilename: filepath.Base(result.Path),
	}, data)
	if err != nil {
		return "", err
	}
	return response.PackageID, nil
}
