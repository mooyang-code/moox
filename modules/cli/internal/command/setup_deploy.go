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
	if s.operatorDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		s.operatorDir = filepath.Join(home, ".config", "moox")
	}
	if s.operatorDir, err = filepath.Abs(s.operatorDir); err != nil {
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

// deployOptions 组出一台主机的部署参数。
func (s setupSource) deployOptions(cmd *cobra.Command, snapshot *setupSnapshot, file, host string, components []string) deploy.Options {
	return deploy.Options{
		Config: snapshot.Manifest, Secrets: snapshot.Secrets, HostID: host, Components: components,
		RepositoryRoot: s.root, BinaryDirectory: s.binaryDir, Bundle: s.bundle, OperatorDirectory: s.operatorDir,
		Version: Version, Commit: GitCommit, Dial: dialSetupHost, Log: cmd.ErrOrStderr(),
		SyncHost: func(ctx context.Context, hostID string) error {
			gateway, err := openCommandGateway(ctx, file, snapshot.Manifest)
			if err != nil {
				return err
			}
			defer gateway.Close()
			return setupclient.New(gateway).SyncHostDeployments(ctx, snapshot.Manifest, hostID)
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

// setupComponentOnHost 校验主机和组件：组件要在这台主机该有的组件里（含每台主机都有的主机级组件与内置对象存储）。
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
	var source setupSource
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "把一台主机部署成 moox.toml 描述的样子：放二进制、渲染配置、按顺序重启有变化的组件，等它们就绪",
		Long: `部署一台主机上的全部组件，或用 --component 只部署其中几个。每个组件是一个 systemd 用户服务 moox-<组件>。
重跑同一条命令就是再做一遍：没有变化的组件不重启；上次装好之后被停用（setup stop）的组件只更新文件、保持停用。
控制机每次部署都会先执行 moox-admin-cli bootstrap（建库、CA、主密钥、管理员、登记主机与部署，只建不改）；
其余主机先把登记同步到 Admin，再向控制机要身份材料。只部署个别组件时，服务目录变了会把这台主机的主机网关一起升级。
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
			if err := setupvalidate.CheckDeployments(snapshot.Manifest); err != nil {
				return err
			}
			result, err := deps.deployHost(cmd.Context(), source.deployOptions(cmd, snapshot, file, host, components))
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
	source.flags(cmd)
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

// setupStatusOutput 是 setup status 的输出。
type setupStatusOutput struct {
	Route string              `json:"route"`
	Hosts []deploy.HostStatus `json:"hosts"`
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
			output := setupStatusOutput{Route: deployscan.Route(reports), Hosts: []deploy.HostStatus{}}
			for _, report := range reports {
				expected, err := snapshot.Manifest.HostComponents(report.Host)
				if err != nil {
					return err
				}
				version, err := deploy.ParseVersion(report.Version)
				if err != nil {
					return fmt.Errorf("version_invalid: 主机 %s 的 version.json 无法解析", report.Host)
				}
				output.Hosts = append(output.Hosts, deploy.Status(report.Host, expected, version, report.Units, report.Active))
			}
			return writeSetupJSON(cmd, output)
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
	return newSetupOperateCommand(deps, "stop", deploy.OperationStop, "停掉一个组件并不再开机自启（systemctl --user disable --now）；之后的部署只更新它的文件、保持停用")
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
	cmd.Flags().StringVar(&component, "component", "", "组件 ID，比如 monitor、access；不写看全部")
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
	var repositoryRoot, binaryDir, outputDir, version, goarch string
	cmd := &cobra.Command{
		Use:   "package-components",
		Short: "按服务目录把编译好的 Linux 二进制打成每个组件一个包（发布脚本用）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if binaryDir == "" || outputDir == "" || version == "" {
				return errors.New("package-components 要带 --binary-dir、--output-dir 与 --version")
			}
			if repositoryRoot == "" {
				root, err := os.Getwd()
				if err != nil {
					return err
				}
				repositoryRoot = root
			}
			catalog, err := servicecatalog.LoadEmbedded()
			if err != nil {
				return err
			}
			components, err := bundle.PackageComponents(catalog, repositoryRoot, binaryDir, outputDir, version, goarch)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"catalog_sha256": bundle.CatalogSHA256(), "components": components})
		},
	}
	cmd.Flags().StringVar(&repositoryRoot, "repo-root", "", "仓库根目录（默认当前目录）：第三方组件的许可证从这里读")
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

// setupDeployment 是目标机器的部署情况。Route：fresh（都没装过）或 installed（至少一台装过）。
type setupDeployment struct {
	Route string `json:"route"`
	// NextSteps 是接下来要运行的命令；ManualSteps 是要用户在目标机上自己做的事（需要 sudo），做完再继续。
	NextSteps   []string                `json:"next_steps"`
	ManualSteps []string                `json:"manual_steps,omitempty"`
	Warnings    []string                `json:"warnings,omitempty"`
	Hosts       []deployscan.HostReport `json:"hosts"`
}

// minDeployDiskKB 是部署前每台主机至少要有的可用空间：放得下全部二进制的新旧两份和下载的组件包。
const minDeployDiskKB = 2 << 20

// setupDeploymentStatus 只读检测各主机，给出路线、要用户先做的事与提醒。
func setupDeploymentStatus(ctx context.Context, deps setupDeps, snapshot *setupSnapshot) (*setupDeployment, error) {
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{})
	if err != nil {
		return nil, err
	}
	status := &setupDeployment{Route: deployscan.Route(reports), Hosts: reports, NextSteps: []string{}}
	for _, report := range reports {
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
		status.NextSteps = append(status.NextSteps, "moox-cli setup install --space-id <空间>（先不带 --yes 看预览）")
	} else {
		status.NextSteps = append(status.NextSteps, "moox-cli setup status（看各主机的版本与组件状态）", "moox-cli setup deploy --host <主机>（升级或改配置后重新部署一台主机）")
	}
	return status, nil
}

func newSetupValidateCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "validate", Short: "校验两份配置和连接，并报告各主机的部署情况（route）和接下来要运行的命令（next_steps）", Args: cobra.NoArgs,
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
