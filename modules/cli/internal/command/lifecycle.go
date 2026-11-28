package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	"github.com/mooyang-code/moox/modules/cli/internal/sysctl"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

// lifecycleModule 是有模块级生命周期的模块：GetLifecycle、ControlLifecycle 在它的服务上（设计 3.7）。
type lifecycleModule struct {
	Name    string // 命令行里的模块名（与服务目录的组件 ID 一致）
	Service string
}

// lifecycleModules 是 moox-cli lifecycle 能操作的模块。前四个参与系统暂停（moox-cli system），factor-mgr 不参与；
// Monitor 的暂停就是告警屏蔽，用 moox-cli monitor mute。
var lifecycleModules = []lifecycleModule{
	{sysctl.Collector, collectMgr},
	{sysctl.FactorEngine, engineControl},
	{sysctl.Strategy, "trpc.moox.strategy.StrategyMgr"},
	{sysctl.Trade, "trpc.moox.trade.TradeConsoleService"},
	{"factor-mgr", factorMgr},
}

func lifecycleModuleByName(name string) (lifecycleModule, bool) {
	for _, module := range lifecycleModules {
		if module.Name == name {
			return module, true
		}
	}
	return lifecycleModule{}, false
}

func lifecycleModuleNames() string {
	names := make([]string, 0, len(lifecycleModules))
	for _, module := range lifecycleModules {
		names = append(names, module.Name)
	}
	return strings.Join(names, "、")
}

// directoryGateway 是给得出服务目录的网关（操作员网关实现，见 gatewayclient.Client.Directory）。
type directoryGateway interface {
	Directory() servicecatalog.Directory
}

// 真实的操作员网关必须给得出服务目录，否则 deployedService 会把没部署的模块当作已部署。
var _ directoryGateway = (*gatewayio.Client)(nil)

// deployedService 报告服务有没有启用的部署：操作员网关给得出服务目录时按目录判断，给不出（测试替身）时当作已部署。
func deployedService(gateway any, service string) bool {
	if source, ok := gateway.(directoryGateway); ok {
		return len(source.Directory().Services[service]) > 0
	}
	return true
}

// lifecycleOptions 是 moox-cli lifecycle 的参数。
type lifecycleOptions struct {
	// Module 是要操作的模块（factor-engine、strategy、trade……）。
	Module string
	File   string
	JSON   bool
	// 暂停与恢复的参数。
	Reason        string
	PauseKind     string
	By            string
	ManualRelease bool
	// ExpiresIn 用于 pause：这么久之后自动恢复（0 表示不自动恢复）。
	ExpiresIn time.Duration
	Yes       bool
}

var lifecycleFlags lifecycleOptions

var lifecycleCmd = &cobra.Command{
	Use:   "lifecycle",
	Short: "查看和控制模块的生命周期（运行、暂停、恢复追赶）",
	Long: `查看和控制一个模块的生命周期。模块只有三种状态：运行、暂停、恢复追赶。

  moox-cli lifecycle status                      查看状态（暂停详情、追赶进度）
  moox-cli lifecycle pause  [--reason 原因] [--pause-kind manual|system] [--for 2h] [--yes]
                                                 暂停（默认只预览，--yes 才执行；默认手动暂停：只有用户 resume 才恢复；--for 到点自动恢复）
  moox-cli lifecycle resume [--pause-kind system] [--manual-release]
                                                 恢复：先进入恢复追赶（补齐暂停期间缺失的数据，不计算、不下单），追平后自动放行回到运行；
                                                 --manual-release 追平后停在待放行，等 release（系统级恢复用）；
                                                 --pause-kind system 只恢复系统暂停（不动手动暂停）
  moox-cli lifecycle release                     放行：恢复追赶已追平的模块回到运行
  moox-cli lifecycle catchup                     先让模块推进一次恢复追赶，再看进度

默认操作 factor-engine，用 --module 换成 collector、strategy、trade、factor-mgr。
经操作员网关调用模块服务的 GetLifecycle、ControlLifecycle（因子机只能出站时经 control 中转），按 --file（moox.toml）建立隧道。
Monitor 的暂停就是告警屏蔽，用 moox-cli monitor mute / unmute。
因子任务能不能计算看 moox-cli factor task status，策略实例的确认实盘用 moox-cli strategy instance confirm。`,
}

