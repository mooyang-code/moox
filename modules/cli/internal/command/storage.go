package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var storageCmd = &cobra.Command{
	Use:     "storage",
	Aliases: []string{"存储"},
	Short:   "存储节点登记、View 维护与元数据重置",
	Long: "存储节点登记（node）与 Storage 的维护命令。维护命令（repair-view、force-rebuild-view、reset-view-consumers、reset-metadata、delete-view、purge-dataset-events、period-inventory）" +
		"按 --file（moox.toml）经 SSH 在存储机上执行部署包里的 moox-storage-cli，不需要本机装存储程序。历史数据导入请使用 moox-cli import。",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(storageCmd)
	for _, action := range storageMaintenanceActions {
		storageCmd.AddCommand(action.command())
	}
	storageCmd.AddCommand(newStorageNodeCommand())
}

// storageMaintenanceAction 描述一个在存储机上执行的维护动作。参数交给存储机上的 moox-storage-cli 解析
// （两边版本一致，不在这里重复声明一套），这里只负责：补齐存储机上的路径参数、没有 --yes 时改为预览、
// 把输出同时留在主机日志里、透传退出码。
type storageMaintenanceAction struct {
	Action  string
	Aliases []string
	Short   string
	Long    string
	// Defaults 是存储机上的路径参数默认值，用户没给时补上；storageRoot 是 <storage_root>/current。
	Defaults func(storageRoot string) map[string]string
	// DryRun 表示该动作支持 --dry-run：用户既没给 --yes 也没给 --dry-run 时自动加上 --dry-run，默认只预览。
	DryRun bool
}

func storageConfDefaults(root string) map[string]string {
	return map[string]string{
		"--storage-conf": path.Join(root, "storage-view", "config", "storage.yaml"),
		"--package-root": root,
	}
}

func storageMetadataDefaults(root string) map[string]string {
	return map[string]string{
		"--metadata-db":  path.Join(root, "storage-primary", "var", "storage", "metadata", "storage_metadata.db"),
		"--package-root": root,
	}
}

var storageMaintenanceActions = []storageMaintenanceAction{
	{
		Action: "repair-view", Aliases: []string{"修复视图", "repair"}, Defaults: storageConfDefaults, DryRun: true,
		Short: "清理 View 消费积压并重建索引（默认只预览，--yes 才执行）",
		Long: "停止 storage-view、备份元数据、删除目标数据集的 durable consumer、重启。常用参数：--space-id、--view-id、--consumer（必填，view_<12 位十六进制>）、\n" +
			"--reset-view-indexes --deliver-policy=all（高风险：删除物理索引后从 Primary 重建）、--restart=false。",
	},
	{
		Action: "force-rebuild-view", Aliases: []string{"强制重建视图", "重建视图"}, Defaults: storageConfDefaults, DryRun: true,
		Short: "删除旧 View、清空 durable consumer 状态并从头重建（默认只预览，--yes 才执行）",
		Long:  "停止整个 Storage 生命周期、备份元数据、清空 View 状态与物理索引，重启后从 Primary 回填。常用参数：--space-id、--view-id、--lookback。新建或重建 View 前先估算存储机内存。",
	},
	{
		Action: "reset-view-consumers", Aliases: []string{"重置视图消费", "清理视图队列"}, Defaults: storageConfDefaults, DryRun: true,
		Short: "删除所有 View durable/历史消息/索引并按回溯窗口重新构建（默认只预览，--yes 才执行）",
		Long:  "常用参数：--lookback、--reset-all-storage-data（连 Primary 数据一起删，系统重置用）、--restart=false。",
	},
	{
		Action: "reset-metadata", Aliases: []string{"重置元数据"}, Defaults: func(root string) map[string]string {
			return map[string]string{"--package-root": root}
		}, DryRun: true,
		Short: "备份后清空全部业务定义并重新导入种子（Storage 必须已停止；默认只预览，--yes 才执行）",
		Long:  "常用参数：--seed（种子文件，在存储机上的路径）。",
	},
	{
		Action: "delete-view", Defaults: storageMetadataDefaults,
		Short: "离线删除一个 View 的元数据（不带 --yes 时存储机上的程序会拒绝执行）",
		Long: "参数：--view <空间/视图 ID>（必填）、--yes。要先暂停 storage-view（直接 stop 会被主机守护拉起，用 moox-runtime pause）。" +
			"物理索引文件由 storage-view 的清理定时器回收。删除前先备份元数据库。",
	},
	{
		Action: "purge-dataset-events", Defaults: storageConfDefaults, DryRun: true,
		Short: "精确清理一个数据集在 EventBus 里的历史事件（默认只预览，--yes 才执行）",
		Long:  "参数：--space、--dataset（必填）。只用于可由后续采样重新生成的运维数据，行情、交易和因子业务数据集不得使用。",
	},
	{
		Action: "period-inventory", Defaults: func(string) map[string]string { return nil },
		Short: "只读盘点 DataNode 的周期完成标记（要求 DataNode 没有打开该数据库）",
		Long:  "参数：--path（Pebble 数据库路径，存储机上的路径）、--node-id、--space、--dataset、--frequency、--period-time-min、--period-time-max。",
	},
}

