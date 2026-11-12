package command

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/config"
	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/spf13/cobra"
)

type doctorExitError struct{ code int }

func (e doctorExitError) Error() string { return fmt.Sprintf("doctor exited with status %d", e.code) }
func (e doctorExitError) ExitCode() int { return e.code }

type doctorCommandDeps struct {
	loadConfig func() (*config.Config, error)
	newClient  func(gatewayclient.Invoker) *doctorcli.Client
}

func init() {
	rootCmd.AddCommand(newDoctorCommand(doctorCommandDeps{
		loadConfig: config.LoadConfig,
		newClient:  doctorcli.New,
	}))
}

func newDoctorCommand(deps doctorCommandDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "doctor", Short: "部署与运行诊断：告警、新部署检查、按监控事实诊断模块", SilenceUsage: true}
	cmd.AddCommand(newDoctorModeCommand("bootstrap", deps), newDoctorModeCommand("diagnose", deps), newDoctorAlertsCommand(deps))
	return cmd
}

// newDoctorAlertsCommand 读取 Monitor 的健康概览，输出首页“需要处理”里属于监控的部分（当前告警与不健康的数据集、
// 业务检查、组件、主机），默认 JSON，便于 Agent 逐项处理；退出码同其他 doctor 子命令（0 正常、1 降级、2 异常、3 无法判断）。
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
	cmd.Flags().StringVar(&file, "file", "./moox.toml", "deployment manifest for the operator SSH gateway")
	cmd.Flags().StringVar(&space, "space-id", "", "只看某个空间；为空时看全部")
	cmd.Flags().StringVar(&format, "format", "json", "报告格式：json、text 或 markdown")
	cmd.Flags().StringVar(&output, "output", "", "把报告原子写入该路径")
	return cmd
}

func newDoctorModeCommand(mode string, deps doctorCommandDeps) *cobra.Command {
	var nodeID, format, output, file string
	var checks []string
	cmd := &cobra.Command{
		Use:          mode,
		Short:        map[string]string{"bootstrap": "检查新部署各模块的安装与配置", "diagnose": "按 Monitor 的事实诊断本机的模块；操作电脑上看远端用 setup logs"}[mode],
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return validateDoctorFlags(format, output, checks)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := deps.loadConfig()
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				cfg = &config.Config{}
			}
			doctorCfg := cfg.EffectiveDoctor()
			if nodeID == "" {
				nodeID = doctorCfg.NodeID
			}
			gateway, err := openCommandGateway(cmd.Context(), file, nil)
			if err != nil {
				return err
			}
			defer gateway.Close()
			client := deps.newClient(gateway)
			auth, err := loadDoctorHealthAuth(doctorCfg.ReleaseRoot)
			if err != nil {
				return err
			}
			prober := doctorcli.HTTPProber{Auth: auth}
			// 外部程序（systemd 用户服务）与只能出站的主机：状态经 SSH（本机则直接执行）读取，健康经 control 中转探测。
			var systemd doctorcli.SystemdChecker
			var relay *doctorcli.RelayProber
			var deployRoot string
			if remote := loadHostRemote(file, doctorCfg.NodeID, gateway); remote != nil {
				defer remote.Close()
				systemd, relay = remote.Systemd(), remote.Relay(auth)
				deployRoot = remote.manifest.Paths.Resolved().DeployRoot
			}
			var reportValue core.Report
			switch mode {
			case "bootstrap":
				reportValue, err = doctorcli.RunBootstrap(cmd.Context(), doctorcli.BootstrapOptions{
					NodeID: nodeID, LocalNodeID: doctorCfg.NodeID, ReleaseRoot: doctorCfg.ReleaseRoot,
					DatasetHealthPolicyPath: resolveReleasePath(doctorCfg.ReleaseRoot, doctorCfg.DatasetHealthPolicyPath),
					CheckIDs:                checks, Client: client, MonitorClient: client, Prober: prober,
					Systemd: systemd, DeployRoot: deployRoot,
				})
			case "diagnose":
				if doctorCfg.ReleaseRoot == "" {
					fmt.Fprintln(cmd.ErrOrStderr(), "提示：这台电脑没有部署目录，diagnose 只查本机；看目标机日志用 moox-cli setup logs --host <主机> --component <组件>")
				}
				reportValue, err = doctorcli.RunDiagnose(cmd.Context(), doctorcli.DiagnoseOptions{
					NodeID: nodeID, CheckIDs: checks, Client: client, Prober: prober, Relay: relay, Systemd: systemd,
					HealthChecks: report.BuiltInModuleHealthChecks(),
				})
			}
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
	cmd.Flags().StringVar(&file, "file", "./moox.toml", "deployment manifest for the operator SSH gateway")
	cmd.Flags().StringVar(&nodeID, "node", "", "target node ID")
	cmd.Flags().StringSliceVar(&checks, "check", nil, "specific bounded check ID")
	cmd.Flags().StringVar(&format, "format", "json", "report format: json, text, or markdown")
	cmd.Flags().StringVar(&output, "output", "", "atomically write the report to this path")
	return cmd
}

func validateDoctorFlags(format, output string, checks []string) error {
	if format != "json" && format != "text" && format != "markdown" {
		return fmt.Errorf("format must be json, text, or markdown")
	}
	if len(checks) > core.MaxSelectedChecks {
		return fmt.Errorf("check selection exceeds %d", core.MaxSelectedChecks)
	}
	seen := map[string]bool{}
	for _, check := range checks {
		if strings.TrimSpace(check) == "" || seen[check] {
			return fmt.Errorf("check IDs must be non-empty and unique")
		}
		seen[check] = true
	}
	if output != "" {
		info, err := os.Stat(filepath.Dir(output))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("output directory must already exist")
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

func resolveReleasePath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func loadDoctorHealthAuth(releaseRoot string) (doctorcli.HealthAuth, error) {
	auth := doctorcli.HealthAuth{Version: envOr("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1"), AccessKey: envOr("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor"), SecretKey: os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY")}
	if auth.SecretKey != "" {
		return auth, nil
	}
	path := filepath.Join(releaseRoot, "secrets", "health-auth.env")
	info, statErr := os.Lstat(path)
	if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		return auth, fmt.Errorf("health auth file must be a regular 0600 file")
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return auth, nil
		}
		return auth, err
	}
	defer file.Close()
	if err := readHealthEnv(file, &auth); err != nil {
		return auth, err
	}
	return auth, nil
}

func readHealthEnv(reader io.Reader, auth *doctorcli.HealthAuth) error {
	scanner := bufio.NewScanner(io.LimitReader(reader, 64<<10))
	for scanner.Scan() {
		name, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		switch name {
		case "MOOX_HEALTH_AUTH_VERSION":
			auth.Version = value
		case "MOOX_HEALTH_AUTH_ACCESS_KEY":
			auth.AccessKey = value
		case "MOOX_HEALTH_AUTH_SECRET_KEY":
			auth.SecretKey = value
		}
	}
	return scanner.Err()
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
