package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// setupVerifyTimeout 是 setup install 里验收的时限；setupReadyTimeout 是最后统一等全部组件就绪的时限。
const (
	setupVerifyTimeout = 10 * time.Minute
	setupReadyTimeout  = 5 * time.Minute
)

// setupInstallOptions 是一次 setup install 的参数。
type setupInstallOptions struct {
	file, configDir, spaceID, wipe string
	yes                            bool
	source                         setupSource
}

// 安装步骤的种类。
const (
	installOperator     = "operator"
	installFirewall     = "firewall"
	installDeploy       = "deploy"
	installInitMetadata = "init-metadata"
	installReady        = "ready"
	installInit         = "init"
	installBackup       = "backup"
	installStart        = "start"
	installVerify       = "verify"
)

// setupInstallStep 是安装的一步。Components 为空表示这台主机的全部组件。
type setupInstallStep struct {
	Kind       string   `json:"kind"`
	Host       string   `json:"host,omitempty"`
	Components []string `json:"components,omitempty"`
	Command    string   `json:"command"`
}

// dependencyComponents 是控制机上其余组件启动前就要在的组件：Collector 等启动时要连 EventBus 与 Storage 核对元数据。
var dependencyComponents = []string{"eventbus", "storage-primary", "storage-view"}

// externalProgramComponents 是外部程序：它们依赖控制机上的管理端与存储机，所在的主机排在最后装。
var externalProgramComponents = []string{"factor-engine", "archive-worker"}

func hostHasAny(manifest *topology.Config, host string, components []string) bool {
	return slices.ContainsFunc(manifest.Deployments[host], func(id string) bool { return slices.Contains(components, id) })
}

// setupInstallSteps 排出安装顺序：这台电脑的 moox-cli 签名私钥（已有就复用，重跑不换）；防火墙；控制机先装 Admin 与主机级组件（别的主机要向它要身份）；
// 再装带 EventBus、Storage 的主机；导入业务空间与元数据（Collector 启动要它）；控制机其余组件；其余主机（外部程序所在的排最后）；
// 统一等一次全部就绪；完整初始化（云账号、日志服务、采集计划、告警渠道——这些要等 Collector、Monitor 起来）；备份配置；启动空间；验收。
func setupInstallSteps(manifest *topology.Config, spaceID string) []setupInstallStep {
	control := manifest.ControlHost().Name
	first := []string{"admin", "host-gateway", "host-agent"}
	for _, id := range dependencyComponents {
		if slices.Contains(manifest.Deployments[control], id) {
			first = append(first, id)
		}
	}
	deployStep := func(host string, components []string) setupInstallStep {
		command := "moox-cli setup deploy --host " + host
		if len(components) != 0 {
			command += " --component " + strings.Join(components, ",")
		}
		return setupInstallStep{Kind: installDeploy, Host: host, Components: components, Command: command}
	}
	steps := []setupInstallStep{{Kind: installOperator, Command: "moox-cli setup operator"}, {Kind: installFirewall, Command: "moox-cli setup firewall --yes"}, deployStep(control, first)}
	var later, last []string
	for _, host := range manifest.Hosts() {
		switch {
		case host.Name == control:
		case hostHasAny(manifest, host.Name, dependencyComponents):
			steps = append(steps, deployStep(host.Name, nil))
		case hostHasAny(manifest, host.Name, externalProgramComponents):
			last = append(last, host.Name)
		default:
			later = append(later, host.Name)
		}
	}
	storage := manifest.StorageHost().Name
	steps = append(steps, setupInstallStep{Kind: installInitMetadata, Command: "moox-cli setup init --stage storage --storage-host " + storage}, deployStep(control, nil))
	for _, host := range append(later, last...) {
		steps = append(steps, deployStep(host, nil))
	}
	return append(steps,
		setupInstallStep{Kind: installReady, Command: "moox-cli setup status"},
		setupInstallStep{Kind: installInit, Command: "moox-cli setup init --storage-host " + storage},
		setupInstallStep{Kind: installBackup, Command: "moox-cli config backup"},
		setupInstallStep{Kind: installStart, Command: "moox-cli setup start --space-id " + spaceID},
		setupInstallStep{Kind: installVerify, Command: "moox-cli setup verify --space-id " + spaceID},
	)
}