func (a storageMaintenanceAction) command() *cobra.Command {
	return &cobra.Command{
		Use:                a.Action + " [参数]",
		Aliases:            a.Aliases,
		Short:              a.Short,
		Long:               a.Long + "\n\n经 SSH 在存储机上执行 moox-storage-cli " + a.Action + "；--file 指定 moox.toml（默认 ./moox.toml），--host 指定存储机（默认 storage-primary 所在主机）。\n其余参数原样交给存储机上的程序，路径参数缺省时按部署目录补齐；输出同时保存在存储机的维护日志里。",
		DisableFlagParsing: true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
				return cmd.Help()
			}
			return runStorageMaintenance(cmd, a, args)
		},
	}
}

// hasCLIFlag 判断参数里是否给了某个 flag（--name、--name=v、-name、-name=v）。
func hasCLIFlag(args []string, name string) bool {
	bare := strings.TrimPrefix(name, "--")
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		for _, prefix := range []string{"--", "-"} {
			if arg == prefix+bare || strings.HasPrefix(arg, prefix+bare+"=") {
				return true
			}
		}
	}
	return false
}

// storageRemoteArgs 组装存储机上执行的参数：补路径默认值；支持预览的动作没给 --yes 时加 --dry-run。
func storageRemoteArgs(action storageMaintenanceAction, storageRoot string, args []string) []string {
	remote := append([]string{}, args...)
	if action.Defaults != nil {
		defaults := action.Defaults(storageRoot)
		names := make([]string, 0, len(defaults))
		for name := range defaults {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !hasCLIFlag(remote, name) {
				remote = append(remote, name, defaults[name])
			}
		}
	}
	if action.DryRun && !hasCLIFlag(remote, "--yes") && !hasCLIFlag(remote, "--dry-run") {
		remote = append(remote, "--dry-run")
	}
	return remote
}

// storageRemoteScript 在存储机上执行动作：输出先落到维护日志再回显，SSH 中断时结果（如 backup_path）不会丢；退出码原样返回。
const storageRemoteScript = `umask 077; dir="$1"; name="$2"; shift 2; mkdir -p "$dir" || exit 125; log="$dir/$name-$(date -u +%Y%m%dT%H%M%SZ).log"; "$@" >"$log" 2>&1; code=$?; cat "$log"; echo "（输出已保存在存储机 $log）" >&2; exit $code`

func runStorageMaintenance(cmd *cobra.Command, action storageMaintenanceAction, args []string) error {
	if configured := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CLI")); configured != "" {
		// 显式指定了本机上的存储程序（例如就在存储机上运行）：按原样本机执行。
		return runStorageMaintenanceBinaryAction(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), action.Action, args)
	}
	file, args, _ := takeFlag(args, "--file")
	hostID, args, _ := takeFlag(args, "--host")
	remote, err := openHostRemoteForCommand(defaultFlag(file, defaultSetupFile))
	if err != nil {
		return err
	}
	defer remote.Close()
	paths := remote.manifest.Paths.Resolved()
	if hostID == "" {
		hostID = remote.manifest.ComponentHost("storage-primary")
	}
	if hostID == "" {
		return errors.New("moox.toml 里没有部署 storage-primary，请用 --host 指定存储机")
	}
	root := path.Join(paths.StorageRoot, "current")
	argv := []string{"sh", "-c", storageRemoteScript, "sh", path.Join(paths.StorageRoot, "maintenance"), action.Action,
		path.Join(root, "bin", "moox-storage-cli"), action.Action}
	argv = append(argv, storageRemoteArgs(action, root, args)...)
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Hour)
	defer cancel()
	result, err := remote.Exec(ctx, hostID, argv, nil)
	_, _ = io.WriteString(cmd.OutOrStdout(), result.Stdout)
	_, _ = io.WriteString(cmd.ErrOrStderr(), result.Stderr)
	if err != nil {
		if result.ExitCode != 0 {
			return &hostExitError{code: result.ExitCode}
		}
		return err
	}
	return nil
}

func runStorageMaintenanceBinaryAction(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, action string, args []string) error {
	path, err := resolveStorageMaintenanceBinary()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	cmd := exec.CommandContext(ctx, path, append([]string{action}, args...)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("moox-storage-cli %s failed with exit code %d", action, exitErr.ExitCode())
		}
		return fmt.Errorf("run moox-storage-cli %s: %w", action, err)
	}
	return nil
}

func resolveStorageMaintenanceBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CLI")); configured != "" {
		if isExecutableFile(configured) {
			return configured, nil
		}
		return "", fmt.Errorf("MOOX_STORAGE_CLI %q is not executable", configured)
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		for _, candidate := range []string{
			filepath.Join(dir, "moox-storage-cli"),
			filepath.Join(dir, "moox-storage-primary-cli"),
			filepath.Join(dir, "..", "storage-primary", "bin", "moox-storage-primary-cli"),
		} {
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
	}
	if path, err := exec.LookPath("moox-storage-cli"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("moox-storage-primary-cli"); err == nil {
		return path, nil
	}
	return "", errors.New("moox-storage-cli was not found; set MOOX_STORAGE_CLI or install the storage CLI beside moox-cli")
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
