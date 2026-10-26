// moox-factor-engine consumes collector period events, computes factors with
// the factorexpr expression engine and writes results to Storage. It only dials out: to
// moox-factor-mgr through the service gateway, to EventBus and to
// access.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/packages/healthz/trpclog"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/requestauth"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"

	trpc "trpc.group/trpc-go/trpc-go"
)

var (
	Version   = "dev"
	BuildTime = ""
	GitCommit = ""
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	switch command {
	case "serve":
		return serve(context.Background(), args)
	case "run-once":
		return runOnce(args, out)
	case "health":
		return health(args, out)
	case "reset-gap-ledger":
		return resetGapLedger(args, out)
	case "reconcile":
		return reconcile(args, out)
	case "tune":
		return tune(args, out)
	case "backfill":
		return backfill(args, out)
	case "version":
		_, err := fmt.Fprintf(out, "moox-factor-engine %s %s %s\n", Version, GitCommit, BuildTime)
		return err
	default:
		return fmt.Errorf("unknown command %q (serve, run-once, health, reset-gap-ledger, reconcile, backfill, tune, version)", command)
	}
}

// serve 运行引擎直到收到退出信号，或 ctx 结束。
func serve(ctx context.Context, args []string) (err error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	framework := fs.String("conf", "config/trpc_go.engine.yaml", "factor engine tRPC config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	trpc.ServerConfigPath = *framework
	s := trpc.NewServer()
	trpclog.InstallServiceName("factor-engine")
	runCtx, cancel := context.WithCancel(trpc.BackgroundContext())
	defer cancel()
	runtime, err := engine.Initialize(runCtx, s, cfg, Version)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close()) }()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close(nil) // 与收到 SIGTERM 相同的优雅退出
		case <-stopped:
		}
	}()
	return s.Serve()
}

type listFlag []string

func (v *listFlag) String() string { return strings.Join(*v, ",") }

func (v *listFlag) Set(raw string) error {
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*v = append(*v, item)
		}
	}
	return nil
}

func runOnce(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run-once", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	taskID := fs.String("task", "", "factor task id")
	period := fs.String("period", "", "period start timestamp RFC3339")
	var factors, subjects listFlag
	fs.Var(&factors, "member", "member alias (repeatable; all members when omitted)")
	fs.Var(&subjects, "subject", "subject id (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*taskID) == "" {
		return errors.New("--task is required")
	}
	at, err := time.Parse(time.RFC3339Nano, *period)
	if err != nil {
		return fmt.Errorf("--period must be an RFC3339 timestamp: %w", err)
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	outcome, err := engine.RunOnce(context.Background(), cfg, engine.RunOnceRequest{
		TaskID: *taskID, Members: factors, Subjects: subjects, Period: at.UTC(),
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "task_id": *taskID, "period": at.UTC().Format(time.RFC3339),
		"status": outcome.Status, "rows_written": outcome.RowsWritten, "factors": outcome.Factors,
	})
}

// health asks the local engine for readiness, signing the request with the
// MOOX_HEALTH_AUTH_* environment the engine runs with.
func health(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	url := fs.String("url", "http://127.0.0.1:11417/readyz", "engine health URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodGet, *url, nil)
	if err != nil {
		return err
	}
	if err := signHealthRequest(request); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	_, _ = fmt.Fprintln(out, strings.TrimSpace(string(body)))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("factor engine is not ready: HTTP %d", response.StatusCode)
	}
	return nil
}

// resetGapLedger 删除空洞账本。数据被整体清空之后账本里的待恢复项指向已不存在的数据，必须在引擎停止后执行：
// 引擎持有打开的账本文件，并在内存里维护待恢复任务。
func resetGapLedger(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reset-gap-ledger", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	healthAddr := fs.String("health-addr", "127.0.0.1:11417", "引擎健康检查地址；能连上说明引擎还在运行，拒绝执行")
	yes := fs.Bool("yes", false, "确认删除全部空洞账本")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	if conn, dialErr := net.DialTimeout("tcp", *healthAddr, time.Second); dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("因子引擎仍在运行（%s 可以连上）：先停止引擎再重置账本", *healthAddr)
	}
	if !*yes {
		return errors.New("该命令会删除全部空洞账本（含待恢复记录与审计历史）；确认后加 --yes 重新执行")
	}
	removed, err := gapledger.Reset(cfg.Gap.LedgerDir)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "ok", "ledger_dir": cfg.Gap.LedgerDir, "files_removed": removed})
}