func newSetupInstallCommand(deps setupDeps) *cobra.Command {
	var options setupInstallOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "一条命令装完：防火墙、逐台部署、初始化、启动和验收（默认只预检）",
		Long: `不带 --yes 只读预检：输出路线、安装步骤、要用户先在目标机上做的事，以及（装过时）清空范围。
带 --yes 按顺序执行：控制机先装 Admin、主机网关与 HostAgent，再装带 EventBus、Storage 的主机，导入业务空间与元数据，
然后装控制机其余组件和其余主机，统一等一次全部就绪，再做完整初始化、备份配置、启动空间和验收。
中途失败直接重跑同一条命令：每一步都可以重做，没有变化的组件不会重启。
配置模板、云函数代码包、元数据种子、默认因子任务和默认策略都从 --source-dir（默认当前目录）取：要装哪个分支或工作副本的代码，就在那里运行或把它指过去。
目标机装过时不带 --wipe 是接着做完上次没装完的安装（数据保留）；要卸载后重装，带 --wipe <预检输出的清空摘要>。进度逐行输出到标准错误。
重跑会再执行一次 setup start：手工停用的内置采集任务与默认因子任务成员会被重新启用，走云函数的空间会重建云函数。
装好的系统要覆盖更新（换版本或改配置后重新部署全部主机）用 setup update，不要重跑 setup install。`,
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
			if err := options.source.resolve(); err != nil {
				return err
			}
			if err := setupStartPrerequisites(snapshot.Manifest, options.spaceID); err != nil {
				return err
			}
			return runSetupInstall(cmd, deps, snapshot, options)
		},
	}
	cmd.Flags().StringVar(&options.file, "file", defaultSetupFile, "moox.toml 的路径（同目录要有 moox.secrets.toml）")
	cmd.Flags().StringVar(&options.configDir, "config-dir", defaultSetupConfigDir, "元数据等种子目录（不写时取 --source-dir 下的 config/setup）")
	cmd.Flags().StringVar(&options.spaceID, "space-id", "", "安装后启动的空间，比如 crypto")
	cmd.Flags().BoolVar(&options.yes, "yes", false, "确认执行，不加只预检")
	cmd.Flags().StringVar(&options.wipe, "wipe", "", "预检输出的清空摘要：带上就先彻底清空再装，范围变了会拒绝")
	options.source.flags(cmd)
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

func runSetupInstall(cmd *cobra.Command, deps setupDeps, snapshot *setupSnapshot, options setupInstallOptions) error {
	ctx := cmd.Context()
	status, err := setupDeploymentStatus(ctx, deps, snapshot)
	if err != nil {
		return err
	}
	steps := setupInstallSteps(snapshot.Manifest, options.spaceID)
	var plan deployscan.Plan
	if status.Route == deployscan.RouteInstalled {
		// 清空范围要带大小，单独扫一次。
		reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{Sizes: true})
		if err != nil {
			return err
		}
		plan = deployscan.BuildPlan(reports)
	}
	if !options.yes {
		preview := map[string]any{"action": "install", "route": status.Route, "steps": steps, "manual_steps": status.ManualSteps, "warnings": status.Warnings}
		if status.Route == deployscan.RouteInstalled {
			preview["wipe"] = plan
			preview["note"] = "目标机装过：带 --wipe <wipe.digest> 是卸载（彻底清空）后重新安装；不带 --wipe 是接着做完上次没装完的安装，数据保留。" +
				"不带 --wipe 重跑会再执行一次 setup start：手工停用的内置采集任务与默认因子任务成员会被重新启用，走云函数的空间会重建云函数；装好的系统要覆盖更新请用 setup update"
			if note := wipeTradeNote(snapshot.Manifest, plan); note != "" {
				preview["note"] = preview["note"].(string) + "。" + note
			}
		}
		return writePreview(cmd, preview)
	}
	if len(status.ManualSteps) != 0 {
		return fmt.Errorf("manual_steps_required: 先做完这些再重跑：%s", strings.Join(status.ManualSteps, "；"))
	}
	if options.wipe != "" {
		if status.Route != deployscan.RouteInstalled {
			return errors.New("wipe_not_needed: 目标机没有装过，去掉 --wipe 再运行")
		}
		if options.wipe != plan.Digest {
			return fmt.Errorf("wipe_scope_changed: 清空范围和预检不一致（现在是 %s），没有动任何主机；先不带 --yes 预检，让用户确认后带 --wipe", plan.Digest)
		}
		if _, err := executeSetupWipe(ctx, deps, snapshot, plan, options.source.operatorDir); err != nil {
			return err
		}
	}
	started := time.Now()
	if err := runSetupSteps(cmd, deps, snapshot, options, steps, len(steps), "install_step_failed"); err != nil {
		return err
	}
	return writeSetupJSON(cmd, map[string]any{"status": "installed", "space_id": options.spaceID, "seconds": time.Since(started).Round(time.Second).Seconds()})
}

