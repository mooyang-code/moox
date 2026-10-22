package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/sysctl"
	"github.com/spf13/cobra"
)

// moox-cli system reset：系统级重置（设计第 6 节）。编排逻辑在 packages/sysctl，这里只是入口。

type systemResetOptions struct {
	Scope        string
	SystemName   string
	Env          string
	StateDir     string
	StoragePkg   string
	StorageConf  string
	StorageSeed  string
	AdminDB      string
	Token        string
	ConfirmName  string
	OperationID  string
	ExternalDone []string
	Yes          bool
}

var systemResetFlags = systemResetOptions{
	Scope: "all", Env: envOrDefault("MOOX_SYSTEM_ENV", "production"), SystemName: os.Getenv("MOOX_SYSTEM_NAME"),
	StoragePkg: "/data/moox/storage/current", StorageConf: "/data/moox/storage/current/storage-view/config/storage.yaml",
	StorageSeed: "config/setup/metadata.yaml", AdminDB: "./data/admin.db",
}

func envOrDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func init() {
	reset := &cobra.Command{
		Use:   "reset",
		Short: "系统级重置：删除运行数据（--scope all 再删业务配置），回到新部署后、init 之前的状态",
		Long: `系统级重置。先 plan（只读）再 apply，两步：

  moox-cli system reset plan  --scope data|all --system-name <系统名>
        连接每个模块取各自的重置计划，列出每一步、保留项、不备份项和阻止条件，给出 10 分钟内有效、只能用一次的令牌。
  moox-cli system reset apply --token <令牌> --confirm-name <系统名> --yes
        系统暂停后按下游到上游逐个重置模块：策略 → Trade → 因子引擎 → 因子管理 → 采集 → 归档 →（Storage）→ Monitor →（Admin 业务部分）。
        archive-worker 暂存库、archive-worker 暂存库、Storage 与 Admin 业务部分是要在各自主机上执行的外部步骤：编排器停在那一步并列出命令，执行完用 --external-done <步骤名> 继续。
        失败或中断后用 apply --operation-id <系统重置 ID> 续做，已完成的步骤不重做；不会自动回滚。
  moox-cli system reset status   查看系统重置日志

生产环境（默认；--env test 或 MOOX_SYSTEM_ENV=test 改成测试环境）apply 要逐字输入系统名（--confirm-name）。
每个模块用重置专用密钥签名：MOOX_<模块>_HEALTH_RESET_ACCESS_KEY / _SECRET_KEY，没有设置时用 MOOX_HEALTH_RESET_ACCESS_KEY / _SECRET_KEY。
实盘账户有持仓或未完成订单时 Trade 会阻止重置。重置完成后各模块处于未初始化，要按常规流程 init → start → confirm。`,
	}
	pf := reset.PersistentFlags()
	pf.StringVar(&systemResetFlags.StateDir, "state-dir", defaultResetStateDir(), "计划与系统日志的保存目录（0600）")
	pf.StringVar(&systemResetFlags.SystemName, "system-name", systemResetFlags.SystemName, "系统名（生产环境 apply 时要逐字输入）；环境变量 MOOX_SYSTEM_NAME")
	pf.StringVar(&systemResetFlags.Env, "env", systemResetFlags.Env, "环境：production | test；环境变量 MOOX_SYSTEM_ENV")
	pf.StringVar(&systemResetFlags.StoragePkg, "storage-package-root", systemResetFlags.StoragePkg, "Storage 包根目录（只用于生成外部步骤的命令提示）")
	pf.StringVar(&systemResetFlags.StorageConf, "storage-conf", systemResetFlags.StorageConf, "storage.yaml 路径（只用于生成命令提示）")
	pf.StringVar(&systemResetFlags.StorageSeed, "storage-seed", systemResetFlags.StorageSeed, "Storage 元数据种子（只用于生成命令提示）")
	pf.StringVar(&systemResetFlags.AdminDB, "admin-db", systemResetFlags.AdminDB, "Admin 数据库路径（只用于生成命令提示）")

	plan := &cobra.Command{
		Use: "plan", Short: "系统重置计划（只读），给出令牌", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSystemResetPlan(cmd.Context(), systemFlags, systemResetFlags, cmd.OutOrStdout())
		},
	}
	plan.Flags().StringVar(&systemResetFlags.Scope, "scope", systemResetFlags.Scope, "范围：data（只清运行数据）| all（再清业务配置）")
	apply := &cobra.Command{
		Use: "apply", Short: "执行系统重置（--token）或续做（--operation-id）", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSystemResetApply(cmd.Context(), systemFlags, systemResetFlags, cmd.OutOrStdout())
		},
	}
	af := apply.Flags()
	af.StringVar(&systemResetFlags.Token, "token", "", "plan 给出的令牌")
	af.StringVar(&systemResetFlags.ConfirmName, "confirm-name", "", "生产环境：逐字输入系统名")
	af.StringVar(&systemResetFlags.OperationID, "operation-id", "", "续做未完成的系统重置")
	af.StringSliceVar(&systemResetFlags.ExternalDone, "external-done", nil, "已经人工执行完的外部步骤名（archive-worker、storage、admin）")
	af.BoolVar(&systemResetFlags.Yes, "yes", false, "确认执行（会删除数据，不可撤销；各模块先备份）")
	status := &cobra.Command{
		Use: "status", Short: "查看系统重置日志", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSystemResetStatus(systemFlags, systemResetFlags, cmd.OutOrStdout())
		},
	}
	reset.AddCommand(plan, apply, status)
	systemCmd.AddCommand(reset)
}

