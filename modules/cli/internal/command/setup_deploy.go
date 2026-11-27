package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// setupSource 是部署要用的软件来源与这台电脑的操作员身份目录。
type setupSource struct {
	sourceDir, binaryDir, operatorDir string
	// root、bundle 由 resolve 填好：root 是源码或产物包目录；在产物包目录里运行时 bundle 是它的清单。
	root   string
	bundle *bundle.Manifest
}

func (s *setupSource) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.sourceDir, "source-dir", "", "源码或产物包目录（默认当前目录）：配置模板从这里读")
	cmd.Flags().StringVar(&s.binaryDir, "binary-dir", "", "本机预先编译好的 Linux 二进制目录；在产物包目录里不用写，目标机按清单下载组件包")
	cmd.Flags().StringVar(&s.operatorDir, "operator-dir", "", "这台电脑的操作员身份目录（默认 ~/.config/moox）")
}

// configDir 返回种子目录：命令行写了 --config-dir 就用它，否则取源目录下的 config/setup，
// 保证种子和其余安装内容来自同一个源目录。要在 resolve 之后调用。
func (s *setupSource) configDir(cmd *cobra.Command, explicit string) string {
	if cmd.Flags().Changed("config-dir") {
		return explicit
	}
	return filepath.Join(s.root, filepath.FromSlash(defaultSetupConfigDir))
}

// resolve 把目录补成绝对路径，并在源目录是解压后的产物包时读它的清单。
func (s *setupSource) resolve() error {
	source := s.sourceDir
	var err error
	if source == "" {
		if source, err = os.Getwd(); err != nil {
			return err
		}
	}
	if s.root, err = filepath.Abs(source); err != nil {
		return err
	}
	if err := s.resolveOperatorDir(); err != nil {
		return err
	}
	if s.binaryDir != "" {
		if s.binaryDir, err = filepath.Abs(s.binaryDir); err != nil {
			return err
		}
	}
	manifest, ok, err := bundle.Load(s.root)
	if err != nil || !ok {
		return err
	}
	if err := manifest.CheckCLI(Version, bundle.CatalogSHA256()); err != nil {
		return err
	}
	s.bundle = &manifest
	return nil
}

// resolveOperatorDir 把操作员身份目录补成绝对路径，默认 ~/.config/moox。
func (s *setupSource) resolveOperatorDir() error {
	if s.operatorDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		s.operatorDir = filepath.Join(home, ".config", "moox")
	}
	var err error
	s.operatorDir, err = filepath.Abs(s.operatorDir)
	return err
}

// deployOptions 组出一台主机的部署参数。
func (s setupSource) deployOptions(cmd *cobra.Command, snapshot *setupSnapshot, file, host string, components []string) deploy.Options {
	return deploy.Options{
		Config: snapshot.Manifest, Secrets: snapshot.Secrets, HostID: host, Components: components,
		RepositoryRoot: s.root, BinaryDirectory: s.binaryDir, Bundle: s.bundle, OperatorDirectory: s.operatorDir,
		Version: Version, Commit: GitCommit, Dial: dialSetupHost, Log: cmd.ErrOrStderr(),
		SyncTopology: func(ctx context.Context) error {
			gateway, err := openCommandGateway(ctx, file, snapshot.Manifest)
			if err != nil {
				return err
			}
			defer gateway.Close()
			return setupclient.New(gateway).SyncTopology(ctx, snapshot.Manifest)
		},
		ObjectStoreSecret: func(ctx context.Context, secretID string) ([]byte, error) {
			gateway, err := openCommandGateway(ctx, file, snapshot.Manifest)
			if err != nil {
				return nil, err
			}
			defer gateway.Close()
			credential, err := setupclient.New(gateway).ObjectStoreSecret(ctx, secretID)
			if err != nil {
				return nil, err
			}
			return credential.Env(), nil
		},
	}
}

