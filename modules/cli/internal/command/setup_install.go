package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/installflow"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
)

// setupVerifyTimeout 是 setup install 里验收的时限。
const setupVerifyTimeout = 6 * time.Minute

// setupInstallOptions 是一次 setup install 的参数。
type setupInstallOptions struct {
	file, configDir, spaceID, wipe string
	yes, acceptTradeRisk           bool
	killOutside                    bool
	deploy                         unitdeploy.DeployOptions
}

// setupInstallHost 是预览里一台主机的情况。
type setupInstallHost struct {
	Host      string   `json:"host"`
	Units     []string `json:"units"`
	Reachable bool     `json:"download_reachable"`
}

func newSetupInstallCommand(deps setupDeps) *cobra.Command {
	var options setupInstallOptions
	if deps.deployHost == nil {
		deps.deployHost = unitdeploy.DeployHost
	}
	if deps.deployUnit == nil {
		deps.deployUnit = unitdeploy.DeployUnit
	}
	if deps.backupConfig == nil {
		deps.backupConfig = func(ctx context.Context, file string) error {
			_, err := backupConfigFile(ctx, file)
			return err
		}
	}
	if deps.verifySpace == nil {
		deps.verifySpace = func(ctx context.Context, file string, snapshot *setupconfig.Snapshot, options unitdeploy.DeployOptions, spaceID string) error {
			_, err := runSetupVerifySpace(ctx, file, snapshot, options, spaceID, setupVerifyTimeout)
			return err
		}
	}
	if deps.checkDownload == nil {
		deps.checkDownload, deps.prefetch, deps.localReachable = unitdeploy.CheckDownload, unitdeploy.Prefetch, unitdeploy.LocalDownloadReachable
	}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "一条命令装完：预检、下载、清除旧部署、防火墙、部署、初始化、启动和验收（默认只预检）",
		Long: `不带 --yes 只读预检：输出路线、清除范围、每台要下载的内容。
带 --yes 时下载、清除（有旧部署时要带 --wipe <预览摘要>）和防火墙同时进行，然后按依赖并行部署。
中途失败直接重跑同一条命令，已完成的步骤会跳过。进度逐行输出到标准错误。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(options.spaceID) == "" {
				return errors.New("setup install 要带 --space-id")
			}
			snapshot, err := deps.load(options.file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := resolveSetupUnitOptions(snapshot, "", &options.deploy); err != nil {
				return err
			}
			if err := setupvalidate.CheckDeployments(snapshot.Manifest); err != nil {
				return err
			}
			if err := setupStartPrerequisites(snapshot.Manifest, options.spaceID); err != nil {
				return err
			}
			if options.deploy.Bundle == nil {
				return errors.New("setup install 要在产物包目录里运行；源码目录用分步命令")
			}
			options.deploy.Log = &lockedWriter{out: cmd.ErrOrStderr()}
			return runSetupInstall(cmd, deps, snapshot, options)
		},
	}
	cmd.Flags().StringVar(&options.file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&options.configDir, "config-dir", "./config/setup", "元数据等种子目录")
	cmd.Flags().StringVar(&options.spaceID, "space-id", "", "安装后启动的空间，比如 crypto")
	cmd.Flags().BoolVar(&options.yes, "yes", false, "确认执行，不加只预检")
	cmd.Flags().StringVar(&options.wipe, "wipe", "", "预检输出的清除摘要；有旧部署时必须带，范围变了会拒绝")
	cmd.Flags().BoolVar(&options.acceptTradeRisk, "accept-trade-risk", false, "用户明确同意后，在查到实盘账户或查不了 Trade 时继续")
	cmd.Flags().BoolVar(&options.killOutside, "kill-outside", false, "用户确认后，结束预览里列出的部署目录之外的 MooX 进程")
	cmd.Flags().StringVar(&options.deploy.StateDirectory, "state-dir", "", "持久私有部署状态目录（默认和 bootstrap 相同）")
	cmd.Flags().StringVar(&options.deploy.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	return cmd
}

// lockedWriter 让并行的步骤共用一个日志输出。
type lockedWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *lockedWriter) Write(raw []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(raw)
}

func runSetupInstall(cmd *cobra.Command, deps setupDeps, snapshot *setupconfig.Snapshot, options setupInstallOptions) error {
	ctx := cmd.Context()
	manifest := snapshot.Manifest
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{Sizes: true, Reclaim: true})
	if err != nil {
		return err
	}
	local := unitdeploy.ReadLocalState(options.deploy.StateDirectory, options.deploy.OperatorDirectory, setupBinding(snapshot))
	decision := deployscan.Decide(reports, manifest.ControlHost().Name, local)
	if decision.Route == deployscan.RouteBlocked {
		if err := writeSetupJSON(cmd, setupDeployment{Decision: decision, Hosts: reports}); err != nil {
			return err
		}
		return errDeploymentBlocked
	}
	pending := deployscan.PlanSteps(manifest, decision.Route, local, options.spaceID)
	// 装到一半再跑时路线可能已是 upgrade：control 完成之后的失败走这条，这时不需要也不该再清除。
	rerun := decision.Route == deployscan.RouteFresh || decision.Route == deployscan.RouteResume ||
		decision.Route == deployscan.RouteUpgrade && !local.StaleBinding
	if decision.Route == deployscan.RouteUpgrade && rerun && len(pending) == 0 {
		return writeSetupJSON(cmd, map[string]any{"status": "installed", "route": decision.Route})
	}
	var plan deployscan.Plan
	if !rerun {
		if plan, err = deployscan.BuildPlan(manifest, reports, deployscan.ReadPending(options.deploy.OperatorDirectory)); err != nil {
			return err
		}
	}
	hosts, err := setupInstallPreflight(ctx, deps, manifest, options)
	if err != nil {
		return err
	}
	if !options.yes {
		preview := map[string]any{"action": "install", "route": decision.Route, "reasons": decision.Reasons, "hosts": hosts, "steps": pending, "warnings": diskWarnings(reports, manifest.ControlHost().Name)}
		if !rerun {
			preview["wipe"] = plan
		}
		return writePreview(cmd, preview)
	}
	if !rerun {
		if err := checkSetupWipe(plan, options); err != nil {
			return err
		}
	}
	progress := installflow.NewProgress(cmd.ErrOrStderr())
	started := time.Now()
	if err := prepareSetupInstall(ctx, deps, snapshot, options, plan, !rerun, progress); err != nil {
		return err
	}
	if !rerun || decision.Route == deployscan.RouteFresh {
		if err := checkAdminRoute(ctx, deps, snapshot, options.deploy); err != nil {
			return err
		}
	}
	// 只清除一次：下面的部署用新的路线和记录算步骤。
	local = unitdeploy.ReadLocalState(options.deploy.StateDirectory, options.deploy.OperatorDirectory, setupBinding(snapshot))
	if reports, err = deps.scanDeployment(ctx, snapshot, deployscan.Options{Reclaim: true}); err != nil {
		return err
	}
	decision = deployscan.Decide(reports, manifest.ControlHost().Name, local)
	switch decision.Route {
	case deployscan.RouteFresh, deployscan.RouteResume, deployscan.RouteUpgrade:
	default:
		return fmt.Errorf("deployment_not_fresh: 准备完成后 route=%s：%s", decision.Route, strings.Join(decision.Reasons, "；"))
	}
	if warnings := diskWarnings(reports, manifest.ControlHost().Name); len(warnings) != 0 {
		return errors.New(strings.Join(warnings, "；"))
	}
	steps := deployscan.PlanSteps(manifest, decision.Route, local, options.spaceID)
	execute := func(ctx context.Context, step deployscan.Step) error {
		return executeSetupInstallStep(ctx, deps, snapshot, options, step)
	}
	if err := installflow.Run(ctx, steps, execute, progress); err != nil {
		return fmt.Errorf("%w。修好后重跑同一条命令，已完成的步骤会跳过", err)
	}
	return writeSetupJSON(cmd, map[string]any{"status": "installed", "space_id": options.spaceID, "seconds": time.Since(started).Round(time.Second).Seconds(), "manual_steps": deployscan.ManualSteps(deployscan.RouteFresh)})
}

// checkSetupWipe 确认用户授权的清除范围。
func checkSetupWipe(plan deployscan.Plan, options setupInstallOptions) error {
	if options.wipe != plan.Digest {
		return fmt.Errorf("wipe_scope_changed: 清除范围和预览不一致（现在是 %s），没有动任何主机；先不带 --yes 预检，让用户确认后带 --wipe", plan.Digest)
	}
	if outside := planOutside(plan); len(outside) != 0 && !options.killOutside {
		return fmt.Errorf("wipe_outside_processes: 部署目录之外还有 MooX 进程 %v，没有清除任何主机；用户确认后加 --kill-outside", outside)
	}
	if (plan.Trade == deployscan.TradeLive || plan.Trade == deployscan.TradeUnreadable) && !options.acceptTradeRisk {
		return fmt.Errorf("wipe_trade_risk: Trade 状态是 %s（可能有实盘账户），没有清除任何主机；用户明确同意后加 --accept-trade-risk", plan.Trade)
	}
	return nil
}

// setupInstallPreflight 实测每台的下载源。源不通时部署阶段由这台电脑推送，只是慢，不算失败。
func setupInstallPreflight(ctx context.Context, deps setupDeps, manifest setupconfig.Manifest, options setupInstallOptions) ([]setupInstallHost, error) {
	list := manifest.Hosts()
	hosts := make([]setupInstallHost, len(list))
	errs := make([]error, len(list))
	var workers sync.WaitGroup
	for i, host := range list {
		workers.Add(1)
		go func() {
			defer workers.Done()
			reachable, err := deps.checkDownload(ctx, manifest, options.deploy, host.Name)
			hosts[i], errs[i] = setupInstallHost{Host: host.Name, Units: unitdeploy.HostUnits(manifest, host.Name), Reachable: reachable}, err
		}()
	}
	workers.Wait()
	if err := errors.Join(errs...); err != nil {
		return hosts, err
	}
	// 目标机下载不了时靠这台电脑推送。这里也不通就在动旧部署之前停下。
	if slices.ContainsFunc(hosts, func(host setupInstallHost) bool { return !host.Reachable }) && !deps.localReachable(ctx, options.deploy) {
		return hosts, errors.New("download_failed: 目标机和这台电脑都连不上下载源，没有动任何主机；检查网络后重跑")
	}
	return hosts, nil
}

// prepareSetupInstall 同时启动下载、清除和防火墙，总耗时取决于最慢的一项。清除失败不进入部署；
// 下载失败不致命，部署时会换源和本机推送。下载脚本是系统 python3，不在部署目录内，清除不会碰到它。
func prepareSetupInstall(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options setupInstallOptions, plan deployscan.Plan, wipe bool, progress *installflow.Progress) error {
	var workers sync.WaitGroup
	var wipeErr, firewallErr error
	if wipe {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, wipeErr = executeSetupWipe(ctx, deps, snapshot, options.deploy, plan, options.killOutside)
			progress.Event("wipe_finished", map[string]any{"ok": wipeErr == nil})
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		var summary setupFirewallSummary
		summary, firewallErr = deps.ensureFirewall(ctx, snapshot)
		if firewallErr == nil && summary.Status != "" && summary.Status != "ready" {
			firewallErr = fmt.Errorf("firewall_incomplete: status=%s skipped=%d", summary.Status, summary.Skipped)
		}
		progress.Event("firewall_finished", map[string]any{"ok": firewallErr == nil})
	}()
	for _, host := range snapshot.Manifest.Hosts() {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := deps.prefetch(ctx, snapshot.Manifest, options.deploy, host.Name)
			fields := map[string]any{"host": host.Name, "ok": err == nil}
			if err != nil {
				fields["detail"] = err.Error()
			}
			progress.Event("download_finished", fields)
		}()
	}
	workers.Wait()
	return errors.Join(wipeErr, firewallErr)
}

func executeSetupInstallStep(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, options setupInstallOptions, step deployscan.Step) error {
	deploy := options.deploy
	storageHost := snapshot.Manifest.ComponentHost("storage-primary")
	switch step.Kind {
	case deployscan.StepFirewall:
		return nil // 准备阶段已经做了。
	case deployscan.StepAdmin:
		_, err := deps.bootstrapAdmin(ctx, snapshot, deploy)
		return err
	case deployscan.StepHost:
		_, err := deploySetupHost(ctx, deps, snapshot, unitdeploy.HostOptions{DeployOptions: deploy, HostID: step.Host})
		return err
	case deployscan.StepUnit:
		_, err := deps.deployUnit(ctx, snapshot, unitdeploy.UnitOptions{DeployOptions: deploy, HostID: step.Host, Profile: step.Unit})
		return err
	case deployscan.StepInitStorage:
		bundle, err := deps.loadInitBundle(options.configDir)
		if err != nil {
			return err
		}
		if _, err := deps.initMetadata(ctx, snapshot, storageHost, bundle); err != nil {
			return err
		}
		seed, err := metadataSeedDigest(bundle.Calls)
		if err != nil {
			return err
		}
		return unitdeploy.MarkInitStorage(deploy.StateDirectory, seed)
	case deployscan.StepControl:
		_, err := deploySetupControl(ctx, deps, snapshot, unitdeploy.ControlOptions{DeployOptions: deploy})
		return err
	case deployscan.StepInit:
		bundle, err := deps.loadInitBundle(options.configDir)
		if err != nil {
			return err
		}
		bundle.ImportedSeed = unitdeploy.ImportedSeed(deploy.StateDirectory)
		if _, err := deps.initStorage(ctx, snapshot, options.file, options.configDir, storageHost, bundle); err != nil {
			return err
		}
		return unitdeploy.MarkStep(deploy.StateDirectory, unitdeploy.StepInitReady)
	case deployscan.StepBackup:
		if err := deps.backupConfig(ctx, options.file); err != nil {
			return err
		}
		return unitdeploy.MarkStep(deploy.StateDirectory, unitdeploy.StepBackup)
	case deployscan.StepStart:
		client, closeClient, err := deps.openStart(ctx, options.file, snapshot)
		if err != nil {
			return err
		}
		defer closeClient()
		summary, err := runSetupStart(ctx, deps, client, snapshot, filepath.Dir(options.file), deploy.RepositoryRoot, step.Space, setupStartPollDefault)
		if err != nil {
			return err
		}
		for _, note := range summary.Notes {
			fmt.Fprintln(os.Stderr, note)
		}
		return unitdeploy.MarkStep(deploy.StateDirectory, unitdeploy.StartedStep(step.Space))
	case deployscan.StepVerify:
		return deps.verifySpace(ctx, options.file, snapshot, deploy, step.Space)
	}
	return fmt.Errorf("unknown install step %s", step.Kind)
}