func defaultResetStateDir() string {
	if v := os.Getenv("MOOX_SYSTEM_RESET_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".moox-system-reset"
	}
	return filepath.Join(home, ".moox", "system-reset")
}

// externalSteps 是 Storage 与 Admin 业务部分的外部步骤；命令只作提示，编排器不会替用户登录那些主机。
func (r systemResetOptions) externalSteps(scope string) []sysctl.ExternalStep {
	storageCommands := []string{
		fmt.Sprintf("moox-cli storage reset-view-consumers --reset-all-storage-data --package-root %s --storage-conf %s --timeout 60m --yes", r.StoragePkg, r.StorageConf),
	}
	describe := "Storage：清行数据、事件流和 View 消费者（在 Storage 主机上执行）"
	if scope == string(lifecycle.ResetAll) {
		storageCommands[0] = strings.Replace(storageCommands[0], "--timeout 60m", "--restart=false", 1)
		storageCommands = append(storageCommands,
			fmt.Sprintf("moox-cli storage reset-metadata --storage-conf %s --seed %s --yes", r.StorageConf, r.StorageSeed),
			fmt.Sprintf("启动 Storage（%s/start.sh）并等就绪", r.StoragePkg),
			"moox-storage-cli activate-datasets --metadata-target ip://127.0.0.1:20100",
		)
		describe = "Storage：清行数据、事件流、View 消费者和元数据定义，重新导入种子（在 Storage 主机上执行）"
	}
	return []sysctl.ExternalStep{
		// 暂存库记着每个归档任务的水位和永久分区状态；不清的话重建同名任务会沿用旧水位，甚至重新载入旧文件。
		{Name: "archive-worker", Describe: "归档 worker：停止并删除暂存库（在 archive-worker 所在主机上执行）", After: sysctl.Archive, Commands: []string{
			"停止 archive-worker（moox-archive-worker 进程或它的 systemd 单元）",
			"删除它状态目录里的暂存库：rm -f <state_dir>/.state/staging.db <state_dir>/.state/staging.db-wal <state_dir>/.state/staging.db-shm（要留底先 cp）",
			"已归档的 Parquet/CSV 文件默认不动；要彻底重来时手工清理归档目录",
			"系统重置全部完成后再启动 archive-worker",
		}},
		{Name: "storage", Describe: describe, After: sysctl.Archive, Commands: storageCommands},
		{Name: "admin", Describe: "Admin 业务部分：活动日志、采集发布租约（all 再加业务空间，默认空间保留；在 Admin 主机上执行）",
			Commands: []string{fmt.Sprintf("moox-admin-cli reset-business --db-path %s --scope %s --yes", r.AdminDB, scope)}},
	}
}

func (o systemOptions) resetOrchestrator(r systemResetOptions, scope string, out io.Writer) (*sysctl.Orchestrator, error) {
	if r.Env != "production" && r.Env != "test" {
		return nil, fmt.Errorf("环境只能是 production 或 test，收到 %q", r.Env)
	}
	orch := o.orchestrator(out)
	orch.Reset = sysctl.ResetConfig{StateDir: r.StateDir, SystemName: r.SystemName, Production: r.Env == "production", External: r.externalSteps(scope)}
	return orch, nil
}