func init() {
	rootCmd.AddCommand(lifecycleCmd)
	pf := lifecycleCmd.PersistentFlags()
	pf.StringVar(&lifecycleFlags.Module, "module", sysctl.FactorEngine, "要操作的模块："+lifecycleModuleNames())
	pf.StringVar(&lifecycleFlags.File, "file", defaultSetupFile, "部署配置文件（moox.toml）；用于解析操作员 SSH 网关")
	pf.BoolVar(&lifecycleFlags.JSON, "json", false, "以 JSON 输出")
	pf.StringVar(&lifecycleFlags.Reason, "reason", "", "暂停原因（写进暂停详情）")
	pf.StringVar(&lifecycleFlags.PauseKind, "pause-kind", "", "pause：暂停类型 manual（默认）| system；resume：只恢复该类型的暂停")
	pf.StringVar(&lifecycleFlags.By, "by", "", "操作者（默认 cli:<主机名>）")
	pf.DurationVar(&lifecycleFlags.ExpiresIn, "for", 0, "pause：这么久之后自动恢复（先追赶再放行），例如 2h；不能用于系统暂停")
	pf.BoolVar(&lifecycleFlags.ManualRelease, "manual-release", false, "resume：追平后不自动放行，等 release")

	lifecycleCmd.AddCommand(&cobra.Command{
		Use: "status", Short: "查看生命周期状态", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLifecycleStatus(cmd.Context(), lifecycleFlags, cmd.OutOrStdout())
		},
	})
	lifecycleCmd.AddCommand(&cobra.Command{
		Use: "catchup", Short: "查看恢复追赶的进度", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLifecycleCatchup(cmd.Context(), lifecycleFlags, cmd.OutOrStdout())
		},
	})
	pause := &cobra.Command{
		Use:   string(lifecycle.CmdPause),
		Short: "暂停：不再计算新周期、不下单（默认只预览，--yes 才执行；--for 可设定自动恢复）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !lifecycleFlags.Yes {
				return previewLifecyclePause(cmd, lifecycleFlags)
			}
			return runLifecycleCommand(cmd.Context(), lifecycleFlags, lifecycle.CmdPause, cmd.OutOrStdout())
		},
	}
	pause.Flags().BoolVar(&lifecycleFlags.Yes, "yes", false, "确认暂停；不加只预览将要暂停的模块")
	lifecycleCmd.AddCommand(pause)
	for _, item := range []struct {
		name  lifecycle.Command
		short string
	}{
		{lifecycle.CmdResume, "恢复：先追赶暂停期间的周期，追平后放行"},
		{lifecycle.CmdRelease, "放行：追赶已追平的模块回到运行"},
	} {
		name := item.name
		lifecycleCmd.AddCommand(&cobra.Command{
			Use: string(name), Short: item.short, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runLifecycleCommand(cmd.Context(), lifecycleFlags, name, cmd.OutOrStdout())
			},
		})
	}
}

// previewLifecyclePause 只列出将要暂停的模块和参数（校验模块已部署），不发出暂停请求。
func previewLifecyclePause(cmd *cobra.Command, o lifecycleOptions) error {
	pauseKind := o.PauseKind
	if pauseKind == "" {
		pauseKind = string(lifecycle.PauseManual)
	}
	return withLifecycle(cmd.Context(), o, func(remote *lifecycle.Remote) error {
		details := map[string]any{
			"action":     "lifecycle_pause",
			"module":     o.Module,
			"service":    remote.Service,
			"pause_kind": pauseKind,
			"reason":     o.Reason,
		}
		if o.ExpiresIn > 0 {
			details["for"] = o.ExpiresIn.String()
		}
		return writePreview(cmd, details)
	})
}

