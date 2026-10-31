package command

import (
	"strings"

	"github.com/mooyang-code/moox/packages/seriesreplay/command"
	"github.com/spf13/cobra"
)

var replayCmd = &cobra.Command{
	Use:   "replay",
	Short: "逐期回放：按实盘时序用历史数据验证因子、策略与成交（不产生实际下单）",
	Long: `逐期回放：按时间顺序逐期推进历史数据，每期取因子帧（因子引擎）、求策略决策（策略）、
在 Trade 的纸面账本里撮合，得到接近实盘时序的模拟结果。回放绝不产生实际下单，不影响实盘策略实例。

  moox-cli replay start [--task-id <因子任务>] --instance-id <策略实例> --from <起> --to <止> [--write-factors]
  moox-cli replay status [--replay-id <ID>]
  moox-cli replay pause|resume|cancel|report|delete --replay-id <ID>

中断后用同样的参数（或只给 --replay-id）再次执行 start 即可续跑。回放接口挂在各模块的健康检查端口上：
start、report、delete 默认按 --file（moox.toml，默认 ./moox.toml）自动找到因子引擎、策略、Trade 所在的主机，
建立转发并读取健康密钥，不需要手工建隧道或设置环境变量。
直接在模块所在主机上运行时，用 --factor-endpoint、--strategy-endpoint、--trade-endpoint 指定地址，
鉴权密钥取自环境变量 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY（也可按模块用 MOOX_FACTOR_/MOOX_STRATEGY_/MOOX_TRADE_HEALTH_AUTH_*）。`,
	DisableFlagParsing: true,
	SilenceUsage:       true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 参数交给回放自己解析，所以 --help 要在这里接住。
		if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
			return cmd.Help()
		}
		args, err := prepareReplayEndpoints(cmd, args)
		if err != nil {
			return err
		}
		return command.Run(cmd.Context(), args, cmd.OutOrStdout())
	},
}

// replayNetworkCommands 是会访问模块健康端口的回放子命令；其余只读写本地回放库。
var replayNetworkCommands = map[string]bool{"start": true, "report": true, "delete": true}

// replayEndpointFlags 是回放参数里的模块地址，对应的模块名。
var replayEndpointFlags = []struct{ flag, module string }{
	{"--factor-endpoint", "factor-engine"},
	{"--strategy-endpoint", "strategy"},
	{"--trade-endpoint", "trade"},
}

// takeFlag 取出并移除参数里的 --name value 或 --name=value；没有时返回空串与原参数。
func takeFlag(args []string, name string) (value string, rest []string, found bool) {
	for i, arg := range args {
		switch {
		case arg == name && i+1 < len(args):
			return args[i+1], append(append([]string{}, args[:i]...), args[i+2:]...), true
		case strings.HasPrefix(arg, name+"="):
			return strings.TrimPrefix(arg, name+"="), append(append([]string{}, args[:i]...), args[i+1:]...), true
		}
	}
	return "", args, false
}

// prepareReplayEndpoints 回放自己解析参数，这里先取出 --file，再为没有显式给地址的模块按 moox.toml 补上 --xxx-endpoint，
// 密钥放进本进程的环境变量。没有 moox.toml 时保持原样。
func prepareReplayEndpoints(cmd *cobra.Command, args []string) ([]string, error) {
	file, args, _ := takeFlag(args, "--file")
	if !replayNetworkCommands[args[0]] {
		return args, nil
	}
	given := map[string]bool{}
	var missing []string
	for _, item := range replayEndpointFlags {
		if _, _, found := takeFlag(args, item.flag); found {
			given[item.module] = true
			continue
		}
		missing = append(missing, item.module)
	}
	if len(missing) == 0 {
		return args, nil
	}
	health, err := resolveAndTrackModuleHealth(cmd.Context(), moduleHealthRequest{File: defaultFlag(file, defaultSetupFile), Modules: missing})
	if err != nil {
		if errorsIsManifestUnavailable(err) {
			return args, nil
		}
		return nil, err
	}
	args = append([]string{}, args...)
	for _, item := range replayEndpointFlags {
		if endpoint := health.Endpoint(item.module); endpoint != "" && !given[item.module] {
			args = append(args, item.flag+"="+endpoint)
		}
	}
	return args, nil
}

func init() { rootCmd.AddCommand(replayCmd) }
