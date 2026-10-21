// Package command 是回放命令行的共用实现：moox-replay 与 moox-cli replay 都调用它。
//
//	start    --set <因子集合> --instance <策略实例> --from <起> --to <止> [--replay <ID>] [--quote next_open|close] [--write-factors] ...
//	         开始一次回放；用同样的参数（或只给 --replay）再次执行就是续跑。前台运行，Ctrl-C 中断后可续跑。
//	pause    --replay <ID>   暂停（运行进程在周期边界停下）
//	resume   --replay <ID>   继续被暂停的回放
//	cancel   --replay <ID>   取消回放（保留已有结果）
//	status   [--replay <ID>] 查看进度、当前周期与状态
//	report   --replay <ID>   汇总权益、回撤、费用与成交
//	delete   --replay <ID>   清理这次回放在 Trade 里的结果和本地记录
//
// 回放接口挂在各模块的健康检查端口上，鉴权密钥取自环境变量 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY。
package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/seriesreplay"
)

// Run 执行 args（不含程序名）；out 是标准输出。
func Run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：replay start|pause|resume|cancel|status|report|delete")
	}
	switch args[0] {
	case "start":
		return start(ctx, args[1:], out)
	case "pause":
		return control(args[1:], out, "pause", seriesreplay.StatusPaused, seriesreplay.StatusRunning)
	case "resume":
		return control(args[1:], out, "resume", seriesreplay.StatusRunning, seriesreplay.StatusPaused)
	case "cancel":
		return control(args[1:], out, "cancel", seriesreplay.StatusCancelled, seriesreplay.StatusRunning, seriesreplay.StatusPaused, seriesreplay.StatusFailed)
	case "status":
		return status(args[1:], out)
	case "report":
		return report(ctx, args[1:], out)
	case "delete":
		return remove(ctx, args[1:], out)
	}
	return fmt.Errorf("未知命令 %q（start、pause、resume、cancel、status、report、delete）", args[0])
}

type common struct {
	db                               string
	replayID                         string
	factorURL, strategyURL, tradeURL string
	asJSON                           bool
}

func (c *common) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", "./replay.db", "回放服务的 SQLite 路径（任务状态与周期记录）")
	fs.StringVar(&c.replayID, "replay", "", "回放 ID")
	fs.StringVar(&c.factorURL, "factor-endpoint", "http://127.0.0.1:11417", "因子引擎健康检查地址（可以是 SSH 隧道的本地端口）")
	fs.StringVar(&c.strategyURL, "strategy-endpoint", "http://127.0.0.1:11431", "策略健康检查地址")
	fs.StringVar(&c.tradeURL, "trade-endpoint", "http://127.0.0.1:11210", "Trade 健康检查地址")
	fs.BoolVar(&c.asJSON, "json", false, "以 JSON 输出")
}

func (c *common) endpoint(url string) seriesreplay.Endpoint {
	return seriesreplay.Endpoint{
		URL: url, AuthVersion: os.Getenv("MOOX_HEALTH_AUTH_VERSION"),
		AccessKey: os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY"), SecretKey: os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY"),
	}
}