// setupComponentOnHost 校验主机和组件：组件要在这台主机该有的组件里（含每台主机都有的主机级组件）。
func setupComponentOnHost(manifest *topology.Config, hostID, component string) (topology.Host, error) {
	components, err := manifest.HostComponents(hostID)
	if err != nil {
		return topology.Host{}, err
	}
	if !slices.Contains(components, component) {
		return topology.Host{}, fmt.Errorf("主机 %s 上没有组件 %s（这台主机有：%s）", hostID, component, strings.Join(components, "、"))
	}
	return manifest.HostByID(hostID), nil
}

func newSetupDeployCommand(deps setupDeps) *cobra.Command {
	var file, host string
	var components []string
	var rotateHostKey bool
	var source setupSource
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "把一台主机部署成 moox.toml 描述的样子：放二进制、渲染配置、按顺序重启有变化的组件，等它们就绪",
		Long: `部署一台主机上的全部组件，或用 --component 只部署其中几个。每个组件是一个 systemd 用户服务 moox-<组件>。
重跑同一条命令就是再做一遍：没有变化的组件不重启；组件按 moox.toml 的声明启停：[deployments_disabled] 里的组件只更新文件、单元停掉，
其余组件确保在运行（setup stop 临时停掉的也会被重新拉起）。停用的主机（enabled = false）只留 host-gateway 在跑，连不上也照常写登记。
主机与部署的登记每次都按 moox.toml 整份覆盖 Admin（多出的主机与部署删除，签名公钥与 SSH 指纹保留）：控制机由 moox-admin-cli bootstrap 写
（建库、CA、主密钥、管理员、登记这台电脑的 moox-cli 公钥，这些只建不改），其余主机部署前先经 Admin 的 SyncTopology 写，再向控制机要身份材料。只部署个别组件时，服务目录变了会把这台主机的主机网关一起升级。
每台主机有一把签名私钥（secrets/host.key），本机全部组件共用，Admin 只登记公钥。例行部署不换它：目标机上的私钥和登记的公钥一致就沿用，
只重新签发主机证书；私钥丢了或对不上时会签发新的。--rotate-host-key 显式换掉它：旧私钥立即作废，这台主机上的组件全部重启。
换密钥（显式要求，或发现私钥丢了、对不上）时按整台部署处理，--component 不起作用。
在源码目录里要用 --binary-dir 指定预先编译好的 Linux 二进制；在产物包目录里目标机按清单下载组件包。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := source.resolve(); err != nil {
				return err
			}
			options := source.deployOptions(cmd, snapshot, file, host, components)
			options.RotateHostKey = rotateHostKey
			result, err := deps.deployHost(cmd.Context(), options)
			if len(result.Components) != 0 || len(result.Warnings) != 0 {
				if writeErr := writeSetupJSON(cmd, result); writeErr != nil {
					return writeErr
				}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径（同目录要有 moox.secrets.toml）")
	cmd.Flags().StringVar(&host, "host", "", "主机 ID")
	cmd.Flags().StringSliceVar(&components, "component", nil, "只部署这些组件（逗号分隔或重复写）；不写部署这台主机的全部组件")
	cmd.Flags().BoolVar(&rotateHostKey, "rotate-host-key", false, "换掉这台主机的签名密钥（旧私钥立即作废，本机组件全部重启）；控制机不支持")
	source.flags(cmd)
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

// setupStatusOutput 是 setup status 的输出。
type setupStatusOutput struct {
	Route string              `json:"route"`
	Hosts []deploy.HostStatus `json:"hosts"`
}

// setupHostStatuses 把只读检测的结果整理成每台主机上各组件是否部署过、是否在运行；setup status 与 setup validate、setup update 共用。
func setupHostStatuses(manifest *topology.Config, reports []deployscan.HostReport) ([]deploy.HostStatus, error) {
	hosts := []deploy.HostStatus{}
	for _, report := range reports {
		expected, err := manifest.HostComponents(report.Host)
		if err != nil {
			return nil, err
		}
		version, err := deploy.ParseVersion(report.Version)
		if err != nil {
			return nil, fmt.Errorf("version_invalid: 主机 %s 的 version.json 无法解析", report.Host)
		}
		hosts = append(hosts, deploy.Status(report.Host, expected, version, report.Units, report.Active))
	}
	return hosts, nil
}

func newSetupStatusCommand(deps setupDeps) *cobra.Command {
	var file, host string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "只读查看各主机的部署状态：version.json 里的版本，以及每个组件的用户服务是否在运行",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			reports, err := deps.scanDeployment(cmd.Context(), snapshot, deployscan.Options{Host: host})
			if err != nil {
				return err
			}
			hosts, err := setupHostStatuses(snapshot.Manifest, reports)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, setupStatusOutput{Route: deployscan.Route(reports), Hosts: hosts})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径")
	cmd.Flags().StringVar(&host, "host", "", "只看这一台主机")
	return cmd
}

func newSetupRestartCommand(deps setupDeps) *cobra.Command {
	return newSetupOperateCommand(deps, "restart", deploy.OperationRestart, "重启一个组件（systemctl --user restart；已被停用的组件会被拉起但不恢复开机自启，要恢复用 start-component）")
}

func newSetupStopCommand(deps setupDeps) *cobra.Command {
	return newSetupOperateCommand(deps, "stop", deploy.OperationStop, "临时停掉一个组件并不再开机自启（systemctl --user disable --now）；不改登记，下次部署按 moox.toml 的声明重新拉起，长期停用写进 [deployments_disabled]")
}

func newSetupStartComponentCommand(deps setupDeps) *cobra.Command {
	return newSetupOperateCommand(deps, "start-component", deploy.OperationStart, "恢复一个被停掉的组件并开机自启（systemctl --user enable --now）")
}

// newSetupOperateCommand 是 restart、stop、start-component 三个命令的共同实现：默认只预览，--yes 才执行。
func newSetupOperateCommand(deps setupDeps, use, operation, short string) *cobra.Command {
	var file, host, component string
	var yes bool
	cmd := &cobra.Command{Use: use, Short: short + "（默认只预览）", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		target, err := setupComponentOnHost(snapshot.Manifest, host, component)
		if err != nil {
			return err
		}
		if !yes {
			return writePreview(cmd, map[string]any{"action": use, "host": host, "component": component, "unit": deploy.UnitName(component)})
		}
		state, err := deps.operate(cmd.Context(), target, component, operation)
		if err != nil {
			return err
		}
		return writeSetupJSON(cmd, map[string]any{"status": "ok", "action": use, "host": host, "component": component, "state": state})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径")
	cmd.Flags().StringVar(&host, "host", "", "主机 ID")
	cmd.Flags().StringVar(&component, "component", "", "组件 ID，比如 monitor、collector-stream")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认执行；不加只预览")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("component")
	return cmd
}

func defaultSetupOperate(ctx context.Context, host topology.Host, component, operation string) (string, error) {
	client, err := dialSetupHost(ctx, host)
	if err != nil {
		return "", err
	}
	defer client.Close()
	return deploy.Operate(ctx, client, component, operation)
}

func newSetupLogsCommand(deps setupDeps) *cobra.Command {
	var file, host, component, grep string
	var lines int
	var journal bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "只读查看组件在主机上的日志：先看文件日志（logs/<组件>/），没有再看 journald 里这个服务的输出",
		Long: `文件日志是 tRPC 的框架与业务日志（按大小轮转，单个 50MB、保留 5 份）。标准输出与标准错误（崩溃信息、启动失败的原因）在 journald 里，
用 --journal 直接看。不写 --component 时看这台主机上的全部组件。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			components, err := snapshot.Manifest.HostComponents(host)
			if err != nil {
				return err
			}
			if component != "" {
				if _, err := setupComponentOnHost(snapshot.Manifest, host, component); err != nil {
					return err
				}
				components = []string{component}
			}
			sections, err := deps.readLogs(cmd.Context(), snapshot.Manifest.HostByID(host), snapshot.Manifest.Paths.DeployRoot, components, grep, lines, journal)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"host": host, "logs": sections})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径")
	cmd.Flags().StringVar(&host, "host", "", "主机 ID")
	cmd.Flags().StringVar(&component, "component", "", "组件 ID，比如 monitor、host-gateway；不写看全部")
	cmd.Flags().IntVar(&lines, "lines", 100, "每个组件看最后多少行（1 到 5000）")
	cmd.Flags().StringVar(&grep, "grep", "", "只看含这段文字的行（按原文匹配）")
	cmd.Flags().BoolVar(&journal, "journal", false, "只看 journald 里的标准输出与标准错误")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func defaultSetupReadLogs(ctx context.Context, host topology.Host, root string, components []string, pattern string, lines int, journal bool) ([]deploy.LogSection, error) {
	client, err := dialSetupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return deploy.Logs(ctx, client, deploy.Layout{Root: root}, components, pattern, lines, journal)
}

