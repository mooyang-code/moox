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

	setupdataformat "github.com/mooyang-code/moox/modules/cli/internal/setup/dataformat"
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
	yes, keep, noBackup            bool
	source                         setupSource
}

// 安装步骤的种类。
const (
	installOperator      = "operator"
	installFirewall      = "firewall"
	installDeploy        = "deploy"
	installInitMetadata  = "init-metadata"
	installReady         = "ready"
	installRestoreBackup = "restore-backup"
	installInit          = "init"
	installStart         = "start"
	installVerify        = "verify"
)

// updateStepKinds 是已装好系统选择 --keep（保留数据更新）时执行的步骤：
// 去掉初始化、自动导入备份、启动空间与验收，部署顺序和安装一致。
// 不跑 setup init（含 --stage storage）：不重写元数据与初始秘密；不跑 setup start：手工停用的任务保持停用，不重建云函数。
var updateStepKinds = []string{installOperator, installFirewall, installDeploy, installReady}

// setupUpdateNote 是已装好系统保留数据更新时固定的说明。
const setupUpdateNote = "数据保留；只换二进制与配置，不重新初始化、不启动空间：手工停用的任务保持停用，组件按 moox.toml 的声明启停（setup stop 临时停掉的会被重新拉起）。新版本带来的业务空间、数据集、内置采集任务和云函数代码包不会随保留更新到位，" +
	"在 moox.toml 里新开了云函数采集、日志服务或换了云账号也不会生效，需要时再运行 setup init（只建不改）。" +
	"配置每次部署都从这个版本的模板重新渲染，手工调过的值要写在 [overrides] 里才会保留"

// setupInstallStep 是安装或更新的一步。Components 为空表示这台主机的全部组件。
type setupInstallStep struct {
	Kind       string   `json:"kind"`
	Host       string   `json:"host,omitempty"`
	Components []string `json:"components,omitempty"`
	Command    string   `json:"command"`
}

// setupUpdateMove 是换了主机的组件：旧主机上会停掉删除，新主机上从空数据开始，数据不迁移。
type setupUpdateMove struct {
	Component string `json:"component"`
	From      string `json:"from"`
	To        string `json:"to"`
}

// setupUpdateHost 是保留更新预览里的一台主机：要更新、要新装、要删掉的组件。
type setupUpdateHost struct {
	Host    string   `json:"host"`
	Version string   `json:"version,omitempty"`
	Update  []string `json:"update"`
	Install []string `json:"install"`
	Remove  []string `json:"remove"`
}

// setupInstallState 保存一次 setup install 执行过程中产生的备份恢复摘要。
type setupInstallState struct {
	restoreBackup *setupRestoreBackupSummary
}

// dependencyComponents 是控制机上其余组件启动前就要在的组件：Collector 等启动时要连 EventBus 与 Storage 核对元数据。
var dependencyComponents = []string{"eventbus", "storage-primary", "storage-view"}

// externalProgramComponents 是外部程序：它们依赖控制机上的管理端与存储机，所在的主机排在最后装。
var externalProgramComponents = []string{"factor-engine", "archive-worker"}

func hostHasAny(manifest *topology.Config, host string, components []string) bool {
	return slices.ContainsFunc(manifest.Deployments[host], func(id string) bool { return slices.Contains(components, id) })
}