// runSetupSteps 按顺序执行安装或覆盖更新的步骤，进度逐行写到标准错误（[第几步/total]，total 是整个流程的步数，
// 调用方只交给它前面几步时也按整个流程计数）；一步失败就停下，报错带错误码和单独重跑这一步的命令。
func runSetupSteps(cmd *cobra.Command, deps setupDeps, snapshot *setupSnapshot, options setupInstallOptions, steps []setupInstallStep, total int, code string) error {
	progress := &lockedWriter{out: cmd.ErrOrStderr()}
	for i, step := range steps {
		fmt.Fprintf(progress, "[%d/%d] %s\n", i+1, total, step.Command)
		if err := executeSetupInstallStep(cmd, deps, snapshot, options, step); err != nil {
			return fmt.Errorf("%s: 第 %d 步（%s）失败：%w。修好后重跑同一条命令，每一步都可以重做；单独重跑这一步：%s", code, i+1, step.Kind, err, step.Command)
		}
	}
	return nil
}

func executeSetupInstallStep(cmd *cobra.Command, deps setupDeps, snapshot *setupSnapshot, options setupInstallOptions, step setupInstallStep) error {
	ctx := cmd.Context()
	storageHost := snapshot.Manifest.StorageHost().Name
	switch step.Kind {
	case installOperator:
		// 公钥在下一步部署控制机时由 bootstrap 登记；已经登记过别的公钥时那一步会报错并提示 --register。
		_, _, err := deps.ensureOperatorKey(options.source.operatorDir)
		return err
	case installFirewall:
		summary, err := deps.ensureFirewall(ctx, snapshot)
		if err == nil && summary.Status != "" && summary.Status != "ready" {
			err = fmt.Errorf("firewall_incomplete: status=%s skipped=%d", summary.Status, summary.Skipped)
		}
		return err
	case installDeploy:
		_, err := deps.deployHost(ctx, options.source.deployOptions(cmd, snapshot, options.file, step.Host, step.Components))
		return err
	case installInitMetadata:
		bundle, err := deps.loadInitBundle(options.source.configDir(cmd, options.configDir))
		if err != nil {
			return err
		}
		_, err = deps.initMetadata(ctx, snapshot, storageHost, bundle)
		return err
	case installReady:
		return waitSetupAllReady(ctx, deps, snapshot)
	case installInit:
		bundle, err := deps.loadInitBundle(options.source.configDir(cmd, options.configDir))
		if err != nil {
			return err
		}
		_, err = deps.initStorage(ctx, snapshot, storageHost, options.source.root, bundle)
		return err
	case installBackup:
		return deps.backupConfig(ctx, options.file)
	case installStart:
		client, closeClient, err := deps.openStart(ctx, options.file, snapshot)
		if err != nil {
			return err
		}
		defer closeClient()
		summary, err := runSetupStart(ctx, deps, client, snapshot, options.source.root, options.spaceID, setupStartPollDefault)
		for _, note := range summary.Notes {
			fmt.Fprintln(cmd.ErrOrStderr(), note)
		}
		return err
	case installVerify:
		return deps.verifySpace(ctx, options.file, snapshot, options.spaceID)
	}
	return fmt.Errorf("unknown install step %s", step.Kind)
}

// waitSetupAllReady 统一等一次全部主机上的全部组件就绪（已被停用的不等）。
// 按是否停用（is-enabled）排除，不按当时是否在运行：崩溃后正在自动重启的组件也要等到就绪。
func waitSetupAllReady(ctx context.Context, deps setupDeps, snapshot *setupSnapshot) error {
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{})
	if err != nil {
		return err
	}
	hosts := snapshot.Manifest.Hosts()
	failures := make([]string, len(hosts))
	errs := make([]error, len(hosts))
	var workers sync.WaitGroup
	for i, host := range hosts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			components, err := snapshot.Manifest.HostComponents(host.Name)
			if err != nil {
				errs[i] = err
				return
			}
			// 用户停用的组件不等，其余的都要等到就绪。
			at := slices.IndexFunc(reports, func(report deployscan.HostReport) bool { return report.Host == host.Name })
			if at >= 0 {
				components = slices.DeleteFunc(components, func(id string) bool { return slices.Contains(reports[at].Disabled, deploy.UnitName(id)) })
			}
			pending, err := deps.waitReady(ctx, host, components, setupReadyTimeout)
			if err != nil {
				errs[i] = err
				return
			}
			for id, detail := range pending {
				failures[i] += fmt.Sprintf(" %s/%s（%s）", host.Name, id, detail)
			}
		}()
	}
	workers.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if all := strings.Join(failures, ""); all != "" {
		return fmt.Errorf("not_ready: 这些组件到时限还没就绪：%s。看日志：moox-cli setup logs --host <主机> --component <组件>", strings.TrimSpace(all))
	}
	return nil
}