func (c *common) trade() seriesreplay.TradeClient {
	return seriesreplay.TradeClient{Endpoint: c.endpoint(c.tradeURL)}
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func start(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	var c common
	c.bind(fs)
	var spec seriesreplay.Spec
	fs.StringVar(&spec.SetID, "set", "", "计算因子帧的因子集合 ID")
	fs.StringVar(&spec.InstanceID, "instance", "", "提供策略定义与数据源绑定的策略实例 ID")
	fs.StringVar(&spec.From, "from", "", "回放起点（RFC3339，含）")
	fs.StringVar(&spec.To, "to", "", "回放终点（RFC3339，不含）；本地数据必须包含终点之后的一根")
	fs.StringVar(&spec.Quote, "quote", "next_open", "成交价取法：next_open（下一根开盘）或 close（本期收盘）")
	fs.BoolVar(&spec.WriteFactors, "write-factors", false, "每期把因子结果写回结果数据集（不发周期完成事件）")
	fs.StringVar(&spec.InitialCash, "initial-cash", "", "纸面账本的初始资金（默认 100000）")
	fs.StringVar(&spec.FeeRate, "fee-rate", "", "费率（默认 0.001）")
	fs.StringVar(&spec.SlippageBPS, "slippage-bps", "", "滑点（基点，默认 0）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := seriesreplay.OpenStore(c.db)
	if err != nil {
		return err
	}
	defer store.Close()
	// 只给 --replay：从库里取规格续跑。
	if c.replayID != "" && spec.SetID == "" && spec.InstanceID == "" && spec.From == "" {
		run, err := store.GetRun(c.replayID)
		if err != nil {
			return fmt.Errorf("回放 %s：%w", c.replayID, err)
		}
		spec = run.Spec
	} else {
		spec.ReplayID = c.replayID
		if spec.ReplayID == "" {
			spec.ReplayID = "replay-" + time.Now().UTC().Format("20060102-150405")
		}
	}
	fmt.Fprintf(out, "回放 %s：因子集合 %s，策略实例 %s，区间 [%s, %s)\n", spec.ReplayID, spec.SetID, spec.InstanceID, spec.From, spec.To)
	runner := &seriesreplay.Runner{
		Store:    store,
		Factor:   seriesreplay.FactorClient{Endpoint: c.endpoint(c.factorURL)},
		Strategy: seriesreplay.StrategyClient{Endpoint: c.endpoint(c.strategyURL)},
		Trade:    c.trade(),
		Logf:     func(format string, args ...any) { fmt.Fprintf(out, "提示："+format+"\n", args...) },
	}
	last := time.Now()
	runner.Progress = func(run seriesreplay.Run, t seriesreplay.StepTiming) {
		if time.Since(last) >= 5*time.Second || run.PeriodsDone == run.PeriodsTotal {
			last = time.Now()
			fmt.Fprintf(out, "进度 %d/%d，当前周期 %s（因子 %dms，策略 %dms，Trade %dms）\n", run.PeriodsDone, run.PeriodsTotal,
				t.Period.Format(time.RFC3339), t.Factor, t.Strategy, t.Trade)
		}
	}
	run, err := runner.Run(ctx, spec)
	if err != nil {
		fmt.Fprintf(out, "回放 %s 状态：%s\n", spec.ReplayID, run.Status)
		return err
	}
	fmt.Fprintf(out, "回放 %s 完成：%d/%d 期\n", run.ReplayID, run.PeriodsDone, run.PeriodsTotal)
	summary, err := seriesreplay.Report(ctx, runner.Trade, run.ReplayID)
	if err != nil {
		fmt.Fprintf(out, "汇总失败（可稍后执行 report）：%v\n", err)
		return nil
	}
	return printSummary(out, summary, c.asJSON)
}

func control(args []string, out io.Writer, name, to string, from ...string) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var c common
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c.replayID == "" {
		return errors.New("--replay 是必填项")
	}
	store, err := seriesreplay.OpenStore(c.db)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err := store.GetRun(c.replayID); err != nil {
		return fmt.Errorf("回放 %s：%w", c.replayID, err)
	}
	changed, err := store.Transition(c.replayID, to, from...)
	if err != nil {
		return err
	}
	if !changed {
		run, _ := store.GetRun(c.replayID)
		return fmt.Errorf("回放 %s 当前是 %s，不能执行 %s", c.replayID, run.Status, name)
	}
	_, err = fmt.Fprintf(out, "回放 %s：%s\n", c.replayID, to)
	return err
}

func status(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var c common
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := seriesreplay.OpenStore(c.db)
	if err != nil {
		return err
	}
	defer store.Close()
	var runs []seriesreplay.Run
	if c.replayID != "" {
		run, err := store.GetRun(c.replayID)
		if err != nil {
			return fmt.Errorf("回放 %s：%w", c.replayID, err)
		}
		runs = []seriesreplay.Run{run}
	} else if runs, err = store.ListRuns(); err != nil {
		return err
	}
	if c.asJSON {
		return writeJSON(out, runs)
	}
	for _, run := range runs {
		current := ""
		if run.CurrentPeriod > 0 {
			current = time.Unix(run.CurrentPeriod, 0).UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(out, "%s  %-9s  %d/%d  当前周期 %s  集合 %s  实例 %s\n", run.ReplayID, run.Status, run.PeriodsDone, run.PeriodsTotal, current, run.Spec.SetID, run.Spec.InstanceID)
		if run.Error != "" {
			fmt.Fprintf(out, "    错误：%s\n", run.Error)
		}
	}
	return nil
}

func report(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	var c common
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c.replayID == "" {
		return errors.New("--replay 是必填项")
	}
	summary, err := seriesreplay.Report(ctx, c.trade(), c.replayID)
	if err != nil {
		return err
	}
	return printSummary(out, summary, c.asJSON)
}

func printSummary(out io.Writer, s seriesreplay.Summary, asJSON bool) error {
	if asJSON {
		return writeJSON(out, s)
	}
	_, err := fmt.Fprintf(out, "周期 %d（策略跳过 %d）  初始权益 %.2f  期末权益 %.2f  收益 %.2f%%  最大回撤 %.2f%%  费用 %.2f  成交额 %.2f  订单 %d（拒绝 %d）\n",
		s.Periods, s.SkippedPeriods, s.InitialEquity, s.FinalEquity, s.TotalReturn*100, s.MaxDrawdown*100, s.Fees, s.Turnover, s.Orders, s.RejectedOrders)
	return err
}

func remove(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	var c common
	c.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if c.replayID == "" {
		return errors.New("--replay 是必填项")
	}
	store, err := seriesreplay.OpenStore(c.db)
	if err != nil {
		return err
	}
	defer store.Close()
	run, err := store.GetRun(c.replayID)
	if err == nil && run.Status == seriesreplay.StatusRunning {
		return fmt.Errorf("回放 %s 还在运行：先 pause 或 cancel", c.replayID)
	}
	if err := c.trade().Delete(ctx, c.replayID); err != nil && !strings.Contains(err.Error(), "404") {
		return fmt.Errorf("清理 Trade 里的回放结果：%w", err)
	}
	if err := store.DeleteRun(c.replayID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "回放 %s 的结果已清理\n", c.replayID)
	return err
}
