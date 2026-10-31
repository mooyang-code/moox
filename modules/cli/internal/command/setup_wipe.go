package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
)

// errDeploymentBlocked 表示目标机器有阻塞项，不能清除也不能安装。
var errDeploymentBlocked = errors.New("deployment_blocked: 目标机器有阻塞项，详见输出的 reasons")

type setupDeployment struct {
	deployscan.Decision
	Hosts []deployscan.HostReport `json:"hosts"`
}

type setupValidateOutput struct {
	setupvalidate.Result
	Deployment *setupDeployment `json:"deployment,omitempty"`
}

func setupDeploymentStatus(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options *unitdeploy.CoreOptions) (*setupDeployment, error) {
	if err := resolveSetupUnitOptions(snapshot, "", options); err != nil {
		return nil, err
	}
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{})
	if err != nil {
		return nil, err
	}
	local := unitdeploy.ReadLocalState(options.StateDirectory, options.OperatorDirectory)
	status := &setupDeployment{Decision: deployscan.Decide(reports, snapshot.Manifest.ControlHost().Name, local), Hosts: reports}
	if status.Route == deployscan.RouteBlocked {
		return status, errDeploymentBlocked
	}
	return status, nil
}

// checkCoreRoute 在 bootstrap core 动手打包和上传之前确认这是一次首次安装：
// 全是空机时先归档本机遗留的状态和操作员身份，避免复用旧软件、和新 CA 冲突；上次没做完的首次安装可以继续；其余情况直接停止。
func checkCoreRoute(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options unitdeploy.CoreOptions) error {
	if deps.scanDeployment == nil {
		return nil
	}
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{})
	if err != nil {
		return err
	}
	local := unitdeploy.ReadLocalState(options.StateDirectory, options.OperatorDirectory)
	decision := deployscan.Decide(reports, snapshot.Manifest.ControlHost().Name, local)
	switch decision.Route {
	case deployscan.RouteFresh:
		archived, err := deployscan.ArchiveLocal(options.OperatorDirectory, options.StateDirectory, time.Now())
		for _, location := range archived {
			fmt.Fprintf(options.Log, "目标机器是空机，这台电脑上的旧部署状态已归档到 %s\n", location)
		}
		return err
	case deployscan.RouteResume:
		return nil
	default:
		return fmt.Errorf("deployment_not_fresh: route=%s，目标机器不是空机，没有改动任何东西；运行 setup validate 查看情况，再决定彻底清空还是保留数据", decision.Route)
	}
}

func newSetupWipeCommand(deps setupDeps) *cobra.Command {
	var file, source, expect string
	var yes, acceptTradeRisk bool
	var options unitdeploy.CoreOptions
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
			local := unitdeploy.ReadLocalState(options.StateDirectory, options.OperatorDirectory)
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
			if deps.trialPackage != nil {
				if err := deps.trialPackage(cmd.Context(), options); err != nil {
					return err
				}
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
			archived, err := deployscan.ArchiveLocal(options.OperatorDirectory, options.StateDirectory, time.Now())
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

func defaultSetupTrialPackage(ctx context.Context, options unitdeploy.CoreOptions) error {
	return unitdeploy.TrialPackage(ctx, options, "amd64")
}
