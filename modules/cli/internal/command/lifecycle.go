package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
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
	Module   string
	File     string
	Kind, ID string
	Task     string
	JSON     bool
	// 暂停与恢复的参数。
	Reason        string
	PauseKind     string
	By            string
	Force         bool
	ManualRelease bool
	// ExpiresIn 用于 pause：这么久之后自动恢复（0 表示不自动恢复）。
	ExpiresIn time.Duration
}

var (
	lifecycleFlags lifecycleOptions
	shadowSpace    string
	shadowLimit    int
)

var lifecycleCmd = &cobra.Command{
	Use:   "lifecycle",
	Short: "查看和推进模块、对象的生命周期（未就绪 → 就绪 → 试运行 → 运行）",
	Long: `查看和推进模块或其管理对象（因子任务、策略实例）的生命周期。

  moox-cli lifecycle status                      查看状态、各对象阶段和未就绪原因
  moox-cli lifecycle check  [--task <id>]         重新执行启动前检查
  moox-cli lifecycle init   [--task <id>]         执行初始化（因子任务：载入本地窗口）
  moox-cli lifecycle start  [--task <id>]         启动：进入试运行
  moox-cli lifecycle confirm [--task <id>]        确认试运行结果：进入运行
  moox-cli lifecycle pause  [--task <id>] [--reason 原因] [--pause-kind manual|system]
                                                 暂停（默认手动暂停：只有用户 resume 才恢复；可在就绪、试运行、运行时暂停）
  moox-cli lifecycle resume [--task <id>] [--pause-kind system] [--force] [--manual-release]
                                                 恢复：先进入恢复追赶（补齐暂停期间缺失的数据，不计算、不下单），追平后自动放行回到暂停前的阶段；
                                                 --manual-release 追平后停在待放行，等 release（系统级恢复用）；
                                                 --pause-kind system 只恢复系统暂停（不动手动暂停）；
                                                 --force 在模块暂停期间只放行单个对象的追赶
  moox-cli lifecycle release [--task <id>]        放行：恢复追赶已追平的对象回到暂停前的阶段
  moox-cli lifecycle catchup                     查看恢复追赶的进度

不指定 --task / --kind --id 时作用于模块自身。

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
	module := lifecycleFlags.Module
	if cmd.Name() == "shadow" {
		module = "trade"
	}
	if _, ok := healthModuleByName(module); !ok {
		return fmt.Errorf("未知的模块 %q（%s）", module, healthModuleNames())
	}
	endpoint, err := resolveSingleModuleEndpoint(cmd.Context(), lifecycleFlags.File, module, true, cmd.ErrOrStderr())
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
	pf.StringVar(&lifecycleFlags.Task, "task", "", "因子任务 ID（等价于 --kind factor_task --id <ID>）")
	pf.StringVar(&lifecycleFlags.Kind, "kind", "", "对象类型（factor_task、strategy_instance……）")
	pf.StringVar(&lifecycleFlags.ID, "id", "", "对象 ID")
	pf.BoolVar(&lifecycleFlags.JSON, "json", false, "以 JSON 输出")
	pf.StringVar(&lifecycleFlags.Reason, "reason", "", "暂停原因（写进暂停详情）")
	pf.StringVar(&lifecycleFlags.PauseKind, "pause-kind", "", "pause：暂停类型 manual（默认）| system；resume：只恢复该类型的暂停")
	pf.StringVar(&lifecycleFlags.By, "by", "", "操作者（默认 cli:<主机名>）")
	pf.BoolVar(&lifecycleFlags.Force, "force", false, "resume：模块暂停期间只放行单个对象的追赶")
	pf.DurationVar(&lifecycleFlags.ExpiresIn, "for", 0, "pause：这么久之后自动恢复（先追赶再放行），例如 2h；不能用于系统暂停")
	pf.BoolVar(&lifecycleFlags.ManualRelease, "manual-release", false, "resume：追平后不自动放行，等 release")

	lifecycleCmd.AddCommand(&cobra.Command{
		Use: "status", Short: "查看生命周期状态", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLifecycleStatus(cmd.Context(), lifecycleFlags, cmd.OutOrStdout())
		},
	})
	shadow := &cobra.Command{
		Use: "shadow", Short: "查看试运行中的策略实例的影子目标（固定连接 trade 模块）", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLifecycleShadow(cmd.Context(), lifecycleFlags, shadowSpace, shadowLimit, cmd.OutOrStdout())
		},
	}
	shadow.Flags().StringVar(&shadowSpace, "space", "", "空间 ID")
	shadow.Flags().IntVar(&shadowLimit, "limit", 20, "最多显示的条数")
	lifecycleCmd.AddCommand(shadow)
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
		{lifecycle.CmdCheck, "启动前检查：列出不通过的项和处理办法，不改变阶段"},
		{lifecycle.CmdInit, "初始化：未就绪 → 就绪（准备本地状态与历史）"},
		{lifecycle.CmdStart, "启动：就绪 → 试运行（完整计算，但不真实下单）"},
		{lifecycle.CmdConfirm, "确认：试运行 → 运行"},
		{lifecycle.CmdPause, "暂停：不再计算新周期（--for 可设定自动恢复）"},
		{lifecycle.CmdResume, "恢复：先追赶暂停期间的周期，追平后放行"},
		{lifecycle.CmdRelease, "放行：追赶已追平的对象回到暂停前的阶段"},
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

func (o lifecycleOptions) ref() (lifecycle.Ref, error) {
	kind, id := o.Kind, o.ID
	if o.Task != "" {
		if kind != "" || id != "" {
			return lifecycle.Ref{}, errors.New("--task 不能和 --kind / --id 同时使用")
		}
		kind, id = "factor_task", o.Task
	}
	if (kind == "") != (id == "") {
		return lifecycle.Ref{}, errors.New("--kind 与 --id 要么都写，要么都不写（作用于模块自身）")
	}
	return lifecycle.Ref{Kind: kind, ID: id}, nil
}

func (o lifecycleOptions) client() *lifecycle.Client {
	return &lifecycle.Client{
		Endpoint: o.Endpoint, AuthVersion: os.Getenv("MOOX_HEALTH_AUTH_VERSION"),
		AccessKey: os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY"), SecretKey: os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY"),
	}
}

func runLifecycleStatus(ctx context.Context, o lifecycleOptions, out io.Writer) error {
	snap, err := o.client().Status(ctx)
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, snap)
	}
	degraded := ""
	if snap.Degraded {
		degraded = " · 降级"
	}
	fmt.Fprintf(out, "模块 %s：%s%s（%s）\n", snap.Module, stageText(snap.Stage), degraded, snap.Color)
	if snap.Reason != "" {
		fmt.Fprintf(out, "  原因：%s\n", snap.Reason)
	}
	printObject(out, "模块自身", snap.Self)
	for _, obj := range snap.Objects {
		printObject(out, obj.Kind+"/"+obj.ID, obj)
	}
	return nil
}

func runLifecycleCommand(ctx context.Context, o lifecycleOptions, cmd lifecycle.Command, out io.Writer) error {
	ref, err := o.ref()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	by := o.By
	if by == "" {
		host, _ := os.Hostname()
		by = "cli:" + host
	}
	req := lifecycle.CommandRequest{
		Command: cmd, Kind: ref.Kind, ID: ref.ID,
		PauseKind: lifecycle.PauseKind(o.PauseKind), Reason: o.Reason, By: by, Force: o.Force, ManualRelease: o.ManualRelease,
	}
	if o.ExpiresIn > 0 {
		if cmd != lifecycle.CmdPause {
			return errors.New("--for 只用于 pause")
		}
		req.ExpiresAt = time.Now().Add(o.ExpiresIn)
	}
	obj, err := o.client().Exec(ctx, req)
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, obj)
	}
	name := "模块自身"
	if ref != (lifecycle.Ref{}) {
		name = ref.Kind + "/" + ref.ID
	}
	printObject(out, name, obj)
	return nil
}

// runLifecycleShadow 读 Trade 记录的影子目标：预热中的策略实例发来的、只换算不下单的目标。
func runLifecycleShadow(ctx context.Context, o lifecycleOptions, space string, limit int, out io.Writer) error {
	if space == "" || o.ID == "" {
		return errors.New("需要 --space 与 --id（策略实例 ID）")
	}
	var rsp struct {
		Targets []struct {
			TargetID            string `json:"TargetID"`
			LogicalAccountID    string `json:"LogicalAccountID"`
			BarEndTime          int64  `json:"BarEndTime"`
			Equity              string `json:"Equity"`
			QuantityTargetsJSON string `json:"QuantityTargetsJSON"`
		} `json:"targets"`
	}
	path := fmt.Sprintf("/v1/shadow/targets?space=%s&instance=%s&limit=%d", url.QueryEscape(space), url.QueryEscape(o.ID), limit)
	if err := o.client().Get(ctx, path, &rsp); err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, rsp)
	}
	if len(rsp.Targets) == 0 {
		fmt.Fprintln(out, "没有影子目标")
		return nil
	}
	for _, t := range rsp.Targets {
		fmt.Fprintf(out, "%s  账户 %s  权益 %s  目标数量 %s\n", time.UnixMilli(t.BarEndTime).UTC().Format("2006-01-02 15:04"), t.LogicalAccountID, t.Equity, t.QuantityTargetsJSON)
	}
	return nil
}

func printObject(out io.Writer, name string, obj lifecycle.Object) {
	line := fmt.Sprintf("  %-40s %s", name, stageText(obj.Stage))
	if obj.Recovered {
		line += "（已自动恢复）"
	}
	if obj.Pause != nil {
		line += "（" + pauseKindText(obj.Pause.Kind)
		if obj.Pause.By != "" {
			line += "，" + obj.Pause.By
		}
		if !obj.Pause.ExpiresAt.IsZero() {
			line += "，到 " + obj.Pause.ExpiresAt.Local().Format("01-02 15:04") + " 自动恢复"
		}
		line += "）"
	}
	if obj.Effective != "" && obj.Effective != obj.Stage {
		line += "，有效：" + stageText(obj.Effective)
	}
	if obj.Hold != lifecycle.HoldNone {
		line += "，控制意图：" + string(obj.Hold)
	}
	fmt.Fprintln(out, line)
	if cu := obj.Catchup; cu != nil {
		fmt.Fprintf(out, "      追赶：%s\n", catchupLine(cu))
	}
	if obj.Reason != "" {
		fmt.Fprintf(out, "      原因：%s\n", obj.Reason)
	}
	for _, check := range obj.Checks {
		if check.Status == lifecycle.CheckPass {
			continue
		}
		msg := check.Name
		if check.Message != "" {
			msg += "：" + check.Message
		}
		fmt.Fprintf(out, "      [%s] %s\n", strings.ToUpper(string(check.Status)), msg)
		if check.Remedy != "" {
			fmt.Fprintf(out, "          处理：%s\n", check.Remedy)
		}
	}
}

func stageText(s lifecycle.Stage) string {
	switch s {
	case lifecycle.StageUninitialized:
		return "未初始化"
	case lifecycle.StageNotReady:
		return "未就绪"
	case lifecycle.StageReady:
		return "就绪"
	case lifecycle.StageWarming:
		return "试运行"
	case lifecycle.StageRunning:
		return "运行"
	case lifecycle.StagePaused:
		return "暂停"
	case lifecycle.StageResuming:
		return "恢复追赶"
	case lifecycle.StageResetting:
		return "重置中"
	case lifecycle.StageResetFailed:
		return "重置失败"
	case lifecycle.StageFault:
		return "故障"
	}
	return string(s)
}

func pauseKindText(k lifecycle.PauseKind) string {
	switch k {
	case lifecycle.PauseManual:
		return "手动暂停"
	case lifecycle.PauseSystem:
		return "系统暂停"
	case lifecycle.PauseAuto:
		return "检查失败自动暂停"
	}
	return string(k)
}

func catchupLine(cu *lifecycle.Catchup) string {
	switch {
	case cu.CaughtUp:
		return "已追平，待放行"
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
		fmt.Fprintf(out, "模块 %s：没有恢复追赶中的对象\n", rep.Module)
		return nil
	}
	state := "追赶中"
	if rep.CaughtUp {
		state = "已全部追平，待放行"
	}
	fmt.Fprintf(out, "模块 %s：%s\n", rep.Module, state)
	if rep.Self != nil {
		printObject(out, "模块自身", *rep.Self)
	}
	for _, obj := range rep.Objects {
		printObject(out, obj.Kind+"/"+obj.ID, obj)
	}
	return nil
}

func writeLifecycleJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
