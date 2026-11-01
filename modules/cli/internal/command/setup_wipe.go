package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
)

// errDeploymentBlocked 表示目标机器有阻塞项，不能清除也不能安装。
var errDeploymentBlocked = errors.New("deployment_blocked: 目标机器有阻塞项，详见输出的 reasons")

type setupDeployment struct {
	deployscan.Decision
	// NextSteps 是接下来要按顺序运行的命令，只有首次安装的路线才有。
	NextSteps []string `json:"next_steps"`
	// ManualSteps 是要用户在自己终端做的事，Agent 做不了，也不影响安装继续。
	ManualSteps []string                `json:"manual_steps,omitempty"`
	Warnings    []string                `json:"warnings,omitempty"`
	Hosts       []deployscan.HostReport `json:"hosts"`
}

// minControlDiskKB 是管理台机部署前至少要有的可用空间：一次部署要放离线包和两个发布目录。
const minControlDiskKB = 8 << 20

// minHostDiskKB 是其他主机部署前至少要有的可用空间。
const minHostDiskKB = 2 << 20

// minUploadKB 是不算残留时至少要有的可用空间：安装包要先上传，下一次部署才开始清理残留。
const minUploadKB = 1 << 20

// setupBinding 是这次命令用的配置和版本。
func setupBinding(snapshot *setupconfig.Snapshot) unitdeploy.Binding {
	return unitdeploy.Binding{DeploymentSHA256: snapshot.DeploymentDigest(), ConfigPath: snapshot.Path(), Version: Version}
}

// consoleCertPath 是这台电脑上缓存的管理台证书。
func consoleCertPath(snapshot *setupconfig.Snapshot) string {
	return tlstrust.CAPath(snapshot.Manifest.ControlHost().Address)
}

// diskWarnings 列出可用空间不够的主机。不算阻塞项：清除旧部署正好能腾出空间。
func diskWarnings(reports []deployscan.HostReport, control string) []string {
	warnings := []string{}
	for _, report := range reports {
		need := int64(minHostDiskKB)
		if report.Host == control {
			need = minControlDiskKB
		}
		// 失败尝试的残留下一次部署会自动清掉，算作可用，否则失败后反而重试不了。
		// 残留要到上传之后才清，所以真实可用空间至少要放得下一个安装包。
		if report.DiskFreeKB >= 0 && (report.DiskFreeKB+report.ReclaimableKB < need || report.DiskFreeKB < minUploadKB) {
			warnings = append(warnings, fmt.Sprintf("disk_low: %s（%s）可用磁盘 %.1f GB（另有 %.1f GB 失败残留可自动回收），部署至少要 %d GB，其中 1 GB 要真实可用", report.Host, report.Address, float64(report.DiskFreeKB)/(1<<20), float64(report.ReclaimableKB)/(1<<20), need>>20))
		}
	}
	return warnings
}

// checkInstallDisk 在打包上传之前确认要动的这台主机磁盘够用。只连这一台：别的主机这一步用不到，可能还连不上。
func checkInstallDisk(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, host string) error {
	if deps.scanDeployment == nil {
		return nil
	}
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{Host: host, Reclaim: true})
	if err != nil {
		return err
	}
	if warnings := diskWarnings(reports, snapshot.Manifest.ControlHost().Name); len(warnings) != 0 {
		return errors.New(strings.Join(warnings, "；"))
	}
	return nil
}

type setupValidateOutput struct {
	setupvalidate.Result
	Deployment *setupDeployment `json:"deployment,omitempty"`
}

func setupDeploymentStatus(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options *unitdeploy.DeployOptions) (*setupDeployment, error) {
	if err := resolveSetupUnitOptions(snapshot, "", options); err != nil {
		return nil, err
	}
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{Reclaim: true})
	if err != nil {
		return nil, err
	}
	local := unitdeploy.ReadLocalState(options.StateDirectory, options.OperatorDirectory, setupBinding(snapshot))
	control := snapshot.Manifest.ControlHost().Name
	status := &setupDeployment{Decision: deployscan.Decide(reports, control, local), Hosts: reports, Warnings: diskWarnings(reports, control)}
	status.NextSteps = deployscan.NextSteps(snapshot.Manifest, status.Route, local)
	status.ManualSteps = deployscan.ManualSteps(status.Route)
	if len(deployscan.CollectSpaces(snapshot.Manifest)) == 0 {
		status.Warnings = append(status.Warnings, "这个拓扑没有 collector-stream，也没配云函数，默认的 crypto 采集任务不会运行")
	}
	drift, err := unitdeploy.ConfigDrift(snapshot, options.StateDirectory)
	if err != nil {
		return nil, err
	}
	status.Warnings = append(status.Warnings, drift...)
	if reason := deployscan.UnfinishedReason(snapshot.Manifest, status.Route, local); reason != "" {
		status.Reasons = append(status.Reasons, reason)
	}
	if status.Route == deployscan.RouteBlocked {
		return status, errDeploymentBlocked
	}
	return status, nil
}

