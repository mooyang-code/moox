package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/sysctl"
	"github.com/spf13/cobra"
)

// moox-cli system：系统级暂停与恢复（设计 2.1）。逐个调用各模块自己的生命周期接口，编排逻辑在 packages/sysctl。

type systemOptions struct {
	Collector, Factor, Strategy, Trade string
	// FactorMgr、Archive、Monitor 只在系统重置里参与（暂停/恢复不涉及它们）。
	FactorMgr, Archive, Monitor string
	File                        string
	Reason                      string
	By                          string
	Timeout                     time.Duration
	ReleasePartial              bool
	Yes                         bool
	JSON                        bool
}

var systemFlags systemOptions

var systemCmd = &cobra.Command{
	Use:   "system",
	Short: "系统级暂停与恢复（按依赖顺序逐个模块，恢复先追赶补采、全部追平后统一放行）",
	Long: `系统级暂停与恢复。默认按 --file（moox.toml）自动找到 collector、factor-engine、strategy、trade 所在的主机，
建立转发（只能出站的主机经 control 中转）并读取各模块的健康密钥，没有部署的模块自动跳过；系统重置另外包含 factor-mgr、archive-mgr、monitor。
不需要手工建隧道或设置环境变量。

直接在模块所在的主机上运行、或要覆盖某个模块的地址时，用 --collector / --factor / --strategy / --trade（重置另有 --factor-mgr / --archive / --monitor）
或环境变量 MOOX_COLLECTOR_HEALTH_ENDPOINT / MOOX_FACTOR_ / MOOX_STRATEGY_ / MOOX_TRADE_ / MOOX_FACTORMGR_ / MOOX_ARCHIVE_ / MOOX_MONITOR_HEALTH_ENDPOINT 指定；
这时鉴权密钥按模块取环境变量 MOOX_<COLLECTOR|FACTOR|STRATEGY|TRADE>_HEALTH_AUTH_ACCESS_KEY / _SECRET_KEY，没有设置时用 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY。

  moox-cli system status                  各模块的阶段与暂停详情、恢复追赶进度
  moox-cli system pause  [--reason 原因]  下游到上游逐个暂停：策略 → Trade → 因子引擎 → 采集
  moox-cli system resume [--timeout 30m]  先追赶后放行：采集补采暂停区间，因子同步并补算，策略记录周期，Trade 与交易所对账；
                                          所有模块都追平之后按上游到下游放行（Trade 先于策略）

  moox-cli system reset plan|apply|status  系统级重置（删除运行数据，--scope all 再删业务配置），见 moox-cli system reset --help

命令幂等：中途断开后再执行一次就按各模块当前状态继续。追赶超时只报告，不自动放行；
--release-partial --yes 在超时后放行已追平的模块，让未追平的继续追赶。`,
	PersistentPreRunE: prepareSystemEndpoints,
}