func runSystemResetPlan(ctx context.Context, o systemOptions, r systemResetOptions, out io.Writer) error {
	orch, err := o.resetOrchestrator(r, r.Scope, nil)
	if err != nil {
		return err
	}
	plan, err := orch.PlanReset(ctx, lifecycle.ResetScope(r.Scope))
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, plan)
	}
	fmt.Fprintf(out, "系统重置计划（范围 %s，系统名 %s，%s环境）\n", plan.Scope, plan.SystemName, map[bool]string{true: "生产", false: "测试"}[plan.Production])
	for _, m := range plan.Modules {
		fmt.Fprintf(out, "\n[%s]\n", m.Name)
		for i, s := range m.Steps {
			fmt.Fprintf(out, "  %d. %s\n", i+1, s)
		}
		if len(m.Keeps) > 0 {
			fmt.Fprintf(out, "  保留：%s\n", strings.Join(m.Keeps, "；"))
		}
		for _, n := range m.Notes {
			fmt.Fprintf(out, "  说明：%s\n", n)
		}
	}
	for _, e := range plan.External {
		fmt.Fprintf(out, "\n[外部步骤 %s]（排在 %s 之后，需要人工执行）%s\n", e.Name, firstNonEmptyString(e.After, "最后"), "\n  "+e.Describe)
		for _, c := range e.Commands {
			fmt.Fprintf(out, "    $ %s\n", c)
		}
	}
	if len(plan.Skipped) > 0 {
		fmt.Fprintf(out, "\n没有指定地址、不参与本次重置的模块：%s\n", strings.Join(plan.Skipped, "、"))
	}
	if len(plan.Blockers) > 0 {
		fmt.Fprintln(out, "\n不能执行：")
		for _, b := range plan.Blockers {
			fmt.Fprintf(out, "  - %s\n", b)
		}
		return errors.New("存在阻止重置的条件，先处理")
	}
	fmt.Fprintf(out, "\n令牌（%s 前有效，只能用一次）：%s\n", plan.Expires.Local().Format("15:04:05"), plan.Token)
	confirm := ""
	if plan.Production {
		confirm = fmt.Sprintf(" --confirm-name %s", plan.SystemName)
	}
	fmt.Fprintf(out, "执行：moox-cli system reset apply --token %s%s --yes\n", plan.Token, confirm)
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func runSystemResetApply(ctx context.Context, o systemOptions, r systemResetOptions, out io.Writer) error {
	if !r.Yes {
		return errors.New("系统重置会删除数据且不可撤销，需要加 --yes 确认（先 system reset plan 看计划）")
	}
	if r.OperationID == "" && r.Token == "" {
		return errors.New("发起系统重置需要 --token（先 system reset plan）；续做用 --operation-id")
	}
	orch, err := o.resetOrchestrator(r, "all", out)
	if err != nil {
		return err
	}
	j, err := orch.ApplyReset(ctx, sysctl.ResetApplyRequest{Token: r.Token, ConfirmName: r.ConfirmName, OperationID: r.OperationID, ExternalDone: r.ExternalDone})
	if j != nil {
		fmt.Fprintf(out, "系统重置 %s：%s\n", j.OperationID, j.State)
		printResetSteps(out, j)
	}
	switch {
	case errors.Is(err, sysctl.ErrAwaitingExternal):
		for _, s := range j.Steps {
			if s.State == "awaiting" {
				fmt.Fprintf(out, "\n请先完成外部步骤 %s：%s\n", s.Name, s.Describe)
				for _, c := range s.Commands {
					fmt.Fprintf(out, "  $ %s\n", c)
				}
				fmt.Fprintf(out, "完成后继续：moox-cli system reset apply --operation-id %s --external-done %s --yes\n", j.OperationID, s.Name)
			}
		}
		return nil
	case err != nil:
		return err
	}
	fmt.Fprintln(out, "\n系统重置完成：各模块处于未初始化。下一步按常规流程：")
	fmt.Fprintln(out, "  1. 各模块 moox-cli lifecycle init（Collector 重置后需要 fetch-plan reregister 重新登记）")
	fmt.Fprintln(out, "  2. 重新配置因子集合和策略实例，start → confirm")
	return nil
}

func printResetSteps(out io.Writer, j *sysctl.ResetJournal) {
	for _, s := range j.Steps {
		line := fmt.Sprintf("  [%s] %s", resetStateText(s.State), s.Name)
		if s.Error != "" {
			line += "：" + s.Error
		}
		fmt.Fprintln(out, line)
	}
}

func runSystemResetStatus(o systemOptions, r systemResetOptions, out io.Writer) error {
	orch, err := o.resetOrchestrator(r, "all", nil)
	if err != nil {
		return err
	}
	j, err := orch.LoadResetJournal()
	if err != nil {
		return err
	}
	if j == nil {
		fmt.Fprintln(out, "没有系统重置记录")
		return nil
	}
	if o.JSON {
		return writeLifecycleJSON(out, j)
	}
	fmt.Fprintf(out, "系统重置 %s：%s（范围 %s，开始于 %s）\n", j.OperationID, j.State, j.Scope, j.StartedAt.Local().Format("2006-01-02 15:04:05"))
	printResetSteps(out, j)
	if j.Error != "" {
		fmt.Fprintf(out, "错误：%s\n", j.Error)
	}
	return nil
}