// setupInstallSteps 排出完整安装顺序：这台电脑的 moox-cli 签名私钥（已有就复用，重跑不换）；防火墙；控制机先装 Admin 与主机级组件（别的主机要向它要身份）；
// 再装带 EventBus、Storage 的主机；导入业务空间与元数据（Collector 启动要它）；控制机其余组件；其余主机（外部程序所在的排最后）；
// 统一等一次全部就绪；自动按覆盖导入待恢复的密钥与告警渠道备份（无待导入备份则跳过并提示）；完整初始化（云账号、日志服务、采集计划、告警渠道）；启动空间；验收。
func setupInstallSteps(manifest *topology.Config, spaceID string) []setupInstallStep {
	spaceLabel := strings.TrimSpace(spaceID)
	if spaceLabel == "" {
		spaceLabel = "<空间>"
	}
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
		setupInstallStep{Kind: installRestoreBackup, Command: "自动导入待恢复的密钥与告警渠道备份（moox.backup，按覆盖导入）"},
		setupInstallStep{Kind: installInit, Command: "moox-cli setup init --storage-host " + storage},
		setupInstallStep{Kind: installStart, Command: "moox-cli setup start --space-id " + spaceLabel},
		setupInstallStep{Kind: installVerify, Command: "moox-cli setup verify --space-id " + spaceLabel},
	)
}

// setupUpdateSteps 排出已装好系统保留数据更新的顺序：与安装相同的部署顺序，去掉初始化、导入备份、启动空间与验收。
func setupUpdateSteps(manifest *topology.Config) []setupInstallStep {
	return slices.DeleteFunc(setupInstallSteps(manifest, ""), func(step setupInstallStep) bool { return !slices.Contains(updateStepKinds, step.Kind) })
}

func setupUpdateHosts(statuses []deploy.HostStatus) []setupUpdateHost {
	hosts := make([]setupUpdateHost, 0, len(statuses))
	for _, status := range statuses {
		host := setupUpdateHost{Host: status.Host, Version: status.Version, Update: []string{}, Install: []string{}, Remove: []string{}}
		for _, component := range status.Components {
			switch {
			case component.Extra:
				host.Remove = append(host.Remove, component.ID)
			case component.Deployed():
				host.Update = append(host.Update, component.ID)
			default:
				host.Install = append(host.Install, component.ID)
			}
		}
		hosts = append(hosts, host)
	}
	return hosts
}

// setupUpdateMoves 找出从一台主机挪到另一台的组件：在一台上要删掉、在另一台上要新装。
func setupUpdateMoves(hosts []setupUpdateHost) []setupUpdateMove {
	moves := []setupUpdateMove{}
	for _, from := range hosts {
		for _, id := range from.Remove {
			for _, to := range hosts {
				if to.Host != from.Host && slices.Contains(to.Install, id) {
					moves = append(moves, setupUpdateMove{Component: id, From: from.Host, To: to.Host})
				}
			}
		}
	}
	return moves
}

// setupUpdateMissingCore 返回还没装过的控制机与存储机：已装好系统出现核心主机缺失时不能只做保留更新。
func setupUpdateMissingCore(manifest *topology.Config, statuses []deploy.HostStatus) []string {
	var missing []string
	for _, host := range []string{manifest.ControlHost().Name, manifest.StorageHost().Name} {
		at := slices.IndexFunc(statuses, func(status deploy.HostStatus) bool { return status.Host == host })
		if (at < 0 || !statuses[at].Installed) && !slices.Contains(missing, host) {
			missing = append(missing, host)
		}
	}
	return missing
}

func defaultSetupMarkInstallComplete(ctx context.Context, snapshot *setupSnapshot, spaceID string) error {
	host := snapshot.Manifest.ControlHost()
	target, err := dialSetupHost(ctx, host)
	if err != nil {
		return err
	}
	defer target.Close()
	return deploy.WriteInstallComplete(ctx, target, deploy.Layout{Root: snapshot.Manifest.Paths.DeployRoot}, deploy.InstallCompleteMarker{
		CompletedAt: time.Now().UTC().Format(time.RFC3339),
		SpaceID:     spaceID,
		Version:     Version,
		Commit:      GitCommit,
	})
}

