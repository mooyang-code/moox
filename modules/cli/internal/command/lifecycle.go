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

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/spf13/cobra"
)

// lifecycleOptions 是 moox-cli lifecycle 的参数。
type lifecycleOptions struct {
	Endpoint string
	// Module 是要操作的模块（factor-engine、strategy、trade……），未给 --endpoint 时按 moox.toml 解析它的健康端点。
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
}

var lifecycleFlags lifecycleOptions

var lifecycleCmd = &cobra.Command{
	Use:   "lifecycle",
	Short: "查看和控制模块的生命周期（运行、暂停、恢复追赶）",
	Long: `查看和控制一个模块的生命周期。模块只有三种状态：运行、暂停、恢复追赶。

  moox-cli lifecycle status                      查看状态（暂停详情、追赶进度）
  moox-cli lifecycle pause  [--reason 原因] [--pause-kind manual|system] [--for 2h]
                                                 暂停（默认手动暂停：只有用户 resume 才恢复；--for 到点自动恢复）
  moox-cli lifecycle resume [--pause-kind system] [--manual-release]
                                                 恢复：先进入恢复追赶（补齐暂停期间缺失的数据，不计算、不下单），追平后自动放行回到运行；
                                                 --manual-release 追平后停在待放行，等 release（系统级恢复用）；
                                                 --pause-kind system 只恢复系统暂停（不动手动暂停）
  moox-cli lifecycle release                     放行：恢复追赶已追平的模块回到运行
  moox-cli lifecycle catchup                     查看恢复追赶的进度

因子任务能不能计算看 moox-cli factor task status，策略实例的确认实盘用 moox-cli strategy instance confirm。

连接：默认操作 factor-engine，用 --module 换成 strategy、trade 等模块；按 --file（moox.toml）自动找到模块所在主机、
建立转发（只能出站的主机经 control 中转）并读取健康密钥，不需要手工建隧道或设置环境变量。
给了 --endpoint 就直接连这个地址（例如在模块所在主机上运行），鉴权密钥取自环境变量 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY；
没有 moox.toml 时同样退回本机地址。`,
	PersistentPreRunE: prepareLifecycleEndpoint,
}

// prepareLifecycleEndpoint 在子命令运行前确定模块的健康端点：显式 --endpoint 优先，其次按 moox.toml 解析。
func prepareLifecycleEndpoint(cmd *cobra.Command, _ []string) error {
	if lifecycleFlags.Endpoint != "" {
		return nil
	}
	if _, ok := healthModuleByName(lifecycleFlags.Module); !ok {
		return fmt.Errorf("未知的模块 %q（%s）", lifecycleFlags.Module, healthModuleNames())
	}
	endpoint, err := resolveSingleModuleEndpoint(cmd.Context(), lifecycleFlags.File, lifecycleFlags.Module, true, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	lifecycleFlags.Endpoint = endpoint
	return nil
}

func healthModuleNames() string {
	names := make([]string, 0, len(healthModules))
	for _, module := range healthModules {
		names = append(names, module.Name)
	}
	return strings.Join(names, "、")
}

func init() {
	rootCmd.AddCommand(lifecycleCmd)
	pf := lifecycleCmd.PersistentFlags()
	pf.StringVar(&lifecycleFlags.Endpoint, "endpoint", "", "模块健康检查地址；不给时按 --file 与 --module 自动解析")
	pf.StringVar(&lifecycleFlags.Module, "module", "factor-engine", "要操作的模块："+healthModuleNames())
	pf.StringVar(&lifecycleFlags.File, "file", defaultSetupFile, "部署配置文件（moox.toml）；用于定位模块所在主机")
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
	for _, item := range []struct {
		name  lifecycle.Command
		short string
	}{
		{lifecycle.CmdPause, "暂停：不再计算新周期、不下单（--for 可设定自动恢复）"},
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

func (o lifecycleOptions) client() *lifecycle.Client {
	return &lifecycle.Client{
		Endpoint: o.Endpoint, AuthVersion: os.Getenv("MOOX_HEALTH_AUTH_VERSION"),
		AccessKey: os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY"), SecretKey: os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY"),
	}
}

func runLifecycleStatus(ctx context.Context, o lifecycleOptions, out io.Writer) error {
	state, err := o.client().Status(ctx)
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, state)
	}
	printState(out, state)
	return nil
}

func runLifecycleCommand(ctx context.Context, o lifecycleOptions, cmd lifecycle.Command, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	by := o.By
	if by == "" {
		host, _ := os.Hostname()
		by = "cli:" + host
	}
	req := lifecycle.CommandRequest{Command: cmd, PauseKind: lifecycle.PauseKind(o.PauseKind), Reason: o.Reason, By: by, ManualRelease: o.ManualRelease}
	if o.ExpiresIn > 0 {
		if cmd != lifecycle.CmdPause {
			return errors.New("--for 只用于 pause")
		}
		req.ExpiresAt = time.Now().Add(o.ExpiresIn)
	}
	state, err := o.client().Exec(ctx, req)
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, state)
	}
	printState(out, state)
	return nil
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

// runLifecycleCatchup 读恢复追赶的进度。
func runLifecycleCatchup(ctx context.Context, o lifecycleOptions, out io.Writer) error {
	rep, err := o.client().Catchup(ctx)
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, rep)
	}
	if !rep.Resuming {
		fmt.Fprintf(out, "模块 %s：不在恢复追赶中（%s）\n", rep.Module, stageText(rep.State.Stage))
		return nil
	}
	state := "追赶中"
	if rep.CaughtUp {
		state = "已追平，待放行"
	}
	fmt.Fprintf(out, "模块 %s：%s\n", rep.Module, state)
	if cu := rep.State.Catchup; cu != nil && !rep.CaughtUp {
		fmt.Fprintf(out, "  追赶：%s\n", catchupLine(cu))
	}
	return nil
}

func writeLifecycleJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