// prepareSystemEndpoints 为没有显式给地址的模块按 moox.toml 解析健康端点；没有 moox.toml 时保持原样（只用显式给的地址）。
func prepareSystemEndpoints(cmd *cobra.Command, _ []string) error {
	o := &systemFlags
	type item struct {
		name     string
		endpoint *string
	}
	wanted := []item{{"collector", &o.Collector}, {"factor-engine", &o.Factor}, {"strategy", &o.Strategy}, {"trade", &o.Trade}}
	for parent := cmd; parent != nil; parent = parent.Parent() {
		if parent.Name() == "reset" {
			wanted = append(wanted, item{"factor-mgr", &o.FactorMgr}, item{"archive-mgr", &o.Archive}, item{"monitor", &o.Monitor})
			break
		}
	}
	var missing []string
	for _, w := range wanted {
		if *w.endpoint == "" {
			missing = append(missing, w.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	health, err := resolveAndTrackModuleHealth(cmd.Context(), moduleHealthRequest{File: o.File, Modules: missing})
	if errors.Is(err, errManifestUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, w := range wanted {
		if endpoint := health.Endpoint(w.name); endpoint != "" && *w.endpoint == "" {
			*w.endpoint = endpoint
		}
	}
	if len(health.NotDeployed) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "提示：moox.toml 里没有部署 %s，已跳过\n", strings.Join(health.NotDeployed, "、"))
	}
	return nil
}

func init() {
	rootCmd.AddCommand(systemCmd)
	pf := systemCmd.PersistentFlags()
	pf.StringVar(&systemFlags.Collector, "collector", os.Getenv("MOOX_COLLECTOR_HEALTH_ENDPOINT"), "采集模块的健康检查地址")
	pf.StringVar(&systemFlags.Factor, "factor", os.Getenv("MOOX_FACTOR_HEALTH_ENDPOINT"), "因子引擎的健康检查地址")
	pf.StringVar(&systemFlags.Strategy, "strategy", os.Getenv("MOOX_STRATEGY_HEALTH_ENDPOINT"), "策略模块的健康检查地址")
	pf.StringVar(&systemFlags.Trade, "trade", os.Getenv("MOOX_TRADE_HEALTH_ENDPOINT"), "Trade 的健康检查地址")
	pf.StringVar(&systemFlags.FactorMgr, "factor-mgr", os.Getenv("MOOX_FACTORMGR_HEALTH_ENDPOINT"), "因子管理的健康检查地址（系统重置用）")
	pf.StringVar(&systemFlags.Archive, "archive", os.Getenv("MOOX_ARCHIVE_HEALTH_ENDPOINT"), "归档管理的健康检查地址（系统重置用）")
	pf.StringVar(&systemFlags.Monitor, "monitor", os.Getenv("MOOX_MONITOR_HEALTH_ENDPOINT"), "Monitor 的健康检查地址（系统重置用）")
	pf.StringVar(&systemFlags.File, "file", defaultSetupFile, "部署配置文件（moox.toml）；用于定位各模块所在主机")
	pf.StringVar(&systemFlags.By, "by", "", "操作者（默认 cli:<主机名>）")
	pf.BoolVar(&systemFlags.JSON, "json", false, "以 JSON 输出（status）")

	systemCmd.AddCommand(&cobra.Command{
		Use: "status", Short: "查看各模块的阶段与恢复追赶进度", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSystemStatus(cmd.Context(), systemFlags, cmd.OutOrStdout())
		},
	})
	var pauseYes bool
	pause := &cobra.Command{
		Use: "pause", Short: "系统暂停：下游到上游逐个暂停（默认只预览，--yes 才执行）", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !pauseYes {
				// 暂停顺序是下游到上游，预览按这个顺序列出。
				var modules []string
				for _, module := range systemFlags.orchestrator(nil).Modules {
					modules = append([]string{module.Name}, modules...)
				}
				return writePreview(cmd, map[string]any{"action": "system_pause", "modules": modules, "reason": systemFlags.Reason})
			}
			return runSystemPause(cmd.Context(), systemFlags, cmd.OutOrStdout())
		},
	}
	pause.Flags().StringVar(&systemFlags.Reason, "reason", "", "暂停原因（写进暂停详情）")
	pause.Flags().BoolVar(&pauseYes, "yes", false, "确认暂停；不加只预览将要暂停的模块")
	systemCmd.AddCommand(pause)
	resume := &cobra.Command{
		Use: "resume", Short: "系统恢复：先追赶补采，全部追平后统一放行", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSystemResume(cmd.Context(), systemFlags, cmd.OutOrStdout())
		},
	}
	resume.Flags().DurationVar(&systemFlags.Timeout, "timeout", 0, "等待追平的时限（0 表示一直等）；超时只报告，不自动放行")
	resume.Flags().BoolVar(&systemFlags.ReleasePartial, "release-partial", false, "超时后放行已追平的模块、让未追平的继续追赶（需要 --yes）")
	resume.Flags().BoolVar(&systemFlags.Yes, "yes", false, "确认 --release-partial")
	systemCmd.AddCommand(resume)
}

// healthAPI 按模块名取地址与鉴权密钥：模块自己的环境变量优先，没有设置时用通用的。
func healthAPI(prefix, endpoint string) *lifecycle.Client {
	get := func(name string) string {
		if v := os.Getenv("MOOX_" + prefix + "_HEALTH_AUTH_" + name); v != "" {
			return v
		}
		return os.Getenv("MOOX_HEALTH_AUTH_" + name)
	}
	return &lifecycle.Client{Endpoint: endpoint, AuthVersion: get("VERSION"), AccessKey: get("ACCESS_KEY"), SecretKey: get("SECRET_KEY")}
}

// resetAPI 返回用重置专用密钥签名的客户端：模块自己的环境变量优先，没有设置时用通用的；都没有设置返回 nil。
func resetAPI(prefix, endpoint string) sysctl.ResetAPI {
	get := func(name string) string {
		if v := os.Getenv("MOOX_" + prefix + "_HEALTH_RESET_" + name); v != "" {
			return v
		}
		return os.Getenv("MOOX_HEALTH_RESET_" + name)
	}
	access, secret := get("ACCESS_KEY"), get("SECRET_KEY")
	if access == "" || secret == "" {
		return nil
	}
	return &lifecycle.Client{Endpoint: endpoint, AuthVersion: os.Getenv("MOOX_HEALTH_AUTH_VERSION"), AccessKey: access, SecretKey: secret}
}