func newSetupInstallCommand(deps setupDeps) *cobra.Command {
	var options setupInstallOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "统一部署入口：新安装、续装、保留数据更新与全部覆盖重装（默认只预检）",
		Long: `统一部署入口，按控制机上的完成标记（install-complete.json）与各主机 version.json 区分三种状态：
1. 没装过（fresh）：直接执行全新安装（需带 --space-id），验收通过后在控制机写入完成标记。
2. 没装完（incomplete）：预检并排列出 --keep（保留数据接着装完）与 --wipe <摘要>（全部覆盖重装），并附带数据格式比对结果；带 --yes 时必须明确二选一。
3. 已装好（installed）：预检并排列出 --keep（保留数据更新二进制与配置，不重新初始化、不重新启用手工停掉的任务，不要求 --space-id）与 --wipe <摘要>（全部覆盖重装）；带 --yes 时必须明确二选一。
数据格式有变化或未知时，--keep 会被拒绝并列出不兼容的组件。
--wipe 会先自动备份全部非系统密钥与告警渠道（moox.backup，交易账户不纳入），再清空全部主机的部署根（含归档文件 archive/，它是唯一可能补不回来的数据），然后全新安装并在全部组件就绪后、完整初始化之前自动覆盖导入备份。备份失败会停下不清，除非明确带 --no-backup。
配置模板、云函数代码包、元数据种子、默认因子任务和默认策略都从 --source-dir（默认当前目录）取；进度逐行输出到标准错误。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.spaceID = strings.TrimSpace(options.spaceID)
			options.wipe = strings.TrimSpace(options.wipe)
			if options.keep && options.wipe != "" {
				return errors.New("install_choice_conflict: --keep 与 --wipe 不能同时指定，请明确二选一")
			}
			if options.noBackup && options.yes && options.wipe == "" {
				return errors.New("no_backup_requires_wipe: --no-backup 只能配合 --wipe 使用")
			}
			snapshot, err := deps.load(options.file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := options.source.resolve(); err != nil {
				return err
			}
			return runSetupInstall(cmd, deps, snapshot, options)
		},
	}
	cmd.Flags().StringVar(&options.file, "file", defaultSetupFile, "moox.toml 的路径（同目录要有 moox.secrets.toml）")
	cmd.Flags().StringVar(&options.configDir, "config-dir", defaultSetupConfigDir, "元数据等种子目录（不写时取 --source-dir 下的 config/setup）")
	cmd.Flags().StringVar(&options.spaceID, "space-id", "", "安装后启动的空间，比如 crypto（已装好并选 --keep 时不需要）")
	cmd.Flags().BoolVar(&options.yes, "yes", false, "确认执行，不加只预检")
	cmd.Flags().BoolVar(&options.keep, "keep", false, "目标机装过时保留数据：没装完则接着装完；已装好则只更新程序与配置（格式变化时拒绝）")
	cmd.Flags().StringVar(&options.wipe, "wipe", "", "目标机装过时全部覆盖：传入预检输出的 wipe.digest，先备份再清空重装并自动导入，范围变了会拒绝")
	cmd.Flags().BoolVar(&options.noBackup, "no-backup", false, "配合 --wipe 使用：跳过清空前的在线自动备份（本机已有备份会被沿用并标记待导入）")
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
	formatReport := *status.DataFormat
	fullSteps := setupInstallSteps(snapshot.Manifest, options.spaceID)

	if !options.yes {
		return previewSetupInstall(cmd, deps, snapshot, options, status, formatReport, fullSteps)
	}

	// 执行前先做状态与参数校验，任何拒绝都不动目标机。
	switch status.Route {
	case deployscan.RouteFresh:
		if options.wipe != "" {
			return errors.New("wipe_not_needed: 目标机没有装过，去掉 --wipe 再运行")
		}
		if options.keep {
			return errors.New("keep_not_needed: 目标机没有装过，去掉 --keep 直接运行 setup install --space-id <空间> --yes")
		}
		if options.spaceID == "" {
			return errors.New("setup install 新安装要带 --space-id（例如 --space-id crypto）")
		}
		if err := setupStartPrerequisites(snapshot.Manifest, options.spaceID); err != nil {
			return err
		}
		if len(status.ManualSteps) != 0 {
			return fmt.Errorf("manual_steps_required: 先做完这些再重跑：%s", strings.Join(status.ManualSteps, "；"))
		}
		if err := setupdataformat.IncompatibleError(formatReport); err != nil {
			return err
		}
		return executeFullInstall(cmd, deps, snapshot, options, fullSteps, "fresh", nil)

	case deployscan.RouteIncomplete, deployscan.RouteInstalled:
		if !options.keep && options.wipe == "" {
			return fmt.Errorf("install_choice_required: 目标机已装过（状态：%s），带 --yes 执行时必须明确二选一，没有默认值：保留数据请加 --keep，全部覆盖请加 --wipe <预检输出的 wipe.digest>；先不带 --yes 查看预检与数据格式比对结果", status.Route)
		}
		if options.keep {
			return executeKeepInstall(cmd, deps, snapshot, options, status, formatReport, fullSteps)
		}
		return executeWipeInstall(cmd, deps, snapshot, options, status, fullSteps)

	default:
		return fmt.Errorf("unknown deployment route %q", status.Route)
	}
}

func previewSetupInstall(
	cmd *cobra.Command,
	deps setupDeps,
	snapshot *setupSnapshot,
	options setupInstallOptions,
	status *setupDeployment,
	formatReport setupdataformat.Report,
	fullSteps []setupInstallStep,
) error {
	// 没装过：直接展示新安装预检，不问保留还是覆盖。
	if status.Route == deployscan.RouteFresh {
		if options.spaceID != "" {
			if err := setupStartPrerequisites(snapshot.Manifest, options.spaceID); err != nil {
				return err
			}
		}
		preview := map[string]any{
			"action":         "install",
			"route":          status.Route,
			"steps":          fullSteps,
			"backup_restore": previewRestoreBackup(options.file, snapshot),
			"manual_steps":   status.ManualSteps,
			"warnings":       status.Warnings,
			"notes":          status.Notes,
		}
		if !formatReport.CanKeep {
			preview["data_format"] = formatReport
		}
		note := "目标机没装过，将执行全新安装；确认后加 --yes 执行"
		if options.spaceID == "" {
			note += "（执行时要带 --space-id <空间>）"
		}
		preview["note"] = note
		return writePreview(cmd, preview)
	}

	// 装过（没装完或已装好）：并排列出 --keep 与 --wipe 两个选项各自会做什么，并带上数据格式比对结果。
	if options.spaceID != "" && !(status.Route == deployscan.RouteInstalled && options.keep) {
		if err := setupStartPrerequisites(snapshot.Manifest, options.spaceID); err != nil {
			return err
		}
	}

	keepOption := buildKeepOptionPreview(snapshot.Manifest, options.spaceID, status, formatReport, fullSteps)

	var wipePlan deployscan.Plan
	var wipeOption map[string]any
	wipeReports, wipeScanErr := deps.scanDeployment(cmd.Context(), snapshot, deployscan.Options{Sizes: true, Wipe: true})
	if wipeScanErr != nil {
		wipeOption = map[string]any{
			"available":     false,
			"flag":          "--wipe <摘要>",
			"summary":       "全部覆盖（先自动备份密钥与告警渠道，再清空全部主机部署根并全新安装，最后自动导入备份）",
			"reject_reason": wipeScanErr.Error(),
			"note":          "当前有主机无法连接，无法计算全部覆盖的清空范围；如只需保留数据升级请选 --keep，要全部覆盖请先让全部主机可达或从 moox.toml 移除停用主机",
		}
	} else {
		wipePlan = deployscan.BuildPlan(wipeReports)
		backupPreview := previewWipeBackup(cmd.Context(), deps, options.file, snapshot, options.noBackup)
		wipeOption = buildWipeOptionPreview(snapshot.Manifest, options.spaceID, wipePlan, backupPreview, fullSteps)
	}

	stateDesc := "上次安装未完成（没装完）"
	if status.Route == deployscan.RouteInstalled {
		stateDesc = "已完成安装（已装好）"
	}
	note := fmt.Sprintf("目标机已装过（%s），带 --yes 执行时必须明确二选一：--keep（保留数据）或 --wipe <摘要>（全部覆盖），没有默认值。"+
		"选择 --wipe 时归档文件（archive/）会随部署根一起清掉，它是唯一可能补不回来的数据；非系统密钥与告警渠道会在清空前自动备份、重装后自动导入，交易账户与组合账户不纳入备份", stateDesc)
	if wipeScanErr == nil {
		if tradeNote := wipeTradeNote(snapshot.Manifest, wipePlan); tradeNote != "" {
			note += "。" + tradeNote
		}
	}

	preview := map[string]any{
		"action":       "install",
		"route":        status.Route,
		"data_format":  formatReport,
		"options":      map[string]any{"keep": keepOption, "wipe": wipeOption},
		"manual_steps": status.ManualSteps,
		"warnings":     status.Warnings,
		"notes":        status.Notes,
		"note":         note,
	}
	if wipeScanErr == nil {
		preview["wipe"] = wipePlan
		preview["backup"] = wipeOption["backup"]
	}
	return writePreview(cmd, preview)
}

func buildKeepOptionPreview(
	manifest *topology.Config,
	spaceID string,
	status *setupDeployment,
	formatReport setupdataformat.Report,
	fullSteps []setupInstallStep,
) map[string]any {
	available := formatReport.CanKeep
	var rejectReason string
	if !formatReport.CanKeep {
		rejectReason = setupdataformat.IncompatibleError(formatReport).Error()
	}

	if status.Route == deployscan.RouteInstalled {
		hosts := setupUpdateHosts(status.Components)
		moves := setupUpdateMoves(hosts)
		steps := setupUpdateSteps(manifest)
		if missing := setupUpdateMissingCore(manifest, status.Components); len(missing) != 0 && available {
			available = false
			rejectReason = fmt.Sprintf("update_core_missing: 主机 %s 还没装过（控制机或存储机），不能只做保留更新；请改走 --wipe 全部覆盖或先排查该主机", strings.Join(missing, "、"))
		}
		opt := map[string]any{
			"available": available,
			"mode":      "update",
			"flag":      "--keep",
			"command":   "moox-cli setup install --keep --yes",
			"summary":   "保留数据更新：按安装顺序重新部署全部主机的程序与配置（不重新初始化、不重新启用手工停掉的任务，不要求 --space-id）",
			"hosts":     hosts,
			"moves":     moves,
			"steps":     steps,
			"note":      setupUpdateNote,
		}
		if rejectReason != "" {
			opt["reject_reason"] = rejectReason
		}
		return opt
	}

	// RouteIncomplete：没装完，选 --keep 接着装完。
	spaceArg := spaceID
	if spaceArg == "" {
		spaceArg = "<空间>"
	}
	note := "上次安装未完成，保留已有数据并接着跑完剩余安装步骤（不清空数据，最终完成初始化、启动空间、验收并写入完成标记）"
	if spaceID == "" {
		note += "；执行时要带 --space-id <空间>"
	}
	opt := map[string]any{
		"available": available,
		"mode":      "resume",
		"flag":      "--keep",
		"command":   fmt.Sprintf("moox-cli setup install --space-id %s --keep --yes", spaceArg),
		"summary":   "保留数据继续装完：重跑安装流程，已装好且未变化的组件不重启，完成后验收并写入完成标记",
		"steps":     fullSteps,
		"note":      note,
	}
	if rejectReason != "" {
		opt["reject_reason"] = rejectReason
	}
	return opt
}

func buildWipeOptionPreview(
	manifest *topology.Config,
	spaceID string,
	plan deployscan.Plan,
	backupPreview setupBackupPreview,
	fullSteps []setupInstallStep,
) map[string]any {
	spaceArg := spaceID
	if spaceArg == "" {
		spaceArg = "<空间>"
	}
	note := "全部覆盖会清空全部主机的部署根（含归档文件 archive/，它是唯一可能补不回来的数据；数据全部删除，不分模块）。" +
		"清空前自动备份全部非系统密钥与告警渠道，全部组件就绪后自动覆盖导入；交易账户与组合账户不纳入备份"
	if spaceID == "" {
		note += "。执行 --wipe 时必须带 --space-id <空间>"
	}
	if tradeNote := wipeTradeNote(manifest, plan); tradeNote != "" {
		note += "。" + tradeNote
	}
	return map[string]any{
		"available": true,
		"mode":      "wipe",
		"flag":      "--wipe " + plan.Digest,
		"command":   fmt.Sprintf("moox-cli setup install --space-id %s --wipe %s --yes", spaceArg, plan.Digest),
		"summary":   "全部覆盖：先自动备份密钥与告警渠道 → 清空全部主机的部署根 → 全新安装 → 自动覆盖导入备份 → 验收",
		"plan":      plan,
		"backup":    backupPreview,
		"steps":     fullSteps,
		"note":      note,
	}
}

func executeKeepInstall(
	cmd *cobra.Command,
	deps setupDeps,
	snapshot *setupSnapshot,
	options setupInstallOptions,
	status *setupDeployment,
	formatReport setupdataformat.Report,
	fullSteps []setupInstallStep,
) error {
	if err := setupdataformat.IncompatibleError(formatReport); err != nil {
		return err
	}
	if len(status.ManualSteps) != 0 {
		return fmt.Errorf("manual_steps_required: 先做完这些再重跑：%s", strings.Join(status.ManualSteps, "；"))
	}
	if status.Route == deployscan.RouteInstalled {
		if missing := setupUpdateMissingCore(snapshot.Manifest, status.Components); len(missing) != 0 {
			return fmt.Errorf("update_core_missing: 主机 %s 还没装过（控制机或存储机），不能只做保留更新；请改走 --wipe 全部覆盖或先排查该主机", strings.Join(missing, "、"))
		}
		steps := setupUpdateSteps(snapshot.Manifest)
		started := time.Now()
		state := &setupInstallState{}
		if err := runSetupSteps(cmd, deps, snapshot, options, steps, len(steps), "install_step_failed", state); err != nil {
			return err
		}
		return writeSetupJSON(cmd, map[string]any{
			"status":  "updated",
			"mode":    "keep",
			"seconds": time.Since(started).Round(time.Second).Seconds(),
		})
	}

	// RouteIncomplete：没装完，接着装完。
	if options.spaceID == "" {
		return errors.New("setup install 续装（--keep）要带 --space-id（例如 --space-id crypto）")
	}
	if err := setupStartPrerequisites(snapshot.Manifest, options.spaceID); err != nil {
		return err
	}
	return executeFullInstall(cmd, deps, snapshot, options, fullSteps, "resume", nil)
}

func executeWipeInstall(
	cmd *cobra.Command,
	deps setupDeps,
	snapshot *setupSnapshot,
	options setupInstallOptions,
	status *setupDeployment,
	fullSteps []setupInstallStep,
) error {
	ctx := cmd.Context()
	if options.spaceID == "" {
		return errors.New("setup install --wipe 要带 --space-id（例如 --space-id crypto）")
	}
	if err := setupStartPrerequisites(snapshot.Manifest, options.spaceID); err != nil {
		return err
	}
	if len(status.ManualSteps) != 0 {
		return fmt.Errorf("manual_steps_required: 先做完这些再重跑：%s", strings.Join(status.ManualSteps, "；"))
	}
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{Sizes: true, Wipe: true})
	if err != nil {
		return err
	}
	plan := deployscan.BuildPlan(reports)
	if options.wipe != plan.Digest {
		return fmt.Errorf("wipe_scope_changed: 清空范围和预检不一致（现在是 %s），没有动任何主机；先不带 --yes 预检，让用户确认后带 --wipe", plan.Digest)
	}
	backupSummary, err := prepareBackupBeforeWipe(ctx, deps, options.file, snapshot, plan, options.noBackup)
	if err != nil {
		return err
	}
	if _, err := executeSetupWipe(ctx, deps, snapshot, plan, options.source.operatorDir); err != nil {
		return err
	}
	return executeFullInstall(cmd, deps, snapshot, options, fullSteps, "wipe", backupSummary)
}

func executeFullInstall(
	cmd *cobra.Command,
	deps setupDeps,
	snapshot *setupSnapshot,
	options setupInstallOptions,
	steps []setupInstallStep,
	mode string,
	backupSummary *setupWipeBackupSummary,
) error {
	started := time.Now()
	state := &setupInstallState{}
	if err := runSetupSteps(cmd, deps, snapshot, options, steps, len(steps), "install_step_failed", state); err != nil {
		return err
	}
	if err := deps.markInstallComplete(cmd.Context(), snapshot, options.spaceID); err != nil {
		return fmt.Errorf("install_complete_marker_failed: 安装与验收已通过，但写控制机完成标记失败：%w", err)
	}
	output := map[string]any{
		"status":   "installed",
		"mode":     mode,
		"space_id": options.spaceID,
		"seconds":  time.Since(started).Round(time.Second).Seconds(),
	}
	if backupSummary != nil {
		output["backup"] = backupSummary
	}
	if state.restoreBackup != nil {
		output["backup_restore"] = state.restoreBackup
	}
	return writeSetupJSON(cmd, output)
}

// runSetupSteps 按顺序执行安装或保留更新的步骤，进度逐行写到标准错误（[第几步/total]）；
// 一步失败就停下，报错带错误码和单独重跑这一步的命令。
func runSetupSteps(cmd *cobra.Command, deps setupDeps, snapshot *setupSnapshot, options setupInstallOptions, steps []setupInstallStep, total int, code string, state *setupInstallState) error {
	progress := &lockedWriter{out: cmd.ErrOrStderr()}
	for i, step := range steps {
		fmt.Fprintf(progress, "[%d/%d] %s\n", i+1, total, step.Command)
		if err := executeSetupInstallStep(cmd, deps, snapshot, options, step, state); err != nil {
			return fmt.Errorf("%s: 第 %d 步（%s）失败：%w。修好后重跑同一条命令，每一步都可以重做；单独重跑这一步：%s", code, i+1, step.Kind, err, step.Command)
		}
	}
	return nil
}

func executeSetupInstallStep(cmd *cobra.Command, deps setupDeps, snapshot *setupSnapshot, options setupInstallOptions, step setupInstallStep, state *setupInstallState) error {
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
	case installRestoreBackup:
		summary, err := deps.restoreBackup(ctx, options.file, snapshot)
		if err != nil {
			return err
		}
		if state != nil {
			state.restoreBackup = summary
		}
		if summary != nil && summary.Note != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), summary.Note)
		}
		return nil
	case installInit:
		bundle, err := deps.loadInitBundle(options.source.configDir(cmd, options.configDir))
		if err != nil {
			return err
		}
		_, err = deps.initStorage(ctx, snapshot, storageHost, options.source.root, bundle)
		return err
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

// waitSetupAllReady 统一等一次全部主机上的全部组件就绪：按 moox.toml 的声明，停用的组件不等，停用的主机整台不等
// （它只留 host-gateway 在跑，而且可能连不上）。崩溃后正在自动重启的组件也要等到就绪。
func waitSetupAllReady(ctx context.Context, deps setupDeps, snapshot *setupSnapshot) error {
	var hosts []topology.Host
	for _, host := range snapshot.Manifest.Hosts() {
		if host.Enabled {
			hosts = append(hosts, host)
		}
	}
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
			components = slices.DeleteFunc(components, func(id string) bool { return !snapshot.Manifest.UnitEnabled(host.Name, id) })
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