// reconcile 对账一个因子任务的源数据：本地时序存储与 Storage 保留期内的数据比对并整段替换，完成后重建变更日志位点。
// 变更日志断档（离线超过保留期、Storage 节点更换）时引擎把该数据源判为未就绪，由人工执行本命令。
// 本地存储同一时刻只能由一个进程打开，必须在引擎停止后执行。
func reconcile(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	healthAddr := fs.String("health-addr", "127.0.0.1:11417", "引擎健康检查地址；能连上说明引擎还在运行，拒绝执行")
	set := fs.String("task", "", "要对账的因子任务 ID")
	var subjects listFlag
	fs.Var(&subjects, "subject", "只对账这个标的（可重复）；不写表示本地有数据的全部标的")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*set) == "" {
		return errors.New("--task 是必填项")
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	if conn, dialErr := net.DialTimeout("tcp", *healthAddr, time.Second); dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("因子引擎仍在运行（%s 可以连上）：先停止引擎再对账（本地存储同一时刻只能由一个进程打开）", *healthAddr)
	}
	summary, err := engine.Reconcile(context.Background(), cfg, engine.ReconcileRequest{
		TaskID: *set, Subjects: subjects,
		Progress: func(done, total int) {
			if done == total || done%50 == 0 {
				fmt.Fprintf(os.Stderr, "对账进度 %d/%d\n", done, total)
			}
		},
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(summary)
}

// backfill 把任务源数据的本地历史从 Storage 回补到指定根数（Storage 保留期内有多少补多少）；引擎必须已停止。
func backfill(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	healthAddr := fs.String("health-addr", "127.0.0.1:11417", "引擎健康检查地址；能连上说明引擎还在运行，拒绝执行")
	set := fs.String("task", "", "要回补的因子任务 ID")
	bars := fs.Int("bars", 0, "每个标的本地连续历史的目标根数（以本地最新一根往前数）")
	var subjects listFlag
	fs.Var(&subjects, "subject", "只回补这个标的（可重复）；不写表示本地有数据的全部标的")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*set) == "" || *bars <= 0 {
		return errors.New("--task 与 --bars 是必填项")
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	if conn, dialErr := net.DialTimeout("tcp", *healthAddr, time.Second); dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("因子引擎仍在运行（%s 可以连上）：先停止引擎再回补（本地存储同一时刻只能由一个进程打开）", *healthAddr)
	}
	summary, err := engine.Backfill(context.Background(), cfg, engine.BackfillRequest{
		TaskID: *set, Bars: *bars, Subjects: subjects,
		Progress: func(done, total int) {
			if done == total || done%50 == 0 {
				fmt.Fprintf(os.Stderr, "回补进度 %d/%d\n", done, total)
			}
		},
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(summary)
}

// tune 按本机内存给出内存配置建议（设计第 8 节）；--write 把建议写进 engine.yaml（只写还没设置过的项，--force 覆盖）。
func tune(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("tune", flag.ContinueOnError)
	app := fs.String("config", "config/engine.yaml", "factor engine config")
	memory := fs.Int64("memory-gib", 0, "总内存（GiB）；不写则探测本机")
	tasks := fs.Int("tasks", 4, "计划启用的因子任务数（决定计算并行数）")
	cpus := fs.Int("cpus", runtime.NumCPU(), "CPU 核数")
	series := fs.Int("series", 0, "计划启用的序列总数（标的数 × series_tag 数，所有数据源合计）；给出后按回看数量 N 估算窗口内存并与建议的窗口上限比较")
	fields := fs.Int("fields", 7, "数据集的数值字段数（用于窗口内存估算）")
	write := fs.Bool("write", false, "把建议写进配置文件")
	force := fs.Bool("force", false, "覆盖已经设置过的值")
	if err := fs.Parse(args); err != nil {
		return err
	}
	total := *memory << 30
	if total == 0 {
		detected, err := engine.DetectTotalMemory()
		if err != nil {
			return fmt.Errorf("探测总内存失败：%w（可用 --memory-gib 指定）", err)
		}
		total = detected
	}
	cfg, err := engine.Load(*app)
	if err != nil {
		return err
	}
	plan := engine.PlanMemory(total, *cpus, *tasks, int64(cfg.SeriesStore.BlockCache))
	if _, err := io.WriteString(out, plan.Summary()); err != nil {
		return err
	}
	if *series > 0 {
		estimate := engine.WindowEstimate(*series, cfg.Window.Lookback, *fields, cfg.Window.Headroom)
		if _, err := fmt.Fprintf(out, "窗口内存估算        %d 个序列 × 回看数量 N=%d（%d 个数值字段）≈ %.2fGiB\n", *series, cfg.Window.Lookback, *fields, float64(estimate)/(1<<30)); err != nil {
			return err
		}
		if warn := plan.CheckWindowEstimate(estimate); warn != "" {
			if _, err := fmt.Fprintf(out, "警告：%s\n", warn); err != nil {
				return err
			}
		}
	}
	if !*write {
		_, err = fmt.Fprintln(out, "加 --write 写入配置文件")
		return err
	}
	changes, err := engine.TuneConfig(*app, plan, *force)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		_, err = fmt.Fprintln(out, "配置里的相关项都已设置，没有改动（--force 可覆盖）")
		return err
	}
	for _, change := range changes {
		if _, err := fmt.Fprintf(out, "已写入 %s：%s → %s\n", change.Path, change.Old, change.New); err != nil {
			return err
		}
	}
	return nil
}

func signHealthRequest(request *http.Request) error {
	version := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_VERSION"))
	if version == "" {
		version = "moox-health-v1"
	}
	accessKey := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY"))
	if accessKey == "" || secretKey == "" {
		return errors.New("MOOX_HEALTH_AUTH_ACCESS_KEY and MOOX_HEALTH_AUTH_SECRET_KEY are required")
	}
	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("generate health nonce: %w", err)
	}
	timestamp := time.Now().Unix()
	nonce := hex.EncodeToString(nonceBytes)
	signature, err := requestauth.Sign(secretKey, requestauth.Material{
		Method: request.Method, Path: request.URL.EscapedPath(), Timestamp: timestamp, Nonce: nonce,
	})
	if err != nil {
		return fmt.Errorf("sign health request: %w", err)
	}
	request.Header.Set("X-Moox-Health-Auth", strings.Join([]string{
		version, accessKey, strconv.FormatInt(timestamp, 10), nonce, signature,
	}, "/"))
	return nil
}