func (o systemOptions) orchestrator(out io.Writer) *sysctl.Orchestrator {
	by := o.By
	if by == "" {
		host, _ := os.Hostname()
		by = "cli:" + host
	}
	var modules []sysctl.Module
	for _, m := range []struct{ name, prefix, endpoint string }{
		{sysctl.Collector, "COLLECTOR", o.Collector},
		{sysctl.FactorEngine, "FACTOR", o.Factor},
		{sysctl.Strategy, "STRATEGY", o.Strategy},
		{sysctl.Trade, "TRADE", o.Trade},
		{sysctl.FactorMgr, "FACTORMGR", o.FactorMgr},
		{sysctl.Archive, "ARCHIVE", o.Archive},
		{sysctl.Monitor, "MONITOR", o.Monitor},
	} {
		if m.endpoint == "" {
			continue
		}
		modules = append(modules, sysctl.Module{Name: m.name, API: healthAPI(m.prefix, m.endpoint), Reset: resetAPI(m.prefix, m.endpoint)})
	}
	return &sysctl.Orchestrator{Modules: modules, By: by, Out: out}
}

func runSystemStatus(ctx context.Context, o systemOptions, out io.Writer) error {
	orch := o.orchestrator(nil)
	if len(orch.Modules) == 0 {
		return errors.New("没有可操作的模块：moox.toml 里没有部署任何带健康端口的模块，或没有找到 moox.toml（可用 --collector / --factor / --strategy / --trade 指定地址）")
	}
	states := orch.Status(ctx)
	if o.JSON {
		type item struct {
			Module  string               `json:"module"`
			Stage   lifecycle.Stage      `json:"stage,omitempty"`
			Hold    lifecycle.Hold       `json:"hold,omitempty"`
			Pause   *lifecycle.PauseInfo `json:"pause,omitempty"`
			Catchup *lifecycle.Catchup   `json:"catchup,omitempty"`
			Error   string               `json:"error,omitempty"`
		}
		items := make([]item, 0, len(states))
		for _, s := range states {
			it := item{Module: s.Name, Stage: s.Stage, Hold: s.Hold, Pause: s.Pause, Catchup: s.Catchup}
			if s.Err != nil {
				it.Error = s.Err.Error()
			}
			items = append(items, it)
		}
		return writeLifecycleJSON(out, items)
	}
	for _, s := range states {
		if s.Err != nil {
			fmt.Fprintf(out, "%-14s 读取失败：%v\n", s.Name, s.Err)
			continue
		}
		line := fmt.Sprintf("%-14s %s", s.Name, stageText(s.Stage))
		if s.Pause != nil {
			line += "（" + pauseKindText(s.Pause.Kind)
			if s.Pause.Reason != "" {
				line += "：" + s.Pause.Reason
			}
			line += "）"
		}
		if s.Hold != lifecycle.HoldNone {
			line += "，控制意图：" + string(s.Hold)
		}
		if s.Catchup != nil {
			line += "，追赶：" + catchupLine(s.Catchup)
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

func runSystemPause(ctx context.Context, o systemOptions, out io.Writer) error {
	orch := o.orchestrator(out)
	if len(orch.Modules) == 0 {
		return errors.New("没有可操作的模块：moox.toml 里没有部署任何带健康端口的模块，或没有找到 moox.toml（可用 --collector / --factor / --strategy / --trade 指定地址）")
	}
	result, err := orch.Pause(ctx, o.Reason)
	for _, w := range result.Warnings {
		fmt.Fprintf(out, "提示：%s\n", w)
	}
	if err != nil {
		fmt.Fprintf(out, "已暂停：%v；其余模块没有暂停（暂停无损，重复执行即可继续）\n", result.Paused)
		return err
	}
	fmt.Fprintf(out, "系统已暂停：%v\n", result.Paused)
	return nil
}

func runSystemResume(ctx context.Context, o systemOptions, out io.Writer) error {
	if o.ReleasePartial && !o.Yes {
		return errors.New("--release-partial 会在超时后放行已追平的模块、保持其余模块继续追赶，需要加 --yes 确认")
	}
	orch := o.orchestrator(out)
	if len(orch.Modules) == 0 {
		return errors.New("没有可操作的模块：moox.toml 里没有部署任何带健康端口的模块，或没有找到 moox.toml（可用 --collector / --factor / --strategy / --trade 指定地址）")
	}
	result, err := orch.Resume(ctx, sysctl.ResumeOptions{Timeout: o.Timeout, ReleasePartial: o.ReleasePartial})
	if err != nil {
		fmt.Fprintf(out, "已放行：%v\n", result.Released)
		for name, why := range result.Pending {
			fmt.Fprintf(out, "未放行 %s：%s\n", name, why)
		}
		return err
	}
	if result.TimedOut {
		fmt.Fprintln(out, "等待追平超时，没有自动放行：")
	}
	for name, why := range result.Pending {
		fmt.Fprintf(out, "未放行 %s：%s\n", name, why)
	}
	if len(result.Pending) == 0 {
		fmt.Fprintf(out, "系统已恢复：已放行 %v\n", result.Released)
		return nil
	}
	if result.TimedOut {
		return errors.New("部分模块没有追平；再次执行 system resume 继续等待，或加 --release-partial --yes 放行已追平的模块")
	}
	return nil
}
