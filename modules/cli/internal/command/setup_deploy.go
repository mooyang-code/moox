package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	clicgateway "github.com/mooyang-code/moox/modules/cli/internal/gateway"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

// setupDeployer 是部署命令用到的部署能力（setupdeploy.Deployer），测试时替换。
type setupDeployer interface {
	Bootstrap(ctx context.Context, opts setupdeploy.BootstrapOptions) ([]setupdeploy.Result, error)
	Deploy(ctx context.Context, hostID string, opts setupdeploy.Options) (setupdeploy.Result, error)
	Rollback(ctx context.Context, hostID string, lockHeld bool) (string, error)
	RunScript(ctx context.Context, hostID, script string, lockHeld bool, args ...string) (string, error)
	Plan(hostID string) (release.Plan, error)
}

// setupDeployResult 是一次部署在命令输出中的摘要（安装器输出已经打印到标准错误）。
type setupDeployResult struct {
	Host       string   `json:"host"`
	Release    string   `json:"release"`
	Components []string `json:"components"`
}

func deployResults(results ...setupdeploy.Result) []setupDeployResult {
	out := make([]setupDeployResult, 0, len(results))
	for _, result := range results {
		out = append(out, setupDeployResult{Host: result.Host, Release: result.Release, Components: result.Components})
	}
	return out
}

// defaultOpenSetupDeployer 创建部署器：仓库根目录为当前目录，版本为当前 git 提交。syncPlacements 为 true 时，
// 每次部署先经 SSH 隧道调用 Admin 同步这台主机的部署记录（隧道在第一次同步时才建立）。
func defaultOpenSetupDeployer(snapshot *setupconfig.Snapshot, file string, out io.Writer, syncPlacements bool) (setupDeployer, func(), error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, nil, err
	}
	configFile, err := filepath.Abs(file)
	if err != nil {
		return nil, nil, err
	}
	version, err := repositoryVersion(root)
	if err != nil {
		return nil, nil, err
	}
	deployer := &setupdeploy.Deployer{
		Manifest:       snapshot.Manifest,
		RepositoryRoot: root,
		Version:        version,
		Dial:           dialSetupHost,
		Builder: setupdeploy.ScriptBuilder{
			RepositoryRoot: root, ConfigFile: configFile,
			CompileHostPassword: snapshot.Manifest.CompileHost.SSH.Password, Out: out,
		},
		Out: out,
	}
	placements := &controlPlacements{manifest: snapshot.Manifest}
	if syncPlacements {
		deployer.Placements = placements
	}
	return deployer, placements.Close, nil
}

// repositoryVersion 是当前 git 提交的短哈希；工作区有未提交的改动时加 -dirty。
func repositoryVersion(root string) (string, error) {
	output, err := exec.Command("git", "-C", root, "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("读取当前 git 提交（部署需要在仓库根目录执行）: %w", err)
	}
	version := strings.TrimSpace(string(output))
	status, err := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return "", fmt.Errorf("读取 git 工作区状态: %w", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		version += "-dirty"
	}
	return version, nil
}

// controlPlacements 经 SSH 隧道调用 Admin 的 SysDeploy.SyncHostPlacements；隧道在第一次调用时建立。
type controlPlacements struct {
	manifest setupconfig.Manifest
	mu       sync.Mutex
	gateway  *clicgateway.Client
}

func (p *controlPlacements) SyncHostPlacements(ctx context.Context, host setupconfig.Host, components []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gateway == nil {
		gateway, err := openControlGateway(p.manifest)
		if err != nil {
			return err
		}
		p.gateway = gateway
	}
	return setupclient.New(p.gateway).SyncHostPlacements(ctx, host, components)
}

func (p *controlPlacements) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gateway != nil {
		p.gateway.Close()
		p.gateway = nil
	}
}