func defaultSetupWaitReady(ctx context.Context, host topology.Host, components []string, timeout time.Duration) (map[string]string, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	client, err := dialSetupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return deploy.WaitReady(ctx, client, catalog, components, timeout)
}

func defaultSetupScanDeployment(ctx context.Context, snapshot *setupSnapshot, options deployscan.Options) ([]deployscan.HostReport, error) {
	return deployscan.Scan(ctx, snapshot.Manifest, dialSetupHost, options)
}

func defaultSetupWipeHost(ctx context.Context, host topology.Host, root string) (deployscan.HostResult, error) {
	return deployscan.Wipe(ctx, dialSetupHost, host, root)
}

// newSetupPackageComponentsCommand 给发布脚本用：按服务目录把编译好的二进制打成每个组件一个包，输出清单要用的摘要。
func newSetupPackageComponentsCommand() *cobra.Command {
	var binaryDir, outputDir, version, goarch string
	cmd := &cobra.Command{
		Use:   "package-components",
		Short: "按服务目录把编译好的 Linux 二进制打成每个组件一个包（发布脚本用）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if binaryDir == "" || outputDir == "" || version == "" {
				return errors.New("package-components 要带 --binary-dir、--output-dir 与 --version")
			}
			catalog, err := servicecatalog.LoadEmbedded()
			if err != nil {
				return err
			}
			components, err := bundle.PackageComponents(catalog, binaryDir, outputDir, version, goarch)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"catalog_sha256": bundle.CatalogSHA256(), "components": components})
		},
	}
	cmd.Flags().StringVar(&binaryDir, "binary-dir", "", "编译好的 Linux 二进制目录")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "组件包的输出目录")
	cmd.Flags().StringVar(&version, "version", "", "发布版本号，例如 v0.0.2")
	cmd.Flags().StringVar(&goarch, "goarch", "amd64", "目标架构：amd64 或 arm64")
	return cmd
}

