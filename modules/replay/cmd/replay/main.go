// moox-replay 编排逐期回放（设计第 9 节）：按时间顺序逐期推进历史数据，每期取因子帧（因子引擎）、
// 求策略决策（策略）、在 Trade 的纸面账本里撮合，得到接近实盘时序的模拟结果；回放绝不产生实际下单。
//
// 用法：
//
//	moox-replay start --set <因子集合> --instance <策略实例> --from 2026-01-01T00:00:00Z --to 2026-02-01T00:00:00Z [--write-factors]
//	moox-replay pause|resume|cancel|report|delete --replay <ID>
//	moox-replay status [--replay <ID>]
//
// 中断后用同样的参数（或只给 --replay）再次执行 start 即可续跑。
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/mooyang-code/moox/packages/seriesreplay/command"
)

var Version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "version" {
		_, err := fmt.Fprintf(out, "moox-replay %s\n", Version)
		return err
	}
	return command.Run(ctx, args, out)
}
