package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/sysctl"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/spf13/cobra"
)

// moox-cli system：系统级暂停与恢复（设计 2.1）。经操作员网关逐个调用各模块服务的 GetLifecycle、ControlLifecycle，
// 编排逻辑在 internal/sysctl。

type systemOptions struct {
	File           string
	Reason         string
	By             string
	Timeout        time.Duration
	ReleasePartial bool
	Yes            bool
	JSON           bool
}

var systemFlags systemOptions

// systemModules 是参与系统暂停的模块（顺序与依赖由 internal/sysctl 决定）；factor-mgr、archive-mgr、Monitor 不参与。
var systemModules = []string{sysctl.Collector, sysctl.FactorEngine, sysctl.Strategy, sysctl.Trade}

// systemPollInterval 是恢复时轮询追赶进度的间隔（测试里调小）。
var systemPollInterval = 5 * time.Second

var systemCmd = &cobra.Command{
	Use:   "system",
	Short: "系统级暂停与恢复（按依赖顺序逐个模块，恢复先追赶补采、全部追平后统一放行）",
	Long: `系统级暂停与恢复：collector、factor-engine、strategy、trade 四个模块，经操作员网关调用各模块服务的
GetLifecycle、ControlLifecycle（因子机只能出站时经 control 中转），按 --file（moox.toml）建立隧道；服务目录里没有部署的模块自动跳过。

  moox-cli system status                  各模块的阶段与暂停详情、恢复追赶进度
  moox-cli system pause  [--reason 原因]  下游到上游逐个暂停：策略 → Trade → 因子引擎 → 采集
  moox-cli system resume [--timeout 30m]  先追赶后放行：采集补采暂停区间，因子同步并补算，策略记录周期，Trade 与交易所对账；
                                          所有模块都追平之后按上游到下游放行（Trade 先于策略）

命令幂等：中途断开后再执行一次就按各模块当前状态继续。追赶超时只报告，不自动放行；
--release-partial --yes 在超时后放行已追平的模块，让未追平的继续追赶。`,
}

func init() {
	rootCmd.AddCommand(systemCmd)
	pf := systemCmd.PersistentFlags()
	pf.StringVar(&systemFlags.File, "file", defaultSetupFile, "部署配置文件（moox.toml）；用于解析操作员 SSH 网关")
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
				return previewSystemPause(cmd, systemFlags)
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

// withSystem 打开操作员网关，按服务目录装配编排器（没有部署的模块跳过并提示）交给 fn。
func withSystem(ctx context.Context, o systemOptions, out, warn io.Writer, fn func(*sysctl.Orchestrator) error) error {
	gateway, err := openCommandGateway(ctx, o.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	by := o.By
	if by == "" {
		host, _ := os.Hostname()
		by = "cli:" + host
	}
	orch := &sysctl.Orchestrator{By: by, Out: out, Poll: systemPollInterval}
	var skipped []string
	for _, name := range systemModules {
		module, _ := lifecycleModuleByName(name)
		if !deployedService(gateway, module.Service) {
			skipped = append(skipped, name)
			continue
		}
		orch.Modules = append(orch.Modules, sysctl.Module{Name: name, API: &lifecycle.Remote{Gateway: gateway, Service: module.Service}})
	}
	if len(skipped) > 0 && warn != nil {
		fmt.Fprintf(warn, "提示：服务目录里没有部署 %s，已跳过\n", strings.Join(skipped, "、"))
	}
	if len(orch.Modules) == 0 {
		return errors.New("没有可操作的模块：服务目录里 collector、factor-engine、strategy、trade 都没有部署")
	}
	return fn(orch)
}

// previewSystemPause 只列出将要暂停的模块（按暂停顺序：下游到上游），不执行。
func previewSystemPause(cmd *cobra.Command, o systemOptions) error {
	return withSystem(cmd.Context(), o, nil, cmd.ErrOrStderr(), func(orch *sysctl.Orchestrator) error {
		var modules []string
		for _, module := range orch.Modules {
			modules = append([]string{module.Name}, modules...)
		}
		return writePreview(cmd, map[string]any{"action": "system_pause", "modules": modules, "reason": o.Reason})
	})
}

func runSystemStatus(ctx context.Context, o systemOptions, out io.Writer) error {
	return withSystem(ctx, o, nil, os.Stderr, func(orch *sysctl.Orchestrator) error {
		states := orch.Status(ctx)
		if o.JSON {
			type item struct {
				Module  string               `json:"module"`
				Stage   lifecycle.Stage      `json:"stage,omitempty"`
				Pause   *lifecycle.PauseInfo `json:"pause,omitempty"`
				Catchup *lifecycle.Catchup   `json:"catchup,omitempty"`
				Error   string               `json:"error,omitempty"`
			}
			items := make([]item, 0, len(states))
			for _, s := range states {
				it := item{Module: s.Name, Stage: s.Stage, Pause: s.Pause, Catchup: s.Catchup}
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
			if s.Catchup != nil {
				line += "，追赶：" + catchupLine(s.Catchup)
			}
			fmt.Fprintln(out, line)
		}
		return nil
	})
}

func runSystemPause(ctx context.Context, o systemOptions, out io.Writer) error {
	return withSystem(ctx, o, out, out, func(orch *sysctl.Orchestrator) error {
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
	})
}

func runSystemResume(ctx context.Context, o systemOptions, out io.Writer) error {
	if o.ReleasePartial && !o.Yes {
		return errors.New("--release-partial 会在超时后放行已追平的模块、保持其余模块继续追赶，需要加 --yes 确认")
	}
	return withSystem(ctx, o, out, out, func(orch *sysctl.Orchestrator) error {
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
	})
}
