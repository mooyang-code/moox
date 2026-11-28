package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/spf13/cobra"
)

type doctorExitError struct{ code int }

func (e doctorExitError) Error() string { return fmt.Sprintf("自检退出，状态码 %d", e.code) }
func (e doctorExitError) ExitCode() int { return e.code }

type doctorCommandDeps struct {
	newClient func(gatewayclient.Invoker) *doctorcli.Client
}

type doctorEnv struct {
	nodeID     string
	deployRoot string
}

func resolveDoctorEnv() doctorEnv {
	nodeID := strings.TrimSpace(os.Getenv("MOOX_NODE_ID"))
	if nodeID == "" {
		nodeID, _ = os.Hostname()
	}
	return doctorEnv{
		nodeID:     nodeID,
		deployRoot: strings.TrimSpace(os.Getenv("MOOX_DEPLOY_ROOT")),
	}
}

func init() {
	rootCmd.AddCommand(newDoctorCommand(doctorCommandDeps{
		newClient: doctorcli.New,
	}))
}

func newDoctorCommand(deps doctorCommandDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "doctor", Short: "安装后自检与监控告警查看", SilenceUsage: true}
	cmd.AddCommand(newDoctorBootstrapCommand(deps), newDoctorAlertsCommand(deps))
	return cmd
}

// newDoctorAlertsCommand 读取 Monitor 的健康概览，输出首页“需要处理”里属于监控的部分（当前告警与不健康的数据集、
// 业务检查、组件、主机），默认 JSON，便于 Agent 逐项处理；退出码同自检命令（0 正常、1 降级、2 异常、3 无法判断）。
func newDoctorAlertsCommand(deps doctorCommandDeps) *cobra.Command {
	var space, format, output, file string
	cmd := &cobra.Command{
		Use:          "alerts",
		Short:        "读取 Monitor 的告警与不健康项，供 Agent 处理",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return validateDoctorFlags(format, output, nil)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			gateway, err := openCommandGateway(cmd.Context(), file, nil)
			if err != nil {
				return err
			}
			defer gateway.Close()
			newClient := deps.newClient
			if newClient == nil {
				newClient = doctorcli.New
			}
			overview, err := newClient(gateway).GetHealthOverview(cmd.Context(), space)
			if err != nil {
				return err
			}
			alerts := doctorcli.BuildAlertsReport(overview)
			rendered, err := doctorcli.RenderAlerts(alerts, format)
			if err != nil {
				return err
			}
			if output != "" {
				if err := doctorcli.WriteAtomic(output, rendered); err != nil {
					return err
				}
			} else if _, err := cmd.OutOrStdout().Write(rendered); err != nil {
				return err
			}
			if code := doctorExitCode(alerts.Conclusion); code != 0 {
				return doctorExitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "./moox.toml", "操作员 SSH 网关使用的部署配置文件")
	cmd.Flags().StringVar(&space, "space-id", "", "只看某个空间；为空时看全部")
	cmd.Flags().StringVar(&format, "format", "json", "报告格式：json、text 或 markdown")
	cmd.Flags().StringVar(&output, "output", "", "把报告原子写入该路径")
	return cmd
}

// newDoctorBootstrapCommand 对一台主机执行安装后自检：
// 第一部分在目标机上探测版本摘要、主机清单、服务身份、网络、路径权限、自启与 systemd 状态、本机就绪端口；
// 第二部分直接读取 Monitor 的健康概览（GetHealthOverview），检查各组件是否已在上报、是否就绪、有没有严重告警。
func newDoctorBootstrapCommand(deps doctorCommandDeps) *cobra.Command {
	var nodeID, format, output, file string
	var checks []string
	cmd := &cobra.Command{
		Use:          "bootstrap",
		Short:        "安装后自检一台主机：核对本机事实并读取 Monitor 的上报、就绪与严重告警",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return validateDoctorFlags(format, output, checks)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			env := resolveDoctorEnv()
			if nodeID == "" {
				nodeID = env.nodeID
			}
			gateway, err := openCommandGateway(cmd.Context(), file, nil)
			if err != nil {
				return err
			}
			defer gateway.Close()
			newClient := deps.newClient
			if newClient == nil {
				newClient = doctorcli.New
			}
			client := newClient(gateway)
			// 健康端口只听本机：本节点直接请求 127.0.0.1，其他主机经 SSH 在目标机上请求；
			// systemd 用户服务状态与目录可写探测同样经 SSH（本机则直接执行）完成。读不到 moox.toml 时只能查本机。
			probe := doctorcli.LocalHealthProbe(env.nodeID)
			var systemd doctorcli.SystemdChecker
			var readFile func(context.Context, string, string) ([]byte, error)
			var probeWritable func(context.Context, string, string) error
			deployRoot := firstNonEmpty(env.deployRoot, topology.DefaultDeployRoot)
			remote := loadHostRemote(file, env.nodeID)
			if remote != nil {
				defer remote.Close()
				systemd, probe = remote.Systemd(), remote.HealthProbe()
				readFile = remote.ReadFile
				probeWritable = remote.ProbeWritable(nodeID)
				if env.deployRoot == "" {
					deployRoot = remote.manifest.Paths.DeployRoot
				}
			}
			reportValue, err := doctorcli.RunBootstrap(cmd.Context(), doctorcli.BootstrapOptions{
				NodeID:        nodeID,
				LocalNodeID:   env.nodeID,
				CheckIDs:      checks,
				Client:        client,
				MonitorClient: client,
				Probe:         probe,
				Systemd:       systemd,
				DeployRoot:    deployRoot,
				ReadFile:      readFile,
				ProbeWritable: probeWritable,
			})
			if err != nil {
				return err
			}
			rendered, err := doctorcli.Render(reportValue, format)
			if err != nil {
				return err
			}
			if output != "" {
				if err := doctorcli.WriteAtomic(output, rendered); err != nil {
					return err
				}
			} else if _, err := cmd.OutOrStdout().Write(rendered); err != nil {
				return err
			}
			if code := doctorExitCode(reportValue.Conclusion); code != 0 {
				return doctorExitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "./moox.toml", "操作员 SSH 网关使用的部署配置文件")
	cmd.Flags().StringVar(&nodeID, "node", "", "目标主机 ID（默认本机）")
	cmd.Flags().StringSliceVar(&checks, "check", nil, "只跑指定的自检项 ID")
	cmd.Flags().StringVar(&format, "format", "json", "报告格式：json、text 或 markdown")
	cmd.Flags().StringVar(&output, "output", "", "把报告原子写入该路径")
	return cmd
}

func validateDoctorFlags(format, output string, checks []string) error {
	if format != "json" && format != "text" && format != "markdown" {
		return fmt.Errorf("报告格式必须是 json、text 或 markdown")
	}
	if len(checks) > core.MaxSelectedChecks {
		return fmt.Errorf("指定的自检项超过上限 %d", core.MaxSelectedChecks)
	}
	seen := map[string]bool{}
	for _, check := range checks {
		if strings.TrimSpace(check) == "" || seen[check] {
			return fmt.Errorf("自检项 ID 不能为空且不能重复")
		}
		seen[check] = true
	}
	if output != "" {
		info, err := os.Stat(filepath.Dir(output))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("输出路径所在目录必须已存在")
		}
	}
	return nil
}

func doctorExitCode(conclusion core.Conclusion) int {
	switch conclusion {
	case core.ConclusionHealthy:
		return 0
	case core.ConclusionDegraded:
		return 1
	case core.ConclusionUnhealthy:
		return 2
	default:
		return 3
	}
}