// withLifecycle 打开操作员网关，把模块的生命周期客户端交给 fn；模块没有部署时报错。
func withLifecycle(ctx context.Context, o lifecycleOptions, fn func(*lifecycle.Remote) error) error {
	module, ok := lifecycleModuleByName(o.Module)
	if !ok {
		return fmt.Errorf("未知的模块 %q（%s）", o.Module, lifecycleModuleNames())
	}
	gateway, err := openCommandGateway(ctx, o.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if !deployedService(gateway, module.Service) {
		return fmt.Errorf("服务目录里没有启用的 %s（%s 没有部署）", module.Name, module.Service)
	}
	return fn(&lifecycle.Remote{Gateway: gateway, Service: module.Service})
}

func runLifecycleStatus(ctx context.Context, o lifecycleOptions, out io.Writer) error {
	return withLifecycle(ctx, o, func(remote *lifecycle.Remote) error {
		state, err := remote.Status(ctx)
		if err != nil {
			return err
		}
		if o.JSON {
			return writeLifecycleJSON(out, state)
		}
		printState(out, state)
		return nil
	})
}

func runLifecycleCommand(ctx context.Context, o lifecycleOptions, cmd lifecycle.Command, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	by := o.By
	if by == "" {
		host, _ := os.Hostname()
		by = "cli:" + host
	}
	req := lifecycle.Request{Command: cmd, PauseKind: lifecycle.PauseKind(o.PauseKind), Reason: o.Reason, By: by, ManualRelease: o.ManualRelease}
	if o.ExpiresIn > 0 {
		if cmd != lifecycle.CmdPause {
			return errors.New("--for 只用于 pause")
		}
		req.ExpiresAt = time.Now().Add(o.ExpiresIn)
	}
	return withLifecycle(ctx, o, func(remote *lifecycle.Remote) error {
		state, err := remote.Exec(ctx, req)
		if err != nil {
			return err
		}
		if o.JSON {
			return writeLifecycleJSON(out, state)
		}
		printState(out, state)
		return nil
	})
}

func printState(out io.Writer, state lifecycle.State) {
	line := fmt.Sprintf("模块 %s：%s", state.Module, stageText(state.Stage))
	if state.Pause != nil {
		line += "（" + pauseKindText(state.Pause.Kind)
		if state.Pause.By != "" {
			line += "，" + state.Pause.By
		}
		if !state.Pause.ExpiresAt.IsZero() {
			line += "，到 " + state.Pause.ExpiresAt.Local().Format("01-02 15:04") + " 自动恢复"
		}
		line += "）"
	}
	fmt.Fprintln(out, line)
	if cu := state.Catchup; cu != nil {
		fmt.Fprintf(out, "  追赶：%s\n", catchupLine(cu))
	}
	if state.Reason != "" {
		fmt.Fprintf(out, "  原因：%s\n", state.Reason)
	}
}

func stageText(s lifecycle.Stage) string {
	switch s {
	case lifecycle.StageRunning:
		return "运行"
	case lifecycle.StagePaused:
		return "暂停"
	case lifecycle.StageResuming:
		return "恢复追赶"
	}
	return string(s)
}

func pauseKindText(k lifecycle.PauseKind) string {
	switch k {
	case lifecycle.PauseManual:
		return "手动暂停"
	case lifecycle.PauseSystem:
		return "系统暂停"
	}
	return string(k)
}

func catchupLine(cu *lifecycle.Catchup) string {
	switch {
	case cu.CaughtUp:
		return "已追平，待放行"
	case cu.BlockedBy != "" && cu.Message != "":
		return "被 " + cu.BlockedBy + " 阻塞（" + cu.Message + "）"
	case cu.BlockedBy != "":
		return "被 " + cu.BlockedBy + " 阻塞"
	case cu.Total > 0:
		return fmt.Sprintf("%d/%d", cu.Done, cu.Total)
	case cu.Message != "":
		return cu.Message
	}
	return "追赶中"
}

// runLifecycleCatchup 先让模块推进一次恢复追赶，再读进度（追平且设为自动放行的模块就此放行）。
func runLifecycleCatchup(ctx context.Context, o lifecycleOptions, out io.Writer) error {
	return withLifecycle(ctx, o, func(remote *lifecycle.Remote) error {
		state, err := remote.Catchup(ctx)
		if err != nil {
			return err
		}
		resuming, caughtUp := state.Stage == lifecycle.StageResuming, state.CaughtUp()
		if o.JSON {
			return writeLifecycleJSON(out, map[string]any{"module": state.Module, "resuming": resuming, "caught_up": caughtUp, "state": state})
		}
		if !resuming {
			fmt.Fprintf(out, "模块 %s：不在恢复追赶中（%s）\n", state.Module, stageText(state.Stage))
			return nil
		}
		text := "追赶中"
		if caughtUp {
			text = "已追平，待放行"
		}
		fmt.Fprintf(out, "模块 %s：%s\n", state.Module, text)
		if cu := state.Catchup; cu != nil && !caughtUp {
			fmt.Fprintf(out, "  追赶：%s\n", catchupLine(cu))
		}
		return nil
	})
}

func writeLifecycleJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
