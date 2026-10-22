package command

import (
	"github.com/mooyang-code/moox/packages/seriesreplay/command"
	"github.com/spf13/cobra"
)

var replayCmd = &cobra.Command{
	Use:   "replay",
	Short: "逐期回放：按实盘时序用历史数据验证因子、策略与成交（不产生实际下单）",
	Long: `逐期回放：按时间顺序逐期推进历史数据，每期取因子帧（因子引擎）、求策略决策（策略）、
在 Trade 的纸面账本里撮合，得到接近实盘时序的模拟结果。回放绝不产生实际下单，不影响实盘策略实例。

  moox-cli replay start --set <因子集合> --instance <策略实例> --from <起> --to <止> [--write-factors]
  moox-cli replay status [--replay <ID>]
  moox-cli replay pause|resume|cancel|report|delete --replay <ID>

中断后用同样的参数（或只给 --replay）再次执行 start 即可续跑。回放接口挂在各模块的健康检查端口上
（--factor-endpoint、--strategy-endpoint、--trade-endpoint，可以是 SSH 隧道的本地端口），
鉴权密钥取自环境变量 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY。`,
	DisableFlagParsing: true,
	SilenceUsage:       true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return command.Run(cmd.Context(), args, cmd.OutOrStdout())
	},
}

func init() { rootCmd.AddCommand(replayCmd) }