// checkAdminRoute 在 bootstrap admin 动手打包和上传之前确认这是一次首次安装：
// 全是空机时先归档本机遗留的状态和操作员身份，避免复用旧软件、和新 CA 冲突；上次没做完的首次安装可以继续；其余情况直接停止。
func checkAdminRoute(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options unitdeploy.DeployOptions) error {
	if deps.scanDeployment == nil {
		return nil
	}
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{})
	if err != nil {
		return err
	}
	local := unitdeploy.ReadLocalState(options.StateDirectory, options.OperatorDirectory, setupBinding(snapshot))
	decision := deployscan.Decide(reports, snapshot.Manifest.ControlHost().Name, local)
	switch decision.Route {
	case deployscan.RouteFresh:
		archived, err := deployscan.ArchiveLocal(options.OperatorDirectory, options.StateDirectory, consoleCertPath(snapshot), time.Now())
		for _, location := range archived {
			fmt.Fprintf(options.Log, "目标机器是空机，这台电脑上的旧部署状态已归档到 %s\n", location)
		}
		if err != nil {
			return err
		}
		return unitdeploy.WriteBinding(options.StateDirectory, setupBinding(snapshot))
	case deployscan.RouteResume:
		return nil
	default:
		reason := ""
		if len(decision.Reasons) != 0 {
			reason = "：" + strings.Join(decision.Reasons, "；")
		}
		return fmt.Errorf("deployment_not_fresh: route=%s%s。没有改动任何东西；运行 setup validate 查看情况，再决定彻底清空还是保留数据", decision.Route, reason)
	}
}

func newSetupWipeCommand(deps setupDeps) *cobra.Command {
	var file, source, expect string
	var yes, acceptTradeRisk bool
	var options unitdeploy.DeployOptions
	cmd := &cobra.Command{
		Use:   "wipe",
		Short: "彻底清除 moox.toml 所列主机上全部 MooX 旧程序和数据（默认只预览，不可恢复）",
		Long: `停止并删除 moox.toml 所列主机上 MooX 的全部程序、数据和服务项，包括历史导入包和归档，用于检测到旧部署、用户同意彻底清空之后全新发布。
不带 --yes 只预览；执行要带 --yes --expect <预览里的 digest>，范围变了会拒绝。
云函数不清理，要用户到腾讯云控制台删除；不在 moox.toml 里的主机也不清理。中途失败后再运行一次会接着清。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.scanDeployment == nil || deps.wipeHost == nil {
				return errors.New("setup wipe dependencies are unavailable")
			}
			if yes && expect == "" {
				return errors.New("setup wipe --yes 要带 --expect <预览里的 digest>，先运行一次不带 --yes 的预览")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := resolveSetupUnitOptions(snapshot, source, &options); err != nil {
				return err
			}
			manifest := snapshot.Manifest
			reports, err := deps.scanDeployment(cmd.Context(), snapshot, deployscan.Options{Sizes: true})
			if err != nil {
				return err
			}
			local := unitdeploy.ReadLocalState(options.StateDirectory, options.OperatorDirectory, setupBinding(snapshot))
			decision := deployscan.Decide(reports, manifest.ControlHost().Name, local)
			if decision.Route == deployscan.RouteBlocked {
				if err := writeSetupJSON(cmd, setupDeployment{Decision: decision, Hosts: reports}); err != nil {
					return err
				}
				return errDeploymentBlocked
			}
			pending := deployscan.ReadPending(options.OperatorDirectory)
			plan, err := deployscan.BuildPlan(manifest, reports, pending)
			if err != nil {
				return err
			}
			if !yes {
				return writePreview(cmd, map[string]any{"action": "wipe", "route": decision.Route, "plan": plan})
			}
			if expect != plan.Digest {
				return fmt.Errorf("wipe_scope_changed: 清除范围和预览不一致（现在是 %s），没有清除任何主机；重新预览并让用户确认", plan.Digest)
			}
			if (plan.Trade == deployscan.TradeLive || plan.Trade == deployscan.TradeUnreadable) && !acceptTradeRisk {
				return fmt.Errorf("wipe_trade_risk: Trade 状态是 %s（可能有实盘账户），没有清除任何主机；用户明确同意后加 --accept-trade-risk", plan.Trade)
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return errors.New("config_changed")
			}
			if err := deployscan.MarkPending(options.OperatorDirectory, plan); err != nil {
				return err
			}
			dirs := map[string][]string{}
			for _, host := range plan.Hosts {
				dirs[host.Host] = host.Dirs
			}
			results := []deployscan.HostResult{}
			for _, host := range deployscan.Order(manifest) {
				result, err := deps.wipeHost(cmd.Context(), host, manifest.Paths.Resolved().DeployRoot, dirs[host.Name])
				if err != nil {
					return err
				}
				results = append(results, result)
			}
			archived, err := deployscan.ArchiveLocal(options.OperatorDirectory, options.StateDirectory, consoleCertPath(snapshot), time.Now())
			if err != nil {
				return err
			}
			reports, err = deps.scanDeployment(cmd.Context(), snapshot, deployscan.Options{})
			if err != nil {
				return err
			}
			decision = deployscan.Decide(reports, manifest.ControlHost().Name, deployscan.LocalState{})
			if decision.Route != deployscan.RouteFresh {
				return fmt.Errorf("wipe_incomplete: 清除后 route=%s，重新运行 setup wipe 会接着清", decision.Route)
			}
			if err := deployscan.ClearPending(options.OperatorDirectory); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "wiped", "route": decision.Route, "hosts": results, "archived": archived})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&source, "source-dir", "", "源码或产物包目录（默认当前目录）")
	cmd.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录（默认和 bootstrap 相同）")
	cmd.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认清除，不加只预览")
	cmd.Flags().StringVar(&expect, "expect", "", "预览输出的 digest，范围变了会拒绝")
	cmd.Flags().BoolVar(&acceptTradeRisk, "accept-trade-risk", false, "用户明确同意后，在查到实盘账户或查不了 Trade 时继续")
	return cmd
}

func defaultSetupScanDeployment(ctx context.Context, snapshot *setupconfig.Snapshot, options deployscan.Options) ([]deployscan.HostReport, error) {
	return deployscan.Scan(ctx, snapshot.Manifest, dialSetupHost, options)
}

func defaultSetupWipeHost(ctx context.Context, host setupconfig.Host, root string, dirs []string) (deployscan.HostResult, error) {
	return deployscan.WipeHost(ctx, dialSetupHost, host, root, dirs)
}