// cliKeyFile 是操作员机器上 moox-cli 签名密钥的位置，与隧道方式的 gatewayclient 读取的位置一致。
func cliKeyFile() string {
	if path := strings.TrimSpace(os.Getenv(clicgateway.EnvKeyFile)); path != "" {
		return path
	}
	return clicgateway.DefaultKeyFile
}

func newSetupBootstrapCommand(deps setupDeps) *cobra.Command {
	var file string
	var skipBuild, controlOnly, lockHeld bool
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "空环境首次部署：初始化管理后台并部署全部主机",
		Long: `空环境首次部署（设计文档 3.12）：
  1. 把 control 的发布装上，暂不启动；
  2. 在 control 上离线执行 moox-admin-cli bootstrap：建表、生成 CA、写入全部主机和部署、生成调用方密钥、
     为 control 签发主机网关证书；
  3. 启动 control 上的组件；
  4. 把 moox-cli 的签名密钥取回本机；
  5. 依次部署其他主机（--control-only 时跳过，之后用 deploy-host 逐台部署）。
可以重复执行：已有的表、CA 和密钥都会复用。停机切换时操作员在 control 上持有维护锁，
用 --control-only --maintenance-lock-held。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			deployer, closeDeployer, err := deps.openDeployer(snapshot, file, cmd.ErrOrStderr(), false)
			if err != nil {
				return err
			}
			defer closeDeployer()
			manifest := snapshot.Manifest
			results, err := deployer.Bootstrap(cmd.Context(), setupdeploy.BootstrapOptions{
				SkipBuild:           skipBuild,
				ControlOnly:         controlOnly,
				MaintenanceLockHeld: lockHeld,
				CLIKeyFile:          cliKeyFile(),
				NewPlacements: func(context.Context) (setupdeploy.PlacementSyncer, func(), error) {
					placements := &controlPlacements{manifest: manifest}
					return placements, placements.Close, nil
				},
			})
			if err != nil {
				return err
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, map[string]any{"status": "ready", "hosts": deployResults(results...)})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().BoolVar(&skipBuild, "skip-build", false, "复用仓库 bin/ 中已有的二进制")
	cmd.Flags().BoolVar(&controlOnly, "control-only", false, "只初始化管理后台并部署 control，不部署其他主机")
	cmd.Flags().BoolVar(&lockHeld, "maintenance-lock-held", false, "操作员已在 control 上持有维护锁（需要同时指定 --control-only）")
	return cmd
}

// setupDeployFlags 是部署命令的公共选项。
type setupDeployFlags struct {
	file                string
	skipBuild           bool
	reuseBinaries       bool
	noStart             bool
	noSync              bool
	maintenanceLockHeld bool
}

func (f *setupDeployFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().BoolVar(&f.skipBuild, "skip-build", false, "复用仓库 bin/ 中已有的二进制")
	cmd.Flags().BoolVar(&f.reuseBinaries, "reuse-binaries", false, "不上传二进制，复用主机当前发布中的（只更新配置、密钥和证书）")
	cmd.Flags().BoolVar(&f.noStart, "no-start", false, "只安装，不启动组件")
	cmd.Flags().BoolVar(&f.noSync, "no-sync", false, "不同步 Admin 中的部署记录（Admin 不可用、需要先修复 control 时使用）")
	cmd.Flags().BoolVar(&f.maintenanceLockHeld, "maintenance-lock-held", false, "操作员已在主机上持有维护锁，安装器不再加锁")
}

func (f *setupDeployFlags) options(components []string) setupdeploy.Options {
	return setupdeploy.Options{
		Components: components, SkipBuild: f.skipBuild, ReuseBinaries: f.reuseBinaries,
		NoStart: f.noStart, MaintenanceLockHeld: f.maintenanceLockHeld,
	}
}

func newSetupDeployHostCommand(deps setupDeps) *cobra.Command {
	var flags setupDeployFlags
	var hostID string
	var components []string
	cmd := &cobra.Command{
		Use:   "deploy-host",
		Short: "部署一台主机：主机组件和部署表中的业务组件",
		Long: `部署一台主机：渲染发布、构建二进制、从 control 取签名密钥与证书（每次都重新签发主机网关证书）、
同步 Admin 中这台主机的部署记录、上传并安装。--components 只部署其中几个组件，其余组件不动。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(flags.file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if _, err := findSetupHost(snapshot.Manifest, hostID); err != nil {
				return err
			}
			deployer, closeDeployer, err := deps.openDeployer(snapshot, flags.file, cmd.ErrOrStderr(), !flags.noSync)
			if err != nil {
				return err
			}
			defer closeDeployer()
			result, err := deployer.Deploy(cmd.Context(), hostID, flags.options(components))
			if err != nil {
				return err
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, deployResults(result)[0])
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID")
	cmd.Flags().StringSliceVar(&components, "components", nil, "只部署这些组件（逗号分隔）")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newSetupDeployServiceCommand(deps setupDeps) *cobra.Command {
	var flags setupDeployFlags
	var component, hostID string
	cmd := &cobra.Command{
		Use:   "deploy-service",
		Short: "在部署了某个组件的全部主机（或指定主机）上重新部署这个组件",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(flags.file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			component = strings.TrimSpace(component)
			hosts := snapshot.Manifest.HostsOf(component)
			if len(hosts) == 0 {
				return fmt.Errorf("moox.toml 的部署表中没有组件 %q", component)
			}
			if hostID = strings.TrimSpace(hostID); hostID != "" {
				if !snapshot.Manifest.HasComponent(hostID, component) {
					return fmt.Errorf("主机 %s 上没有部署组件 %s", hostID, component)
				}
				hosts = []string{hostID}
			}
			deployer, closeDeployer, err := deps.openDeployer(snapshot, flags.file, cmd.ErrOrStderr(), !flags.noSync)
			if err != nil {
				return err
			}
			defer closeDeployer()
			var results []setupdeploy.Result
			for _, id := range hosts {
				result, err := deployer.Deploy(cmd.Context(), id, flags.options([]string{component}))
				if err != nil {
					return err
				}
				results = append(results, result)
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, map[string]any{"component": component, "hosts": deployResults(results...)})
		},
	}
	flags.register(cmd)
	cmd.Flags().StringVar(&component, "component", "", "组件 ID")
	cmd.Flags().StringVar(&hostID, "host", "", "只部署这台主机")
	_ = cmd.MarkFlagRequired("component")
	return cmd
}

func newSetupRollbackCommand(deps setupDeps) *cobra.Command {
	var file, hostID string
	var lockHeld bool
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "把主机切回上一个发布并重启组件",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetupHostAction(cmd, deps, file, hostID, func(ctx context.Context, deployer setupDeployer) (string, error) {
				return deployer.Rollback(ctx, hostID, lockHeld)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID")
	cmd.Flags().BoolVar(&lockHeld, "maintenance-lock-held", false, "操作员已在主机上持有维护锁（停机窗口里回滚），安装器不再加锁")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newSetupPauseCommand(deps setupDeps) *cobra.Command {
	return newSetupPauseResumeCommand(deps, "pause", "暂停组件：写入暂停标记并停止进程，启动脚本、健康检查和重新部署都不会拉起它")
}

func newSetupResumeCommand(deps setupDeps) *cobra.Command {
	return newSetupPauseResumeCommand(deps, "resume", "恢复暂停的组件：删除暂停标记并启动")
}

func newSetupPauseResumeCommand(deps setupDeps, script, short string) *cobra.Command {
	var file, hostID string
	var components []string
	var lockHeld bool
	cmd := &cobra.Command{
		Use:   script,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(components) == 0 {
				return fmt.Errorf("必须指定 --components")
			}
			return runSetupHostAction(cmd, deps, file, hostID, func(ctx context.Context, deployer setupDeployer) (string, error) {
				return deployer.RunScript(ctx, hostID, script, lockHeld, components...)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID")
	cmd.Flags().StringSliceVar(&components, "components", nil, "组件（逗号分隔）")
	cmd.Flags().BoolVar(&lockHeld, "maintenance-lock-held", false, "操作员已在主机上持有维护锁")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newSetupServiceCommand(deps setupDeps) *cobra.Command {
	var file, hostID string
	var components []string
	var force bool
	cmd := &cobra.Command{
		Use:       "service <start|stop|restart|status>",
		Short:     "在主机上启动、停止、重启组件或查看状态",
		Long:      "不指定 --components 时作用于主机上的全部组件（restart 必须指定）。暂停的组件不会被 start、restart 拉起，--force 时忽略暂停标记。",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"start", "stop", "restart", "status"},
		RunE: func(cmd *cobra.Command, args []string) error {
			action := args[0]
			var scriptArgs []string
			switch action {
			case "start", "restart":
				if force {
					scriptArgs = append(scriptArgs, "--force")
				}
			case "stop", "status":
				if force {
					return fmt.Errorf("--force 只用于 start 和 restart")
				}
			default:
				return fmt.Errorf("不支持的操作 %q（可选 start、stop、restart、status）", action)
			}
			scriptArgs = append(scriptArgs, components...)
			return runSetupHostAction(cmd, deps, file, hostID, func(ctx context.Context, deployer setupDeployer) (string, error) {
				return deployer.RunScript(ctx, hostID, action, false, scriptArgs...)
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", servicecatalog.ControlHostID, "主机 ID")
	cmd.Flags().StringSliceVar(&components, "components", nil, "组件（逗号分隔）")
	cmd.Flags().BoolVar(&force, "force", false, "忽略暂停标记")
	return cmd
}

// runSetupHostAction 在一台主机上执行一个运行操作，把脚本输出写到标准输出。
func runSetupHostAction(cmd *cobra.Command, deps setupDeps, file, hostID string, action func(context.Context, setupDeployer) (string, error)) error {
	snapshot, err := deps.load(file)
	if err != nil {
		return err
	}
	defer clearSetupSecrets(snapshot)
	if _, err := findSetupHost(snapshot.Manifest, hostID); err != nil {
		return err
	}
	deployer, closeDeployer, err := deps.openDeployer(snapshot, file, cmd.ErrOrStderr(), false)
	if err != nil {
		return err
	}
	defer closeDeployer()
	output, err := action(cmd.Context(), deployer)
	fmt.Fprint(cmd.OutOrStdout(), output)
	return err
}

func newSetupRenderCommand(deps setupDeps) *cobra.Command {
	var file, hostID, outDir string
	cmd := &cobra.Command{
		Use:   "render",
		Short: "渲染一台主机的发布（配置与启动参数）到本地目录，用于检查，不连接主机",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if _, err := findSetupHost(snapshot.Manifest, hostID); err != nil {
				return err
			}
			deployer, closeDeployer, err := deps.openDeployer(snapshot, file, cmd.ErrOrStderr(), false)
			if err != nil {
				return err
			}
			defer closeDeployer()
			plan, err := deployer.Plan(hostID)
			if err != nil {
				return err
			}
			if err := writeRenderedPlan(outDir, plan); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{
				"host": plan.HostID, "root": plan.Root, "components": plan.ComponentIDs(), "files": len(plan.Files), "out": outDir,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID")
	cmd.Flags().StringVar(&outDir, "out", "", "输出目录（必须不存在或为空）")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

// writeRenderedPlan 把发布中的文件写到 dir；dir 必须不存在或为空。
func writeRenderedPlan(dir string, plan release.Plan) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("必须指定 --out")
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("输出目录 %s 不为空", dir)
	}
	for _, file := range plan.Files {
		target := filepath.Join(dir, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, file.Data, file.Mode); err != nil {
			return err
		}
	}
	return nil
}
