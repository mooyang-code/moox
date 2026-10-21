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
	Kind, ID string
	Set      string
	JSON     bool
}

var lifecycleFlags lifecycleOptions

var lifecycleCmd = &cobra.Command{
	Use:   "lifecycle",
	Short: "查看和推进模块、对象的生命周期（未就绪 → 就绪 → 预热 → 运行）",
	Long: `查看和推进模块或其管理对象（因子集合、策略实例）的生命周期。

  moox-cli lifecycle status                      查看状态、各对象阶段和未就绪原因
  moox-cli lifecycle check  [--set <id>]         重新执行启动前检查
  moox-cli lifecycle init   [--set <id>]         执行初始化（因子集合：载入本地窗口）
  moox-cli lifecycle start  [--set <id>]         启动：进入预热
  moox-cli lifecycle confirm [--set <id>]        确认预热结果：进入运行
  moox-cli lifecycle pause  [--set <id>]         暂停
  moox-cli lifecycle resume [--set <id>]         恢复

不指定 --set / --kind --id 时作用于模块自身。--endpoint 指向模块的健康检查地址
（可以是 SSH 隧道的本地端口），鉴权密钥取自环境变量 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY。`,
}

func init() {
	rootCmd.AddCommand(lifecycleCmd)
	pf := lifecycleCmd.PersistentFlags()
	pf.StringVar(&lifecycleFlags.Endpoint, "endpoint", "http://127.0.0.1:11417", "模块健康检查地址（因子引擎默认 11417）")
	pf.StringVar(&lifecycleFlags.Set, "set", "", "因子集合 ID（等价于 --kind factor_set --id <ID>）")
	pf.StringVar(&lifecycleFlags.Kind, "kind", "", "对象类型（factor_set、strategy_instance……）")
	pf.StringVar(&lifecycleFlags.ID, "id", "", "对象 ID")
	pf.BoolVar(&lifecycleFlags.JSON, "json", false, "以 JSON 输出")

	lifecycleCmd.AddCommand(&cobra.Command{
		Use: "status", Short: "查看生命周期状态", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLifecycleStatus(cmd.Context(), lifecycleFlags, cmd.OutOrStdout())
		},
	})
	for _, name := range []lifecycle.Command{lifecycle.CmdCheck, lifecycle.CmdInit, lifecycle.CmdStart, lifecycle.CmdConfirm, lifecycle.CmdPause, lifecycle.CmdResume} {
		name := name
		lifecycleCmd.AddCommand(&cobra.Command{
			Use: string(name), Short: "生命周期命令：" + string(name), Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return runLifecycleCommand(cmd.Context(), lifecycleFlags, name, cmd.OutOrStdout())
			},
		})
	}
}

func (o lifecycleOptions) ref() (lifecycle.Ref, error) {
	kind, id := o.Kind, o.ID
	if o.Set != "" {
		if kind != "" || id != "" {
			return lifecycle.Ref{}, errors.New("--set 不能和 --kind / --id 同时使用")
		}
		kind, id = "factor_set", o.Set
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
	obj, err := o.client().Do(ctx, cmd, ref)
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

func printObject(out io.Writer, name string, obj lifecycle.Object) {
	line := fmt.Sprintf("  %-40s %s", name, stageText(obj.Stage))
	if obj.Recovered {
		line += "（已自动恢复）"
	}
	if obj.AutoPaused {
		line += "（检查失败自动暂停）"
	}
	fmt.Fprintln(out, line)
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
		return "预热"
	case lifecycle.StageRunning:
		return "运行"
	case lifecycle.StagePaused:
		return "暂停"
	case lifecycle.StageFault:
		return "故障"
	}
	return string(s)
}

func writeLifecycleJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