// setupValidateOutput 是 setup validate 的输出：配置与连接的检查结果，各主机的部署情况，以及接下来要做的事。
type setupValidateOutput struct {
	setupvalidate.Result
	Deployment *setupDeployment `json:"deployment,omitempty"`
}

// 部署方式：没装过只能新安装；装过时由用户在覆盖更新与卸载之间选一个（对整套部署统一选择，不逐台选）。
const (
	setupModeInstall   = "install"
	setupModeUpdate    = "update"
	setupModeUninstall = "uninstall"
)

// setupDeployment 是目标机器的部署情况。Route：fresh（都没装过）或 installed（至少一台装过）；Modes 是可选的部署方式。
type setupDeployment struct {
	Route string   `json:"route"`
	Modes []string `json:"modes"`
	// Components 是每台主机上 moox.toml 要求的组件（以及多出来的组件）是否已部署、是否在运行。
	Components []deploy.HostStatus `json:"components"`
	// NextSteps 是接下来要运行的命令；ManualSteps 是要用户在目标机上自己做的事（需要 sudo），做完再继续。
	NextSteps   []string `json:"next_steps"`
	ManualSteps []string `json:"manual_steps,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
	// Notes 是判断部署方式时要告诉用户的情况：部分主机没装过、主机上有没装完的残留。
	Notes []string                `json:"notes,omitempty"`
	Hosts []deployscan.HostReport `json:"hosts"`
}

// minDeployDiskKB 是部署前每台主机至少要有的可用空间：放得下全部二进制的新旧两份和下载的组件包。
const minDeployDiskKB = 2 << 20

// setupDeploymentStatus 只读检测各主机，给出路线、要用户先做的事与提醒。
func setupDeploymentStatus(ctx context.Context, deps setupDeps, snapshot *setupSnapshot) (*setupDeployment, error) {
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{})
	if err != nil {
		return nil, err
	}
	components, err := setupHostStatuses(snapshot.Manifest, reports)
	if err != nil {
		return nil, err
	}
	status := &setupDeployment{Route: deployscan.Route(reports), Components: components, Hosts: reports, NextSteps: []string{}}
	var empty []string
	for i, report := range reports {
		switch {
		case report.Installed:
			var lacking []string
			for _, component := range components[i].Components {
				if !component.Extra && !component.Deployed() {
					lacking = append(lacking, component.ID)
				}
			}
			if len(lacking) != 0 {
				status.Notes = append(status.Notes, fmt.Sprintf("主机 %s 装过，但缺这些组件：%s。如果是上次 setup install 没做完，重跑同一条 setup install；如果是 moox.toml 里新加的组件，覆盖更新会装上", report.Host, strings.Join(lacking, "、")))
			}
		case len(report.Entries) != 0 || len(report.Units) != 0 || report.Processes != 0:
			status.Notes = append(status.Notes, fmt.Sprintf("主机 %s 没有 version.json，但部署根或用户服务里已有东西（失败的部署尝试或别的文件）：部署会直接装在上面、覆盖同名文件；不清楚这些是什么时先让用户确认（setup wipe 会清空 moox.toml 里的全部主机，不能只清这一台）", report.Host))
			empty = append(empty, report.Host)
		default:
			empty = append(empty, report.Host)
		}
		host := snapshot.Manifest.HostByID(report.Host)
		if !report.Linger {
			status.ManualSteps = append(status.ManualSteps, fmt.Sprintf("在主机 %s 上执行（没开 linger 时登出后服务会被停掉，部署会停下）：%s", report.Host, deploy.LingerCommand(host.Username)))
		}
		if !report.Journald {
			status.Warnings = append(status.Warnings, fmt.Sprintf("主机 %s 的 journald 没有持久化，重启后看不到之前的标准输出日志。建议执行：%s", report.Host, deploy.JournaldCommand))
		}
		if report.DiskFreeKB >= 0 && report.DiskFreeKB < minDeployDiskKB {
			status.Warnings = append(status.Warnings, fmt.Sprintf("主机 %s 的部署盘只剩 %.1f GB，部署至少要 %d GB", report.Host, float64(report.DiskFreeKB)/(1<<20), minDeployDiskKB>>20))
		}
	}
	if status.Route == deployscan.RouteFresh {
		status.Modes = []string{setupModeInstall}
		status.NextSteps = append(status.NextSteps, "新安装：moox-cli setup install --space-id <空间>（先不带 --yes 看预检）")
		return status, nil
	}
	status.Modes = []string{setupModeUpdate, setupModeUninstall}
	if len(empty) != 0 {
		status.Notes = append(status.Notes, fmt.Sprintf("主机 %s 还没装过：如果是上次 setup install 没做完，重跑同一条 setup install；如果是新加的主机，覆盖更新会把它装上", strings.Join(empty, "、")))
	}
	status.NextSteps = append(status.NextSteps,
		"覆盖更新（数据保留）：moox-cli setup update（先不带 --yes 看预览）",
		"卸载（删除全部数据，不可恢复）：moox-cli setup wipe（先不带 --yes 看清空范围）")
	return status, nil
}

func newSetupValidateCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "validate", Short: "校验两份配置和连接，检测每台主机上各组件是否已部署，给出可选的部署方式（modes）和接下来要运行的命令（next_steps）", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			result, err := deps.validate(cmd.Context(), snapshot)
			output := setupValidateOutput{Result: result}
			var deployment error
			if err == nil {
				output.Deployment, deployment = setupDeploymentStatus(cmd.Context(), deps, snapshot)
			}
			if encodeErr := writeSetupJSON(cmd, output); encodeErr != nil {
				return encodeErr
			}
			return errors.Join(err, deployment)
		}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径（同目录要有 moox.secrets.toml）")
	return cmd
}
